//go:build unit

package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOAuthDegradationModelScopeIsAdditionalBoundary(t *testing.T) {
	for _, scope := range []string{`["gpt-6-luna"]`, `[]`, `null`} {
		t.Run(scope, func(t *testing.T) {
			extra := map[string]any{}
			require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"models":`+scope+`,"concurrency_cap":10}}`), &extra))
			a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}, Extra: extra}
			require.False(t, a.IsModelSupported("gpt-6.1-sol"), "degradation action must deny a model outside its persisted intersection")
			require.Equal(t, scope == `["gpt-6-luna"]`, a.IsModelSupported("gpt-6-luna"), "empty scope never becomes allow-all")
			a.Credentials["model_mapping"] = map[string]any{"gpt-6-luna": "gpt-6.1-sol"}
			require.False(t, a.IsModelSupported("gpt-6-luna"), "mapping must not bypass degraded model scope")
		})
	}
}

func TestOAuthDegradationPublicModels(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}, Extra: map[string]any{}}
	require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"models":["gpt-6-luna"],"concurrency_cap":10}}`), &a.Extra))
	body, err := projectAccountModelAliases([]byte(`{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6.1-sol"}]}`), a, nil, false)
	require.NoError(t, err)
	require.Contains(t, string(body), "gpt-6-luna", "allowed models remain discoverable with default mapping")
	require.NotContains(t, string(body), "gpt-6.1-sol", "degraded models must not be published")
}

func TestOAuthDegradationCompactAndPassthroughCannotBypassScope(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"compact_model_mapping": map[string]any{"gpt-6-luna": "gpt-6.1-sol"}}, Extra: map[string]any{}}
	require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"models":["gpt-6-luna"],"concurrency_cap":10}}`), &a.Extra))
	require.True(t, supportsOpenAIRequestModel(a, "gpt-6-luna", false))
	require.False(t, supportsOpenAIRequestModel(a, "gpt-6-luna", true), "compact final target must stay inside degradation scope")
	a.Extra["openai_passthrough"] = true
	require.False(t, a.IsModelSupported("gpt-6.1-sol"), "passthrough cannot bypass degradation scope")
	require.True(t, a.IsModelSupported("gpt-6-luna"))
}
func TestOAuthDegradationImportedMarkerCannotFabricateAction(t *testing.T) {
	svc := &adminServiceImpl{}
	input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"oauth_degradation_action": map[string]any{"models": []string{}, "concurrency_cap": 10}, "manual": "keep"}}
	require.NoError(t, svc.ApplyOAuthAutoConfig(context.Background(), input))
	require.NotContains(t, input.Extra, "oauth_degradation_action", "new imports must not fabricate persisted degradation action")
	require.Equal(t, "keep", input.Extra["manual"])
}
