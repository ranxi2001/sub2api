package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Constructed only at transport boundaries before the CURRENT generation has
// been written. Neither a transport failure nor lack of output proves this.
type openAIWSUnsentTurnError struct {
	cause error
	body  []byte
	model string
	turn  int
}

func (e *openAIWSUnsentTurnError) Error() string {
	return "websocket current turn not sent: " + e.cause.Error()
}
func (e *openAIWSUnsentTurnError) Unwrap() error { return e.cause }

func openAIWSUnsentTurn(err error, body []byte, model string, turn int) error {
	var denied *OpenAITurnAdmissionError
	if turn <= 1 || !errors.As(err, &denied) || !gjson.ValidBytes(body) {
		return err
	}
	switch denied.Reason {
	case "account_ineligible", "account_unavailable", "account_binding_changed", "credential_parent_ineligible", "group_membership_changed":
	default:
		return err // Auth, quota, model policy, store failures and sent work stay terminal.
	}
	return &openAIWSUnsentTurnError{cause: err, body: append([]byte(nil), body...), model: model, turn: turn}
}

func OpenAIWSUnsentTurnPayload(err error) ([]byte, string, int, bool) {
	var unsent *openAIWSUnsentTurnError
	if !errors.As(err, &unsent) {
		return nil, "", 0, false
	}
	return append([]byte(nil), unsent.body...), unsent.model, unsent.turn, true
}

type openAIWSRecoveryPrincipal struct{ userID, keyID int64 }

// The authenticated handler supplies this identity. It grants shadow scope,
// not HTTP response-pointer authorization or permission to replay a generation.
func SetOpenAIWSRecoveryPrincipal(c *gin.Context, userID, keyID int64) {
	c.Set("openai_ws_recovery_principal", openAIWSRecoveryPrincipal{userID, keyID})
}

func openAIReadableRecoveryUser(c *gin.Context, keyID int64) int64 {
	if c != nil && keyID > 0 {
		value, _ := c.Get("openai_ws_recovery_principal")
		if principal, ok := value.(openAIWSRecoveryPrincipal); ok && principal.keyID == keyID && principal.userID > 0 {
			return principal.userID
		}
	}
	return openAIAgentRecoveryHTTPUser(c, keyID)
}

// A new provider conversation may reuse completed results, never unfinished
// tool work. Reject duplicate, orphan or unknown-status tool records.
func openAIWSCompletedToolInputs(body []byte) bool {
	calls, results := map[string]string{}, map[string]string{}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		kind, id := item.Get("type").String(), item.Get("call_id").String()
		switch kind {
		case "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
			if id == "" || (item.Get("status").Exists() && item.Get("status").String() != "completed") {
				return false
			}
			if strings.HasSuffix(kind, "_output") {
				if results[id] != "" || !item.Get("output").Exists() || item.Get("output").Type == gjson.Null {
					return false
				}
				results[id] = kind
			} else {
				if calls[id] != "" {
					return false
				}
				calls[id] = kind + "_output"
			}
		}
	}
	if len(calls) != len(results) {
		return false
	}
	for id, kind := range calls {
		if results[id] != kind {
			return false
		}
	}
	return true
}

// Called before fresh admission/moderation of an unsent frame. Lookup is scoped
// to this exact task; missing historical intent is never fabricated.
func (s *OpenAIGatewayService) RebuildOpenAIWSUnsentTurn(ctx context.Context, c *gin.Context, body []byte, session, model string) (context.Context, []byte, bool, error) {
	if s == nil || s.settingService == nil || !s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return ctx, body, false, nil
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return ctx, body, false, openAIAgentRecoveryError("readable_history_unsupported")
	}
	ctx = context.WithValue(ctx, openAIReadableShadowContextKey{}, (*openAIReadableShadowObservation)(nil))
	ctx, body = s.RestoreOpenAIReadableShadow(ctx, c, body, session)
	next := s.WithOpenAIWSAgentRecovery(ctx, c, body, session, model)
	if !openAIWSCompletedToolInputs(body) {
		return ctx, body, false, openAIAgentRecoveryError("owner_conflict")
	}
	r := openAIAgentRecoveryFromContext(next)
	if len(r.items) == 0 && !openAI409HasProviderState(body) && !r.disallowLearning {
		if !openAI409HasReadableIntent(body) {
			return ctx, body, false, openAIAgentRecoveryError("readable_intent_missing")
		}
		input := gjson.GetBytes(body, "input")
		if input.Type == gjson.String || !input.Exists() || input.Type == gjson.Null {
			text := input.String()
			if input.Type != gjson.String {
				text = gjson.GetBytes(body, "instructions").String()
			}
			encoded, _ := json.Marshal([]any{map[string]any{"type": "message", "role": "user", "content": text}})
			var err error
			body, err = sjson.SetRawBytes(body, "input", encoded)
			if err != nil {
				return ctx, body, false, openAIAgentRecoveryError("readable_history_unsupported")
			}
		}
		clean, err := prepareOpenAI409ReadableReplay(body)
		if err != nil {
			return ctx, body, false, openAIAgentRecoveryError("readable_history_unsupported")
		}
		next = context.WithValue(next, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{})
		next = WithOpenAIEncryptedReasoningAffinity(next, clean)
		r.omittedHistory, r.omittedSource = true, nil
		r.inputDigest, r.replayControlsDigest = openAIAgentRecoveryInputDigest(clean), openAI409ControlsDigest(clean)
		return next, clean, true, nil
	}
	next, clean, changed, err := s.TryRebuildOpenAI409History(next, c, body, session, model, openAIAgentRecoveryError("owner_unavailable"))
	if changed {
		next = context.WithValue(next, openAIWSAgentRecoveryRetryKey{}, &atomic.Bool{})
	}
	return next, clean, changed, err
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
	initial := openAIAgentRecoveryFromContext(r.ctx)
	if initial == nil || !initial.omittedHistory {
		return source, nil
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
	}
	r.readable = &snapshot
	r.readableSource = openAIAgentRecoveryInputDigest(source)
	r.readableControls = openAI409ControlsDigest(source)
	return clean, nil
}
