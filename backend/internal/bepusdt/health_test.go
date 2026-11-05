//go:build unit

package bepusdt

import (
	"context"
	"errors"
	"github.com/gofrs/flock"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/v03413/bepusdt/app/model"
)

func TestCollectionHandoffWaitsForExclusiveOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.lock")
	owner, standby := flock.New(path), flock.New(path)
	require.NoError(t, owner.Lock())
	t.Cleanup(func() { _ = owner.Unlock(); _ = standby.Unlock() })
	firstAttempt := make(chan struct{})
	acquired := make(chan error, 1)
	go func() {
		first := true
		acquired <- waitForEngine(context.Background(), time.Second, func() error {
			locked, err := standby.TryLock()
			if first {
				close(firstAttempt)
				first = false
			}
			if err != nil {
				return err
			}
			if !locked {
				return errEngineInUse
			}
			return nil
		})
	}()
	<-firstAttempt
	select {
	case <-acquired:
		t.Fatal("standby must not start while the old scanner owns the lock")
	default:
	}
	require.NoError(t, owner.Unlock())
	require.NoError(t, <-acquired)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, waitForEngine(ctx, time.Second, func() error { return errEngineInUse }), errEngineInUse)
	permanent := errors.New("invalid collection database")
	require.ErrorIs(t, waitForEngine(context.Background(), time.Second, func() error { return permanent }), permanent)
}

func TestNodePreflightSharesChecksAndBlocksUncollectableOrders(t *testing.T) {
	cleanup, err := InitializeForTest(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	oldProbe := probeNetwork
	oldEnabled := probeOnCreate.Load()
	invalidateNodeChecks()
	var calls atomic.Int64
	var unavailable atomic.Bool
	unavailable.Store(true)
	probeNetwork = func(context.Context, string) error {
		calls.Add(1)
		if unavailable.Load() {
			return errors.New("RPC HTTP 403")
		}
		return nil
	}
	probeOnCreate.Store(true)
	t.Cleanup(func() { probeNetwork = oldProbe; probeOnCreate.Store(oldEnabled); invalidateNodeChecks() })
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan NodeCheck, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); result, _ := checkNetwork(ctx, "bsc"); results <- result }()
	}
	wg.Wait()
	close(results)
	for result := range results {
		require.Equal(t, "error", result.Status)
	}
	require.Equal(t, int64(1), calls.Load(), "concurrent customers share the node check and cached result")
	request := CreateRequest{OrderID: "preflight", Amount: "10", TradeType: "usdt.bep20", Currency: "CNY", NotifyURL: "https://merchant.example/callback", ReturnURL: "https://merchant.example/result", ExpiresAt: time.Now().Add(5 * time.Minute)}
	_, err = Create(ctx, request)
	require.ErrorContains(t, err, "temporarily unavailable")
	var count int64
	require.NoError(t, model.Db.Model(&model.Order{}).Count(&count).Error)
	require.Zero(t, count, "do not allocate a payment address when collection is unavailable")
	unavailable.Store(false)
	require.NoError(t, SaveSettings(ctx, map[string]string{"rpc_endpoint_bsc": "https://replacement.example"}))
	require.NoError(t, SaveWallet(ctx, model.Wallet{Address: "0x1111111111111111111111111111111111111111", TradeType: "usdt.bep20", Status: 1}))
	require.NoError(t, SaveRate(ctx, "CNY", "USDT", "7"))
	order, err := Create(ctx, request)
	require.NoError(t, err)
	require.NotZero(t, order.ID)
	require.Equal(t, int64(2), calls.Load(), "changing a saved node invalidates its previous failed check")
}
