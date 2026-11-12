package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// These are compatibility observations, not plaintext history or a substitute
// for admission. Redis shares them when available; local adapters expire them
// with affinity. Neither storage mode can reconstruct unknown task contents.
// An old/first-seen payload has no observation and follows ordinary admission.
const openAIAgentRecoveryMaxEntries = 65536

type openAIAgentRecoveryContextKey struct{}

type openAIAgentRecoveryBinding struct {
	accountID  int64
	config     [32]byte
	wire       [32]byte
	expiresAt  time.Time
	conflicted bool
	pending    string
	failed     bool
}

type openAIAgentRecoveryState struct {
	mu       sync.Mutex
	bindings map[[32]byte]openAIAgentRecoveryBinding
}

type openAIAgentRecoveryRequest struct {
	omittedHistory      bool // explicit Beta sanitization; preserve the resulting input and prohibit replay
	disableOwnerBinding bool
	previousResponseID  string
	inputDigest         [32]byte
	keyID               int64
	userID              int64
	delivery            *openAIAgentRecoveryDelivery
	outputs             [][32]byte
	disallowLearning    bool
	nativeCompaction    bool
	lookupErr           error
	toolResult          bool
	known               map[[32]byte]bool
	keys                [][32]byte
	items               [][32]byte
	owner               *openAIAgentRecoveryBinding
	conflict            bool
	groupID             int64
	model               string
	session             string
}

// WithOpenAIAgentMessageRecovery observes only opaque item identities. A route
// becomes eligible for transparent continuation only after this same key,
// group, session and model consumed or generated the intact items on native
// HTTP Responses. Generated ownership routes the first consumption back to its
// producer; it cannot prove a provider will accept its own output in the future.
// Ordinary sticky writes, HTTP 200 and reasoning are not proof.
func (s *OpenAIGatewayService) WithOpenAIAgentMessageRecovery(ctx context.Context, c *gin.Context, body []byte, sessionHash, model string) context.Context {
	request := &openAIAgentRecoveryRequest{groupID: getOpenAIGroupIDFromContext(c), model: model, session: sessionHash}
	request.disableOwnerBinding = s != nil && s.cfg != nil && s.cfg.Gateway.DisableEncryptedAgentOwnerBinding
	if request.disableOwnerBinding {
		request.previousResponseID = strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	}
	keyID := getAPIKeyIDFromContext(c)
	request.keyID = keyID
	request.userID = openAIAgentRecoveryHTTPUser(c, keyID)
	request.nativeCompaction = isOpenAINativeCompactionV2(c)
	request.disallowLearning = isOpenAIResponsesCompactPath(c) || request.nativeCompaction
	if s == nil || keyID <= 0 || sessionHash == "" || model == "" {
		return context.WithValue(ctx, openAIAgentRecoveryContextKey{}, request)
	}
	if (bytes.Contains(body, []byte("agent_message")) || bytes.Contains(body, []byte(`\u`))) &&
		(!utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body)) {
		request.conflict = true
		return context.WithValue(ctx, openAIAgentRecoveryContextKey{}, request)
	}
	request.items = openAIAgentRecoveryItemDigests(body)
	if request.preservesHistory() {
		request.inputDigest = openAIAgentRecoveryInputDigest(body)
	}
	gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
		request.toolResult = request.toolResult || isCodexToolCallOutputItemType(item.Get("type").String())
		return !request.toolResult
	})
	for _, item := range request.items {
		request.keys = append(request.keys, request.itemKey(item))
	}
	state := &s.agentMessageRecovery
	shared, _ := s.cache.(OpenAIAgentRecoveryCache)
	storeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, key := range request.keys {
		binding, found, err := s.awaitOpenAIAgentRecovery(storeCtx, shared, state, key)
		if err != nil {
			request.lookupErr = err
			break
		}
		if !found {
			continue
		}
		if binding.conflicted || (request.ownerBindingRequired() && !sameOpenAIAgentRecoveryRoute(*request.owner, binding)) {
			request.conflict = true
			break
		}
		if request.known == nil {
			request.known = make(map[[32]byte]bool)
		}
		request.known[key] = true
		copy := binding
		request.owner = &copy
	}
	return context.WithValue(ctx, openAIAgentRecoveryContextKey{}, request)
}

func openAIAgentRecoveryFromContext(ctx context.Context) *openAIAgentRecoveryRequest {
	request, _ := ctx.Value(openAIAgentRecoveryContextKey{}).(*openAIAgentRecoveryRequest)
	return request
}

func openAIAgentRecoveryBound(ctx context.Context) bool {
	request := openAIAgentRecoveryFromContext(ctx)
	// Evidence still requires native integrity/delivery protection in opt-out
	// mode. This must not be used to decide whether to pin the producer account.
	return request != nil && (request.owner != nil || request.conflict || request.lookupErr != nil || request.preservesHistory() || request.previousResponseID != "")
}

func (r *openAIAgentRecoveryRequest) ownerBindingRequired() bool {
	return r != nil && r.owner != nil && !r.disableOwnerBinding
}

func (r *openAIAgentRecoveryRequest) preservesHistory() bool {
	return r != nil && (r.omittedHistory || (r.disableOwnerBinding && len(r.items) > 0))
}

func openAIAgentRecoveryPreservesHistory(ctx context.Context) bool {
	return openAIAgentRecoveryFromContext(ctx).preservesHistory()
}

// Keep the complete input, including ordinary history and executed tool
// results. Only JSON object-key ordering is ignored, never array order/numbers.
func openAIAgentRecoveryInputDigest(body []byte) [32]byte {
	decoder := json.NewDecoder(strings.NewReader(gjson.GetBytes(body, "input").Raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return [32]byte{}
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return [32]byte{}
	}
	return sha256.Sum256(canonical)
}

func (s *OpenAIGatewayService) preserveOpenAIAgentHistory(body []byte) bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.DisableEncryptedAgentOwnerBinding && len(openAIAgentRecoveryItemDigests(body)) > 0
}

func openAIAgentRecoveryItemDigests(body []byte) [][32]byte {
	return openAIAgentRecoveryDigests(body, "input")
}

func openAIAgentRecoveryDigests(body []byte, path string) [][32]byte {
	if !bytes.Contains(body, []byte("agent_message")) && !bytes.Contains(body, []byte(`\u`)) {
		return nil
	}
	var items [][32]byte
	visit := func(item gjson.Result) {
		if item.Get("type").String() != "agent_message" {
			return
		}
		opaque := item.Get("encrypted_content").Type == gjson.String && item.Get("encrypted_content").String() != ""
		item.Get("content").ForEach(func(_, part gjson.Result) bool {
			opaque = opaque || part.Get("type").String() == "encrypted_content"
			return true
		})
		if opaque {
			// Hash the entire original item, including author/recipient and the
			// readable envelope. Do not interpret/decrypt the opaque value.
			// Canonicalize JSON object order only; numbers remain exact. Normal
			// request transforms can reorder fields without changing the item.
			decoder := json.NewDecoder(bytes.NewReader([]byte(item.Raw)))
			decoder.UseNumber()
			var value any
			if decoder.Decode(&value) == nil {
				canonical, err := json.Marshal(value)
				if err == nil {
					items = append(items, sha256.Sum256(canonical))
				}
			}
		}
	}
	input := gjson.GetBytes(body, path)
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool { visit(item); return true })
	} else if input.IsObject() {
		visit(input)
	}
	return items
}

func sameOpenAIAgentRecoveryRoute(a, b openAIAgentRecoveryBinding) bool {
	return a.accountID == b.accountID && a.config == b.config && a.wire == b.wire
}

func openAIAgentRecoveryConfig(account *Account, model string) [32]byte {
	// Exact routing/auth identity fails closed on rotation or route edits.
	// No credential or digest is logged, serialized externally or decrypted.
	value, _ := json.Marshal([]any{account.ID, account.Type, account.GetMappedModel(model),
		account.GetOpenAIBaseURL(), account.GetAPIProtocol(), account.GetChatGPTAccountID(),
		account.GetCredential("api_key"), account.GetCredential("access_token"),
		account.IsExcelBPSEnabledForModel(model), account.IsPrismBrowserEnabledForModel(model),
		account.IsOpenAIPassthroughEnabled(), account.IsCopilotSDKEnabled(), account.IsSIWCEnabled()})
	return sha256.Sum256(value)
}

func openAIAgentCompletedResponse(body []byte, nested bool) bool {
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return false
	}
	response := gjson.ParseBytes(body)
	if nested {
		response = response.Get("response")
	}
	return response.Get("status").String() == "completed" && response.Get("error").Type == gjson.Null
}

func (s *OpenAIGatewayService) checkOpenAIAgentRecoveryAccount(ctx context.Context, account *Account) error {
	request := openAIAgentRecoveryFromContext(ctx)
	if request == nil {
		return nil
	}
	if request.lookupErr != nil {
		return request.lookupErr
	}
	if request.conflict {
		return openAIAgentRecoveryError("owner_conflict")
	}
	// Provider-side response state is never made portable by the agent policy,
	// even on a pointer-only request with no known opaque item owner.
	if request.previousResponseID != "" {
		owner, err := s.getOpenAIWSStateStore().GetResponseAccount(ctx, request.groupID, request.previousResponseID)
		if err != nil || owner <= 0 || account == nil || owner != account.ID {
			return openAIAgentRecoveryError("owner_unavailable")
		}
		if request.userID > 0 && !account.IsOpenAIApiKey() {
			return openAIAgentRecoveryError("http_previous_response_unsupported")
		}
	}
	if !request.ownerBindingRequired() {
		return nil
	}
	if account == nil || account.ID != request.owner.accountID {
		return openAIAgentRecoveryError("owner_unavailable")
	}
	if openAIAgentRecoveryConfig(account, request.model) != request.owner.config {
		return openAIAgentRecoveryError("route_changed")
	}
	return nil
}

func (s *OpenAIGatewayService) checkOpenAIAgentRecoveryDispatch(ctx context.Context, c *gin.Context, account *Account, body []byte) error {
	if err := s.checkOpenAIAgentRecoveryAccount(ctx, account); err != nil {
		return err
	}
	if !openAIAgentRecoveryBound(ctx) {
		return nil
	}
	model := gjson.GetBytes(body, "model").String()
	// Native remote compaction v2 stays on the proven HTTP Responses route and is
	// rechecked against the owner's exact wire at send time. The legacy
	// /responses/compact bridge rewrites the body and endpoint, so it remains
	// outside this contract.
	if !account.IsOpenAI() || account.HasManagedOpenAIConnections() || account.AuthConnectionID != "" ||
		account.IsOpenAIPassthroughEnabled() || account.IsCopilotSDKEnabled() || account.IsSIWCEnabled() ||
		account.IsExcelBPSEnabledForModel(model) || account.IsPrismBrowserEnabledForModel(model) ||
		isOpenAIResponsesCompactPath(c) ||
		(isOpenAINativeCompactionV2(c) && shouldForwardDeepSeekResponsesCompactViaChatCompletions(account, body)) ||
		shouldForwardDeepSeekResponsesLiteViaChatCompletions(account, body) ||
		s.resolveUpstreamProtocolFor(ctx, account, APIProtocolResponses, upstreamRoutingModel(account, body, "")) != APIProtocolResponses {
		return openAIAgentRecoveryError("route_changed")
	}
	return nil
}

// This gate is called only at the real native HTTP send, after routing,
// transformation, account revalidation and final credential/header selection.
// It captures the exact admitted route; success publication never re-reads a
// possibly changed account or reconstructs a task from an envelope.
func (s *OpenAIGatewayService) prepareOpenAIAgentRecoveryAttempt(ctx context.Context, account *Account, req *http.Request) (func(bool), error) {
	noop := func(bool) {}
	if ctx.Err() != nil {
		return noop, ctx.Err()
	}
	request := openAIAgentRecoveryFromContext(ctx)
	if request == nil || request.keyID <= 0 || request.session == "" || request.model == "" {
		return noop, nil
	}
	if request.disallowLearning {
		if request.nativeCompaction && (request.owner != nil || request.preservesHistory() || request.previousResponseID != "") {
			return noop, s.checkOpenAIAgentRecoveryCompaction(ctx, account, req)
		}
		return noop, nil
	}
	if request.delivery != nil {
		request.delivery.finish(false)
	}
	request.outputs = nil
	request.delivery = nil
	if err := s.checkOpenAIAgentRecoveryAccount(ctx, account); err != nil {
		return noop, err
	}
	// Managed credentials and adapters need their own route ownership contract.
	if !account.IsOpenAI() || account.HasManagedOpenAIConnections() || account.AuthConnectionID != "" ||
		account.IsOpenAIPassthroughEnabled() || account.IsCopilotSDKEnabled() || account.IsSIWCEnabled() {
		if request.owner != nil {
			return noop, openAIAgentRecoveryError("wire_changed")
		}
		return noop, nil
	}
	if req.GetBody == nil {
		if len(request.items) == 0 && request.owner == nil && request.previousResponseID == "" {
			return noop, nil
		}
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	reader, err := req.GetBody()
	if err != nil {
		if len(request.items) == 0 && request.owner == nil && request.previousResponseID == "" {
			return noop, nil
		}
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		if len(request.items) == 0 && request.owner == nil && request.previousResponseID == "" {
			return noop, nil
		}
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		if len(request.items) == 0 && request.owner == nil && request.previousResponseID == "" {
			return noop, nil
		}
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	if request.preservesHistory() && openAIAgentRecoveryInputDigest(body) != request.inputDigest {
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	if request.previousResponseID != "" && gjson.GetBytes(body, "previous_response_id").String() != request.previousResponseID {
		return noop, openAIAgentRecoveryError("owner_unavailable")
	}
	items := openAIAgentRecoveryItemDigests(body)
	if len(items) != len(request.items) {
		if request.owner != nil {
			return noop, openAIAgentRecoveryError("wire_changed")
		}
		return noop, nil
	}
	for i := range items {
		if items[i] != request.items[i] {
			if request.owner != nil {
				return noop, openAIAgentRecoveryError("wire_changed")
			}
			return noop, nil
		}
	}
	wire := openAIAgentRecoveryWireDigest(req.URL.String(), req.Header, gjson.GetBytes(body, "model").String())
	binding := openAIAgentRecoveryBinding{accountID: account.ID, config: openAIAgentRecoveryConfig(account, request.model), wire: wire, expiresAt: time.Now().Add(s.openAIWSSessionStickyTTL())}
	if request.ownerBindingRequired() && !sameOpenAIAgentRecoveryRoute(*request.owner, binding) {
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	if err := s.checkOpenAIAgentRecoveryRecords(ctx, request, binding); err != nil {
		return noop, err
	}
	delivery := newOpenAIAgentRecoveryDelivery(s, ctx, request, binding)
	request.delivery = delivery
	return func(completed bool) {
		delivery.finish(completed && ctx.Err() == nil)
		if !completed || ctx.Err() != nil || request.disableOwnerBinding {
			// Consumption is not production. In opt-out mode inputs retain their
			// original records (including concurrent fences); only actual new
			// outputs receive the admitted producer's transactional grant above.
			return
		}
		binding.expiresAt = time.Now().Add(s.openAIWSSessionStickyTTL())
		s.publishOpenAIAgentRecoveryInputs(ctx, request, binding)
	}, nil
}

func (s *OpenAIGatewayService) checkOpenAIAgentRecoveryRecords(ctx context.Context, request *openAIAgentRecoveryRequest, binding openAIAgentRecoveryBinding) error {
	shared, _ := s.cache.(OpenAIAgentRecoveryCache)
	if shared != nil {
		storeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		for _, key := range request.keys {
			raw, err := shared.GetOpenAIAgentRecovery(storeCtx, hex.EncodeToString(key[:]))
			if err != nil {
				return openAIAgentRecoveryError("store_unavailable")
			}
			old, found, err := decodeOpenAIAgentRecoveryBinding(raw)
			if err != nil {
				return openAIAgentRecoveryError("store_unavailable")
			}
			if found && (old.conflicted || old.failed || old.pending != "" || (!request.disableOwnerBinding && !sameOpenAIAgentRecoveryRoute(old, binding))) {
				return openAIAgentRecoveryError("owner_conflict")
			}
			if !found && request.known[key] {
				return openAIAgentRecoveryError("owner_unavailable")
			}
		}
	} else {
		state := &s.agentMessageRecovery
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, key := range request.keys {
			old, exists := state.bindings[key]
			found := exists && time.Now().Before(old.expiresAt)
			if found && (old.conflicted || old.failed || old.pending != "" || (!request.disableOwnerBinding && !sameOpenAIAgentRecoveryRoute(old, binding))) {
				return openAIAgentRecoveryError("owner_conflict")
			}
			if request.disableOwnerBinding && !found && request.known[key] {
				return openAIAgentRecoveryError("owner_unavailable")
			}
		}
	}
	return nil
}

func (s *OpenAIGatewayService) publishOpenAIAgentRecoveryInputs(ctx context.Context, request *openAIAgentRecoveryRequest, binding openAIAgentRecoveryBinding) {
	keys := append([][32]byte(nil), request.keys...)
	shared, _ := s.cache.(OpenAIAgentRecoveryCache)
	if shared != nil {
		storeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		for _, key := range keys {
			// After delivery a failed publication cannot be undone. It loses
			// future availability; it never authorizes an unproven owner.
			if err := shared.PublishOpenAIAgentRecovery(storeCtx, hex.EncodeToString(key[:]), encodeOpenAIAgentRecoveryBinding(binding), time.Until(binding.expiresAt)); err != nil {
				logger.L().Warn("openai.agent_recovery_publish_failed", zap.Int64("account_id", binding.accountID), zap.Error(err))
				break
			}
		}
		return
	}
	state := &s.agentMessageRecovery
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.bindings == nil {
		state.bindings = make(map[[32]byte]openAIAgentRecoveryBinding)
	}
	if len(state.bindings) >= openAIAgentRecoveryMaxEntries {
		for key, value := range state.bindings {
			if !time.Now().Before(value.expiresAt) {
				delete(state.bindings, key)
			}
		}
	}
	for _, key := range keys {
		old, exists := state.bindings[key]
		if exists && time.Now().Before(old.expiresAt) && (old.conflicted || old.failed || old.pending != "" || !sameOpenAIAgentRecoveryRoute(old, binding)) {
			old.conflicted = true
			state.bindings[key] = old
			continue
		}
		if !exists && len(state.bindings) >= openAIAgentRecoveryMaxEntries {
			// Saturation loses recovery availability, never task contents.
			continue
		}
		retained := binding
		if exists && old.expiresAt.After(retained.expiresAt) {
			retained.expiresAt = old.expiresAt
		}
		state.bindings[key] = retained
	}
}

func (r *openAIAgentRecoveryRequest) itemKey(item [32]byte) [32]byte {
	scope, _ := json.Marshal([]string{strconv.FormatInt(r.keyID, 10), strconv.FormatInt(r.groupID, 10), r.session, r.model, hex.EncodeToString(item[:])})
	return sha256.Sum256(scope)
}

// Capture only hashes from the final client representation, after terminal
// output reconstruction and tool/name restoration. The attempt's completion
// callback decides whether delivery succeeded; item.done alone grants nothing.
func observeOpenAIAgentRecoveryOutput(c *gin.Context, body []byte, nested bool) error {
	if !bytes.Contains(body, []byte("agent_message")) && !bytes.Contains(body, []byte(`\u`)) {
		return nil
	}
	request := openAIAgentRecoveryFromContext(c.Request.Context())
	if request == nil || request.delivery == nil {
		return nil
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return nil
	}
	path := "output"
	if nested {
		path = "response.output"
	}
	items := openAIAgentRecoveryDigests(body, path)
	// Even an early item/response event can already contain the entire opaque
	// item. Fence it before any bytes are released, without granting success.
	if nested {
		items = append(items, openAIAgentRecoveryDigests(body, "item")...)
	}
	if openAIAgentCompletedResponse(body, nested) {
		request.outputs = openAIAgentRecoveryDigests(body, path)
	}
	return request.delivery.reserve(items)
}

// This does not deduplicate client requests or external tools. It only prevents
// the handler from automatically replaying an ambiguous result submission on a
// proven native task route, including pool-mode same-account retry.
func OpenAIAgentToolResultContinuation(ctx context.Context) bool {
	r := openAIAgentRecoveryFromContext(ctx)
	return r != nil && (r.owner != nil || r.preservesHistory()) && r.toolResult
}

// Opt-out permits one intact attempt, not automatic cross-account retries or
// input-rewriting retries. Strict mode retains its existing tool-result rule.
func OpenAIAgentRecoveryNoReplay(ctx context.Context) bool {
	return OpenAIAgentToolResultContinuation(ctx) || openAIAgentRecoveryPreservesHistory(ctx)
}

// Native remote compaction v2 consumes an already proven task but proves no new
// route, so it is never published. It must reach the recorded owner with the
// same account configuration, URL, identity headers, model and intact opaque
// items. The only tolerated wire difference is the remote_compaction_v2
// negotiation token that the gateway adds to every v2 compaction turn (API key
// routes do not advertise it on ordinary turns).
func (s *OpenAIGatewayService) checkOpenAIAgentRecoveryCompaction(ctx context.Context, account *Account, req *http.Request) error {
	request := openAIAgentRecoveryFromContext(ctx)
	if request == nil || (request.owner == nil && !request.preservesHistory() && request.previousResponseID == "") {
		return nil
	}
	if err := s.checkOpenAIAgentRecoveryAccount(ctx, account); err != nil {
		return err
	}
	if !account.IsOpenAI() || account.HasManagedOpenAIConnections() || account.AuthConnectionID != "" ||
		account.IsOpenAIPassthroughEnabled() || account.IsCopilotSDKEnabled() || account.IsSIWCEnabled() ||
		req == nil || req.URL == nil || req.GetBody == nil {
		return openAIAgentRecoveryError("wire_changed")
	}
	reader, err := req.GetBody()
	if err != nil {
		return openAIAgentRecoveryError("wire_changed")
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return openAIAgentRecoveryError("wire_changed")
	}
	if request.preservesHistory() && openAIAgentRecoveryInputDigest(body) != request.inputDigest {
		return openAIAgentRecoveryError("wire_changed")
	}
	if request.previousResponseID != "" && gjson.GetBytes(body, "previous_response_id").String() != request.previousResponseID {
		return openAIAgentRecoveryError("owner_unavailable")
	}
	items := openAIAgentRecoveryItemDigests(body)
	if len(items) != len(request.items) {
		return openAIAgentRecoveryError("wire_changed")
	}
	for i := range items {
		if items[i] != request.items[i] {
			return openAIAgentRecoveryError("wire_changed")
		}
	}
	if request.disableOwnerBinding {
		return s.checkOpenAIAgentRecoveryRecords(ctx, request, openAIAgentRecoveryBinding{})
	}
	model := gjson.GetBytes(body, "model").String()
	url := req.URL.String()
	if openAIAgentRecoveryWireDigest(url, req.Header, model) == request.owner.wire ||
		openAIAgentRecoveryWireDigest(url, withoutOpenAIRemoteCompactionV2Feature(req.Header), model) == request.owner.wire {
		return nil
	}
	return openAIAgentRecoveryError("wire_changed")
}

func openAIAgentRecoveryWireDigest(url string, header http.Header, model string) [32]byte {
	wire, _ := json.Marshal([]any{url, openAIAgentRecoveryWireHeaders(header), model})
	return sha256.Sum256(wire)
}

// withoutOpenAIRemoteCompactionV2Feature removes only the compaction
// negotiation token; every other beta feature and header stays significant.
func withoutOpenAIRemoteCompactionV2Feature(header http.Header) http.Header {
	stripped := header.Clone()
	for key, values := range stripped {
		if !strings.EqualFold(key, "x-codex-beta-features") {
			continue
		}
		kept := make([]string, 0, len(values))
		for _, value := range values {
			tokens := make([]string, 0, 2)
			for _, token := range strings.Split(value, ",") {
				if token = strings.TrimSpace(token); token != "" && token != openAIRemoteCompactionV2Feature {
					tokens = append(tokens, token)
				}
			}
			if len(tokens) > 0 {
				kept = append(kept, strings.Join(tokens, ","))
			}
		}
		if len(kept) == 0 {
			delete(stripped, key)
		} else {
			stripped[key] = kept
		}
	}
	return stripped
}

// Header overrides may retain wire casing rather than net/http's canonical
// spelling. Header.Get/Values would miss those actual outbound identity fields.
// Include every spelling/value deterministically, including duplicate variants.
func openAIAgentRecoveryWireHeaders(header http.Header) []any {
	var keys []string
	for key := range header {
		switch strings.ToLower(key) {
		case "authorization", "chatgpt-account-id", "openai-organization", "openai-project", "openai-beta", "x-codex-beta-features", strings.ToLower(responsesLiteHeader):
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	values := make([]any, 0, len(keys))
	for _, key := range keys {
		values = append(values, []any{key, header[key]})
	}
	return values
}
