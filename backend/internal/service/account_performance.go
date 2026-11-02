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
	Enabled     bool              `json:"enabled"`
	TPS         PerformanceBounds `json:"tps"`
	Degradation PerformanceBounds `json:"degradation"`
	TTFT        PerformanceBounds `json:"ttft"`
}

func (c PerformanceConfig) Validate() error {
	for i, b := range []PerformanceBounds{c.TPS, c.Degradation, c.TTFT} {
		if err := b.Validate(i == 1); err != nil {
			return err
		}
	}
	return nil
}

type AccountPerformance struct {
	AccountID          int64             `json:"account_id"`
	AvgTPS             *float64          `json:"avg_tps"`
	DegradationPercent *float64          `json:"degradation_percent"`
	AvgTTFTMs          *float64          `json:"avg_ttft_ms"`
	TPSSamples         int               `json:"tps_samples"`
	TTFTSamples        int               `json:"ttft_samples"`
	QualitySamples     int               `json:"quality_samples"`
	Config             PerformanceConfig `json:"config"`
	Revision           int64             `json:"revision"`
	Paused             bool              `json:"paused"`
	PauseGeneration    int64             `json:"pause_generation"`
	PausedAt           *time.Time        `json:"paused_at"`
	Reason             string            `json:"reason"`
	WindowStart        time.Time         `json:"window_start"`
	WindowEnd          time.Time         `json:"window_end"`
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
