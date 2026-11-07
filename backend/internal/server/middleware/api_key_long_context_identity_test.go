package middleware

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyLongContextPermissionChangeRequiresFreshAdmission(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		key := &service.APIKey{ID: 1, AllowLongContext: allowed}
		identity := captureAPIKeyQueueIdentity(key)
		require.False(t, apiKeyQueueCoreIdentityChanged(identity, key))
		key.AllowLongContext = !allowed
		require.True(t, apiKeyQueueCoreIdentityChanged(identity, key), "queued and websocket requests must acquire the new policy")
	}
}
