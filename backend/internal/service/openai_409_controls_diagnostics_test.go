package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAI409OAuthReplayDiagnosesWebSearchHistoryProjection(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	body := []byte(`{"model":"gpt-6-astra","instructions":"continue from recorded search results","input":[{"type":"message","role":"user","content":"continue"},{"type":"web_search_call","id":"ws_1"}],"tools":[]}`)
	r := &openAIAgentRecoveryRequest{
		omittedHistory:       true,
		keyID:                715,
		model:                "gpt-6-astra",
		session:              "fixture-session",
		inputDigest:          openAIAgentRecoveryInputDigest(body),
		replayControlsDigest: openAI409ControlsDigest(body),
	}
	ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, r)
	var decoded map[string]any
	require.NoError(t, decodeOpenAIJSONUseNumber(body, &decoded))
	require.NoError(t, prepareOpenAI409OAuthReplay(ctx, c, decoded))

	transformed := applyCodexOAuthTransformWithOptions(decoded, codexOAuthTransformOptions{
		SkipDefaultInstructions: true,
		PreserveReadableReplay:  true,
	})
	require.NoError(t, transformed.Error)
	require.Len(t, decoded["tools"], 1, "the normal Codex transform declares the historical web_search call")

	wire, err := json.Marshal(decoded)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(wire))
	require.NoError(t, err)
	_, err = svc.prepareOpenAIAgentRecoveryAttempt(ctx, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, req)
	var recovery *OpenAIAgentRecoveryError
	require.ErrorAs(t, err, &recovery)
	require.Equal(t, "controls_integrity", recovery.InternalSubreason())
	report := recovery.RecoveryControlsDiagnostic()
	require.NotNil(t, report)
	require.Equal(t, "oauth_replay", report.Snapshot)
	require.Equal(t, "changed", report.Tools)
	require.Equal(t, "changed", report.ToolChoice)
	require.Equal(t, "match", report.ParallelToolCalls)
	require.Equal(t, "match", report.Instructions)
	require.True(t, report.DigestChanged)
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "ws_1")
	require.NotContains(t, string(encoded), "continue from recorded search results")
}

func TestOpenAI409ControlDiagnosticReportsEachControlAndSessionMarker(t *testing.T) {
	base := []byte(`{"tools":[],"tool_choice":"auto","parallel_tool_calls":true,"instructions":"private instruction"}`)
	expected := openAI409FingerprintControls(base)
	for _, tc := range []struct {
		field string
		body  string
	}{
		{"tools", `{"tools":[{"type":"function","name":"private-tool"}],"tool_choice":"auto","parallel_tool_calls":true,"instructions":"private instruction"}`},
		{"tool_choice", `{"tools":[],"tool_choice":"none","parallel_tool_calls":true,"instructions":"private instruction"}`},
		{"parallel_tool_calls", `{"tools":[],"tool_choice":"auto","parallel_tool_calls":false,"instructions":"private instruction"}`},
		{"instructions", `{"tools":[],"tool_choice":"auto","parallel_tool_calls":true,"instructions":"changed private instruction"}`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			report := openAI409ControlDiagnostic([]byte(tc.body), expected, "original_replay", true, true, false)
			require.Equal(t, "complete", report.Observation)
			for field, got := range map[string]string{
				"tools": report.Tools, "tool_choice": report.ToolChoice,
				"parallel_tool_calls": report.ParallelToolCalls, "instructions": report.Instructions,
			} {
				want := "match"
				if field == tc.field {
					want = "changed"
				}
				require.Equal(t, want, got, field)
			}
			require.True(t, report.PreviousResponseIDPresent)
			require.False(t, report.ConversationPresent)
		})
	}

	unknown := openAI409ControlDiagnostic(base, openAI409ControlFingerprints{}, "original_replay", true, false, true)
	require.Equal(t, "partial", unknown.Observation)
	require.Equal(t, "unknown", unknown.Tools)
	require.True(t, unknown.ConversationPresent)
}
