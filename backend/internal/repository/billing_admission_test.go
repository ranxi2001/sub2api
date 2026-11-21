//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBillingAdmissionSerializesOwnerAndReservesPoolCapacity(t *testing.T) {
	var gate billingAdmission
	ctx := context.Background()
	first, err := gate.acquire(ctx, "1", 8)
	require.NoError(t, err)
	wait := func(user string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		_, err := gate.acquire(ctx, user, 8)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	wait("1")
	second, err := gate.acquire(ctx, "2", 8)
	require.NoError(t, err)
	wait("3")
	first()
	third, err := gate.acquire(ctx, "3", 8)
	require.NoError(t, err)
	second()
	third()
	require.Empty(t, gate.owners)
}
