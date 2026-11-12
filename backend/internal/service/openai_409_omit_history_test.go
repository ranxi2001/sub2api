package service

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOmitOpenAIHistoricalCiphertextPreservesReadableContext(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.1","metadata":{"sequence":9007199254740993},"input":[
	{"type":"agent_message","author":"worker","recipient":"parent","encrypted_content":"OLD_TOP","content":"readable finding"},
	{"type":"message","role":"assistant","content":[{"type":"output_text","text":"result"},{"type":"encrypted_content","encrypted_content":"OLD_PART"}]},
	{"type":"function_call","name":"lookup","call_id":"call_a","arguments":"{\"encrypted_content\":\"literal\"}"},
	{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_text","text":"recorded result"},{"type":"encrypted_content","encrypted_content":"OLD_TOOL"}]},
	{"type":"reasoning","encrypted_content":"REASONING","summary":[]},
	{"role":"user","content":"continue from the visible findings"}]}`)
	clean, changed := omitOpenAIHistoricalCiphertext(raw)
	require.True(t, changed)
	for _, secret := range []string{"OLD_TOP", "OLD_PART", "OLD_TOOL"} {
		require.NotContains(t, string(clean), secret)
	}
	for _, text := range []string{"readable finding", "recorded result", "REASONING", "9007199254740993", "worker", "parent"} {
		require.Contains(t, string(clean), text)
	}
	require.Equal(t, "message", gjson.GetBytes(clean, "input.0.type").String())
	require.Equal(t, "user", gjson.GetBytes(clean, "input.0.role").String())
	for _, path := range []string{"input.2", "input.4", "input.5"} {
		require.JSONEq(t, gjson.GetBytes(raw, path).Raw, gjson.GetBytes(clean, path).Raw)
	}
	require.Equal(t, "call_a", gjson.GetBytes(clean, "input.3.call_id").String())
	require.Equal(t, "output_text", gjson.GetBytes(clean, "input.1.content.1.type").String())
	again, changed := omitOpenAIHistoricalCiphertext(clean)
	require.False(t, changed)
	require.Equal(t, clean, again)
}

func TestOmitOpenAIHistoricalCiphertextRefusesCurrentTaskAndPointers(t *testing.T) {
	for name, raw := range map[string]string{
		"new agent":           `{"input":[{"type":"agent_message","encrypted_content":"CURRENT","content":"header"}]}`,
		"current user":        `{"input":[{"type":"agent_message","encrypted_content":"OLD"},{"role":"user","content":[{"type":"encrypted_content","encrypted_content":"CURRENT"}]}]}`,
		"current tool result": `{"input":[{"type":"agent_message","encrypted_content":"OLD"},{"role":"user","content":"lookup"},{"type":"function_call_output","call_id":"x","output":[{"type":"encrypted_content","encrypted_content":"CURRENT"}]}]}`,
		"response pointer":    `{"previous_response_id":"resp_x","input":[{"type":"agent_message","encrypted_content":"OLD"},{"role":"user","content":"continue"}]}`,
		"conversation":        `{"conversation":{"id":"conv_x"},"input":[{"type":"agent_message","encrypted_content":"OLD"},{"role":"user","content":"continue"}]}`,
		"reference":           `{"input":[{"type":"item_reference","id":"item_x"},{"type":"agent_message","encrypted_content":"OLD"},{"role":"user","content":"continue"}]}`,
		"compaction":          `{"input":[{"type":"compaction","encrypted_content":"STATE"},{"type":"agent_message","encrypted_content":"OLD"},{"role":"user","content":"continue"}]}`,
		"duplicate keys":      `{"input":[{"type":"agent_message","encrypted_content":"OLD"},{"role":"user","content":"continue"}],"input":[]}`,
		"plain history":       `{"input":[{"role":"user","content":"keep encrypted_content as literal text"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			clean, changed := omitOpenAIHistoricalCiphertext([]byte(raw))
			require.False(t, changed)
			require.Equal(t, raw, string(clean), "refusal must not partially sanitize input")
		})
	}
}

func TestOpenAI409OmissionSettingDefaultsOff(t *testing.T) {
	for _, value := range []string{"", "false", "true", "invalid"} {
		s := NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: value}}, nil)
		require.Equal(t, value == "true", s.IsOpenAI409OmitEncryptedHistoryEnabled(context.Background()))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.False(t, s.IsOpenAI409OmitEncryptedHistoryEnabled(ctx))
	}
	var missing *SettingService
	require.False(t, missing.IsOpenAI409OmitEncryptedHistoryEnabled(context.Background()))
}

func TestOpenAI409OmissionCannotClearRecoveryBarriers(t *testing.T) {
	s := &OpenAIGatewayService{settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
	body := []byte(`{"input":[{"type":"agent_message","encrypted_content":"OLD"},{"role":"user","content":"continue"}]}`)
	for _, reason := range []string{"delivery_pending", "store_unavailable", "owner_conflict", "owner_rate_limited", "owner_overloaded", "http_previous_response_unsupported"} {
		ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, &openAIAgentRecoveryRequest{owner: &openAIAgentRecoveryBinding{accountID: 1}})
		_, clean, changed := s.TryOmitOpenAI409History(ctx, nil, body, "fixture", "gpt-5.1", &OpenAIAgentRecoveryError{Reason: reason})
		require.False(t, changed, reason)
		require.Equal(t, body, clean)
	}
}

func TestOmitOpenAIHistoricalAssistantAgentUsesUserTextParts(t *testing.T) {
	raw := []byte(`{"input":[{"type":"agent_message","role":"assistant","content":[{"type":"output_text","text":"readable"},{"type":"encrypted_content","encrypted_content":"OLD"}]},{"role":"user","content":"continue"}]}`)
	clean, changed := omitOpenAIHistoricalCiphertext(raw)
	require.True(t, changed)
	require.Equal(t, "user", gjson.GetBytes(clean, "input.0.role").String())
	for _, part := range gjson.GetBytes(clean, "input.0.content").Array() {
		require.Equal(t, "input_text", part.Get("type").String())
	}
	require.Contains(t, string(clean), "readable")
}

func TestOpenAI409ReplayInputTamperingStillFails(t *testing.T) {
	for _, mode := range []string{"unchanged", "user_text", "tool_arguments", "tool_result", "call_id", "large_integer", "reasoning_removed", "visible_reasoning_removed", "order", "replay_id_reintroduced"} {
		t.Run(mode, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			issue715LearnRoute(t, svc, a, issue715OpaqueBody)
			raw := strings.TrimSuffix(issue715OpaqueBody, "]}") + `,
			{"type":"reasoning","id":"rs_fixture_823","status":"completed","encrypted_content":"fixture-reasoning","content":[{"type":"reasoning_text","text":"visible reasoning must survive"}]},
			{"type":"function_call","name":"record","call_id":"call_823","arguments":"{\"value\":9007199254740993}"},
			{"type":"function_call_output","call_id":"call_823","output":"already recorded exactly once"},
			{"type":"message","role":"user","sequence":9007199254740993,"content":"continue with the result"}],"store":false}`
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(raw), "fixture-session", "gpt-6-astra")
			next, clean, changed := svc.TryOmitOpenAI409History(ctx, c, []byte(raw), "fixture-session", "gpt-6-astra", openAIAgentRecoveryError("owner_unavailable"))
			require.True(t, changed)
			require.NotContains(t, string(clean), "fixture-opaque-reference")
			require.Contains(t, string(clean), "visible reasoning must survive")
			require.Equal(t, "9007199254740993", gjson.GetBytes(clean, "input.4.sequence").Raw)
			require.False(t, gjson.GetBytes(clean, "input.1.id").Exists())
			require.False(t, gjson.GetBytes(clean, "input.1.status").Exists())
			require.True(t, gjson.GetBytes(clean, "input.1.summary").IsArray())
			originalDigest := openAIAgentRecoveryFromContext(next).inputDigest
			var err error
			switch mode {
			case "user_text":
				clean, err = sjson.SetBytes(clean, "input.4.content", "changed user task")
			case "tool_arguments":
				clean, err = sjson.SetBytes(clean, "input.2.arguments", `{}`)
			case "tool_result":
				clean, err = sjson.SetBytes(clean, "input.3.output", "lost result")
			case "call_id":
				clean, err = sjson.SetBytes(clean, "input.3.call_id", "different_call")
			case "large_integer":
				clean, err = sjson.SetRawBytes(clean, "input.4.sequence", []byte("9007199254740992"))
			case "reasoning_removed":
				clean, err = sjson.DeleteBytes(clean, "input.1")
			case "visible_reasoning_removed":
				clean, _, err = normalizeOpenAIResponsesReasoningContentReplay(clean)
			case "order":
				items := gjson.GetBytes(clean, "input").Array()
				clean, err = sjson.SetRawBytes(clean, "input.2", []byte(items[3].Raw))
				require.NoError(t, err)
				clean, err = sjson.SetRawBytes(clean, "input.3", []byte(items[2].Raw))
			case "replay_id_reintroduced":
				clean, err = sjson.SetBytes(clean, "input.1.id", "rs_untrusted_replacement")
			}
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(clean))
			require.NoError(t, err)
			finish, err := svc.prepareOpenAIAgentRecoveryAttempt(next, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, req)
			defer finish(false)
			if mode == "unchanged" {
				require.NoError(t, err)
			} else {
				var refusal *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, "wire_changed", refusal.Reason)
			}
			require.Equal(t, originalDigest, openAIAgentRecoveryFromContext(next).inputDigest, "send validation must not rebase the trusted snapshot")
		})
	}
}

func TestOpenAI409ReplayPreservesOrdinaryAndStoredReasoning(t *testing.T) {
	for _, store := range []string{"true", "false"} {
		svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
		issue715LearnRoute(t, svc, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, issue715OpaqueBody)
		body := strings.TrimSuffix(issue715OpaqueBody, "]}") + `,{"type":"reasoning","id":"rs_original","summary":[],"encrypted_content":"reasoning-cipher"},{"type":"reasoning","summary":[{"type":"summary_text","text":"readable reasoning"}]},{"role":"user","content":"continue"}],"store":` + store + `}`
		c, _ := newTurnStateTestContext(t, 715, "fixture-session")
		c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
		ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(body), "fixture-session", "gpt-6-astra")
		_, clean, changed := svc.TryOmitOpenAI409History(ctx, c, []byte(body), "fixture-session", "gpt-6-astra", openAIAgentRecoveryError("owner_unavailable"))
		require.True(t, changed)
		require.Equal(t, store == "true", gjson.GetBytes(clean, "input.1.id").Exists(), "stored reasoning IDs are not disposable")
		require.JSONEq(t, gjson.Get(body, "input.2").Raw, gjson.GetBytes(clean, "input.2").Raw, "readable reasoning without ciphertext must not be discarded")
	}
}
