package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
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
	deliveredTools                   map[string]openAIWSDeliveredTool
	pendingTools                     map[string]openAIWSDeliveredTool
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
	// Rebuilding one generation does not turn every later native delta into
	// complete replay history. A response actually delivered by this manager
	// can continue on that same account; begin still verifies its current
	// authoritative pointer, account admission and actual outgoing bytes.
	if initial != nil && initial.omittedHistory && account != nil && account.ID == r.completedAccount &&
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
	if initial == nil || !initial.omittedHistory {
		clean, err := r.service.PrepareOpenAIAgentReplayBody(source)
		if err != nil {
			return nil, err
		}
		if r.service.preserveOpenAIAgentHistory(source) {
			if account.UsesOpenAICodexProtocol() {
				clean, err = projectOpenAIOAuthReasoningMetadata(clean)
				if err != nil {
					return nil, err
				}
			}
			// Freeze before compatibility, timezone, OAuth or tool builders.
			// begin may refresh owner records, but cannot trust a new input.
			model := gjson.GetBytes(source, "model").String()
			if initial != nil {
				model = initial.model
			}
			ctx := r.service.WithOpenAIAgentMessageRecovery(r.ctx, r.c, clean, r.session, model)
			r.protected = openAIAgentRecoveryFromContext(ctx)
			r.protectedSource = openAIAgentRecoveryInputDigest(source)
			r.protectedControls = openAI409ControlsDigest(source)
		}
		return append([]byte(nil), clean...), nil
	}
	ctx, clean := r.ctx, source
	if openAIAgentRecoveryInputDigest(source) != initial.inputDigest || openAI409ControlsDigest(source) != initial.replayControlsDigest {
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
	if req.GetBody != nil {
		// HTTP builders can still normalize the prepared body. Validate the
		// built request's bytes, rather than replacing them with the earlier
		// bridge payload and accidentally granting a transformed opaque item.
		reader, err := req.GetBody()
		if err != nil {
			return openAIAgentRecoveryError("wire_changed")
		}
		actual, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			return openAIAgentRecoveryError("wire_changed")
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
			return openAIAgentRecoveryError("wire_changed")
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
			return openAIAgentRecoveryError("wire_changed")
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
			if kind != "function_call" && kind != "custom_tool_call" {
				continue
			}
			fingerprint, valid := openAIToolFingerprint(item)
			observed, found := r.deliveredTools[r.request.model+"\x00"+item.Get("call_id").String()]
			if !valid || !found || observed.conflicted || observed.fingerprint != fingerprint || observed.accountID != account.ID || observed.config != openAIAgentRecoveryConfig(account, r.request.model) {
				return openAIRecoveryDiagnosticError("owner_conflict", "tool_provenance_unknown", r.request)
			}
		}
		r.request.previousResponseID = ""
		projected = true
	}
	if !projected && !r.request.omittedHistory && previous != "" && (r.request.owner != nil || r.request.disableOwnerBinding || r.request.previousResponseID != "") &&
		gjson.GetBytes(payload, "previous_response_id").String() != previous {
		return openAIAgentRecoveryError("wire_changed")
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
	toolItems := gjson.GetBytes(payload, "response.output").Array()
	if event == "response.output_item.done" {
		toolItems = append(toolItems, gjson.GetBytes(payload, "item"))
	}
	for _, item := range toolItems {
		kind := item.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
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
				observed.conflicted = observed.conflicted || old.conflicted || exists
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
		r.finish = nil
	}
	return nil
}

func openAIWSAgentRecoveryClose(err error) error {
	return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "agent task continuation is unavailable; original task was preserved", err)
}
