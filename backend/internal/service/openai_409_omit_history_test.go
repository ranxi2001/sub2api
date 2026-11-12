package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
