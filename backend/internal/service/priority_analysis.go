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
	Model      string
	RouteModel string `json:"-"` // Resolved server-side; never bound from HTTP.
	GroupID    *int64
	Minutes    int
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
	ctx, cancel := context.WithTimeout(ctx, priorityAnalysisTimeout)
	defer cancel()
	preparation, release, err := s.priorityAnalysis.prepare(ctx)
	if err != nil {
		return nil, err
	}
	// Release preparation before joining a flight, including every error path.
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	cfg, err := s.settingService.GetPrioritySchedulingConfig(preparation)
	if err != nil {
		return nil, err
	}
	if s.channelService != nil && q.GroupID != nil {
		lookup, err := s.priorityAnalysisChannel(preparation, *q.GroupID)
		if err != nil {
			return nil, fmt.Errorf("priority analysis mapping: %w", err)
		}
		if lookup != nil {
			q.RouteModel = resolveMapping(lookup, *q.GroupID, q.Model).MappedModel
		}
	}
	key := priorityAnalysisFlightKey{model: q.Model, routeModel: q.RouteModel, revision: priorityConfigRevision(cfg), minutes: q.Minutes}
	key.principal, _ = ctx.Value(priorityAnalysisPrincipalKey{}).(string)
	if key.principal == "" {
		key.private = new(byte)
	}
	if q.GroupID != nil {
		id := *q.GroupID
		q.GroupID = &id
		key.group = id
	}
	release()
	released = true
	return s.priorityAnalysis.read(ctx, key, func(work context.Context) (*PriorityAnalysis, error) {
		return ReadPriorityAnalysis(work, s.accountRepo, s.usageLogRepo, cfg, q)
	})
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
	if accounts == nil || !ok {
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
	routeModel := strings.TrimSpace(q.RouteModel)
	if routeModel == "" {
		routeModel = q.Model
	}
	selected := make([]Account, 0, len(pool))
	ids := make([]int64, 0, len(pool))
	for _, a := range pool {
		if a.IsModelSupported(routeModel) && a.IsModelAllowedInGroup(q.GroupID, routeModel) {
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
	if len(ids) > 0 {
		signals, err = reader.ReadPrioritySchedulingSignals(ctx, PrioritySchedulingQuery{AccountIDs: ids, Model: q.Model, QualityModel: routeModel, GroupID: q.GroupID, UsageSince: since, UsageUntil: now, QualitySince: now.Add(-time.Duration(cfg.QualityMaxAgeHours) * time.Hour), LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, TargetTTFTMs: cfg.TargetTTFTMs})
		if err != nil {
			return nil, fmt.Errorf("priority analysis history: %w", err)
		}

	}
	snapshot := &PrioritySchedulingSnapshot{At: now, EvaluatedAt: now, Model: q.Model, GroupID: q.GroupID, Mode: cfg.Mode, SelectionPolicy: "read_only_analysis", PerformanceSource: "account_performance", ConfigRevision: revision, WindowMinutes: q.Minutes, MinSamples: cfg.MinSamples, LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, LatencyPenaltyMode: cfg.LatencyPenaltyMode, HistoryReady: true, HistoryStatus: "ready", HistoryObservedAt: &now, HistoryWindowEnd: &now, Candidates: make([]PrioritySchedulingScore, 0, len(ids))}
	series := &PriorityLatencySeries{ConfigRevision: revision, TargetTTFTMs: cfg.TargetTTFTMs, At: now, Model: q.Model, GroupID: q.GroupID, BucketMinutes: 5, LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, Accounts: make([]PriorityLatencyAccountSeries, 0, len(ids))}
	for i := range selected {
		a := &selected[i]
		item := openAIAccountCandidateScore{account: a}
		signal, present := signals[a.ID]
		if !present {
			return nil, errors.New("priority analysis incomplete history")
		}
		score := applyPriorityCandidate(cfg, &item, signal, now)
		score.HistoryStatus = "ready"
		score.CapacityBand = priorityCapacityBand(item)
		score.SelectionWeight = prioritySelectionWeight(item, now)
		score.BoundGroups = priorityAccountGroupCount(a)
		snapshot.Candidates = append(snapshot.Candidates, score)

	}
	slices.SortStableFunc(snapshot.Candidates, comparePrioritySnapshotScores)
	return &PriorityAnalysis{Snapshot: snapshot, Latency: series}, nil
}

// PriorityAnalysisModels uses persisted configuration only; it never performs
// upstream discovery or limits the catalog to the latest/transient routing pool.
func (s *OpenAIGatewayService) PriorityAnalysisModels(ctx context.Context, groupID *int64) ([]string, error) {
	if s == nil || s.accountRepo == nil {
		return nil, ErrPriorityLatencySeriesUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctx, release, err := s.priorityAnalysis.prepare(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, groupID, []string{PlatformOpenAI}, false)
	if err != nil {
		return nil, err
	}
	var extra []string
	if s.settingService != nil {
		cfg, err := s.settingService.GetPrioritySchedulingConfig(ctx)
		if err != nil {
			return nil, err
		}
		extra = append(extra, cfg.Models...)
	}
	if s.channelService != nil && groupID != nil {
		lookup, err := s.priorityAnalysisChannel(ctx, *groupID)
		if err != nil {
			return nil, err
		}
		if lookup != nil {
			for name := range lookup.channel.ModelMapping[PlatformOpenAI] {
				extra = append(extra, name)
			}
		}
		if s.channelService.groupRepo != nil {
			group, err := s.channelService.groupRepo.GetByIDLite(ctx, *groupID)
			if err != nil {
				return nil, err
			}
			extra = append(extra, group.ModelAllowlist.Models...)
		}
	}
	return priorityAnalysisModelNames(accounts, groupID, extra), nil
}

func priorityAnalysisModelNames(accounts []Account, groupID *int64, extra []string) []string {
	names := make(map[string]struct{})
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && len(name) <= 200 && !strings.ContainsAny(name, "*?") {
			names[name] = struct{}{}
		}
	}
	for _, name := range defaultModelsListCandidateIDs(PlatformOpenAI) {
		add(name)
	}
	for _, name := range extra {
		add(name)
	}
	for i := range accounts {
		for name := range accounts[i].GetModelMapping() {
			add(name)
		}
		for _, name := range groupAllowedConcreteModels(&accounts[i], groupID) {
			add(name)
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Read the selected channel directly. Routing's shared error cache intentionally
// fails open; diagnostics must propagate those failures instead of reporting an
// authoritative empty pool. The temporary mapping cache is owned by this call.
func (s *OpenAIGatewayService) priorityAnalysisChannel(ctx context.Context, groupID int64) (*channelLookup, error) {
	if s.channelService == nil {
		return nil, nil
	}
	if s.channelService.repo == nil {
		return nil, ErrPriorityLatencySeriesUnavailable
	}
	repo := s.channelService.repo
	id, err := repo.GetChannelIDByGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, nil
	}
	channel, err := repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if channel == nil {
		return nil, errors.New("priority analysis channel missing")
	}
	if !channel.IsActive() {
		return nil, nil
	}
	platforms, err := repo.GetGroupPlatforms(ctx, []int64{groupID})
	if err != nil {
		return nil, err
	}
	platform := platforms[groupID]
	if platform == "" {
		return nil, errors.New("priority analysis group platform missing")
	}
	cache := populateChannelCache([]Channel{*channel}, platforms)
	owned := cache.channelByGroupID[groupID]
	if owned == nil {
		return nil, errors.New("priority analysis channel binding changed")
	}
	return &channelLookup{cache: cache, channel: owned, platform: PlatformOpenAI}, nil
}
