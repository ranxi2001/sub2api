//go:build unit

package service

import (
 "encoding/json"
 "testing"
 "github.com/stretchr/testify/require"
)

func TestOAuthDegradationModelScopeIsAdditionalBoundary(t *testing.T) {
 for _, scope := range []string{`["gpt-6-luna"]`, `[]`, `null`} {
  t.Run(scope, func(t *testing.T) {
   extra := map[string]any{}
   require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"models":`+scope+`,"concurrency_cap":10}}`), &extra))
   a := &Account{Platform:PlatformOpenAI, Type:AccountTypeOAuth, Credentials:map[string]any{}, Extra:extra}
   require.False(t, a.IsModelSupported("gpt-6.1-sol"), "degradation action must deny a model outside its persisted intersection")
   require.Equal(t, scope == `["gpt-6-luna"]`, a.IsModelSupported("gpt-6-luna"), "empty scope never becomes allow-all")
   a.Credentials["model_mapping"] = map[string]any{"gpt-6-luna":"gpt-6.1-sol"}
   require.False(t, a.IsModelSupported("gpt-6-luna"), "mapping must not bypass degraded model scope")
  })
 }
}
