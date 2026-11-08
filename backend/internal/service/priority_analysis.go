package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// PriorityAnalysisQuery is a display scope, never a scheduling configuration or
// a mutation identity. Nil GroupID means ungrouped accounts, not all groups.
type PriorityAnalysisQuery struct {
	Model   string
	GroupID *int64
	Minutes int
}

type PriorityAnalysis struct {
	Snapshot *PrioritySchedulingSnapshot `json:"snapshot"`
	Latency  *PriorityLatencySeries      `json:"latency"`
}

var ErrPriorityAnalysisScope = errors.New("invalid priority analysis scope")

func (s *OpenAIGatewayService) PriorityAnalysis(ctx context.Context, q PriorityAnalysisQuery) (*PriorityAnalysis, error) {
	if s == nil || s.settingService == nil {
		return nil, ErrPriorityLatencySeriesUnavailable
	}
	cfg, err := s.settingService.GetPrioritySchedulingConfig(ctx)
	if err != nil {
		return nil, err
	}
	return ReadPriorityAnalysis(ctx, s.accountRepo, s.usageLogRepo, cfg, q)
}

// ReadPriorityAnalysis only uses repository reads and locally owned scores. It
// never changes latestInput, routing history caches, sticky bindings or Redis
// leases. Capacity remains unknown rather than invoking lease-cleaning readers.
func ReadPriorityAnalysis(ctx context.Context, accounts AccountRepository, usage UsageLogRepository, cfg PrioritySchedulingConfig, q PriorityAnalysisQuery) (*PriorityAnalysis, error) {
	q.Model = strings.TrimSpace(q.Model)
	if q.Model == "" || len(q.Model) > 200 || q.Minutes < 5 || q.Minutes > 1440 || (q.GroupID != nil && *q.GroupID <= 0) {
		return nil, ErrPriorityAnalysisScope
	}
	if err := ValidatePrioritySchedulingConfig(cfg); err != nil {
		return nil, err
	}
	reader, ok := usage.(PrioritySchedulingSignalReader)
	seriesReader, seriesOK := usage.(PriorityLatencySeriesReader)
	if accounts == nil || !ok || !seriesOK {
		return nil, ErrPriorityLatencySeriesUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if q.GroupID != nil {
		id := *q.GroupID
		q.GroupID = &id
	}
	pool, err := accounts.ListModelAvailabilityCandidates(ctx, q.GroupID, []string{PlatformOpenAI}, false)
	if err != nil {
		return nil, fmt.Errorf("priority analysis accounts: %w", err)
	}
	selected := make([]Account, 0, len(pool))
	ids := make([]int64, 0, len(pool))
	for _, a := range pool {
		if a.IsModelSupported(q.Model) && a.IsModelAllowedInGroup(q.GroupID, q.Model) {
			selected = append(selected, a)
			ids = append(ids, a.ID)
		}
	}
	if len(ids) > priorityHistoryMaxAccounts {
		return nil, fmt.Errorf("priority analysis exceeds %d accounts", priorityHistoryMaxAccounts)
	}
	now := time.Now()
	since := now.Add(-time.Duration(q.Minutes) * time.Minute)
	revision := priorityConfigRevision(cfg)
	cfg.WindowMinutes = q.Minutes
	signals := map[int64]PrioritySchedulingSignal{}
	var buckets []PriorityLatencyBucket
	if len(ids) > 0 {
		signals, err = reader.ReadPrioritySchedulingSignals(ctx, PrioritySchedulingQuery{AccountIDs: ids, Model: q.Model, QualityModel: q.Model, GroupID: q.GroupID, UsageSince: since, UsageUntil: now, QualitySince: now.Add(-time.Duration(cfg.QualityMaxAgeHours) * time.Hour), LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, TargetTTFTMs: cfg.TargetTTFTMs})
		if err != nil {
			return nil, fmt.Errorf("priority analysis history: %w", err)
		}
		buckets, err = seriesReader.ReadPriorityLatencySeries(ctx, PriorityLatencySeriesQuery{AccountIDs: ids, Model: q.Model, GroupID: q.GroupID, Since: since, Until: now, BucketMinutes: 5, LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, SlowTTFTMs: cfg.TargetTTFTMs})
		if err != nil {
			return nil, fmt.Errorf("priority analysis latency: %w", err)
		}
	}
	snapshot := &PrioritySchedulingSnapshot{At: now, EvaluatedAt: now, Model: q.Model, GroupID: q.GroupID, Mode: cfg.Mode, SelectionPolicy: "read_only_analysis", ConfigRevision: revision, WindowMinutes: q.Minutes, MinSamples: cfg.MinSamples, LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, LatencyPenaltyMode: cfg.LatencyPenaltyMode, HistoryReady: true, HistoryStatus: "ready", HistoryObservedAt: &now, HistoryWindowEnd: &now, Candidates: make([]PrioritySchedulingScore, 0, len(ids))}
	series := &PriorityLatencySeries{ConfigRevision: revision, At: now, Model: q.Model, GroupID: q.GroupID, BucketMinutes: 5, LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, Accounts: make([]PriorityLatencyAccountSeries, 0, len(ids))}
	byAccount := make(map[int64][]PriorityLatencyBucket)
	for _, b := range buckets {
		byAccount[b.AccountID] = append(byAccount[b.AccountID], b)
	}
	for i := range selected {
		a := &selected[i]
		item := openAIAccountCandidateScore{account: a}
		score := applyPriorityCandidate(cfg, &item, signals[a.ID], now)
		score.HistoryStatus = "ready"
		score.BoundGroups = priorityAccountGroupCount(a)
		snapshot.Candidates = append(snapshot.Candidates, score)
		rows := byAccount[a.ID]
		if rows == nil {
			rows = []PriorityLatencyBucket{}
		}
		row := PriorityLatencyAccountSeries{AccountID: a.ID, AccountName: a.Name, Samples: score.Samples, P50TTFTMs: score.P50TTFTMs, P70TTFTMs: score.P70TTFTMs, P90TTFTMs: score.P90TTFTMs, P95TTFTMs: score.P95TTFTMs, P99TTFTMs: score.P99TTFTMs, LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, SlowSamples: score.SlowSamples, ErrorSamples: score.ErrorSamples, Buckets: rows}
		if score.SelectedTTFTMs != nil {
			row.SelectedTTFTMs = *score.SelectedTTFTMs
		}
		// Both counts use this persisted window, not the scheduler's in-memory
		// attempt estimator (which may describe a different endpoint or window).
		if total := row.Samples + row.ErrorSamples; total > 0 {
			row.ErrorRate = float64(row.ErrorSamples) / float64(total)
		}
		var weighted float64
		var samples int
		for _, b := range rows {
			weighted += b.MeanTTFTMs * float64(b.Samples)
			samples += b.Samples
		}
		if samples > 0 {
			row.MeanTTFTMs = weighted / float64(samples)
		}
		series.Accounts = append(series.Accounts, row)
	}
	slices.SortStableFunc(snapshot.Candidates, comparePrioritySnapshotScores)
	return &PriorityAnalysis{Snapshot: snapshot, Latency: series}, nil
}
