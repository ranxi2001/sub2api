package repository

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional/control"
	"github.com/Wei-Shaw/sub2api/internal/regional/slots"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// regionalConcurrencyCache keeps admission, FIFO, and Live ownership in SQL.
// Redis supplies auxiliary waiting statistics and idle ingress leases only.
type regionalConcurrencyCache struct {
	fundsHandler    atomic.Pointer[http.Handler]
	runtimeConfig   *RegionalConcurrencyRuntimeConfig
	runtimeFailed   atomic.Bool
	controlServer   *http.Server
	controlListener net.Listener
	broker          *control.Broker
	brokerClient    *control.Client
	service.ConcurrencyCache
	primary    *slots.Primary
	local      *slots.Local
	keyStats   service.APIKeyConcurrencyCache
	keyRefresh service.APIKeySlotRefreshCache
	wsIngress  service.OpenAIWSIngressLeaseCache
}

func NewRegionalConcurrencyCache(base service.ConcurrencyCache, p *slots.Primary, l *slots.Local) (service.ConcurrencyCache, error) {
	if base == nil || (p == nil) == (l == nil) || (p != nil && (p.DB == nil || p.Limit == nil)) || (l != nil && (l.DB == nil || l.Region == "")) {
		return nil, errors.New("regional concurrency requires one local/primary authority and local auxiliary cache")
	}
	c := &regionalConcurrencyCache{ConcurrencyCache: base, primary: p, local: l}
	c.keyStats, _ = base.(service.APIKeyConcurrencyCache)
	c.keyRefresh, _ = base.(service.APIKeySlotRefreshCache)
	c.wsIngress, _ = base.(service.OpenAIWSIngressLeaseCache)
	return c, nil
}
func (c *regionalConcurrencyCache) acquire(ctx context.Context, kind string, id int64, request string) (bool, error) {
	if c.runtimeFailed.Load() {
		return false, errors.New("regional runtime unavailable")
	}
	r := slots.Resource{Kind: kind, ID: id}
	if c.primary != nil {
		return c.primary.Acquire(ctx, r, request)
	}
	return c.local.AcquireResource(ctx, r, request)
}
func (c *regionalConcurrencyCache) release(ctx context.Context, kind string, id int64, request string) error {
	r := slots.Resource{Kind: kind, ID: id}
	if c.primary != nil {
		return c.primary.Release(ctx, r, request)
	}
	return c.local.ReleaseResource(ctx, r, request)
}
func (c *regionalConcurrencyCache) count(ctx context.Context, kind string, id int64) (int, error) {
	r := slots.Resource{Kind: kind, ID: id}
	if c.primary != nil {
		return c.primary.Count(ctx, r)
	}
	return c.local.Count(ctx, r)
}
func (c *regionalConcurrencyCache) AcquireAccountSlot(ctx context.Context, id int64, _ int, request string) (bool, error) {
	return c.acquire(ctx, "account", id, request)
}
func (c *regionalConcurrencyCache) ReleaseAccountSlot(ctx context.Context, id int64, request string) error {
	return c.release(ctx, "account", id, request)
}
func (c *regionalConcurrencyCache) AcquireUserSlot(ctx context.Context, id int64, _ int, request string) (bool, error) {
	return c.acquire(ctx, "user", id, request)
}
func (c *regionalConcurrencyCache) ReleaseUserSlot(ctx context.Context, id int64, request string) error {
	return c.release(ctx, "user", id, request)
}
func (c *regionalConcurrencyCache) AcquireAPIKeySlot(ctx context.Context, id int64, _ int, request string) (bool, error) {
	return c.acquire(ctx, "api_key", id, request)
}
func (c *regionalConcurrencyCache) GetAccountConcurrency(ctx context.Context, id int64) (int, error) {
	return c.count(ctx, "account", id)
}
func (c *regionalConcurrencyCache) GetUserConcurrency(ctx context.Context, id int64) (int, error) {
	loads, err := c.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: id}})
	if err != nil {
		return 0, err
	}
	return loads[id].CurrentConcurrency, nil
}
func (c *regionalConcurrencyCache) TrackUserSlot(ctx context.Context, id int64, request string) error {
	if tracker, ok := c.ConcurrencyCache.(service.UserSlotTracker); ok {
		return tracker.TrackUserSlot(ctx, id, request)
	}
	return errors.New("user slot tracking unavailable")
}
func (c *regionalConcurrencyCache) ReleaseTrackedUserSlot(ctx context.Context, id int64, request string) error {
	if tracker, ok := c.ConcurrencyCache.(service.UserSlotTracker); ok {
		return tracker.ReleaseTrackedUserSlot(ctx, id, request)
	}
	return nil
}
func (c *regionalConcurrencyCache) GetAccountConcurrencyBatch(ctx context.Context, ids []int64) (map[int64]int, error) {
	return c.counts(ctx, "account", ids)
}
func (c *regionalConcurrencyCache) counts(ctx context.Context, kind string, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		n, err := c.count(ctx, kind, id)
		if err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, nil
}
func (c *regionalConcurrencyCache) GetAPIKeyConcurrencyBatch(ctx context.Context, ids []int64) (map[int64]int, error) {
	out, err := c.counts(ctx, "api_key", ids)
	if err != nil {
		return nil, err
	}
	if c.keyStats != nil {
		stats, err := c.keyStats.GetAPIKeyConcurrencyBatch(ctx, ids)
		if err != nil {
			return nil, err
		}
		for id, n := range stats {
			out[id] += n
		}
	}
	return out, nil
}
func (c *regionalConcurrencyCache) TrackAPIKeySlot(ctx context.Context, id int64, request string) error {
	if c.keyStats == nil {
		return errors.New("regional key stats cache unavailable")
	}
	return c.keyStats.TrackAPIKeySlot(ctx, id, request)
}
func (c *regionalConcurrencyCache) ReleaseAPIKeySlot(ctx context.Context, id int64, request string) error {
	if err := c.release(ctx, "api_key", id, request); err != nil {
		return err
	}
	if c.keyStats != nil {
		return c.keyStats.ReleaseAPIKeySlot(ctx, id, request)
	}
	return nil
}
func (c *regionalConcurrencyCache) RefreshAPIKeySlot(ctx context.Context, id int64, request string) (bool, error) {
	r := slots.Resource{Kind: "api_key", ID: id}
	var held bool
	var err error
	if c.primary != nil {
		held, err = c.primary.Held(ctx, r, request)
	} else {
		held, err = c.local.Held(ctx, r, request)
	}
	if err != nil || held {
		return held, err
	}
	if c.keyRefresh != nil {
		return c.keyRefresh.RefreshAPIKeySlot(ctx, id, request)
	}
	return false, nil
}
func (c *regionalConcurrencyCache) APIKeySlotRefreshInterval() time.Duration { return 10 * time.Second }
func (c *regionalConcurrencyCache) APIKeySlotTTL() time.Duration             { return time.Minute }
func (c *regionalConcurrencyCache) AcquireOpenAIWSIngressLease(ctx context.Context, id int64, max int, lease string) (bool, error) {
	if c.wsIngress == nil {
		return false, errors.New("regional WS ingress cache unavailable")
	}
	return c.wsIngress.AcquireOpenAIWSIngressLease(ctx, id, max, lease)
}
func (c *regionalConcurrencyCache) RefreshOpenAIWSIngressLease(ctx context.Context, id int64, lease string) (bool, error) {
	if c.wsIngress == nil {
		return false, errors.New("regional WS ingress cache unavailable")
	}
	return c.wsIngress.RefreshOpenAIWSIngressLease(ctx, id, lease)
}
func (c *regionalConcurrencyCache) ReleaseOpenAIWSIngressLease(ctx context.Context, id int64, lease string) error {
	if c.wsIngress == nil {
		return errors.New("regional WS ingress cache unavailable")
	}
	return c.wsIngress.ReleaseOpenAIWSIngressLease(ctx, id, lease)
}
func (c *regionalConcurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	out := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, a := range accounts {
		n, err := c.GetAccountConcurrency(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		waiting, err := c.GetAccountWaitingCount(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		rate := 0
		if a.MaxConcurrency > 0 {
			rate = (n + waiting) * 100 / a.MaxConcurrency
		}
		out[a.ID] = &service.AccountLoadInfo{AccountID: a.ID, CurrentConcurrency: n, WaitingCount: waiting, LoadRate: rate}
	}
	return out, nil
}
func (c *regionalConcurrencyCache) GetUsersLoadBatch(ctx context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	if len(users) == 0 {
		return map[int64]*service.UserLoadInfo{}, nil
	}
	before, err := c.userLiveLoadSnapshot(ctx, users)
	if err != nil {
		return nil, err
	}
	liveCounts := make(map[int64]int)
	excluded := make(map[int64][]string)
	for _, row := range before {
		if row.State == "active" {
			if !row.Fresh {
				return nil, errors.New("regional Live user statistics freshness unavailable")
			}
			liveCounts[row.UserID]++
		}
		// Closed/aborted handoffs also exclude their retired source, including
		// when Redis cleanup failed or is still queued behind the SQL commit.
		if row.Source != "" {
			excluded[row.UserID] = append(excluded[row.UserID], row.Source)
		}
	}
	var out map[int64]*service.UserLoadInfo
	if len(excluded) > 0 {
		reader, ok := c.ConcurrencyCache.(interface {
			getUsersLoadBatchExcluding(context.Context, []service.UserWithConcurrency, map[int64][]string) (map[int64]*service.UserLoadInfo, error)
		})
		if !ok {
			return nil, errors.New("regional user statistics reader unavailable")
		}
		out, err = reader.getUsersLoadBatchExcluding(ctx, users, excluded)
	} else {
		out, err = c.ConcurrencyCache.GetUsersLoadBatch(ctx, users)
	}
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		n, err := c.count(ctx, "user", u.ID)
		if err != nil {
			return nil, err
		}
		if out[u.ID] == nil {
			return nil, errors.New("regional user statistics incomplete")
		}
		n += out[u.ID].CurrentConcurrency + liveCounts[u.ID]
		out[u.ID].CurrentConcurrency = n
		if u.MaxConcurrency > 0 {
			out[u.ID].LoadRate = (n + out[u.ID].WaitingCount) * 100 / u.MaxConcurrency
		}
	}
	// Detect inserts, migration, refresh, close and abort across Redis sampling.
	// A changed/expired proof is unknown, never an authoritative zero/double count.
	after, err := c.userLiveLoadSnapshot(ctx, users)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(before, after) {
		return nil, errors.New("regional Live user statistics changed during sampling")
	}
	return out, nil
}

// Persistent ownership is never reclaimed by Redis TTL/startup cleanup.
func (c *regionalConcurrencyCache) CleanupStaleProcessSlots(context.Context, string) error {
	return nil
}
func (c *regionalConcurrencyCache) CleanupExpiredAccountSlots(context.Context, int64) error {
	return nil
}
func (c *regionalConcurrencyCache) CleanupExpiredAccountSlotKeys(context.Context) error { return nil }

// CheckRegionalSlots observes capacity; actual requests must still acquire it
// transactionally. It never renews a grant or releases an in-flight holder.
func (c *regionalConcurrencyCache) CheckRegionalSlots(ctx context.Context) error {
	if c.runtimeConfig == nil || c.runtimeFailed.Load() {
		return errors.New("regional slots runtime unavailable")
	}
	if c.local == nil {
		return errors.New("regional local slots required")
	}
	if len(c.runtimeConfig.Targets) == 0 {
		return errors.New("regional slots targets missing")
	}
	for _, target := range c.runtimeConfig.Targets {
		var ready bool
		err := c.database().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM regional_local_slot_grants WHERE region_id=$1 AND kind=$2 AND resource_id=$3 AND state='open' AND used<slots AND (grant_payload->>'issued_at')::timestamptz<=clock_timestamp() AND (grant_payload->>'expires_at')::timestamptz>clock_timestamp())", c.local.Region, target.Resource.Kind, target.Resource.ID).Scan(&ready)
		if err != nil {
			return err
		}
		if !ready {
			return slots.ErrUnavailable
		}
	}
	return nil
}
