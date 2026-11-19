package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

type openAIWSAgentRecoveryKey struct{}
type openAIWSAgentRecoveryRetryKey struct{}
type openAIWSAgentSourceKey struct{}

// The scope uses immutable client input before account projection. The original
// identity callbacks, fresh authorization and per-turn admission keep their order.
func (s *OpenAIGatewayService) WithOpenAIWSAgentRecovery(ctx context.Context, c *gin.Context, body []byte, session, model string) context.Context {
	ctx = s.WithOpenAIAgentMessageRecovery(ctx, c, body, session, model)
	return context.WithValue(ctx, openAIWSAgentRecoveryRetryKey{}, &atomic.Bool{})
}

func OpenAIWSAgentRecoveryCanRetry(ctx context.Context) bool {
	blocked, _ := ctx.Value(openAIWSAgentRecoveryRetryKey{}).(*atomic.Bool)
	return !openAIAgentRecoveryBound(ctx) && (blocked == nil || !blocked.Load())
}

func OpenAIWSAgentRecoveryBound(ctx context.Context) bool { return openAIAgentRecoveryBound(ctx) }

// One manager per forwarding attempt serializes an immediate next create with
// the previous terminal write/commit. Passthrough's AfterTurn runs BEFORE that
// write and must never grant ownership. The current request's integrity/shadow
// snapshot is bounded by the ingress frame limit; it is not a generation replay buffer.
type openAIWSAgentRecovery struct {
	mu                               sync.Mutex
	service                          *OpenAIGatewayService
	c                                *gin.Context
	ctx                              context.Context
	session                          string
	request                          *openAIAgentRecoveryRequest
	finish                           func(bool)
	blocked                          *atomic.Bool
	itemDone                         [][32]byte
	known                            map[[32]byte]bool
	readable                         *openAIAgentRecoveryRequest
	readableSource, readableControls [32]byte
	bridgeSource                     []byte // verified pre-builder bytes, bounded by the current frame
	protected                        *openAIAgentRecoveryRequest
	protectedSource                  [32]byte
	protectedControls                [32]byte
	completedResponse                string
	completedAccount                 int64
	completedModel                   string
	completedBridge                  openAIWSBridgeResponseAnchor
	acceptedHistoryTools             map[string]openAIWSAcceptedToolHistory
	pendingHistoryTools              map[string]openAIWSAcceptedToolHistory
	historicalToolIDs                map[[32]byte]struct{}
	historicalToolIDsExhausted       bool
	deliveredTools                   map[string]openAIWSDeliveredTool
	pendingTools                     map[string]openAIWSDeliveredTool
	localHistory                     openAIWSLocalHistory
	historyReserved                  int64
}

func newOpenAIWSAgentRecovery(s *OpenAIGatewayService, ctx context.Context, c *gin.Context) *openAIWSAgentRecovery {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil {
		return nil
	}
	blocked, _ := ctx.Value(openAIWSAgentRecoveryRetryKey{}).(*atomic.Bool)
	known := make(map[[32]byte]bool)
	for key := range r.known {
		known[key] = true
	}
	return &openAIWSAgentRecovery{service: s, ctx: ctx, c: c, session: r.session, blocked: blocked, known: known}
}

func openAIWSAgentRecoveryFromContext(ctx context.Context) *openAIWSAgentRecovery {
	r, _ := ctx.Value(openAIWSAgentRecoveryKey{}).(*openAIWSAgentRecovery)
	return r
}

func (r *openAIWSAgentRecovery) abort() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { r.localHistory = openAIWSLocalHistory{}; r.accountLocalHistory() }()
	if r.finish != nil {
		r.finish(false)
		r.finish = nil
	}
}

func (r *openAIWSAgentRecovery) preventReplay() {
	if r.blocked != nil {
		r.blocked.Store(true)
	}
}

func (r *openAIWSAgentRecovery) readablePrepared() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readable != nil
}

// Prepare before compatibility/OAuth projection and freeze the expected native
// representation. begin validates the original source and actual final bytes;
// it must not mint a new digest from the builder's possibly changed payload.
func (r *openAIWSAgentRecovery) prepareReadable(account *Account, source []byte) ([]byte, error) {
	if r == nil {
		return source, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readable = nil
	r.bridgeSource = nil
	r.protected = nil
	initial := openAIAgentRecoveryFromContext(r.ctx)
	ctx, clean, lossy := r.service.PrepareOpenAILossyHistoryRestart(r.ctx, r.c, source, r.session, initialModel(initial, source))
	// Rebuilding one generation does not turn every later native delta into
	// complete replay history. A response actually delivered by this manager
	// can continue on that same account; begin still verifies its current
	// authoritative pointer, account admission and actual outgoing bytes.
	if !lossy && initial != nil && initial.omittedHistory && account != nil && account.ID == r.completedAccount &&
		r.completedResponse != "" && gjson.GetBytes(source, "previous_response_id").String() == r.completedResponse &&
		!gjson.GetBytes(source, "conversation").Exists() && len(openAIAgentRecoveryItemDigests(source)) == 0 {
		model := gjson.GetBytes(source, "model").String()
		if model == "" {
			model = initial.model
		}
		if model == r.completedModel {
			return append([]byte(nil), source...), nil
		}
	}
	if !lossy && (initial == nil || !initial.omittedHistory) {
		clean, err := r.service.PrepareOpenAIAgentReplayBody(source)
		if err != nil {
			return nil, err
		}
		if r.service.preserveOpenAIAgentHistory(source) {
			clean, err = r.service.projectOpenAIAgentAccountReplay(r.ctx, account, clean, "ws")
			if err != nil {
				return nil, err
			}
			// Freeze the finite account projection before compatibility, OAuth
			// or tool builders. Every turn/account starts from its source frame.
			// begin may refresh owner records, but cannot trust a new input.
			model := gjson.GetBytes(source, "model").String()
			if initial != nil {
				model = initial.model
			}
			ctx := r.service.WithOpenAIAgentMessageRecovery(r.ctx, r.c, clean, r.session, model)
			r.protected = openAIAgentRecoveryFromContext(ctx)
			r.protected.nativeAccountID = account.ID
			r.protectedSource = openAIAgentRecoveryInputDigest(source)
			r.protectedControls = openAI409ControlsDigest(source)
		}
		return append([]byte(nil), clean...), nil
	}
	if !lossy && (openAIAgentRecoveryInputDigest(source) != initial.inputDigest || openAI409ControlsDigest(source) != initial.replayControlsDigest) {
		// The next client turn may still carry the old A opaque history.
		// Rebuild that frame independently, preserving every original fence.
		var changed bool
		var err error
		ctx, clean, changed, err = r.service.RebuildOpenAIWSUnsentTurn(r.ctx, r.c, source, r.session, initial.model)
		if err != nil {
			return nil, err
		}
		if !changed {
			return source, nil
		}
	}
	snapshot := *openAIAgentRecoveryFromContext(ctx)
	snapshot.delivery, snapshot.outputs = nil, nil
	if snapshot.shadowObservation != nil {
		r.c.Request = r.c.Request.WithContext(WithRecoveredContextPrivacy(r.c.Request.Context()))
	}
	ctx = context.WithValue(ctx, openAIAgentRecoveryContextKey{}, &snapshot)
	if account.UsesOpenAICodexProtocol() {
		var decoded map[string]any
		if err := decodeOpenAIJSONUseNumber(clean, &decoded); err != nil {
			return nil, err
		}
		if err := prepareOpenAI409OAuthReplay(ctx, r.c, decoded); err != nil {
			return nil, err
		}
		var err error
		clean, err = json.Marshal(decoded)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		clean, err = prepareOpenAI409APIKeyReplay(ctx, clean, isOpenAIResponsesLiteWebSocketPayload(clean))
		if err != nil {
			return nil, err
		}
		// Freeze a private copy before any builder sees it. Projection errors
		// belong only to an actual HTTP bridge; native WS retains namespaces.
		r.bridgeSource = append([]byte(nil), clean...)
	}
	r.readable = &snapshot
	r.readableSource = openAIAgentRecoveryInputDigest(source)
	r.readableControls = openAI409ControlsDigest(source)
	return clean, nil
}

// Stored native Responses already has a verified response anchor and the
// client is resending known intact opaque items. Legacy bridge reconstruction
// would append history again (including tasks/tool results) on every turn.
// This only chooses representation; begin still rechecks authoritative owners,
// the exact previous response, permissions and the final built HTTP request.
func (r *openAIWSAgentRecovery) storedContinuation(source []byte) bool {
	if r == nil || gjson.GetBytes(source, "store").Type != gjson.True || gjson.GetBytes(source, "previous_response_id").String() == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	items := openAIAgentRecoveryItemDigests(source)
	if len(items) == 0 {
		return false
	}
	scope := *openAIAgentRecoveryFromContext(r.ctx)
	if forward, ok := openAIForwardModelFromContext(r.c.Request.Context()); ok && forward.model != "" {
		scope.model = forward.model
	}
	for _, item := range items {
		if !r.known[scope.itemKey(item)] {
			return false
		}
	}
	return true
}

// req captures the actual URL, final authorization headers and outbound model.
// A fragment namespaces the fingerprint by adapter; this copy is never sent.
// Thus a WS grant cannot authorize an HTTP or different-adapter continuation.
func (r *openAIWSAgentRecovery) begin(account *Account, source, payload []byte, req *http.Request, mode string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finish != nil {
		r.finish(false)
		r.finish = nil
	}
	r.itemDone = nil
	r.pendingTools = nil
	r.pendingHistoryTools = nil
	if req.GetBody != nil {
		// HTTP builders can still normalize the prepared body. Validate the
		// built request's bytes, rather than replacing them with the earlier
		// bridge payload and accidentally granting a transformed opaque item.
		reader, err := req.GetBody()
		if err != nil {
			return openAIWSWireRefusal(r.ctx, "ws_body_unreadable", source, payload)
		}
		actual, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			return openAIWSWireRefusal(r.ctx, "ws_body_unreadable", source, payload)
		}
		payload = actual
	}
	model := gjson.GetBytes(payload, "model").String()
	// Lookup scope follows the channel's forward model, while the wire digest
	// below includes the final account-mapped model.
	if forward, ok := openAIForwardModelFromContext(r.c.Request.Context()); ok && forward.model != "" {
		model = forward.model
	} else if initial := openAIAgentRecoveryFromContext(r.ctx); initial != nil {
		model = initial.model
	}
	ctx := r.service.WithOpenAIAgentMessageRecovery(r.ctx, r.c, source, r.session, model)
	if r.protected != nil {
		if r.protected.nativeAccountID != account.ID {
			return openAIAgentRecoveryRefusal(ctx, "route_identity")
		}
		if openAIAgentRecoveryInputDigest(source) != r.protectedSource || openAI409ControlsDigest(source) != r.protectedControls {
			return openAIAgentRecoveryRefusal(ctx, "input_integrity")
		}
		// Keep fresh authoritative lookup/fences; the trusted input remains
		// the immutable pre-builder snapshot, never the final source/payload.
		fresh := openAIAgentRecoveryFromContext(ctx)
		fresh.inputDigest = r.protected.inputDigest
		if fresh.known == nil {
			fresh.known = make(map[[32]byte]bool)
		}
		for key := range r.protected.known {
			fresh.known[key] = true
		}
		if r.protected.lookupErr != nil {
			fresh.lookupErr = r.protected.lookupErr
		}
		fresh.conflict = fresh.conflict || r.protected.conflict
	}
	if r.readable != nil {
		if openAIAgentRecoveryInputDigest(source) != r.readableSource || openAI409ControlsDigest(source) != r.readableControls {
			return openAIWSWireRefusal(ctx, "ws_readable_source", source, payload)
		}
		ctx = context.WithValue(ctx, openAIAgentRecoveryContextKey{}, r.readable)
		if mode == "http_bridge" && account.IsOpenAIApiKey() {
			// Derive the permitted projection exclusively from the verified
			// pre-builder snapshot, never from source/payload/request final bytes.
			projected, _, err := adaptResponsesClientToolsForFunctionUpstream(r.bridgeSource, "OpenAI WS HTTP bridge")
			if err != nil {
				return err
			}
			snapshot := *r.readable
			snapshot.apiInputDigest, snapshot.apiControlsDigest = openAIAgentRecoveryInputDigest(projected), openAI409ControlsDigest(projected)
			snapshot.apiFingerprints = openAI409FingerprintControls(projected)
			ctx = context.WithValue(ctx, openAIAgentRecoveryContextKey{}, &snapshot)
		}
	}
	r.request = openAIAgentRecoveryFromContext(ctx)
	if r.request.disableOwnerBinding && (openAIAgentRecoveryBound(ctx) || len(r.request.items) > 0) {
		// Record current-turn replay safety even when a later delivery/pointer
		// check refuses the send. The initial warmup context cannot replace it.
		r.preventReplay()
	}
	if r.request.lookupErr != nil {
		return r.request.lookupErr
	}
	if r.request.conflict {
		return openAIRecoveryDiagnosticError("owner_conflict", r.request.diagnostics.subreason, r.request)
	}
	for _, key := range r.request.keys {
		if r.known[key] && !r.request.known[key] {
			return &openAIWSLostOwnerEvidenceError{cause: openAIAgentRecoveryError("owner_unavailable")}
		}
	}
	// These call sites have already selected a native Responses socket or
	// Responses HTTP bridge. Do not run the HTTP ingress protocol selector here:
	// it can query a different model-catalog endpoint and does not describe this
	// established transport. Final fresh admission and the captured wire remain
	// authoritative; managed/other credentials are excluded by prepare below.
	r.request.disallowLearning = r.request.disallowLearning || account.IsExcelBPSEnabledForModel(model) || account.IsPrismBrowserEnabledForModel(model)
	if (r.request.owner != nil || r.request.preservesHistory()) && r.request.disallowLearning {
		return openAIAgentRecoveryError("route_changed")
	}
	previous := gjson.GetBytes(source, "previous_response_id").String()
	projected := false
	if proof, ok := req.Context().Value(openAIWSPlaintextProjectionKey{}).(*openAIWSPlaintextProjection); ok {
		if proof == nil || mode != "http_bridge" || !account.IsOpenAIApiKey() || proof.accountID != account.ID ||
			proof.anchor.response == "" || proof.anchor != r.completedBridge || proof.anchor != openAIWSBridgeAnchor(r.request, account, previous) ||
			r.request.owner != nil || len(r.request.items) != 0 || r.request.preservesHistory() ||
			proof.previous != previous || r.request.previousResponseID != previous ||
			gjson.GetBytes(source, "store").Type != gjson.False || gjson.GetBytes(payload, "previous_response_id").Exists() ||
			proof.sourceInput != openAIAgentRecoveryInputDigest(source) || proof.sourceControls != openAI409ControlsDigest(source) || proof.input != openAIAgentRecoveryInputDigest(payload) ||
			proof.controls != openAI409ControlsDigest(payload) {
			return openAIWSWireRefusal(ctx, "ws_projection_integrity", source, payload)
		}
		// Recheck the original pointer on the actual selected account before
		// allowing its HTTP representation to omit that pointer.
		if err := r.service.checkOpenAIAgentRecoveryAccount(ctx, account); err != nil {
			return err
		}
		if _, reason := inspectOpenAIWSCompletedTools(proof.history); reason != "" {
			return openAIRecoveryDiagnosticError("owner_conflict", reason, r.request)
		}
		for _, item := range gjson.GetBytes(proof.history, "input").Array() {
			kind := item.Get("type").String()
			if kind != "function_call" && kind != "custom_tool_call" && kind != "tool_search_call" {
				continue
			}
			if r.historicalToolIDsExhausted {
				return openAIRecoveryDiagnosticError("owner_conflict", "tool_provenance_unknown", r.request)
			}
			fingerprint, valid := openAIToolFingerprint(item)
			observed, found := r.deliveredTools[r.request.model+"\x00"+item.Get("call_id").String()]
			if !found && acceptsPriorToolPair(r.acceptedHistoryTools, account, r.request.model, item, proof.history) {
				continue
			}
			if !valid || !found || observed.conflicted || observed.fingerprint != fingerprint || observed.accountID != account.ID || observed.config != openAIAgentRecoveryConfig(account, r.request.model) {
				return openAIRecoveryDiagnosticError("owner_conflict", "tool_provenance_unknown", r.request)
			}
		}
		handoff := &openAIWSDiscoveryHandoff{scope: *r.request, input: openAIAgentRecoveryInputDigest(proof.history), controls: openAI409ControlsDigest(proof.history), proofs: make(map[string]openAIToolProofSnapshot)}
		for _, item := range gjson.GetBytes(proof.history, "input").Array() {
			if item.Get("type").String() != "tool_search_call" {
				continue
			}
			key := r.request.toolKey(item.Get("call_id").String())
			raw, err := r.service.toolProofGet(ctx, key)
			if err != nil {
				return err
			}
			handoff.proofs[key] = openAIToolProofSnapshot{raw: raw}
		}
		if err := r.service.verifyOpenAIToolInputProofs(context.WithValue(ctx, openAIWSDiscoveryHandoffKey{}, handoff), r.request, proof.history, true); err != nil {
			return err
		}
		r.request.previousResponseID = ""
		projected = true
	}
	if !projected && !r.request.omittedHistory && previous != "" && (r.request.owner != nil || r.request.disableOwnerBinding || r.request.previousResponseID != "") &&
		gjson.GetBytes(payload, "previous_response_id").String() != previous {
		return openAIWSWireRefusal(ctx, "ws_previous_removed", source, payload)
	}
	if err := r.service.checkOpenAIAgentRecoveryAccount(ctx, account); err != nil {
		if r.request.hasCompletedMixedOwners() {
			return &openAIWSOwnerRouteError{cause: err, completedMixedOwners: true, completedSource: r.request}
		}
		return &openAIWSOwnerRouteError{cause: err}
	}
	if !r.request.omittedHistory && (r.request.owner != nil || r.request.disableOwnerBinding) && previous != "" {
		owner, err := r.service.getOpenAIWSStateStore().GetResponseAccount(ctx, r.request.groupID, previous)
		if err != nil {
			return openAIAgentRecoveryError("store_unavailable")
		}
		if owner != account.ID {
			return &openAIWSOwnerRouteError{cause: openAIAgentRecoveryError("owner_unavailable")}
		}
	}
	copyReq := req.Clone(ctx)
	copyURL := *req.URL
	copyURL.Fragment = "agent-recovery-" + mode
	copyReq.URL = &copyURL
	// The actual request may have already been consumed by a caller. Only the
	// immutable final payload is used for the compatibility check.
	copyReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	finish, err := r.service.prepareOpenAIAgentRecoveryAttempt(ctx, account, copyReq)
	if err != nil {
		return err
	}
	r.finish = finish
	if mode == "http_bridge" {
		r.pendingHistoryTools = acceptedOpenAIWSToolHistory(source, account, r.request.model)
	}
	r.stageLocalHistory(source)
	if r.request.owner != nil || len(r.request.items) > 0 || r.request.omittedHistory ||
		(r.service.settingService != nil && r.service.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx)) {
		// In automatic continuation mode, even a plaintext initial generation
		// becomes non-replayable as it crosses the physical write boundary.
		// Only a separately proven unsent NEXT frame can change accounts.
		r.preventReplay()
	}
	return nil
}

func (r *openAIWSAgentRecovery) beginSocket(account *Account, source, payload []byte, address string, headers http.Header, mode string) error {
	if r == nil {
		return nil
	}
	addressURL, err := url.Parse(address)
	if err != nil {
		return err
	}
	// A compatibility descriptor only; the existing transport owns URL
	// validation and the physical socket. Never submit this synthetic request.
	req := &http.Request{Method: http.MethodPost, URL: addressURL, Header: headers}
	return r.begin(account, source, payload, req, mode)
}

func (r *openAIWSAgentRecovery) write(payload []byte, write func() error) error {
	if r == nil {
		return write()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	request := r.request
	if request == nil || request.delivery == nil || r.finish == nil {
		return write()
	}
	ctx := context.WithValue(r.ctx, openAIAgentRecoveryContextKey{}, request)
	observer := &gin.Context{Request: (&http.Request{}).WithContext(ctx)}
	if err := observeOpenAIAgentRecoveryOutput(observer, payload, true); err != nil {
		r.preventReplay()
		return openAIWSAgentRecoveryClose(err)
	}
	event := gjson.GetBytes(payload, "type").String()
	if event == "response.output_item.done" {
		r.itemDone = append(r.itemDone, openAIAgentRecoveryDigests(payload, "item")...)
	}
	if len(request.delivery.keys) > 0 {
		r.preventReplay()
	}
	err := write()
	if err != nil || r.ctx.Err() != nil {
		r.finish(false)
		r.finish = nil
		return err
	}
	if r.localHistory.pending != nil {
		r.localHistory.outputBytes += len(payload)
		if r.localHistory.outputBytes > openAIWSLocalHistoryLimit {
			r.localHistory = openAIWSLocalHistory{diagnostic: "output_limit"}
			r.accountLocalHistory()
		} else {
			r.localHistory.output.AddEvent(event, payload)
			r.accountLocalHistory()
		}
	}
	toolItems := gjson.GetBytes(payload, "response.output").Array()
	if event == "response.output_item.done" {
		toolItems = append(toolItems, gjson.GetBytes(payload, "item"))
	}
	for _, item := range toolItems {
		kind := item.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" && kind != "tool_search_call" {
			continue
		}
		if r.pendingTools == nil {
			r.pendingTools = make(map[string]openAIWSDeliveredTool)
		}
		key := request.model + "\x00" + item.Get("call_id").String()
		fingerprint, valid := openAIToolFingerprint(item)
		old, exists := r.pendingTools[key]
		if !exists && len(r.pendingTools) >= 1024 {
			continue
		}
		r.pendingTools[key] = openAIWSDeliveredTool{fingerprint: fingerprint, accountID: request.delivery.binding.accountID, config: request.delivery.binding.config,
			conflicted: !valid || old.conflicted || (exists && old.fingerprint != fingerprint)}
	}
	if isOpenAIWSTerminalEvent(event) && r.finish != nil {
		completed := (event == "response.completed" || event == "response.done") && openAIAgentCompletedResponse(payload, true)
		if completed && len(gjson.GetBytes(payload, "response.output").Array()) == 0 {
			// Some WS providers emit complete items separately and an empty
			// terminal output. Only item.done followed by completed qualifies.
			request.outputs = append(request.outputs, r.itemDone...)
		}
		// A response pointer also belongs to its producer when the response
		// contains only ordinary messages or tool calls. Publish after the
		// successful terminal write, before a pipelined next frame can check
		// that pointer. Passthrough has no separate response-account publisher.
		responseID := gjson.GetBytes(payload, "response.id").String()
		if completed && (responseID != "" || len(request.outputs) > 0 || len(request.keys) > 0) {
			storeCtx, cancel := context.WithTimeout(r.ctx, 3*time.Second)
			if responseID == "" || r.service.getOpenAIWSStateStore().BindResponseAccount(storeCtx, request.groupID, responseID, request.delivery.binding.accountID, r.service.openAIWSResponseStickyTTL()) != nil {
				completed = false
			}
			cancel()
		}
		r.finish(completed)
		if completed {
			if r.pendingHistoryTools != nil {
				// Context can be replaced by an independent task, but an old
				// call ID must never become a fresh delivery on this manager.
				// Keep only bounded identity hashes, separate from both the
				// current accepted pairs and actual delivery evidence.
				if r.historicalToolIDs == nil {
					r.historicalToolIDs = make(map[[32]byte]struct{})
				}
				for key := range r.pendingHistoryTools {
					id := sha256.Sum256([]byte(key))
					if _, exists := r.historicalToolIDs[id]; exists {
						continue
					}
					if len(r.historicalToolIDs) >= 1024 {
						// Never evict collision evidence to grant a new proof.
						// Ordinary independent tasks remain available.
						r.historicalToolIDsExhausted = true
						break
					}
					r.historicalToolIDs[id] = struct{}{}
				}
				r.acceptedHistoryTools = r.pendingHistoryTools
			}
			r.completedResponse, r.completedAccount, r.completedModel = responseID, request.delivery.binding.accountID, request.model
			r.completedBridge = openAIWSBridgeResponseAnchor{response: responseID, model: request.model, session: request.session,
				account: request.delivery.binding.accountID, key: request.keyID, user: request.userID, group: request.groupID, config: request.delivery.binding.config}
			if r.deliveredTools == nil {
				r.deliveredTools = make(map[string]openAIWSDeliveredTool)
			}
			for key, observed := range r.pendingTools {
				// Reuse the delivery's terminal verdict for this manager-local
				// proof. Shared persisted proofs additionally require encrypted
				// anchors, which an ordinary plaintext call does not have.
				callID := key[len(request.model)+1:]
				toolKey := request.toolKey(callID)
				delivery := request.delivery
				call, found := delivery.tools[toolKey]
				if !delivery.toolEnabled || !delivery.toolTerminal || delivery.toolInvalid || !delivery.toolAnchorsValid ||
					!found || !call.complete || call.fingerprint != observed.fingerprint ||
					(delivery.toolFinal != nil && !delivery.toolFinal[toolKey]) {
					observed.conflicted = true
				}
				// Where encrypted or shadow anchors apply, retain all existing
				// shared proof checks. The anchorless exception never escapes
				// this manager or relaxes shared/encrypted proof publication.
				if len(delivery.toolAnchors) > 0 || len(request.outputs) > 0 || request.shadowObservation != nil {
					raw, proofErr := r.service.toolProofGet(ctx, toolKey)
					var proof openAIToolProof
					if proofErr != nil || json.Unmarshal([]byte(raw), &proof) != nil ||
						proof.Call != observed.fingerprint || proof.Delivery != openAIToolHash(delivery.pending) ||
						proof.Producer != encodeOpenAIAgentRecoveryBinding(delivery.binding) || r.service.checkToolProof(ctx, proof) != nil {
						observed.conflicted = true
					}
				}
				old, exists := r.deliveredTools[key]
				if !exists && len(r.deliveredTools) >= 1024 {
					continue
				}
				// pendingTools coalesces item.done and terminal observations
				// within one generation. An existing delivered identity is a
				// later generation reusing the call ID, even if bytes match.
				// Historical identities also survive replaced accepted context.
				_, reusedHistoricalID := r.historicalToolIDs[sha256.Sum256([]byte(key))]
				observed.conflicted = observed.conflicted || old.conflicted || exists || reusedHistoricalID || r.historicalToolIDsExhausted
				r.deliveredTools[key] = observed
			}
			for _, key := range request.keys {
				// Opt-out consumption does not publish new input ownership. Only
				// remember inputs that already had completed records; otherwise a
				// later turn would mistake an ordinary miss for record loss.
				if !request.disableOwnerBinding || request.known[key] {
					r.known[key] = true
				}
			}
			for _, item := range request.outputs {
				r.known[request.itemKey(item)] = true
			}
		}
		r.commitLocalHistory(completed)
		r.finish = nil
	}
	return nil
}

func openAIWSAgentRecoveryClose(err error) error {
	return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "agent task continuation is unavailable; original task was preserved", err)
}

// Fixed field-presence/count metadata only. Never log response IDs, hashes,
// input text, tool content, headers, credentials or arbitrary JSON keys.
func openAIWSWireRefusal(ctx context.Context, stage string, source, payload []byte) error {
	sourceInput, wireInput := gjson.GetBytes(source, "input"), gjson.GetBytes(payload, "input")
	inputKind := "absent"
	switch {
	case sourceInput.IsArray():
		inputKind = "array"
	case sourceInput.Type == gjson.String:
		inputKind = "string"
	case sourceInput.Exists():
		inputKind = "other"
	}
	storeKind := "absent"
	switch gjson.GetBytes(source, "store").Type {
	case gjson.True:
		storeKind = "true"
	case gjson.False:
		storeKind = "false"
	default:
		if gjson.GetBytes(source, "store").Exists() {
			storeKind = "other"
		}
	}
	sanitized, idsChanged, sanitizeErr := sanitizeOpenAIResponsesInputItemIDs(source)
	tailMatchesSource, tailMatchesIDs := false, false
	if sourceInput.IsArray() && wireInput.IsArray() {
		original, wire := sourceInput.Array(), wireInput.Array()
		if len(original) > 0 && len(wire) >= len(original) {
			var tail []json.RawMessage
			for _, item := range wire[len(wire)-len(original):] {
				tail = append(tail, json.RawMessage(item.Raw))
			}
			encoded, e := json.Marshal(tail)
			if e == nil {
				tailBody := append(append([]byte(`{"input":`), encoded...), '}')
				tailMatchesSource = openAIAgentRecoveryInputDigest(source) == openAIAgentRecoveryInputDigest(tailBody)
				tailMatchesIDs = sanitizeErr == nil && openAIAgentRecoveryInputDigest(sanitized) == openAIAgentRecoveryInputDigest(tailBody)
			}
		}
	}
	logger.FromContext(ctx).Warn("openai.encrypted_history_409_refused", zap.String("stage", stage),
		zap.Bool("source_ids_normalized", idsChanged), zap.Bool("tail_matches_source", tailMatchesSource), zap.Bool("tail_matches_normalized_ids", tailMatchesIDs),
		zap.Int("source_agent_items", len(openAIAgentRecoveryItemDigests(source))), zap.Int("wire_agent_items", len(openAIAgentRecoveryItemDigests(payload))),
		zap.String("source_input_kind", inputKind), zap.String("source_store", storeKind),
		zap.Int("source_input_items", len(sourceInput.Array())), zap.Int("wire_input_items", len(wireInput.Array())),
		zap.Bool("source_previous_present", gjson.GetBytes(source, "previous_response_id").String() != ""),
		zap.Bool("wire_previous_present", gjson.GetBytes(payload, "previous_response_id").String() != ""),
		zap.Bool("source_model_present", gjson.GetBytes(source, "model").String() != ""),
		zap.Bool("input_changed", openAIAgentRecoveryInputDigest(source) != openAIAgentRecoveryInputDigest(payload)),
		zap.Bool("controls_changed", openAI409ControlsDigest(source) != openAI409ControlsDigest(payload)))
	return openAIRecoveryDiagnosticError("wire_changed", stage, openAIAgentRecoveryFromContext(ctx))
}

// Bounded, manager-local plaintext history for an explicitly unsent next turn.
// It expires with the socket/forwarding attempt, never enters Redis or logs.
const openAIWSLocalHistoryLimit = 8 << 20

var openAIWSLocalHistoryBytes atomic.Int64

type openAIWSLocalHistory struct {
	diagnostic   string // Fixed enum only; never retains caller content.
	inputFailure string // Fixed reason for an unsupported source item.
	outputBytes  int
	tools        json.RawMessage
	committed    []json.RawMessage
	pending      []json.RawMessage
	output       openAIWSLocalOutput
	anchor       openAIWSBridgeResponseAnchor
	clientModel  string
	ready        bool
}

type openAIWSDiscoveryHandoffKey struct{}
type openAIWSDiscoveryHandoff struct {
	proofs          map[string]openAIToolProofSnapshot
	scope           openAIAgentRecoveryRequest
	input, controls [32]byte
}

type openAIWSRestoredUnsentError struct {
	*openAIWSUnsentTurnError
	discovery *openAIWSDiscoveryHandoff
}

func (e *openAIWSRestoredUnsentError) Unwrap() error { return e.openAIWSUnsentTurnError }
func (e *openAIWSRestoredUnsentError) RecoveredWSContext(ctx context.Context) context.Context {
	return context.WithValue(WithRecoveredContextPrivacy(ctx), openAIWSDiscoveryHandoffKey{}, e.discovery)
}

func openAIWSPlainItems(body []byte) ([]json.RawMessage, bool) {
	items, reason := openAIWSReadableHistoryItems(body)
	return items, reason == ""
}
func openAIWSPlainItem(raw []byte) bool {
	item := gjson.ParseBytes(raw)
	if !item.IsObject() || openAIWSHasOpaqueField(item, 0) {
		return false
	}
	switch item.Get("type").String() {
	case "", "message":
		if role := item.Get("role").String(); role != "user" && role != "assistant" && role != "system" && role != "developer" {
			return false
		}
		return openAIReadableShadowTextOnly(item.Get("content"))
	case "function_call", "custom_tool_call", "tool_search_call":
		_, ok := openAIToolFingerprint(item)
		return ok
	case "tool_search_output":
		_, ok := openAIDiscoveryFingerprint(item, true)
		return ok
	case "function_call_output", "custom_tool_call_output":
		return item.Get("call_id").Type == gjson.String && item.Get("call_id").String() != "" && openAIReadableShadowTextOnly(item.Get("output"))
	default:
		return false
	}
}
func openAIWSHistoryCopy(items []json.RawMessage) ([]json.RawMessage, bool) {
	size := 0
	for _, v := range items {
		size += len(v)
		if size > openAIWSLocalHistoryLimit {
			return nil, false
		}
	}
	out := make([]json.RawMessage, len(items))
	for i, v := range items {
		out[i] = append(json.RawMessage(nil), v...)
	}
	return out, true
}
func (r *openAIWSAgentRecovery) stageLocalHistory(source []byte) {
	defer r.accountLocalHistory()
	previousDiagnostic := r.localHistory.diagnostic
	r.localHistory.pending = nil
	r.localHistory.outputBytes = 0
	r.localHistory.output = openAIWSLocalOutput{}
	r.localHistory.diagnostic = "staging"
	if r.service.settingService == nil || !r.service.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(r.ctx) {
		r.localHistory = openAIWSLocalHistory{diagnostic: "disabled"}
		return
	}
	items, inputFailure := openAIWSReadableHistoryItems(source)
	ok := inputFailure == ""
	if !ok || gjson.GetBytes(source, "conversation").Exists() {
		r.localHistory.inputFailure = inputFailure
		r.localHistory.diagnostic = "source_unsupported"
		if gjson.GetBytes(source, "conversation").Exists() {
			r.localHistory.diagnostic = "conversation_reference"
		}
		r.localHistory.ready = false
		return
	}
	previous := gjson.GetBytes(source, "previous_response_id").String()
	if previous != "" {
		anchor := r.localHistory.anchor
		if !r.localHistory.ready || previous != anchor.response || r.request.keyID != anchor.key || r.request.userID != anchor.user || r.request.groupID != anchor.group || r.request.session != anchor.session || r.request.model != anchor.model {
			if r.localHistory.ready || previousDiagnostic == "" {
				r.localHistory.diagnostic = "prior_anchor_mismatch"
			} else {
				r.localHistory.diagnostic = previousDiagnostic
			}
			r.localHistory.ready = false
			return
		}
		// Accept either a delta or the exact complete prefix; never drop existing messages.
		if !openAIWSRawItemsHasPrefix(items, r.localHistory.committed) {
			items = combineOpenAIWSReplayItems(r.localHistory.committed, items)
		}
	}
	if previous == "" {
		r.localHistory.tools = nil
	}
	// Tools retain the socket's declarations. Instructions apply only to the
	// current frame, including when omitted, and must never enter this history.
	if value := gjson.GetBytes(source, "tools"); value.Exists() {
		r.localHistory.tools = append(json.RawMessage(nil), value.Raw...)
	}
	if len(r.localHistory.tools) > openAIWSLocalHistoryLimit {
		r.localHistory = openAIWSLocalHistory{diagnostic: "tools_limit"}
		return
	}
	r.localHistory.pending, ok = openAIWSHistoryCopy(items)
	r.localHistory.ready = false
	if !ok {
		r.localHistory.pending = nil
		r.localHistory.diagnostic = "input_limit"
	}
	if model := gjson.GetBytes(source, "model").String(); model != "" {
		r.localHistory.clientModel = model
	}
}
func (r *openAIWSAgentRecovery) commitLocalHistory(completed bool) {
	defer r.accountLocalHistory()
	h := &r.localHistory
	if !completed || h.pending == nil || strings.TrimSpace(r.completedBridge.response) == "" {
		h.ready = false
		h.committed = nil
		if !completed {
			h.diagnostic = "not_completed"
		} else if h.pending != nil {
			h.diagnostic = "response_anchor_missing"
		}
		return
	}
	output := h.output.items
	if h.output.final != nil {
		output = h.output.final
	}
	if output == nil {
		output = []json.RawMessage{}
	}
	encoded, e := json.Marshal(output)
	if e != nil {
		h.ready = false
		h.committed = nil
		h.diagnostic = "serialize_failed"
		return
	}
	normalized, ok := openAIWSPlainItems(append(append([]byte(`{"input":`), encoded...), '}'))
	if !ok {
		h.ready = false
		h.committed = nil
		h.diagnostic = "output_unsupported"
		return
	}
	output = normalized
	all := combineOpenAIWSReplayItems(h.pending, output)
	h.committed, ok = openAIWSHistoryCopy(all)
	h.ready = ok
	h.diagnostic = "history_limit"
	if ok {
		h.diagnostic = "ready"
		h.anchor = r.completedBridge
	}
	h.pending = nil
	h.outputBytes = 0
	h.output = openAIWSLocalOutput{}
}

// Called at the outer proxy return boundary, after transport goroutines stop.
// Only the typed not-written admission error grants permission to attach prior
// delivered context. Every current auth/moderation/long-context gate runs again.
func (r *openAIWSAgentRecovery) restoreUnsentHistory(err error) error {
	if r == nil {
		return err
	}
	var unsent *openAIWSUnsentTurnError
	var admission *OpenAITurnAdmissionError
	if !errors.As(err, &unsent) {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	h := &r.localHistory
	reason := "history_unready"
	restored := false
	defer func() {
		if !restored {
			logger.FromContext(r.ctx).Warn("openai.ws_unsent_history_restore_skipped",
				zap.String("restore_reason", reason), zap.String("history_state", h.diagnostic),
				zap.String("history_input_failure", h.inputFailure),
				zap.Int("turn", unsent.turn), zap.Int("history_items", len(h.committed)),
				zap.Int64("history_bytes", r.historyReserved), zap.Bool("history_ready", h.ready),
				zap.Bool("delivery_pending", r.finish != nil),
				zap.Bool("source_previous_present", gjson.GetBytes(unsent.body, "previous_response_id").String() != ""))
		}
	}()
	if unsent.keepAccount || !errors.As(err, &admission) {
		reason = "not_admission"
		return err
	}
	if !h.ready || r.finish != nil || r.service.settingService == nil || !r.service.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(r.ctx) {
		return err
	}
	reason = "response_anchor"
	source := unsent.body
	previous := gjson.GetBytes(source, "previous_response_id")
	// Two blank IDs describe no continuation. Only the client's explicit
	// pointer to a delivered, completed response may attach another turn.
	if len(source) > openAIWSLocalHistoryLimit || previous.Type != gjson.String || strings.TrimSpace(previous.String()) == "" ||
		strings.TrimSpace(h.anchor.response) == "" || previous.String() != h.anchor.response || gjson.GetBytes(source, "conversation").Exists() {
		return err
	}
	reason = "client_model"
	if unsent.model != "" && unsent.model != h.clientModel {
		return err
	}
	reason = "principal_or_owner"
	scope := openAIAgentRecoveryFromContext(r.service.WithOpenAIAgentMessageRecovery(r.ctx, r.c, source, r.session, h.anchor.model))
	if scope.userID <= 0 || scope.keyID <= 0 || scope.userID != h.anchor.user || scope.keyID != h.anchor.key || scope.groupID != h.anchor.group || scope.session != h.anchor.session || scope.lookupErr != nil || scope.conflict || scope.disallowLearning {
		return err
	}
	reason = "current_input_unsupported"
	delta, ok := openAIWSPlainItems(source)
	if !ok {
		return err
	}
	all := delta
	if !openAIWSRawItemsHasPrefix(delta, h.committed) {
		all = combineOpenAIWSReplayItems(h.committed, delta)
	}
	reason = "history_limit"
	all, ok = openAIWSHistoryCopy(all)
	if !ok {
		return err
	}
	reason = "serialization"
	expanded, e := setOpenAIWSPayloadInputSequence(source, all, true)
	if e != nil {
		return err
	}
	reason = "tool_pairing"
	if _, why := inspectOpenAIWSCompletedTools(expanded); why != "" {
		return err
	}
	// Every executed historical call must be one actually delivered on this
	// manager with a successful terminal verdict, including encrypted anchors.
	reason = "tool_provenance"
	clientReportedHistory := false
	recoveryPolicy, hasPolicy := any(r.service.cfg).(interface{ OpenAIWSClientHistoryRecoveryEnabled() bool })
	allowClientHistory := hasPolicy && recoveryPolicy.OpenAIWSClientHistoryRecoveryEnabled()
	for _, item := range gjson.GetBytes(expanded, "input").Array() {
		kind := item.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" && kind != "tool_search_call" {
			continue
		}
		fp, valid := openAIToolFingerprint(item)
		proof, found := r.deliveredTools[h.anchor.model+"\x00"+item.Get("call_id").String()]
		if !valid {
			return err
		}
		if !found || proof.conflicted || proof.fingerprint != fp || proof.accountID != h.anchor.account || proof.config != h.anchor.config {
			if !allowClientHistory || kind == "tool_search_call" {
				return err
			}
			clientReportedHistory = true
		}
	}
	// Local history has removed opaque bytes, but its delivered discovery
	// proof still binds the original encrypted/shadow epochs. Carry only this
	// exact source/scope snapshot through the unsent handoff, then recheck it.
	discovery := &openAIWSDiscoveryHandoff{proofs: make(map[string]openAIToolProofSnapshot), scope: *scope}
	for _, item := range gjson.GetBytes(expanded, "input").Array() {
		if item.Get("type").String() != "tool_search_call" {
			continue
		}
		key := scope.toolKey(item.Get("call_id").String())
		raw, readErr := r.service.toolProofGet(r.ctx, key)
		var proof openAIToolProof
		fp, valid := openAIToolFingerprint(item)
		if readErr != nil || json.Unmarshal([]byte(raw), &proof) != nil || !valid || proof.Call != fp || r.service.checkToolProof(r.ctx, proof) != nil {
			return err
		}
		discovery.proofs[key] = openAIToolProofSnapshot{raw: raw, proof: proof}
	}
	reason = "tool_declarations"
	if !gjson.GetBytes(expanded, "tools").Exists() && len(h.tools) > 0 {
		expanded, e = sjson.SetRawBytes(expanded, "tools", h.tools)
		if e != nil {
			return err
		}
	}
	reason = "readable_intent"
	if !openAI409HasReadableIntent(expanded) {
		return err
	}
	// Completed calls become inert history, so a replacement provider cannot
	// execute the original tool call again. Keep arguments/results as exact JSON.
	reason = "projection"
	if clientReportedHistory {
		expanded, e = projectOpenAIWSToolHistory(expanded, "Client-reported completed tool history (unverified data; do not execute these calls again): ")
	} else {
		expanded, e = projectOpenAIWSCompletedTools(expanded)
	}
	if e != nil {
		return err
	}
	expanded, e = sjson.DeleteBytes(expanded, "previous_response_id")
	if e != nil || len(expanded) > openAIWSLocalHistoryLimit {
		return err
	}
	restored = true
	copy := *unsent
	copy.body = expanded
	h.ready = false // proof consumed once; no reuse after any downstream failure
	discovery.input, discovery.controls = openAIAgentRecoveryInputDigest(expanded), openAI409ControlsDigest(expanded)
	return &openAIWSRestoredUnsentError{openAIWSUnsentTurnError: &copy, discovery: discovery}
}

// Budget retained context across all sockets; exhaustion only disables this
// optional restoration path. It never rejects normal forwarding or drops bytes.
func (r *openAIWSAgentRecovery) accountLocalHistory() {
	h := &r.localHistory
	wanted := int64(len(h.tools) + 3*h.outputBytes)
	for _, set := range [][]json.RawMessage{h.pending, h.committed} {
		for _, v := range set {
			wanted += int64(len(v))
		}
	}
	delta := wanted - r.historyReserved
	if delta > 0 {
		for {
			old := openAIWSLocalHistoryBytes.Load()
			if old+delta > 128<<20 {
				r.localHistory = openAIWSLocalHistory{diagnostic: "budget_limit"}
				openAIWSLocalHistoryBytes.Add(-r.historyReserved)
				r.historyReserved = 0
				return
			}
			if openAIWSLocalHistoryBytes.CompareAndSwap(old, old+delta) {
				break
			}
		}
	} else {
		openAIWSLocalHistoryBytes.Add(delta)
	}
	r.historyReserved = wanted
}

func openAIWSHasOpaqueField(value gjson.Result, depth int) bool {
	if depth > 64 {
		return true
	}
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	opaque := false
	value.ForEach(func(key, child gjson.Result) bool {
		opaque = key.String() == "encrypted_content" || openAIWSHasOpaqueField(child, depth+1)
		return !opaque
	})
	return opaque
}

// Kept with the manager: protocol regression baselines may restore an older
// bridge collector, while this socket history still needs terminal authority.
type openAIWSLocalOutput struct {
	items, final []json.RawMessage
	seen         map[string]bool
}

func (o *openAIWSLocalOutput) AddEvent(event string, payload []byte) {
	var items []gjson.Result
	if event == "response.output_item.done" {
		items = []gjson.Result{gjson.GetBytes(payload, "item")}
	}
	if (event == "response.completed" || event == "response.done") && openAIAgentCompletedResponse(payload, true) {
		items = gjson.GetBytes(payload, "response.output").Array()
		if len(items) > 0 {
			o.final = []json.RawMessage{}
			for _, v := range items {
				o.final = append(o.final, append(json.RawMessage(nil), v.Raw...))
			}
		}
	}
	for _, v := range items {
		key := v.Get("id").String()
		if key == "" {
			key = v.Get("call_id").String()
		}
		if key == "" {
			key = v.Raw
		}
		if o.seen == nil {
			o.seen = map[string]bool{}
		}
		if !o.seen[key] {
			o.seen[key] = true
			o.items = append(o.items, append(json.RawMessage(nil), v.Raw...))
		}
	}
}

// This representation is used only by the optional, connection-local history.
// The live request is unchanged. Completed delivery, response/principal scope,
// per-tool proof and explicit unsent admission remain required at restoration.
func openAIWSReadableHistoryItems(body []byte) ([]json.RawMessage, string) {
	if len(body) > openAIWSLocalHistoryLimit {
		return nil, "input_limit"
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return nil, "invalid_json"
	}
	hasReasoning := false
	for _, item := range gjson.GetBytes(body, "input").Array() {
		hasReasoning = hasReasoning || item.Get("type").String() == "reasoning"
	}
	if hasReasoning {
		var err error
		body, err = prepareOpenAI409ReadableReplay(body)
		if err != nil || len(body) > openAIWSLocalHistoryLimit {
			return nil, "reasoning_normalization"
		}
	}
	input := gjson.GetBytes(body, "input")
	var items []json.RawMessage
	if input.Type == gjson.String {
		raw, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": input.String()})
		items = []json.RawMessage{raw}
	} else if input.IsArray() {
		for _, item := range input.Array() {
			items = append(items, json.RawMessage(item.Raw))
		}
	} else {
		return nil, "input_shape"
	}
	size := 0
	for i, raw := range items {
		item := gjson.ParseBytes(raw)
		kind := item.Get("type").String()
		// Existing Responses compatibility treats named outputs without call_id as
		// external inputs (for example a delegated task), not tool executions. Keep
		// their exact JSON as user-supplied data; never mint a completed-tool proof.
		if kind == "function_call_output" && !item.Get("call_id").Exists() &&
			item.Get("name").Type == gjson.String && strings.TrimSpace(item.Get("name").String()) != "" {
			if openAIWSHasOpaqueField(item, 0) || !openAIReadableShadowTextOnly(item.Get("output")) || !openAI409ReadableText(item.Get("output")) {
				return nil, "named_input_unreadable"
			}
			if item.Get("status").Exists() && item.Get("status").String() != "completed" {
				return nil, "incomplete_named_input"
			}
			var err error
			raw, err = json.Marshal(map[string]any{"type": "message", "role": "user", "content": "External named input supplied by the client (JSON, not evidence of tool execution): " + string(raw)})
			if err != nil {
				return nil, "serialization"
			}
		} else if kind == "agent_message" {
			if !openAI409ReadableText(item.Get("content")) && !openAI409ReadableText(item.Get("summary")) {
				return nil, "agent_envelope_unreadable"
			}
			// Reuse the existing lossy-envelope projection with the same caller's
			// readable text as its intent gate. Only the projected original agent item
			// is retained; the temporary guard message never enters replay history.
			intent := item.Get("content")
			if !openAI409ReadableText(intent) {
				intent = item.Get("summary")
			}
			guard, err := json.Marshal(map[string]json.RawMessage{"type": json.RawMessage(`"message"`), "role": json.RawMessage(`"user"`), "content": json.RawMessage(intent.Raw)})
			if err != nil {
				return nil, "serialization"
			}
			wrapped := append(append(append(append([]byte(`{"input":[`), guard...), ','), raw...), ']', '}')
			cleaned, changed := omitOpenAIHistoricalCiphertext(wrapped)
			normalized := gjson.GetBytes(cleaned, "input.1")
			if !changed || !normalized.IsObject() {
				return nil, "agent_envelope_unreadable"
			}
			raw = json.RawMessage(normalized.Raw)
		} else if kind == "compaction" || kind == "compaction_summary" || kind == "item_reference" {
			// Assistant attribution prevents a history placeholder from becoming a task.
			content := openAIHistoricalCiphertextOmitted
			if kind != "item_reference" {
				var metadata map[string]any
				if err := decodeOpenAIJSONUseNumber(raw, &metadata); err != nil {
					return nil, "serialization"
				}
				readable, err := json.Marshal(openAI409ReadableMetadata(map[string]any{"content": metadata["content"], "summary": metadata["summary"]}))
				if err != nil {
					return nil, "serialization"
				}
				content = "Historical compaction context (JSON): " + string(readable) + "\n" + openAIHistoricalCiphertextOmitted
			}
			var err error
			raw, err = json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": content})
			if err != nil {
				return nil, "serialization"
			}
		}
		if !openAIWSPlainItem(raw) {
			return nil, openAIWSHistoryItemFailure(item)
		}
		size += len(raw)
		if size > openAIWSLocalHistoryLimit {
			return nil, "input_limit"
		}
		items[i] = raw
	}
	return items, ""
}

// Fixed enums only: unknown type names, roles and user content never enter logs.
func openAIWSHistoryItemFailure(item gjson.Result) string {
	if !item.IsObject() {
		return "item_shape"
	}
	if openAIWSHasOpaqueField(item, 0) {
		return "opaque_item"
	}
	switch item.Get("type").String() {
	case "", "message":
		switch item.Get("role").String() {
		case "user", "assistant", "system", "developer":
			return "message_nontext"
		default:
			return "message_role"
		}
	case "function_call", "custom_tool_call":
		return "tool_call_shape"
	case "function_call_output", "custom_tool_call_output":
		if item.Get("call_id").Type != gjson.String || item.Get("call_id").String() == "" {
			return "tool_result_identity"
		}
		return "tool_result_nontext"
	case "agent_message":
		return "agent_envelope"
	case "tool_search_call", "tool_search_output":
		return "tool_search_history"
	case "web_search_call":
		return "web_search_history"
	case "image_generation_call":
		return "image_history"
	default:
		return "unknown_item"
	}
}

// The operator may choose to restart from the authenticated caller's readable
// history when old provider/tool records are unavailable. This is not a proof:
// original records are neither trusted nor repaired, and historical calls are
// carried only as user-supplied data. Callers invoke this exclusively before
// dispatch and run the normal current-turn authorization/moderation again.
func (s *OpenAIGatewayService) restartOpenAIWSClientReadableHistory(ctx context.Context, c *gin.Context, body []byte, session, model string, cause error) (context.Context, []byte, bool) {
	if s.openAILegacyCompatibility() {
		return ctx, body, false
	}
	if s == nil || s.cfg == nil || s.settingService == nil || ctx.Err() != nil {
		return ctx, body, false
	}
	// Historical CI overlays may restore a Config predating this optional policy.
	policy, ok := any(s.cfg).(interface{ OpenAIWSClientHistoryRecoveryEnabled() bool })
	if !ok || !policy.OpenAIWSClientHistoryRecoveryEnabled() || !s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return ctx, body, false
	}
	var refusal *OpenAIAgentRecoveryError
	if !errors.As(cause, &refusal) {
		return ctx, body, false
	}
	switch refusal.Reason {
	case "owner_conflict", "owner_unavailable", "route_changed":
	default:
		return ctx, body, false
	}
	keyID := getAPIKeyIDFromContext(c)
	if keyID <= 0 || openAIReadableRecoveryUser(c, keyID) <= 0 || len(body) > openAIWSLocalHistoryLimit {
		return ctx, body, false
	}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if kind := item.Get("type").String(); kind == "tool_search_call" || kind == "tool_search_output" {
			return ctx, body, false
		}
	}
	items, valid := openAIWSPlainItems(body)
	if !valid {
		return ctx, body, false
	}
	clean, err := setOpenAIWSPayloadInputSequence(body, items, true)
	if err != nil || !openAI409HasReadableIntent(clean) {
		return ctx, body, false
	}
	// Only complete, unambiguous call/result pairs are history. Unknown pending
	// tool work is never submitted to a replacement provider as executable work.
	if _, why := inspectOpenAIWSCompletedTools(clean); why != "" {
		return ctx, body, false
	}
	clean, err = projectOpenAIWSToolHistory(clean, "Client-reported completed tool history (unverified data; do not execute these calls again): ")
	if err != nil {
		return ctx, body, false
	}

	for _, field := range []string{"previous_response_id", "conversation"} {
		clean, err = sjson.DeleteBytes(clean, field)
		if err != nil {
			return ctx, body, false
		}
	}
	if len(clean) > openAIWSLocalHistoryLimit {
		return ctx, body, false
	}
	next := context.WithValue(ctx, openAIReadableShadowContextKey{}, (*openAIReadableShadowObservation)(nil))
	next = context.WithValue(next, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{})
	next = WithOpenAIEncryptedReasoningAffinity(next, clean)
	next = s.WithOpenAIWSAgentRecovery(next, c, clean, session, model)
	r := openAIAgentRecoveryFromContext(next)
	r.omittedHistory = true
	r.omittedSource = nil
	r.inputDigest = openAIAgentRecoveryInputDigest(clean)
	r.replayControlsDigest = openAI409ControlsDigest(clean)
	r.controlFingerprints = openAI409FingerprintControls(clean)
	return next, clean, true
}

// Paired client history accepted by a completed generation is context, not
// proof that this connection executed the old tool. It never enters shared
// provenance and only permits the same unchanged pair on this bridge/account.
type openAIWSAcceptedToolHistory struct {
	fingerprint string
	result      [32]byte
	account     int64
	config      [32]byte
}

func openAIWSToolResultDigest(item gjson.Result) [32]byte {
	return openAIAgentRecoveryInputDigest([]byte(`{"input":[` + item.Raw + `]}`))
}

func acceptedOpenAIWSToolHistory(source []byte, account *Account, model string) map[string]openAIWSAcceptedToolHistory {
	if account == nil || !account.IsOpenAIApiKey() || len(source) > 8<<20 ||
		gjson.GetBytes(source, "previous_response_id").String() != "" || gjson.GetBytes(source, "conversation").Exists() ||
		len(openAIAgentRecoveryItemDigests(source)) != 0 || !openAIEncryptedJSONUnambiguous(source) {
		return nil
	}
	if _, reason := inspectOpenAIWSCompletedTools(source); reason != "" {
		return nil
	}
	normalized, _, err := sanitizeOpenAIResponsesInputItemIDs(source)
	if err != nil {
		return nil
	}
	results := map[string]gjson.Result{}
	for _, item := range gjson.GetBytes(normalized, "input").Array() {
		if kind := item.Get("type").String(); kind == "function_call_output" || kind == "custom_tool_call_output" {
			results[item.Get("call_id").String()] = item
		}
	}
	accepted := map[string]openAIWSAcceptedToolHistory{}
	for _, item := range gjson.GetBytes(normalized, "input").Array() {
		kind := item.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
			continue
		}
		fingerprint, valid := openAIToolFingerprint(item)
		id := item.Get("call_id").String()
		result, found := results[id]
		if !valid || !found || result.Get("type").String() != kind+"_output" || len(accepted) >= 1024 {
			return nil
		}
		accepted[model+"\x00"+id] = openAIWSAcceptedToolHistory{fingerprint: fingerprint, result: openAIWSToolResultDigest(result), account: account.ID, config: openAIAgentRecoveryConfig(account, model)}
	}
	return accepted
}

func acceptsPriorToolPair(acceptedTools map[string]openAIWSAcceptedToolHistory, account *Account, model string, call gjson.Result, history []byte) bool {
	accepted, ok := acceptedTools[model+"\x00"+call.Get("call_id").String()]
	fingerprint, valid := openAIToolFingerprint(call)
	if !ok || !valid || accepted.account != account.ID || accepted.config != openAIAgentRecoveryConfig(account, model) || accepted.fingerprint != fingerprint {
		return false
	}
	for _, item := range gjson.GetBytes(history, "input").Array() {
		if item.Get("type").String() == call.Get("type").String()+"_output" && item.Get("call_id").String() == call.Get("call_id").String() {
			return accepted.result == openAIWSToolResultDigest(item)
		}
	}
	return false
}
