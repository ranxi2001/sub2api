package conf

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFirstNodeFailureIsVisibleAndBackfillDoesNotRewindDisplay(t *testing.T) {
	const network = "test-network"
	data.Delete(network)
	t.Cleanup(func() { data.Delete(network) })
	RecordFailure(network)
	info := GetStats()[network]
	require.Equal(t, "error", info.Status)
	require.NotEmpty(t, info.Error)
	require.Zero(t, info.Time)
	require.Positive(t, info.LastAttempt)
	RecordSuccess(network, "100")
	RecordSuccess(network, "50")
	info = GetStats()[network]
	require.Equal(t, "100", info.Block)
	require.Equal(t, "healthy", info.Status)
	require.Empty(t, info.Error)
}
