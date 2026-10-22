package bepusdt

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	beconf "github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/task"
	"golang.org/x/sync/singleflight"
)

type NodeCheck struct {
	Status    string `json:"status"`
	CheckedAt int64  `json:"checked_at"`
	Error     string `json:"error,omitempty"`
}

type cachedNodeCheck struct {
	result  NodeCheck
	expires time.Time
}

var nodeChecks = struct {
	sync.Mutex
	values     map[string]cachedNodeCheck
	generation uint64
}{values: make(map[string]cachedNodeCheck)}
var nodeCheckGroup singleflight.Group
var probeNetwork = task.ProbeNetwork

func invalidateNodeChecks() {
	nodeChecks.Lock()
	defer nodeChecks.Unlock()
	nodeChecks.values = make(map[string]cachedNodeCheck)
	nodeChecks.generation++
}

func checkNetwork(ctx context.Context, network string) (NodeCheck, error) {
	nodeChecks.Lock()
	entry, exists := nodeChecks.values[network]
	generation := nodeChecks.generation
	nodeChecks.Unlock()
	if exists && time.Now().Before(entry.expires) {
		return entry.result, nil
	}
	result := nodeCheckGroup.DoChan(fmt.Sprintf("%s:%d", network, generation), func() (any, error) {
		// Another caller may have filled the cache after our first lookup but
		// before this singleflight began.
		nodeChecks.Lock()
		cached, found := nodeChecks.values[network]
		current := nodeChecks.generation
		nodeChecks.Unlock()
		if found && current == generation && time.Now().Before(cached.expires) {
			return cached.result, nil
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := probeNetwork(probeCtx, network)
		check := NodeCheck{Status: "healthy", CheckedAt: time.Now().Unix()}
		ttl := 30 * time.Second
		if err != nil {
			check.Status = "error"
			check.Error = err.Error()
			ttl = 5 * time.Second
		}
		nodeChecks.Lock()
		if generation == nodeChecks.generation {
			nodeChecks.values[network] = cachedNodeCheck{check, time.Now().Add(ttl)}
		}
		nodeChecks.Unlock()
		return check, nil
	})
	select {
	case <-ctx.Done():
		return NodeCheck{}, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return NodeCheck{}, value.Err
		}
		return value.Val.(NodeCheck), nil
	}
}

func NodeChecks() map[string]NodeCheck {
	nodeChecks.Lock()
	defer nodeChecks.Unlock()
	checks := make(map[string]NodeCheck, len(nodeChecks.values))
	for network, entry := range nodeChecks.values {
		checks[network] = entry.result
	}
	return checks
}

func CheckNodes(ctx context.Context) (map[string]NodeCheck, error) {
	if err := Ensure(); err != nil {
		return nil, err
	}
	var networks []string
	for network := range model.GetAllNetwork() {
		networks = append(networks, network)
	}
	sort.Strings(networks)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 3)
	for _, network := range networks {
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func() { defer wg.Done(); defer func() { <-semaphore }(); _, _ = checkNetwork(ctx, network) }()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return NodeChecks(), nil
}

func PaymentNetworkStatus(tradeType string) string {
	config, ok := model.GetAllTradeConfig()[tradeType]
	if !ok {
		return "delayed"
	}
	network := string(config.Network)
	stat := beconf.GetStats()[network]
	if stat.Status == "error" {
		return "delayed"
	}
	nodeChecks.Lock()
	entry, exists := nodeChecks.values[network]
	nodeChecks.Unlock()
	if exists && entry.result.Status == "error" && entry.result.CheckedAt >= stat.Time {
		return "delayed"
	}
	if stat.LastAttempt > time.Now().Add(-2*time.Minute).Unix() || (exists && entry.result.Status == "healthy" && time.Now().Before(entry.expires)) {
		return "healthy"
	}
	return "checking"
}
