package mihomo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Real HTTP proxy endpoints observe the selected physical egress. The sole
// injected boundary is the external HTTPS health probe, never selection.
func TestBPSRequestPoolDistributesConcurrentRequestsThroughSelectedProxies(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	var mu sync.Mutex
	hits := map[int]int{}
	records := make([]BPSStaticProxy, 0, 4)
	for i := 1; i <= 4; i++ {
		id := i
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[id]++
			mu.Unlock()
			_, _ = io.WriteString(w, "proxied")
		}))
		t.Cleanup(proxy.Close)
		records = append(records, BPSStaticProxy{ID: int64(i), URL: proxy.URL})
	}
	SetBPSStaticProxyRecords(records)
	SetBPSStaticRequestGroups(map[int64][]int64{300: {1, 2, 3}})
	WarmBPSPools(t.Context(), 0, 1)
	leases := make(chan *BPSLease, 6)
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1, 2, 3})
			if err != nil {
				errs <- err
				return
			}
			leases <- lease
			u, _ := url.Parse(lease.ProxyURL)
			tr := &http.Transport{Proxy: http.ProxyURL(u)}
			defer tr.CloseIdleConnections()
			resp, err := (&http.Client{Transport: tr}).Get("http://local-upstream.invalid/responses")
			if err != nil {
				errs <- err
				return
			}
			_, readErr := io.ReadAll(resp.Body)
			err = errors.Join(readErr, resp.Body.Close())
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(leases)
	for lease := range leases {
		lease.Release()
	}
	for err := range errs {
		require.NoError(t, err)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, map[int]int{1: 2, 2: 2, 3: 2}, hits, fmt.Sprint(hits))
}

func TestBPSRequestPoolAccountsKeepIndependentSelectedGroupsAndLoads(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	SetBPSStaticProxyRecords([]BPSStaticProxy{{ID: 1, URL: "http://one.invalid:8080"}, {ID: 2, URL: "http://two.invalid:8080"}, {ID: 3, URL: "http://three.invalid:8080"}})
	SetBPSStaticRequestGroups(map[int64][]int64{300: {1, 2}, 301: {2, 3}})
	WarmBPSPools(t.Context(), 0, 1)
	a := make([]*BPSLease, 0, 4)
	for i := 0; i < 4; i++ {
		l, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1, 2})
		require.NoError(t, err)
		t.Cleanup(l.Release)
		a = append(a, l)
	}
	require.ElementsMatch(t, []string{"http://one.invalid:8080", "http://one.invalid:8080", "http://two.invalid:8080", "http://two.invalid:8080"}, []string{a[0].ProxyURL, a[1].ProxyURL, a[2].ProxyURL, a[3].ProxyURL})
	b1, err := AcquireBPSStaticRequestLease(t.Context(), 301, []int64{2, 3})
	require.NoError(t, err)
	t.Cleanup(b1.Release)
	b2, err := AcquireBPSStaticRequestLease(t.Context(), 301, []int64{2, 3})
	require.NoError(t, err)
	t.Cleanup(b2.Release)
	require.ElementsMatch(t, []string{"http://two.invalid:8080", "http://three.invalid:8080"}, []string{b1.ProxyURL, b2.ProxyURL}, "another account's load cannot suppress this account's idle selected exit")
}

func TestBPSRequestPoolReleasedRequestsRotateAndReleaseOnlyOnce(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	SetBPSStaticProxyRecords([]BPSStaticProxy{{ID: 1, URL: "http://one.invalid:8080"}, {ID: 2, URL: "http://two.invalid:8080"}, {ID: 3, URL: "http://three.invalid:8080"}})
	SetBPSStaticRequestGroups(map[int64][]int64{300: {1, 2, 3}})
	WarmBPSPools(t.Context(), 0, 1)
	hits := map[string]int{}
	for i := 0; i < 6; i++ {
		l, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1, 2, 3})
		require.NoError(t, err)
		hits[l.ProxyURL]++
		l.ReportSuccess()
		l.Release()
		l.Release()
	}
	require.Equal(t, map[string]int{"http://one.invalid:8080": 2, "http://two.invalid:8080": 2, "http://three.invalid:8080": 2}, hits)
	// A double release must not make an exit appear to have negative load.
	first, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1, 2, 3})
	require.NoError(t, err)
	t.Cleanup(first.Release)
	second, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1, 2, 3})
	require.NoError(t, err)
	t.Cleanup(second.Release)
	require.NotEqual(t, first.ProxyURL, second.ProxyURL)
}

func TestBPSRequestPoolRotatesBeforeNextBackgroundAccountRefresh(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	SetBPSStaticProxyRecords([]BPSStaticProxy{{ID: 1, URL: "http://one.invalid:8080"}, {ID: 2, URL: "http://two.invalid:8080"}})
	// These exits may already be healthy before a newly enabled account appears
	// in the next background account projection. Its requests still rotate.
	WarmBPSPools(t.Context(), 0, 2)
	hits := map[string]int{}
	for i := 0; i < 4; i++ {
		l, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1, 2})
		require.NoError(t, err)
		hits[l.ProxyURL]++
		l.Release()
	}
	require.Equal(t, map[string]int{"http://one.invalid:8080": 2, "http://two.invalid:8080": 2}, hits)
}

func TestBPSRequestPoolFailsClosedWhenSelectedExitExpiresFailsOrIsRemoved(t *testing.T) {
	for _, scenario := range []string{"expired", "removed", "failed", "excluded"} {
		t.Run(scenario, func(t *testing.T) {
			bpsStaticTestPool(t, func(context.Context, string) error { return nil })
			expires := time.Now().Add(time.Hour)
			records := []BPSStaticProxy{{ID: 1, URL: "http://selected.invalid:8080", ExpiresAt: &expires}, {ID: 2, URL: "http://unselected.invalid:8080"}}
			SetBPSStaticProxyRecords(records)
			SetBPSStaticRequestGroups(map[int64][]int64{300: {1}})
			WarmBPSPools(t.Context(), 0, 2)
			l, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1})
			require.NoError(t, err)
			excluded := []string{}
			switch scenario {
			case "expired":
				past := time.Now().Add(-time.Second)
				records[0].ExpiresAt = &past
				SetBPSStaticProxyRecords(records)
			case "removed":
				SetBPSStaticProxyRecords(records[1:])
			case "failed":
				l.ReportStreamFailure()
			case "excluded":
				excluded = []string{l.ProxyURL}
			}
			l.Release()
			_, err = AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1}, excluded...)
			require.Error(t, err, "an unselected healthy exit must never replace an unavailable selected group")
		})
	}
}

func TestBPSRequestPoolSelectedColdSubsetWarmsAfterGlobalTargetAlreadyMet(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	SetBPSStaticProxyRecords([]BPSStaticProxy{{ID: 1, URL: "http://global-ready.invalid:8080"}})
	WarmBPSPools(t.Context(), 0, 1)
	SetBPSStaticProxyRecords([]BPSStaticProxy{{ID: 1, URL: "http://global-ready.invalid:8080"}, {ID: 2, URL: "http://selected-cold.invalid:8080"}})
	SetBPSStaticRequestGroups(map[int64][]int64{300: {2}})
	_, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{2})
	require.Error(t, err, "request must not qualify a cold selected exit")
	WarmBPSPools(t.Context(), 0, 1)
	l, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{2})
	require.NoError(t, err, "selected subset must warm independently of an already full global target")
	require.Equal(t, "http://selected-cold.invalid:8080", l.ProxyURL)
	l.Release()
}

func TestBPSRequestPoolRejectsStaleMembershipEvenWhenHealthIsStillVerified(t *testing.T) {
	bpsStaticTestPool(t, func(context.Context, string) error { return nil })
	SetBPSStaticProxyRecords([]BPSStaticProxy{{ID: 1, URL: "http://selected.invalid:8080"}})
	SetBPSStaticRequestGroups(map[int64][]int64{300: {1}})
	WarmBPSPools(t.Context(), 0, 1)
	// Shift time at the snapshot boundary, not the health/proxy choice, to
	// model repeated ListActive failures without sleeping in a request test.
	bpsStaticManager.bpsMu.Lock()
	bpsStaticManager.bpsRequests.refreshedAt = time.Now().Add(-BPSStaticRequestMembershipMaxAge - time.Second)
	bpsStaticManager.bpsMu.Unlock()
	_, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1})
	require.Error(t, err, "reachability cannot prove stale database membership remains authorized")
	// The old session mode retains its pre-existing snapshot behavior.
	legacy, err := AcquireBPSStaticLease(t.Context(), "legacy/session")
	require.NoError(t, err)
	legacy.Release()
	SetBPSStaticProxyRecords([]BPSStaticProxy{{ID: 1, URL: "http://selected.invalid:8080"}})
	recovered, err := AcquireBPSStaticRequestLease(t.Context(), 300, []int64{1})
	require.NoError(t, err)
	recovered.Release()
}
