package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

const (
	performanceDisplayFresh     = time.Minute
	performanceDisplayMaxAge    = 3 * time.Minute
	performanceDisplayQueueSize = 512
	performanceDisplayLease     = 10 * time.Second
)

// Only expensive numerical evidence is cached. Monitor permissions/configuration,
// account deletion and process-local HTTP counters never cross this boundary.
type performanceDisplaySnapshot struct {
	AccountID      int64     `json:"account_id"`
	Minutes        int       `json:"minutes"`
	At             time.Time `json:"at"`
	TPS            *float64  `json:"tps"`
	TTFT           *float64  `json:"ttft"`
	Cache          *float64  `json:"cache"`
	Degradation    *float64  `json:"degradation"`
	TPSSamples     int       `json:"tps_samples"`
	TTFTSamples    int       `json:"ttft_samples"`
	CacheSamples   int       `json:"cache_samples"`
	QualitySamples int       `json:"quality_samples"`
}

type performanceDisplayJob struct {
	id      int64
	minutes int
}
type performanceDisplayLoad func(context.Context, []int64, int, time.Time) (map[int64]service.AccountPerformance, error)
type accountPerformanceDisplayCache struct {
	redis   *redis.Client
	prefix  string
	load    performanceDisplayLoad
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
	mu      sync.Mutex
	stopped bool
	pending map[performanceDisplayJob]bool
	jobs    chan performanceDisplayJob
	wg      sync.WaitGroup
}

func newAccountPerformanceDisplayCache(rdb *redis.Client, database string, load performanceDisplayLoad) *accountPerformanceDisplayCache {
	ctx, cancel := context.WithCancel(context.Background())
	return &accountPerformanceDisplayCache{redis: rdb, prefix: "account-performance:v1:" + database + ":", load: load, ctx: ctx, cancel: cancel, pending: make(map[performanceDisplayJob]bool), jobs: make(chan performanceDisplayJob, performanceDisplayQueueSize)}
}
func (c *accountPerformanceDisplayCache) key(j performanceDisplayJob) string {
	return fmt.Sprintf("%s%d:%d", c.prefix, j.minutes, j.id)
}
func (c *accountPerformanceDisplayCache) stop() {
	c.mu.Lock()
	c.stopped = true
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
}
func (c *accountPerformanceDisplayCache) enqueue(j performanceDisplayJob) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || c.pending[j] || len(c.pending) >= performanceDisplayQueueSize {
		return
	}
	c.once.Do(func() { c.wg.Add(1); go c.run() })
	select {
	case c.jobs <- j:
		c.pending[j] = true
	default:
	}
}
func (c *accountPerformanceDisplayCache) run() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case job := <-c.jobs:
			// One account per query isolates a hot account from its neighbors.
			c.refresh([]performanceDisplayJob{job})
			c.mu.Lock()
			delete(c.pending, job)
			c.mu.Unlock()
		}
	}
}

func (c *accountPerformanceDisplayCache) lookup(ctx context.Context, jobs []performanceDisplayJob, now time.Time) (map[int64]performanceDisplaySnapshot, map[int64]bool, error) {
	values := make(map[int64]performanceDisplaySnapshot)
	backoff := make(map[int64]bool)
	if len(jobs) == 0 {
		return values, backoff, nil
	}
	if c.redis == nil {
		return nil, nil, fmt.Errorf("performance display cache unavailable")
	}
	keys := make([]string, 0, len(jobs)*2)
	for _, j := range jobs {
		keys = append(keys, c.key(j), c.key(j)+":retry")
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	raw, err := c.redis.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, nil, err
	}
	for i, j := range jobs {
		backoff[j.id] = raw[2*i+1] != nil
		str, ok := raw[2*i].(string)
		if !ok {
			continue
		}
		var v performanceDisplaySnapshot
		if json.Unmarshal([]byte(str), &v) != nil || v.AccountID != j.id || v.Minutes != j.minutes {
			continue
		}
		age := now.Sub(v.At)
		if age < 0 || age >= performanceDisplayMaxAge {
			continue
		}
		values[j.id] = v
	}
	return values, backoff, nil
}

var performanceDisplayUnlock = redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`)
var performanceDisplayPublish = redis.NewScript(`if redis.call('GET',KEYS[1]) ~= ARGV[1] then return 0 end redis.call('SET',KEYS[2],ARGV[2],'PX',ARGV[3]) return 1`)

func (c *accountPerformanceDisplayCache) refresh(jobs []performanceDisplayJob) {
	if c.redis == nil || c.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 300*time.Millisecond)
	token := uuid.NewString()
	lock := c.prefix + "refresh"
	owned, err := c.redis.SetNX(ctx, lock, token, performanceDisplayLease).Result()
	cancel()
	if err != nil || !owned {
		return
	} // Redis failure/peer ownership never falls through to SQL.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_ = performanceDisplayUnlock.Run(ctx, c.redis, []string{lock}, token).Err()
	}()
	now := time.Now()
	cached, backoff, err := c.lookup(c.ctx, jobs, now)
	if err != nil {
		return
	}
	ids := make([]int64, 0, len(jobs))
	selected := make([]performanceDisplayJob, 0, len(jobs))
	for _, j := range jobs {
		if v, ok := cached[j.id]; backoff[j.id] || (ok && now.Sub(v.At) < performanceDisplayFresh) {
			continue
		}
		ids = append(ids, j.id)
		selected = append(selected, j)
	}
	if len(ids) == 0 {
		return
	}
	ctx, cancel = context.WithTimeout(c.ctx, 5*time.Second)
	metrics, err := c.load(ctx, ids, jobs[0].minutes, now)
	cancel()
	if err != nil {
		// Bound retry load across replicas while preserving the last good snapshot.
		ctx, cancel = context.WithTimeout(c.ctx, 300*time.Millisecond)
		defer cancel()
		for _, j := range selected {
			_ = performanceDisplayPublish.Run(ctx, c.redis, []string{lock, c.key(j) + ":retry"}, token, "1", 30_000).Err()
		}
		slog.Warn("account performance display refresh failed", "accounts", len(ids))
		return
	}
	ctx, cancel = context.WithTimeout(c.ctx, 300*time.Millisecond)
	defer cancel()
	for _, j := range selected {
		m, ok := metrics[j.id]
		if !ok {
			continue
		}
		snapshot := performanceDisplaySnapshot{AccountID: j.id, Minutes: j.minutes, At: now, TPS: m.AvgTPS, TTFT: m.AvgTTFTMs, Cache: m.AvgCachePercent, Degradation: m.DegradationPercent, TPSSamples: m.TPSSamples, TTFTSamples: m.TTFTSamples, CacheSamples: m.CacheSamples, QualitySamples: m.QualitySamples}
		raw, e := json.Marshal(snapshot)
		if e != nil {
			continue
		}
		_ = performanceDisplayPublish.Run(ctx, c.redis, []string{lock, c.key(j)}, token, string(raw), performanceDisplayMaxAge.Milliseconds()).Err()
	}
}

const performanceDisplayMetadataSQL = `SELECT a.id,COALESCE(m.config,'{}'),COALESCE(m.revision,0),COALESCE(m.paused,false),COALESCE(m.pause_generation,0),m.paused_at,COALESCE(m.reason,'')
FROM accounts a LEFT JOIN account_performance_monitors m ON m.account_id=a.id
WHERE a.id=ANY($1) AND a.deleted_at IS NULL`

func (r *accountPerformanceRepository) readDisplay(ctx context.Context, ids []int64, now time.Time) (map[int64]service.AccountPerformance, error) {
	out := make(map[int64]service.AccountPerformance, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	minutes, err := readAccountPerformanceWindowMinutes(ctx, r.db)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, performanceDisplayMetadataSQL, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	jobs := make([]performanceDisplayJob, 0, len(ids))
	for rows.Next() {
		var m service.AccountPerformance
		var config []byte
		if err = rows.Scan(&m.AccountID, &config, &m.Revision, &m.Paused, &m.PauseGeneration, &m.PausedAt, &m.Reason); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(config, &m.Config); err != nil {
			_ = rows.Close()
			return nil, err
		}
		m.WindowStart, m.WindowEnd = now.Add(-time.Duration(minutes)*time.Minute), now
		out[m.AccountID] = m
		jobs = append(jobs, performanceDisplayJob{m.AccountID, minutes})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	cached, backoff, cacheErr := r.display.lookup(ctx, jobs, now)
	evidence := upstreamstats.Default.Snapshot(ids, now.Add(-time.Duration(minutes)*time.Minute), now)
	for _, j := range jobs {
		m := out[j.id]
		m.UpstreamError = evidence[j.id]
		m.StatisticsStatus = "pending"
		if cacheErr != nil || backoff[j.id] {
			m.StatisticsStatus = "unavailable"
		}
		v, ok := cached[j.id]
		if ok {
			m.AvgTPS, m.AvgTTFTMs, m.AvgCachePercent, m.DegradationPercent = v.TPS, v.TTFT, v.Cache, v.Degradation
			m.TPSSamples, m.TTFTSamples, m.CacheSamples, m.QualitySamples = v.TPSSamples, v.TTFTSamples, v.CacheSamples, v.QualitySamples
			m.WindowStart, m.WindowEnd = v.At.Add(-time.Duration(minutes)*time.Minute), v.At
			m.StatisticsUpdatedAt = &v.At
			m.StatisticsStatus = "ready"
			if now.Sub(v.At) >= performanceDisplayFresh {
				m.StatisticsStatus = "stale"
			}
		}
		if cacheErr == nil && !backoff[j.id] && (!ok || now.Sub(v.At) >= performanceDisplayFresh) {
			r.display.enqueue(j)
		}
		out[j.id] = m
	}
	return out, nil
}
