package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// These are compatibility observations, not plaintext history or a substitute
// for admission. They are deliberately process-local and expire with affinity.
// An old/first-seen payload has no observation and follows ordinary admission.
const openAIAgentRecoveryMaxEntries = 65536

var errOpenAIAgentRecoveryUnavailable = errors.New("encrypted agent continuation has no unchanged compatible route")

// OpenAIAgentRecoveryError is a local compatibility refusal, not a failure of
// the entire account pool. Reasons contain no route IDs, credentials or input.
type OpenAIAgentRecoveryError struct {
	Reason string
}

func (e *OpenAIAgentRecoveryError) Error() string { return e.ClientMessage() }
func (e *OpenAIAgentRecoveryError) Unwrap() error { return errOpenAIAgentRecoveryUnavailable }
func (e *OpenAIAgentRecoveryError) ClientCode() string {
	return "encrypted_agent_recovery_" + e.Reason
}
func (e *OpenAIAgentRecoveryError) ClientMessage() string {
	var reason string
	switch e.Reason {
	case "owner_conflict":
		reason = "The encrypted agent items have conflicting compatible owners."
	case "route_changed":
		reason = "The proven encrypted agent route or credentials changed."
	case "wire_changed":
		reason = "The proven encrypted agent HTTP transport changed."
	case "store_unavailable":
		reason = "Encrypted agent compatibility records are temporarily unavailable."
	default:
		reason = "The proven encrypted agent owner is currently unavailable."
	}
	return reason + " Preserve this task and context; restore the original compatible route before continuing."
}

func openAIAgentRecoveryError(reason string) error {
	return &OpenAIAgentRecoveryError{Reason: reason}
}

type openAIAgentRecoveryContextKey struct{}

type openAIAgentRecoveryBinding struct {
	accountID  int64
	config     [32]byte
	wire       [32]byte
	expiresAt  time.Time
	conflicted bool
}

type openAIAgentRecoveryState struct {
	mu       sync.Mutex
	bindings map[[32]byte]openAIAgentRecoveryBinding
}

type openAIAgentRecoveryRequest struct {
	keys     [][32]byte
	items    [][32]byte
	owner    *openAIAgentRecoveryBinding
	conflict bool
	groupID  int64
	model    string
	session  string
}

// WithOpenAIAgentMessageRecovery observes only opaque item identities. A route
// becomes eligible for transparent continuation only after this same key,
// group, session and model successfully sent the intact items through native
// HTTP Responses. Ordinary sticky writes, HTTP 200 and reasoning are not proof.
func (s *OpenAIGatewayService) WithOpenAIAgentMessageRecovery(ctx context.Context, c *gin.Context, body []byte, sessionHash, model string) context.Context {
	request := &openAIAgentRecoveryRequest{groupID: getOpenAIGroupIDFromContext(c), model: model, session: sessionHash}
	keyID := getAPIKeyIDFromContext(c)
	if s == nil || keyID <= 0 || sessionHash == "" || model == "" {
		return context.WithValue(ctx, openAIAgentRecoveryContextKey{}, request)
	}
	if (bytes.Contains(body, []byte("agent_message")) || bytes.Contains(body, []byte(`\u`))) &&
		(!utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body)) {
		request.conflict = true
		return context.WithValue(ctx, openAIAgentRecoveryContextKey{}, request)
	}
	request.items = openAIAgentRecoveryItemDigests(body)
	if len(request.items) == 0 {
		return ctx
	}
	for _, item := range request.items {
		scope, _ := json.Marshal([]string{strconv.FormatInt(keyID, 10), strconv.FormatInt(request.groupID, 10), sessionHash, model, hex.EncodeToString(item[:])})
		request.keys = append(request.keys, sha256.Sum256(scope))
	}
	state := &s.agentMessageRecovery
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, key := range request.keys {
		binding, found := state.bindings[key]
		if !found || !time.Now().Before(binding.expiresAt) {
			continue
		}
		if binding.conflicted || (request.owner != nil && !sameOpenAIAgentRecoveryRoute(*request.owner, binding)) {
			request.conflict = true
			break
		}
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
	return request != nil && (request.owner != nil || request.conflict)
}

func openAIAgentRecoveryItemDigests(body []byte) [][32]byte {
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
	input := gjson.GetBytes(body, "input")
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
	if request == nil || (!request.conflict && request.owner == nil) {
		return nil
	}
	if request.conflict {
		return openAIAgentRecoveryError("owner_conflict")
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
	if !account.IsOpenAI() || account.HasManagedOpenAIConnections() || account.AuthConnectionID != "" ||
		account.IsOpenAIPassthroughEnabled() || account.IsCopilotSDKEnabled() || account.IsSIWCEnabled() ||
		account.IsExcelBPSEnabledForModel(model) || account.IsPrismBrowserEnabledForModel(model) ||
		isOpenAIResponsesCompactPath(c) || isOpenAINativeCompactionV2(c) ||
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
func (s *OpenAIGatewayService) prepareOpenAIAgentRecoveryAttempt(ctx context.Context, account *Account, req *http.Request, body []byte) (func(bool), error) {
	noop := func(bool) {}
	if ctx.Err() != nil {
		return noop, ctx.Err()
	}
	request := openAIAgentRecoveryFromContext(ctx)
	if request == nil || len(request.keys) == 0 {
		return noop, nil
	}
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
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	reader, err := req.GetBody()
	if err != nil {
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	body, err = io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return noop, openAIAgentRecoveryError("wire_changed")
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
	wire, _ := json.Marshal([]any{req.URL.String(), req.Header.Get("Authorization"),
		req.Header.Get("ChatGPT-Account-Id"), req.Header.Get("OpenAI-Organization"), req.Header.Get("OpenAI-Project"),
		req.Header.Values("OpenAI-Beta"), req.Header.Values("x-codex-beta-features"), req.Header.Values(responsesLiteHeader),
		gjson.GetBytes(body, "model").String()})
	binding := openAIAgentRecoveryBinding{accountID: account.ID, config: openAIAgentRecoveryConfig(account, request.model), wire: sha256.Sum256(wire), expiresAt: time.Now().Add(s.openAIWSSessionStickyTTL())}
	if request.owner != nil && !sameOpenAIAgentRecoveryRoute(*request.owner, binding) {
		return noop, openAIAgentRecoveryError("wire_changed")
	}
	state := &s.agentMessageRecovery
	state.mu.Lock()
	for _, key := range request.keys {
		if old, exists := state.bindings[key]; exists && time.Now().Before(old.expiresAt) &&
			(old.conflicted || !sameOpenAIAgentRecoveryRoute(old, binding)) {
			state.mu.Unlock()
			return noop, openAIAgentRecoveryError("owner_conflict")
		}
	}
	state.mu.Unlock()
	return func(completed bool) {
		if !completed {
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
		for _, key := range request.keys {
			old, exists := state.bindings[key]
			if exists && time.Now().Before(old.expiresAt) && (old.conflicted || !sameOpenAIAgentRecoveryRoute(old, binding)) {
				old.conflicted = true
				state.bindings[key] = old
				continue
			}
			if !exists && len(state.bindings) >= openAIAgentRecoveryMaxEntries {
				// Saturation loses recovery availability, never task contents.
				continue
			}
			state.bindings[key] = binding
		}
	}, nil
}
