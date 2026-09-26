package mihomo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func bpsStaticTestPool(t *testing.T, probe func(context.Context, string) error) {
	t.Helper()
	previous := bpsStaticManager
	bpsStaticManager = &Manager{bpsStaticMode: true, bpsProbe: probe}
	t.Cleanup(func() { bpsStaticManager = previous })
}

func TestBPSStaticPoolBindsSessionsToAdminProxies(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	a := "http://user:secret@a.example.com:8080"
	b := "socks5h://b.example.com:1080"
	SetBPSStaticProxies([]string{a, " ", b, a})
	for _, proxy := range []string{a, b} {
		require.NoError(t, bpsStaticManager.checkBPSHealth(context.Background(), bpsStaticNodeKey(proxy), proxy))
	}

	first, err := AcquireBPSStaticLease(context.Background(), "account:1/thread:a")
	require.NoError(t, err)
	second, err := AcquireBPSStaticLease(context.Background(), "account:1/thread:b")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a, b}, []string{first.ProxyURL, second.ProxyURL})
	first.Release()
	second.Release()

	again, err := AcquireBPSStaticLease(context.Background(), "account:1/thread:a")
	require.NoError(t, err)
	require.Equal(t, first.ProxyURL, again.ProxyURL, "a session stays on its exit between turns")
	again.Release()
}

func TestBPSStaticPoolExcludesFailedExitAndEmptyPoolFails(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	a := "http://a.example.com:8080"
	b := "http://b.example.com:8080"
	SetBPSStaticProxies([]string{a, b})
	bpsStaticManager.warmBPSPool(t.Context(), 2)

	lease, err := AcquireBPSStaticTransientLease(context.Background(), "request:1", a)
	require.NoError(t, err)
	require.Equal(t, b, lease.ProxyURL)
	lease.Release()

	_, err = AcquireBPSStaticLease(context.Background(), "request:2", a, b)
	require.Error(t, err)

	SetBPSStaticProxies(nil)
	_, err = AcquireBPSStaticLease(context.Background(), "request:3")
	require.Error(t, err, "an empty IP pool must fail instead of going direct")

	_, err = AcquireBPSStaticLease(context.Background(), "")
	require.Error(t, err)
}

func TestBPSStaticPoolSkipsUnreachableExit(t *testing.T) {
	bad := "http://bad.example.com:8080"
	good := "http://good.example.com:8080"
	var mu sync.Mutex
	probed := map[string]int{}
	bpsStaticTestPool(t, func(_ context.Context, proxy string) error {
		mu.Lock()
		defer mu.Unlock()
		probed[proxy]++
		if proxy == bad {
			return errors.New("connect refused")
		}
		return nil
	})
	SetBPSStaticProxies([]string{bad, good})
	bpsStaticManager.warmBPSPool(t.Context(), 2)

	for i := 0; i < 4; i++ {
		lease, err := AcquireBPSStaticTransientLease(context.Background(), fmt.Sprintf("request:%d", i))
		require.NoError(t, err)
		require.Equal(t, good, lease.ProxyURL)
		lease.Release()
	}
	mu.Lock()
	defer mu.Unlock()
	require.Positive(t, probed[good])
}

func TestBPSStaticPoolRebindsAfterExitRemoved(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	a := "http://a.example.com:8080"
	b := "http://b.example.com:8080"
	SetBPSStaticProxies([]string{a})
	bpsStaticManager.warmBPSPool(t.Context(), 1)
	lease, err := AcquireBPSStaticLease(context.Background(), "account:1/thread:a")
	require.NoError(t, err)
	require.Equal(t, a, lease.ProxyURL)
	lease.Release()

	SetBPSStaticProxies([]string{b})
	bpsStaticManager.warmBPSPool(t.Context(), 1)
	moved, err := AcquireBPSStaticLease(context.Background(), "account:1/thread:a")
	require.NoError(t, err)
	require.Equal(t, b, moved.ProxyURL)
	moved.Release()
}

func TestBPSStaticProxyLogValueHidesURL(t *testing.T) {
	require.Equal(t, "ip-pool", bpsProxyLogValue(&Manager{bpsStaticMode: true}, "http://user:secret@a.example.com:8080"))
	require.Equal(t, "http://127.0.0.1:3200", bpsProxyLogValue(&Manager{}, "http://127.0.0.1:3200"))
}

func TestBPSStaticProbeClientRejectsInvalidProxy(t *testing.T) {
	_, err := newBPSStaticProbeClient("not a proxy")
	require.Error(t, err)
	client, err := newBPSStaticProbeClient("socks5h://a.example.com:1080")
	require.NoError(t, err)
	require.NotNil(t, client)
}
