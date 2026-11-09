//go:build unit

package service

import (
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
