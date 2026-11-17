package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// PrepareOpenAIAgentReplayBody is a pre-snapshot stage, never a final-wire
// exemption. Only output metadata is portable here. Readable reasoning,
// ciphertext, tool relationships, array order and JSON numbers remain intact.
// The caller must forward these bytes and freeze them before account builders.
func (s *OpenAIGatewayService) PrepareOpenAIAgentReplayBody(body []byte) ([]byte, error) {
	if !s.preserveOpenAIAgentHistory(body) {
		return body, nil
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return nil, openAIAgentRecoveryError("wire_changed")
	}
	return normalizeOpenAIReplayMetadata(body)
}

// Derive the selected account's native input from the immutable source before
// other builders run. OAuth stateless metadata and administrator-enabled manual
// timezone normalization are the only permitted projections here.
func (s *OpenAIGatewayService) prepareOpenAIAgentOAuthReplay(ctx context.Context, account *Account, body []byte) (context.Context, []byte, error) {
	r := openAIAgentRecoveryFromContext(ctx)
	if r == nil || !r.disableOwnerBinding || !r.preservesHistory() || r.omittedHistory || r.omitted400History || account == nil || !account.IsOpenAI() {
		return ctx, body, nil
	}
	if len(r.replaySource) == 0 || openAIAgentRecoveryInputDigest(body) != r.inputDigest ||
		openAI409ControlsDigest(body) != openAI409ControlsDigest(r.replaySource) {
		return ctx, nil, openAIAgentRecoveryRefusal(ctx, "input_integrity")
	}
	projected, err := s.projectOpenAIAgentAccountReplay(ctx, account, r.replaySource, "http")
	if err != nil {
		return ctx, nil, err
	}
	next, err := sjson.SetRawBytes(body, "input", []byte(gjson.GetBytes(projected, "input").Raw))
	if err != nil {
		return ctx, nil, err
	}
	if tools := gjson.GetBytes(projected, "tools"); tools.Exists() {
		next, err = sjson.SetRawBytes(next, "tools", []byte(tools.Raw))
		if err != nil {
			return ctx, nil, err
		}
	}
	private := *r
	private.nativeInputDigest = openAIAgentRecoveryInputDigest(projected)
	private.nativeReplayPrepared = true
	private.nativeAccountID = account.ID
	return context.WithValue(ctx, openAIAgentRecoveryContextKey{}, &private), next, nil
}

func (s *OpenAIGatewayService) projectOpenAIAgentAccountReplay(ctx context.Context, account *Account, source []byte, transport string) ([]byte, error) {
	projected := append([]byte(nil), source...)
	if account.UsesOpenAICodexProtocol() {
		var err error
		projected, err = projectOpenAIOAuthReasoningMetadata(projected)
		if err != nil {
			return nil, err
		}
	}
	// Auto mode retains its existing transport-boundary body preservation.
	projected = s.normalizeRequestTimezone(ctx, account, projected, transport)
	// An opaque item's readable envelope is part of its protected identity,
	// even when role=user makes it eligible for the general timezone builder.
	// Refuse that projection; never authorize it by replacing the item proof.
	if !slices.Equal(openAIAgentRecoveryItemDigests(source), openAIAgentRecoveryItemDigests(projected)) {
		return nil, openAIAgentRecoveryRefusal(ctx, "input_integrity")
	}
	return projected, nil
}

// Match the existing OAuth stateless reasoning contract without its general
// input filter: no readable content, ciphertext, item or tool relationship is
// removed. Missing/null summary becomes []; a nonempty summary stays exact.
func projectOpenAIOAuthReasoningMetadata(body []byte) ([]byte, error) {
	var source map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &source); err != nil {
		return nil, err
	}
	input, _ := source["input"].([]any)
	for _, value := range input {
		item, ok := value.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "reasoning" {
			continue
		}
		delete(item, "id")
		if item["summary"] == nil {
			item["summary"] = []any{}
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "input", encoded)
}

func normalizeOpenAIResponsesReasoningContentReplayProtected(body []byte, protected bool) ([]byte, bool, error) {
	if protected {
		return body, false, nil
	}
	return normalizeOpenAIResponsesReasoningContentReplay(body)
}

func normalizeOpenAIAPIKeyStoreFalseReasoningReplayProtected(body []byte, knownStoreFalse, protected bool) ([]byte, bool, error) {
	if protected {
		return body, false, nil
	}
	return normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, knownStoreFalse)
}

// The allowlist is internal and fixed. Never attach bodies, ciphertext, item
// digests, route URLs or credentials, including in unexpected-stage fallback.
func openAIAgentRecoveryRefusal(ctx context.Context, stage string) error {
	switch stage {
	case "input_integrity", "body_unreadable", "unsupported_adapter", "route_identity":
	default:
		stage = "route_identity"
	}
	if r := openAIAgentRecoveryFromContext(ctx); r != nil && r.disableOwnerBinding {
		logger.FromContext(ctx).Warn("openai.encrypted_agent_recovery_refused", zap.String("stage", stage))
	}
	return openAIRecoveryDiagnosticError("wire_changed", stage, openAIAgentRecoveryFromContext(ctx))
}

// Shared with readable Beta preparation. This deliberately excludes OAuth
// system promotion, namespace flattening, timezone and call reconstruction.
func normalizeOpenAIReplayMetadata(body []byte) ([]byte, error) {
	clean, _, err := sanitizeOpenAIResponsesInputItemIDs(body)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if err := decodeOpenAIJSONUseNumber(clean, &source); err != nil {
		return nil, err
	}
	input, _ := source["input"].([]any)
	storeFalse := gjson.GetBytes(clean, "store").Type == gjson.False
	changed := false
	for _, value := range input {
		item, ok := value.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "reasoning" {
			continue
		}
		if _, exists := item["status"]; exists {
			delete(item, "status")
			changed = true
		}
		encrypted, _ := item["encrypted_content"].(string)
		if !storeFalse || strings.TrimSpace(encrypted) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(firstNonEmptyString(item["id"])), "rs_") {
			delete(item, "id")
			changed = true
		}
		if item["summary"] == nil {
			item["summary"] = []any{}
			changed = true
		}
	}
	if !changed {
		return clean, nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(clean, "input", encoded)
}
