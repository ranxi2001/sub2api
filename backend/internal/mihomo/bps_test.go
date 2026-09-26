package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func bpsTestManager(t *testing.T) *Manager {
	t.Helper()
	m := New(t.TempDir())
	t.Cleanup(m.Close)
	m.state.Running = true
	m.bpsProbe = func(context.Context, string) error { return nil }
	m.saved = saved{UseOnce: true, Nodes: []map[string]any{{"name": "one"}, {"name": "two"}}, Disabled: map[string]string{"one": "used"}}
	_, err := m.config(m.saved)
	require.NoError(t, err)
	return m
}

func TestBPSSessionsConcurrentAndSticky(t *testing.T) {
	m := bpsTestManager(t)
	// Load distribution is checked across exits that have passed HTTPS preflight.
	for node, port := range m.bpsPorts {
		require.NoError(t, m.checkBPSHealth(context.Background(), node, fmt.Sprintf("http://127.0.0.1:%d", port)))
	}
	// Harvest is busy, but session allocation must not wait for its gate.
	m.gate <- struct{}{}
	defer m.release()
	first, release, err := AcquireBPSSession(context.Background(), "account:1/key:2/thread:a")
	require.NoError(t, err)
	defer release()
	second, release2, err := AcquireBPSSession(context.Background(), "account:1/key:2/thread:b")
	require.NoError(t, err)
	defer release2()
	require.NotEqual(t, first, second)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, done, e := AcquireBPSSession(context.Background(), "account:1/key:2/thread:a")
			if e != nil {
				t.Error(e)
				return
			}
			defer done()
			if p != first {
				t.Errorf("session changed proxy")
			}
			done()
		}()
	}
	wg.Wait()
	// A gap between turns preserves the binding; release is idempotent.
	release()
	release()
	again, done, err := AcquireBPSSession(context.Background(), "account:1/key:2/thread:a")
	require.NoError(t, err)
	require.Equal(t, first, again)
	done()
	for _, b := range m.bpsSessions {
		require.GreaterOrEqual(t, b.active, 0)
	}
	require.Len(t, m.bpsSessions, 2)
}

func TestBPSSessionsDistribute200Sessions(t *testing.T) {
	m := bpsTestManager(t)
	// Load distribution is checked across exits that have passed HTTPS preflight.
	for node, port := range m.bpsPorts {
		require.NoError(t, m.checkBPSHealth(context.Background(), node, fmt.Sprintf("http://127.0.0.1:%d", port)))
	}
	var wg sync.WaitGroup
	var allocated sync.WaitGroup
	allocated.Add(200)
	releaseAll := make(chan struct{})
	var mu sync.Mutex
	counts := map[string]int{}
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, done, err := AcquireBPSSession(context.Background(), fmt.Sprintf("account:1/key:2/thread:%d", i))
			if err != nil {
				t.Error(err)
				allocated.Done()
				return
			}
			defer done()
			mu.Lock()
			counts[p]++
			mu.Unlock()
			allocated.Done()
			<-releaseAll
		}(i)
	}
	allocated.Wait()
	close(releaseAll)
	wg.Wait()
	require.Len(t, m.bpsSessions, 200)
	require.Len(t, counts, 2)
	for _, count := range counts {
		require.Equal(t, 100, count)
	}
}

func TestBPSSessionUnavailableNodeRebinds(t *testing.T) {
	m := bpsTestManager(t)
	m.warmBPSPool(t.Context(), 2)
	proxy, done, err := AcquireBPSSession(context.Background(), "a")
	require.NoError(t, err)
	done()
	var selected string
	for _, b := range m.bpsSessions {
		selected = b.node
	}
	for _, n := range m.saved.Nodes {
		if harvestDigest(n) == selected {
			name, ok := n["name"].(string)
			require.True(t, ok)
			m.saved.Disabled[name] = "failed"
		}
	}
	replacement, release, err := AcquireBPSSession(context.Background(), "a")
	require.NoError(t, err)
	require.NotEqual(t, proxy, replacement)
	release()
	// Disabled listeners explicitly reject; no port can be reused for a new node.
	before := m.bpsPorts[selected]
	m.saved.Nodes = append(m.saved.Nodes, map[string]any{"name": "new"})
	raw, err := m.config(m.saved)
	require.NoError(t, err)
	var cfg struct {
		Listeners []struct {
			Port   int
			Proxy  string
			Listen string
		}
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	found := false
	for _, l := range cfg.Listeners {
		if l.Port == before {
			found = true
			require.Equal(t, "REJECT", l.Proxy)
			require.Equal(t, "127.0.0.1", l.Listen)
		}
	}
	require.True(t, found)
	require.Equal(t, fmt.Sprintf("http://127.0.0.1:%d", before), proxy)
	again, release, err := AcquireBPSSession(context.Background(), "a")
	require.NoError(t, err)
	require.Equal(t, replacement, again)
	release()
}

func TestBPSSessionIdleExpiryAndCapacity(t *testing.T) {
	m := bpsTestManager(t)
	_, active, err := m.acquireBPSSession("active", time.Now())
	require.NoError(t, err)
	defer active()
	_, done, err := m.acquireBPSSession("idle", time.Now())
	require.NoError(t, err)
	done()
	_, fresh, err := m.acquireBPSSession("fresh", time.Now().Add(bpsSessionIdleTTL+time.Second))
	require.NoError(t, err)
	defer fresh()
	require.Len(t, m.bpsSessions, 2, "active bindings must not expire")
	for len(m.bpsSessions) < bpsMaxSessions {
		m.bpsSessions[fmt.Sprint(len(m.bpsSessions))] = &bpsSession{active: 1}
	}
	_, _, err = m.acquireBPSSession("over-capacity", time.Now())
	require.ErrorContains(t, err, "capacity")
}

func TestBPSSessionCountryFilterAndIdentityChanges(t *testing.T) {
	m := bpsTestManager(t)
	m.saved.CountryFilter = CountryFilter{Mode: "include", Codes: []string{"US"}}
	_, _, err := AcquireBPSSession(context.Background(), "a")
	require.ErrorContains(t, err, "no eligible")
	m.saved.CountryFilter = CountryFilter{Mode: "off"}
	m.warmBPSPool(t.Context(), 2)
	_, done, err := AcquireBPSSession(context.Background(), "a")
	require.NoError(t, err)
	done()
	for _, n := range m.saved.Nodes {
		n["server"] = "changed.example"
	}
	_, err = m.config(m.saved)
	require.NoError(t, err)
	m.warmBPSPool(t.Context(), 2)
	_, release, err := AcquireBPSSession(context.Background(), "a")
	require.NoError(t, err)
	release()
}
