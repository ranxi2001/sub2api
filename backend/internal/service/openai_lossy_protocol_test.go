//go:build unit

package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAILossyProtocolPreservesAgentInput(t *testing.T) {
	for name, body := range map[string]string{
		"inherited_tools_delta": `{"previous_response_id":"resp_fixture","input":[{"role":"user","content":"continue fixture task"}]}`,
		"conversation_delta":    `{"conversation":"fixture_conversation","input":"continue fixture task"}`,
		"declarations":          `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}],"previous_response_id":"resp_fixture","input":[{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"additional_tools":      `{"additional_tools":[{"type":"function","name":"fixture"}],"input":[{"type":"reasoning","encrypted_content":"fixture-opaque"}]}`,
		"function_pair":         `{"previous_response_id":"resp_fixture","input":[{"type":"function_call","call_id":"call_fixture","namespace":"functions","name":"exec","arguments":"{\"n\":9007199254740993}"},{"type":"function_call_output","call_id":"call_fixture","output":"fixture result"}]}`,
		"custom_pair":           `{"input":[{"type":"custom_tool_call","call_id":"call_fixture","name":"exec","input":"fixture command"},{"type":"custom_tool_call_output","call_id":"call_fixture","output":"fixture result"}]}`,
		"discovery":             `{"input":[{"type":"tool_search_call","call_id":"search_fixture","arguments":{}},{"type":"tool_search_output","call_id":"search_fixture","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}]}`,
		"orphan":                `{"input":[{"type":"function_call_output","call_id":"missing","output":"fixture result"},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"pending":               `{"input":[{"type":"function_call","call_id":"call_fixture","name":"fixture","arguments":"{}","status":"in_progress"}]}`,
		"compact_trigger":       `{"input":[{"type":"agent_message","encrypted_content":"fixture-opaque"},{"type":"compaction_trigger"}]}`,
		"mcp_discovery":         `{"input":[{"type":"mcp_list_tools","server_label":"fixture","tools":[{"name":"exec","input_schema":{"type":"object"}}]}]}`,
		"mcp_approval_request":  `{"input":[{"type":"mcp_approval_request","id":"approval_fixture","name":"exec","arguments":"{}","server_label":"fixture"}]}`,
		"mcp_approval_response": `{"input":[{"type":"mcp_approval_response","approval_request_id":"approval_fixture","approve":true}]}`,
		"unknown_item":          `{"input":[{"type":"future_tool_catalog","tools":[{"name":"exec"}]},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"malformed_item":        `{"input":[null,{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"nested_tool":           `{"input":[{"role":"assistant","content":[{"type":"tool_search_output","tools":[{"name":"exec"}]}]},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"tool_role":             `{"input":[{"role":"tool","content":"fixture result"},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"missing_role":          `{"input":[{"type":"message","content":"fixture result"},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"media":                 `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/fixture.png"}]},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"text_metadata":         `{"input":[{"role":"user","content":[{"type":"input_text","text":"fixture","future_state":{}}]},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"envelope_metadata":     `{"input":[{"type":"agent_message","encrypted_content":"fixture-opaque","recipient":"functions.exec"}]}`,
		"null_type":             `{"input":[{"type":null,"role":"user","content":"fixture"},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"object_input":          `{"input":{"role":"user","content":"fixture","encrypted_content":"fixture-opaque"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			original := []byte(body)
			result, changed := discardOpenAIProviderHistory(original)
			// Exact preservation includes discovery schemas, call IDs, namespaces,
			// large numeric arguments and malformed records for the normal gate.
			require.False(t, changed)
			require.Equal(t, original, result)
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = true
			svc := &OpenAIGatewayService{cfg: cfg, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := context.Background()
			next, prepared, changed := svc.PrepareOpenAILossyHistoryRestart(ctx, c, original, "fixture-session", "fixture-model")
			require.False(t, changed)
			require.Equal(t, original, prepared)
			require.Equal(t, ctx, next, "declining text recovery must not replace the original request context")
		})
	}
}

func TestOpenAILossyProtocolCompactionPreservesContext(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "compact_endpoint", true: "native_v2"}[native], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = true
			svc := &OpenAIGatewayService{cfg: cfg, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			path := "/v1/responses/compact"
			if native {
				path = "/v1/responses"
				MarkOpenAINativeCompactionV2(c)
			}
			c.Request = httptest.NewRequest(http.MethodPost, path, nil)
			body := []byte(issue715OpaqueBody)
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, body, "fixture-session", "gpt-6-astra")
			next, result, changed := svc.PrepareOpenAILossyHistoryRestart(ctx, c, body, "fixture-session", "gpt-6-astra")
			require.False(t, changed)
			require.Equal(t, body, result)
			require.Same(t, openAIAgentRecoveryFromContext(ctx), openAIAgentRecoveryFromContext(next))
			require.False(t, openAIAgentRecoveryFromContext(next).omittedHistory)
			if native {
				// The incident reached this real pre-send gate after rewriting the
				// compaction input. An unchanged, unknown-owner request still uses
				// the ordinary admission behavior and does not invent an owner.
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
				req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(result))
				require.NoError(t, err)
				complete, err := svc.prepareOpenAIAgentRecoveryAttempt(next, account, req)
				require.NoError(t, err)
				complete(false)
			}
		})
	}
}

func TestOpenAILossyProtocolOwnerlessCompactionRefused(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"fixture context"},{"type":"compaction_trigger"}]}`)
	for _, omitted400 := range []bool{false, true} {
		r := &openAIAgentRecoveryRequest{keyID: 715, session: "fixture-session", model: "gpt-6-astra", nativeCompaction: true, disallowLearning: true,
			omittedHistory: !omitted400, omitted400History: omitted400, inputDigest: openAIAgentRecoveryInputDigest(body)}
		account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
		if omitted400 {
			r.retry400Route = &openAIAgentRecoveryBinding{accountID: account.ID, config: openAIAgentRecoveryConfig(account, r.model)}
		}
		ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, r)
		req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(body))
		require.NoError(t, err)
		svc := &OpenAIGatewayService{cfg: &config.Config{}}
		require.NotPanics(t, func() {
			complete, attemptErr := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
			var refusal *OpenAIAgentRecoveryError
			require.ErrorAs(t, attemptErr, &refusal)
			require.Equal(t, "owner_unavailable", refusal.Reason)
			complete(false)
		})
		require.Nil(t, r.delivery, "a refused compaction must not reserve or publish delivery")
	}
}
