package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
	require.Equal(t, "none", decoded["tool_choice"])

	wire, err := json.Marshal(decoded)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(wire))
	require.NoError(t, err)
	account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	finish, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
	require.NoError(t, err)
	finish(false)

	decoded["tools"] = []any{map[string]any{"type": "function", "name": "unexpected"}}
	decoded["tool_choice"] = "auto"
	wire, err = json.Marshal(decoded)
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodPost, req.URL.String(), bytes.NewReader(wire))
	require.NoError(t, err)
	_, err = svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
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

func TestOpenAI409OAuthReplayToolPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, fields, path string
		lite, refuse       bool
	}{
		{name: "legacy", fields: `"functions":[{"name":"lookup"}],"function_call":{"name":"lookup"}`},
		{name: "changed_legacy_source", fields: `"functions":[{"name":"lookup"}]`, refuse: true},
		{name: "mixed_alias", fields: `"functions":[],"tools":[{"type":"function","name":"lookup"}]`, refuse: true},
		{name: "missing_forced_function", fields: `"functions":[],"function_call":{"name":"lookup"}`, refuse: true},
		{name: "search_lite", fields: `"tools":[]`, lite: true},
		{name: "search_compact", fields: `"tools":[]`, path: "/v1/responses/compact"},
		{name: "search_with_other_tools", fields: `"tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"`, refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			if tc.path != "" {
				c.Request.URL.Path = tc.path
			}
			if tc.lite {
				c.Request.Header.Set(responsesLiteHeader, "true")
			}
			input := `[{"role":"user","content":"continue"}]`
			if tc.name == "search_lite" || tc.name == "search_compact" || tc.name == "search_with_other_tools" {
				input = `[{"type":"web_search_call","id":"ws_1"}]`
			}
			body := []byte(`{"model":"gpt-6-astra","input":` + input + `,` + tc.fields + `}`)
			r := &openAIAgentRecoveryRequest{omittedHistory: true, keyID: 715, session: "fixture-session", model: "gpt-6-astra", inputDigest: openAIAgentRecoveryInputDigest(body), replayControlsDigest: openAI409ControlsDigest(body)}
			ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, r)
			var decoded map[string]any
			require.NoError(t, decodeOpenAIJSONUseNumber(body, &decoded))
			if tc.name == "changed_legacy_source" {
				decoded["functions"] = []any{map[string]any{"name": "other"}}
			}
			err := prepareOpenAI409OAuthReplay(ctx, c, decoded)
			if tc.refuse && tc.name != "search_with_other_tools" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			result := applyCodexOAuthTransformWithOptions(decoded, codexOAuthTransformOptions{ResponsesLite: tc.lite, IsCompact: tc.path != "", PreserveReadableReplay: true, SkipDefaultInstructions: true})
			require.NoError(t, result.Error)
			wire, err := json.Marshal(decoded)
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(wire))
			require.NoError(t, err)
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			finish, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, req)
			if tc.refuse {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			finish(false)
			switch tc.name {
			case "legacy":
				require.Equal(t, "lookup", gjson.GetBytes(wire, "tool_choice.name").String())
				require.Equal(t, "lookup", gjson.GetBytes(wire, "tools.0.name").String())
			case "search_lite":
				require.Len(t, decoded["tools"], 0)
				require.Equal(t, "web_search", gjson.GetBytes(wire, `input.#(type=="additional_tools").tools.0.type`).String())
				require.Equal(t, "none", decoded["tool_choice"])
			case "search_compact":
				require.Len(t, decoded["tools"], 0)
				require.NotContains(t, decoded, "tool_choice")
			}
		})
	}
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
