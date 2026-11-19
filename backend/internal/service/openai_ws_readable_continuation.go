package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Constructed only at transport boundaries before the CURRENT generation has
// been written. Neither a transport failure nor lack of output proves this.
type openAIWSUnsentTurnError struct {
	cause           error
	body            []byte
	model           string
	turn            int
	keepAccount     bool // an owner mismatch does not disqualify a healthy selected account
	completedSource *openAIAgentRecoveryRequest
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

// Retains send-boundary evidence even when the specific guard is terminal.
type openAIWSRecoveryBeforeSendError struct{ cause error }

func (e *openAIWSRecoveryBeforeSendError) Error() string { return e.cause.Error() }
func (e *openAIWSRecoveryBeforeSendError) Unwrap() error { return e.cause }
func OpenAIWSAgentRecoveryBeforeSend(err error) bool {
	var before *openAIWSRecoveryBeforeSendError
	return errors.As(err, &before)
}

// Losing ownership already observed on this socket is not ordinary account
// unavailability. It must never become permission to discard that fence.
type openAIWSLostOwnerEvidenceError struct{ cause error }

func (e *openAIWSLostOwnerEvidenceError) Error() string { return e.cause.Error() }
func (e *openAIWSLostOwnerEvidenceError) Unwrap() error { return e.cause }

// Only the account/route compatibility check may grant this evidence. The same
// public code from a later integrity or delivery fence stays terminal.
type openAIWSOwnerRouteError struct {
	cause                error
	completedMixedOwners bool
	completedSource      *openAIAgentRecoveryRequest
}

func (e *openAIWSOwnerRouteError) Error() string { return e.cause.Error() }
func (e *openAIWSOwnerRouteError) Unwrap() error { return e.cause }

// Called only before the current generation's first physical write. A guard
// refusal on an internal retry is not proof that earlier work was unsent.
func openAIWSUnsentAgentTurn(err error, body []byte, model string, turn int) error {
	closed := openAIWSAgentRecoveryClose(&openAIWSRecoveryBeforeSendError{cause: err})
	var lost *openAIWSLostOwnerEvidenceError
	if errors.As(err, &lost) {
		return closed
	}
	var route *openAIWSOwnerRouteError
	var recovery *OpenAIAgentRecoveryError
	if turn < 1 || !errors.As(err, &route) || !errors.As(err, &recovery) || !gjson.ValidBytes(body) {
		return closed
	}
	switch recovery.Reason {
	case "owner_conflict":
		if !route.completedMixedOwners {
			return closed
		}
	case "owner_unavailable", "route_changed":
		// Rebuild still checks completed ownership, delivery fences, readable
		// intent, tool results and final payload integrity. Do not turn wire
		// mutation, pending/failed/conflicting ownership or store errors into retries.
	default:
		return closed
	}
	return &openAIWSUnsentTurnError{cause: closed, body: append([]byte(nil), body...), model: model, turn: turn, keepAccount: true, completedSource: route.completedSource}
}

type openAIWSUnsentOwnerProofKey struct{}
type openAIWSUnsentAdmissionKey struct{}

// Carry the route check's observations through the handler's auth boundary.
// Rebuild consumes this current-turn evidence once; it cannot leak to later turns.
func WithOpenAIWSUnsentOwnerProof(ctx context.Context, err error) context.Context {
	var recovered interface {
		RecoveredWSContext(context.Context) context.Context
	}
	if errors.As(err, &recovered) {
		ctx = recovered.RecoveredWSContext(ctx)
	}
	var unsent *openAIWSUnsentTurnError
	var admission *OpenAITurnAdmissionError
	if errors.As(err, &unsent) && !unsent.keepAccount && errors.As(err, &admission) {
		ctx = context.WithValue(ctx, openAIWSUnsentAdmissionKey{}, unsent)
	}
	if errors.As(err, &unsent) && unsent.completedSource != nil {
		return context.WithValue(ctx, openAIWSUnsentOwnerProofKey{}, unsent.completedSource)
	}
	return ctx
}

// Keep completed results as data in a new provider conversation. Historical
// call type, identity and original arguments stay paired with each result as
// inert JSON text. Current declarations remain available for new tool work.
func projectOpenAIWSCompletedTools(body []byte) ([]byte, error) {
	return projectOpenAIWSToolHistory(body, "Historical completed tool result (data only; do not execute again): ")
}

func OpenAIWSUnsentTurnKeepsAccount(err error) bool {
	var unsent *openAIWSUnsentTurnError
	return errors.As(err, &unsent) && unsent.keepAccount
}

func OpenAIWSUnsentTurnPayload(err error) ([]byte, string, int, bool) {
	var unsent *openAIWSUnsentTurnError
	if !errors.As(err, &unsent) {
		return nil, "", 0, false
	}
	return append([]byte(nil), unsent.body...), unsent.model, unsent.turn, true
}

// ResetOpenAIWSReadableProviderSession drops only the provider-specific smart
// session at an authorized unsent rebuild. Client identity and owner records
// remain untouched. A generation already crossing the write boundary cannot reset it.
func ResetOpenAIWSReadableProviderSession(ctx context.Context, c *gin.Context) context.Context {
	r := openAIAgentRecoveryFromContext(ctx)
	blocked, _ := ctx.Value(openAIWSAgentRecoveryRetryKey{}).(*atomic.Bool)
	if c == nil || r == nil || !r.omittedHistory || r.delivery != nil || blocked == nil || blocked.Load() {
		return ctx
	}
	c.Set(smartProtocolSessionKey, (*openAISmartProtocolSession)(nil))
	return context.WithValue(ctx, openAIWSReadableProviderKey{}, true)
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
	_, reason := inspectOpenAIWSCompletedTools(body)
	return reason == ""
}

// Called before fresh admission/moderation of an unsent frame. Lookup is scoped
// to this exact task; missing historical intent is never fabricated.
func (s *OpenAIGatewayService) RebuildOpenAIWSUnsentTurn(ctx context.Context, c *gin.Context, body []byte, session, model string) (nextCtx context.Context, nextBody []byte, changed bool, resultErr error) {
	if s.openAILegacyCompatibility() {
		return ctx, body, false, nil
	}
	originalBody := body
	unsent, _ := ctx.Value(openAIWSUnsentAdmissionKey{}).(*openAIWSUnsentTurnError)
	ctx = context.WithValue(ctx, openAIWSUnsentAdmissionKey{}, (*openAIWSUnsentTurnError)(nil))
	defer func() {
		if resultErr != nil {
			if restarter, available := any(s).(interface {
				restartOpenAIWSClientReadableHistory(context.Context, *gin.Context, []byte, string, string, error) (context.Context, []byte, bool)
			}); available {
				if next, clean, ok := restarter.restartOpenAIWSClientReadableHistory(ctx, c, originalBody, session, model, resultErr); ok {
					nextCtx, nextBody, changed, resultErr = next, clean, true, nil
				}
			}
			// A result-only delta is valid while its response anchor is alive.
			// Once unsent admission loses that lane and local history cannot be
			// rebuilt, ask the client for its full canonical input. Codex handles
			// this standard code by retrying the full request. This does not grant
			// ownership or authorize replay: the next request passes every gate.
			var recovery *OpenAIAgentRecoveryError
			if unsent != nil && errors.As(resultErr, &recovery) && recovery.Reason == "owner_conflict" &&
				recovery.InternalSubreason() == "tool_unpaired" && recovery.diagnostic != nil &&
				recovery.diagnostic.owners.observed && recovery.diagnostic.owners.total == 0 &&
				gjson.GetBytes(originalBody, "previous_response_id").String() != "" &&
				gjson.GetBytes(originalBody, "previous_response_id").Raw == gjson.GetBytes(unsent.body, "previous_response_id").Raw &&
				openAIAgentRecoveryInputDigest(originalBody) == openAIAgentRecoveryInputDigest(unsent.body) {
				counts, why := inspectOpenAIWSCompletedTools(originalBody)
				if why == "tool_unpaired" && counts.calls == 0 && counts.results > 0 &&
					len(gjson.GetBytes(originalBody, "input").Array()) == counts.results {
					copy := *recovery
					copy.Reason = "continuation_history_missing"
					resultErr = &copy
				}
			}
		}
	}()

	proof, _ := ctx.Value(openAIWSUnsentOwnerProofKey{}).(*openAIAgentRecoveryRequest)
	ctx = context.WithValue(ctx, openAIWSUnsentOwnerProofKey{}, (*openAIAgentRecoveryRequest)(nil))
	if s == nil || s.settingService == nil || !s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) {
		return ctx, body, false, nil
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return ctx, body, false, openAIAgentRecoveryError("readable_history_unsupported")
	}
	ctx = context.WithValue(ctx, openAIReadableShadowContextKey{}, (*openAIReadableShadowObservation)(nil))
	ctx, body = s.RestoreOpenAIReadableShadow(ctx, c, body, session)
	// A missing declaration field inherits the callable tools on this client
	// socket. Resolve it BEFORE freezing controls, never from the final wire.
	// Explicit empty/null declarations retain their replacement semantics.
	if _, present := openAIWSHTTPBridgeRawField(body, "tools"); !present {
		if state, ok := openAIWSHTTPBridgeToolStateFromContext(c); ok && len(state.ClientTools) > 0 {
			var err error
			body, err = sjson.SetRawBytes(body, "tools", state.ClientTools)
			if err != nil {
				return ctx, body, false, openAIAgentRecoveryError("readable_history_unsupported")
			}
		}
	}
	if next, clean, changed := s.PrepareOpenAILossyHistoryRestart(ctx, c, body, session, model); changed {
		return next, clean, true, nil
	}
	next := s.WithOpenAIWSAgentRecovery(ctx, c, body, session, model)
	r := openAIAgentRecoveryFromContext(next)
	if proof != nil {
		if r.lookupErr != nil {
			return ctx, body, false, r.lookupErr
		}
		if !r.hasCompletedMixedOwners() || len(r.keys) != len(proof.keys) {
			return ctx, body, false, openAIRecoveryDiagnosticError("owner_conflict", "owner_record_changed", r)
		}
		for _, key := range proof.keys {
			actual, found := r.completedOwners[key]
			if !found || !sameOpenAIAgentRecoveryRoute(proof.completedOwners[key], actual) {
				return ctx, body, false, openAIRecoveryDiagnosticError("owner_conflict", "owner_record_changed", r)
			}
		}
	}
	if _, reason := inspectOpenAIWSCompletedTools(body); reason != "" {
		return ctx, body, false, openAIRecoveryDiagnosticError("owner_conflict", reason, r)
	}
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
		if err := s.verifyOpenAIToolInputs(next, r, body); err != nil {
			return ctx, body, false, err
		}
		clean, err := prepareOpenAI409ReadableReplay(body)
		if err != nil {
			return ctx, body, false, openAIAgentRecoveryError("readable_history_unsupported")
		}
		next = context.WithValue(next, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{})
		next = WithOpenAIEncryptedReasoningAffinity(next, clean)
		r.omittedHistory, r.omittedSource = true, nil
		r.inputDigest, r.replayControlsDigest = openAIAgentRecoveryInputDigest(clean), openAI409ControlsDigest(clean)
		r.controlFingerprints = openAI409FingerprintControls(clean)
		return next, clean, true, nil
	}
	next, clean, changed, err := s.TryRebuildOpenAI409History(next, c, body, session, model, openAIAgentRecoveryError("owner_unavailable"))
	if changed {
		next = context.WithValue(next, openAIWSAgentRecoveryRetryKey{}, &atomic.Bool{})
	}
	return next, clean, changed, err
}
