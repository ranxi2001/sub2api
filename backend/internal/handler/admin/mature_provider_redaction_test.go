package admin

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatureProviderReflectedCredentialIsNotPublic(t *testing.T) {
	err := &providerAPIError{status: http.StatusBadRequest, message: "invalid token synthetic-reflected-dashboard-secret"}
	require.NotContains(t, providerFailureMessage(err), "synthetic-reflected-dashboard-secret", "untrusted upstream rejection text must not expose dashboard credentials")
}

func TestMatureProviderScopeCannotReflectCredential(t *testing.T) {
	err := newAPIAccessError(map[string]any{"code": "ACCESS_TOKEN_SCOPE_DENIED", "message_params": map[string]any{"scope": "nap_synthetic_reflected_dashboard_secret"}})
	require.NotContains(t, providerPublicError(err), "nap_synthetic_reflected_dashboard_secret", "an arbitrary scope label must not reflect a dashboard credential")
	known := newAPIAccessError(map[string]any{"code": "ACCESS_TOKEN_SCOPE_DENIED", "message_params": map[string]any{"scope": "api_key:reveal"}})
	require.Contains(t, providerPublicError(known), "api_key:reveal")
}
