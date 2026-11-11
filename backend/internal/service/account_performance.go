package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
)

var ErrPerformanceConflict = errors.New("performance monitor changed; reload before saving")

type PerformanceBounds struct {
	Min *float64 `json:"min"`
	Max *float64 `json:"max"`
}

func (b PerformanceBounds) Validate(percent bool) error {
	for _, v := range []*float64{b.Min, b.Max} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > 1e9 || (percent && *v > 100)) {
			return fmt.Errorf("invalid performance threshold")
		}
	}
	if b.Min != nil && b.Max != nil && *b.Min > *b.Max {
		return fmt.Errorf("minimum exceeds maximum")
	}
	return nil
}
func (b PerformanceBounds) Breached(v *float64) bool {
	return v != nil && ((b.Min != nil && *v < *b.Min) || (b.Max != nil && *v > *b.Max))
}

type PerformanceConfig struct {
	Enabled       bool              `json:"enabled"`
	TPS           PerformanceBounds `json:"tps"`
	Degradation   PerformanceBounds `json:"degradation"`
	TTFT          PerformanceBounds `json:"ttft"`
	UpstreamError PerformanceBounds `json:"upstream_error"`
}

func (c PerformanceConfig) Validate() error {
	for i, b := range []PerformanceBounds{c.TPS, c.Degradation, c.TTFT} {
		if err := b.Validate(i == 1); err != nil {
			return err
		}
	}
	if err := c.UpstreamError.Validate(true); err != nil {
		return err
	}
	return nil
}

type AccountPerformance struct {
	UpstreamError      upstreamstats.Evidence `json:"upstream_error"`
	AccountID          int64                  `json:"account_id"`
	AvgTPS             *float64               `json:"avg_tps"`
	DegradationPercent *float64               `json:"degradation_percent"`
	AvgTTFTMs          *float64               `json:"avg_ttft_ms"`
	AvgCachePercent    *float64               `json:"avg_cache_percent"`
	CacheSamples       int                    `json:"cache_samples"`
	TPSSamples         int                    `json:"tps_samples"`
	TTFTSamples        int                    `json:"ttft_samples"`
	QualitySamples     int                    `json:"quality_samples"`
	Config             PerformanceConfig      `json:"config"`
	Revision           int64                  `json:"revision"`
	Paused             bool                   `json:"paused"`
	PauseGeneration    int64                  `json:"pause_generation"`
	PausedAt           *time.Time             `json:"paused_at"`
	Reason             string                 `json:"reason"`
	WindowStart        time.Time              `json:"window_start"`
	WindowEnd          time.Time              `json:"window_end"`
}

// Snapshot callers own the returned evidence; do not expose cache pointers.
func cloneAccountPerformance(in *AccountPerformance) *AccountPerformance {
	if in == nil {
		return nil
	}
	out := *in
	clone := func(p *float64) *float64 {
		if p == nil {
			return nil
		}
		value := *p
		return &value
	}
	out.AvgTPS, out.AvgTTFTMs, out.DegradationPercent = clone(in.AvgTPS), clone(in.AvgTTFTMs), clone(in.DegradationPercent)
	out.AvgCachePercent = clone(in.AvgCachePercent)
	out.UpstreamError.Percent = clone(in.UpstreamError.Percent)
	for _, bounds := range []*PerformanceBounds{&out.Config.TPS, &out.Config.TTFT, &out.Config.Degradation, &out.Config.UpstreamError} {
		bounds.Min, bounds.Max = clone(bounds.Min), clone(bounds.Max)
	}
	if in.PausedAt != nil {
		value := *in.PausedAt
		out.PausedAt = &value
	}
	return &out
}

// Shared validation for display evidence and scheduling. Unknown is not zero.
func validPerformanceMetric(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
}

func performanceUpstreamErrorRate(e upstreamstats.Evidence) *float64 {
	if e.Source != "process_local_http_responses" || e.Total <= 0 || e.Failures < 0 || e.Failures > e.Total || !validPerformanceMetric(e.Percent) || *e.Percent > 100 {
		return nil
	}
	value := float64(e.Failures) / float64(e.Total)
	return &value
}

func (m AccountPerformance) Breaches() []string {
	var reasons []string
	if m.Config.TPS.Breached(m.AvgTPS) {
		reasons = append(reasons, "tps")
	}
	if m.Config.Degradation.Breached(m.DegradationPercent) {
		reasons = append(reasons, "degradation")
	}
	if m.Config.TTFT.Breached(m.AvgTTFTMs) {
		reasons = append(reasons, "ttft")
	}
	// This is process-local HTTP response evidence, not a final-request error
	// rate. A partial process window can contain usable samples; no response
	// samples is unknown, while zero failures among real samples is valid 0%.
	if p := m.UpstreamError.Percent; m.UpstreamError.Total > 0 && p != nil &&
		!math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 100 &&
		m.Config.UpstreamError.Breached(p) {
		reasons = append(reasons, "upstream_error")
	}
	return reasons
}

type PerformanceEvent struct {
	ID              int64           `json:"id"`
	Action          string          `json:"action"`
	ActorID         *int64          `json:"actor_id"`
	Revision        int64           `json:"revision"`
	PauseGeneration int64           `json:"pause_generation"`
	Evidence        json.RawMessage `json:"evidence"`
	CreatedAt       time.Time       `json:"created_at"`
}
type AccountPerformanceRepository interface {
	Events(context.Context, int64) ([]PerformanceEvent, error)
	Read(context.Context, []int64, time.Time) (map[int64]AccountPerformance, error)
	Save(context.Context, int64, PerformanceConfig, int64, int64) error
	Release(context.Context, int64, int64, int64, int64) error
	Evaluate(context.Context, int64, time.Time) error
	EnabledIDs(context.Context) ([]int64, error)
	Paused(context.Context, []int64) (map[int64]bool, error)
}

type AccountPerformanceService struct {
	Repo   AccountPerformanceRepository
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// Only the evaluator goroutine owns this cursor. Advance on attempts,
	// including timeouts, so slow accounts cannot starve the remaining IDs.
	evaluationCursor int64
}

func NewAccountPerformanceService(repo AccountPerformanceRepository) *AccountPerformanceService {
	return &AccountPerformanceService{Repo: repo}
}
func (s *AccountPerformanceService) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.evaluate(ctx)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.evaluate(ctx)
			}
		}
	}()
}
func (s *AccountPerformanceService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}
func (s *AccountPerformanceService) evaluate(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	ids, err := s.Repo.EnabledIDs(ctx)
	if err != nil {
		slog.Error("account performance list", "error", err)
		return
	}
	start := 0
	for i, id := range ids {
		if id > s.evaluationCursor {
			start = i
			break
		}
	}
	for offset := range ids {
		if ctx.Err() != nil {
			return
		}
		id := ids[(start+offset)%len(ids)]
		s.evaluationCursor = id
		if err := s.Repo.Evaluate(ctx, id, time.Now()); err != nil {
			slog.Error("account performance evaluate", "account_id", id, "error", err)
		}
	}
}

// Live authority check is intentionally outside all Redis/regional snapshots.
// A database error fails closed; cached permission cannot override an auto pause.
type PerformancePauseReader interface {
	PerformancePaused(context.Context, []int64) (map[int64]bool, error)
}

// A subscriber's permission comes only from its authenticated, expiring local
// snapshot. Missing accounts are denials, never a lookup in the local database.
type localPerformancePauseReader struct{ reader LocalSchedulerSnapshotReader }

func (r localPerformancePauseReader) PerformancePaused(ctx context.Context, ids []int64) (map[int64]bool, error) {
	details, ok := r.reader.(LocalSchedulerDetailReader)
	if !ok {
		return nil, ErrLocalSchedulerSnapshotUnavailable
	}
	accounts, err := details.GetLocalAccounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	paused := make(map[int64]bool, len(ids))
	for _, id := range ids {
		a := accounts[id]
		paused[id] = a == nil || !a.IsSchedulable()
	}
	return paused, nil
}

func (s *SchedulerSnapshotService) filterPerformanceAccounts(ctx context.Context, accounts []Account) ([]Account, error) {
	if s.localSnapshotReader != nil {
		return accounts, nil
	}
	reader, ok := s.accountRepo.(PerformancePauseReader)
	if !ok || len(accounts) == 0 {
		return accounts, nil
	}
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	paused, err := reader.PerformancePaused(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		if !a.PerformancePaused && !paused[a.ID] {
			out = append(out, a)
		}
	}
	return out, nil
}
func (s *SchedulerSnapshotService) markPerformanceAccounts(ctx context.Context, accounts map[int64]*Account) (map[int64]*Account, error) {
	if s.localSnapshotReader != nil {
		return accounts, nil
	}
	reader, ok := s.accountRepo.(PerformancePauseReader)
	if !ok || len(accounts) == 0 {
		return accounts, nil
	}
	ids := make([]int64, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	paused, err := reader.PerformancePaused(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*Account, len(accounts))
	for id, a := range accounts {
		if a != nil {
			copy := *a
			copy.PerformancePaused = copy.PerformancePaused || paused[id]
			out[id] = &copy
		}
	}
	return out, nil
}
