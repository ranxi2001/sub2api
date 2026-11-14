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
