package service

import (
	"context"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityHistorySurvivesCandidateChurn(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 9}), 2: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 6})}}
	g := priorityGateway(c, r)
	req := OpenAIAccountScheduleRequest{RequestedModel: "test"}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0), priorityCandidate(2, .1, 0)}
	require.Eventually(t, func() bool { _, ready := g.prioritySignals(req, c, pool); return ready }, time.Second, time.Millisecond)
	signals, ready := g.prioritySignals(req, c, pool[:1])
	require.True(t, ready, "removing a candidate must not discard another account's history")
	require.Equal(t, 9, signals[1].Samples)
	pool = append(pool, priorityCandidate(3, .1, 0))
	signals, _ = g.prioritySignals(req, c, pool)
	require.Equal(t, 9, signals[1].Samples, "new accounts cannot erase known peers")
}

func TestPriorityHistoryRefreshFailureRetainsRecentEvidence(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 9})}}
	g := priorityGateway(c, r)
	req := OpenAIAccountScheduleRequest{RequestedModel: "test"}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	require.Eventually(t, func() bool { _, ready := g.prioritySignals(req, c, pool); return ready }, time.Second, time.Millisecond)
	r.mu.Lock()
	r.err = errors.New("offline")
	r.mu.Unlock()
	g.priorityScheduling.mu.Lock()
	for _, e := range g.priorityScheduling.entries {
		e.expires = time.Now().Add(-time.Second)
	}
	g.priorityScheduling.mu.Unlock()
	g.prioritySignals(req, c, pool)
	require.Eventually(t, func() bool {
		g.priorityScheduling.mu.Lock()
		defer g.priorityScheduling.mu.Unlock()
		return g.priorityScheduling.active == 0
	}, time.Second, time.Millisecond)
	signals, ready := g.prioritySignals(req, c, pool)
	require.True(t, ready)
	require.Equal(t, 9, signals[1].Samples)
}

func TestPrioritySnapshotUsesSharedPerformanceWithoutMovingSelectionTime(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.Enabled = true
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 9, P90TTFTMs: 100, QualitySamples: 9, QualityPassed: 9})
	percent := 25.0
	signal.Performance.UpstreamError = upstreamstats.Evidence{Source: "process_local_http_responses", Total: 4, Failures: 1, Percent: &percent}
	signal.Performance.WindowEnd = time.Now()
	signal.Performance.WindowStart = signal.Performance.WindowEnd.Add(-time.Hour)
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: signal}}
	g := priorityGateway(c, r)
	scheduler := &defaultOpenAIAccountScheduler{service: g}
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "test", UseUpstreamTokenCost: true}
	plan := openAIAccountLoadPlan{candidates: []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}, topK: 1}
	require.Eventually(t, func() bool { return g.priorityHistory(req, c, plan.candidates).ready() }, time.Second, time.Millisecond)
	scheduler.applyPriorityScheduling(req, &plan)
	first := g.PrioritySchedulingSnapshot()
	require.NotNil(t, first)
	require.Equal(t, int64(3), first.Candidates[0].Successes)
	require.Equal(t, int64(1), first.Candidates[0].Failures)
	require.Equal(t, 60, first.Candidates[0].WindowMinutes)
	last := g.PrioritySchedulingSnapshot()
	require.Equal(t, first.At, last.At)
	require.Equal(t, first.Candidates[0].Performance, last.Candidates[0].Performance)
}

func TestPrioritySnapshotRefreshesWithoutAnotherSelection(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.Enabled = true
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 9, P90TTFTMs: 100, QualitySamples: 9, QualityPassed: 9})}}
	g := priorityGateway(c, r)
	scheduler := &defaultOpenAIAccountScheduler{service: g}
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "test", UseUpstreamTokenCost: true}
	plan := openAIAccountLoadPlan{candidates: []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}, topK: 1}
	scheduler.applyPriorityScheduling(req, &plan)
	first := g.PrioritySchedulingSnapshot()
	require.Eventually(t, func() bool { return g.PrioritySchedulingSnapshot().HistoryReady }, time.Second, time.Millisecond)
	last := g.PrioritySchedulingSnapshot()
	require.Equal(t, first.At, last.At, "refresh must not invent a newer selection/load observation")
	require.Equal(t, 9, last.Candidates[0].Samples)
}

func TestPriorityExplorationCanFinishPartialEvidence(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.Enabled = true
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 6, P90TTFTMs: 100, ProfitSamples: 2, QualitySamples: 6, QualityPassed: 6})}}
	g := priorityGateway(c, r)
	scheduler := &defaultOpenAIAccountScheduler{service: g}
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "test", UseUpstreamTokenCost: true}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	pool[0].account.Type = AccountTypeOAuth
	require.Eventually(t, func() bool { _, ready := g.prioritySignals(req, c, pool); return ready }, time.Second, time.Millisecond)
	plan := openAIAccountLoadPlan{candidates: pool, topK: 1}
	scheduler.applyPriorityScheduling(req, &plan)
	require.True(t, plan.candidates[0].priorityExploration, "one sufficient metric cannot strand the remaining metric")
}

func TestPriorityUnknownLoadDoesNotPanic(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	item := priorityCandidate(1, .1, 0)
	item.loadInfo = nil
	item.loadKnown = false
	require.NotPanics(t, func() {
		score := scorePriorityCandidate(c, item, PrioritySchedulingSignal{}, time.Now())
		require.Nil(t, score.LoadPercent)
	})
}

func TestPriorityHistoryScopeIsolationAndExpiry(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 9})}}
	g := priorityGateway(c, r)
	id := int64(11)
	req := OpenAIAccountScheduleRequest{RequestedModel: "test", GroupID: &id}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	require.Eventually(t, func() bool { _, ready := g.prioritySignals(req, c, pool); return ready }, time.Second, time.Millisecond)
	for _, other := range []OpenAIAccountScheduleRequest{{RequestedModel: "other", GroupID: &id}, {RequestedModel: "test"}} {
		h := g.priorityHistory(other, c, pool)
		require.Empty(t, h.signals, "models and groups must never share evidence")
	}
	g.priorityScheduling.mu.Lock()
	entry := g.priorityScheduling.entries[priorityHistoryKey(req, c)]
	entry.observed = time.Now().Add(-90 * time.Second)
	entry.expires = time.Now().Add(time.Minute)
	g.priorityScheduling.mu.Unlock()
	h := g.priorityHistory(req, c, pool)
	require.Equal(t, "stale", h.status)
	require.True(t, h.ready())
	require.Equal(t, 9, h.signals[1].Samples)
	g.priorityScheduling.mu.Lock()
	entry.observed = time.Now().Add(-3 * time.Minute)
	g.priorityScheduling.mu.Unlock()
	h = g.priorityHistory(req, c, pool)
	require.False(t, h.ready())
	require.Empty(t, h.signals, "expired risks and profits must not live forever")
}

func TestPrioritySnapshotOwnsScoringInputsAndOutput(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.Enabled = true
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 9, P90TTFTMs: 100})}}
	g := priorityGateway(c, r)
	scheduler := &defaultOpenAIAccountScheduler{service: g}
	id := int64(11)
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "test", GroupID: &id, UseUpstreamTokenCost: true}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	require.Eventually(t, func() bool { _, ready := g.prioritySignals(req, c, pool); return ready }, time.Second, time.Millisecond)
	plan := openAIAccountLoadPlan{candidates: pool, topK: 1}
	scheduler.applyPriorityScheduling(req, &plan)
	first := g.PrioritySchedulingSnapshot()
	id = 12
	pool[0].account.Extra[AccountCostMultiplierExtraKey] = .9
	pool[0].loadInfo.CurrentConcurrency = 10
	first.Candidates[0].Reasons = append(first.Candidates[0].Reasons, "mutated")
	*first.GroupID = 99
	next := g.PrioritySchedulingSnapshot()
	require.Equal(t, int64(11), *next.GroupID)
	require.Equal(t, .1, *next.Candidates[0].Rate)
	require.Zero(t, *next.Candidates[0].LoadPercent)
	require.NotContains(t, next.Candidates[0].Reasons, "mutated")
}

func TestPriorityHistoryBoundsAccountUnionAndScopes(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	g := priorityGateway(c, &priorityReaderStub{})
	req := OpenAIAccountScheduleRequest{RequestedModel: "bounded"}
	pool := make([]openAIAccountCandidateScore, priorityHistoryMaxAccounts)
	for i := range pool {
		pool[i] = priorityCandidate(int64(i+1), .1, 0)
	}
	require.Eventually(t, func() bool { _, ready := g.prioritySignals(req, c, pool); return ready }, time.Second, time.Millisecond)
	for i := range pool {
		pool[i] = priorityCandidate(int64(i+1+priorityHistoryMaxAccounts), .1, 0)
	}
	g.prioritySignals(req, c, pool)
	g.priorityScheduling.mu.Lock()
	require.LessOrEqual(t, len(g.priorityScheduling.entries[priorityHistoryKey(req, c)].accounts), priorityHistoryMaxAccounts)
	g.priorityScheduling.mu.Unlock()
	for i := 0; i < 80; i++ {
		id := int64(i)
		req.GroupID = &id
		g.prioritySignals(req, c, pool[:1])
	}
	g.priorityScheduling.mu.Lock()
	defer g.priorityScheduling.mu.Unlock()
	require.LessOrEqual(t, len(g.priorityScheduling.entries), priorityHistoryMaxScopes)
	require.LessOrEqual(t, g.priorityScheduling.active, 2)
}

func TestPriorityStickyDoesNotEscapeToKnownRisk(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.Enabled = true
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{2: withAccountPerformanceFixture(PrioritySchedulingSignal{QualityPassed: 0, QualitySamples: 10})}}
	g := priorityGateway(c, r)
	accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 30, GroupIDs: []int64{11, 12, 13}}, {ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 30, GroupIDs: []int64{11}}}
	g.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	g.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 20}, 2: {AccountID: 2}}})
	id := int64(11)
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-6-astra", GroupID: &id, UseUpstreamTokenCost: true}
	pool := []openAIAccountCandidateScore{{account: &accounts[0]}, {account: &accounts[1]}}
	require.Eventually(t, func() bool { _, ready := g.prioritySignals(req, c, pool); return ready }, time.Second, time.Millisecond)
	scheduler := &defaultOpenAIAccountScheduler{service: g}
	require.False(t, scheduler.shouldRebalancePrioritySticky(context.Background(), req, &accounts[0]))
	g.priorityScheduling.mu.Lock()
	g.priorityScheduling.entries[priorityHistoryKey(req, c)].signals[2] = withAccountPerformanceFixture(PrioritySchedulingSignal{QualityPassed: 10, QualitySamples: 10})
	g.priorityScheduling.mu.Unlock()
	require.True(t, scheduler.shouldRebalancePrioritySticky(context.Background(), req, &accounts[0]), "the same compatible idle peer may attract traffic once its known risk is gone")
}

func TestPrioritySelectionWeightRejectsNonFiniteQuota(t *testing.T) {
	item := priorityCandidate(1, .1, 0)
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		weight := prioritySelectionWeightWithQuota(item, value)
		require.False(t, math.IsNaN(weight) || math.IsInf(weight, 0))
		require.Greater(t, weight, 0.0)
	}
}

type priorityBlockingReader struct {
	UsageLogRepository
	entered chan PrioritySchedulingQuery
	release chan struct{}
}

func (r *priorityBlockingReader) ReadPrioritySchedulingSignals(ctx context.Context, q PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	select {
	case r.entered <- q:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-r.release:
		return map[int64]PrioritySchedulingSignal{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestPriorityHistoryWorkersAreBoundedAndDoNotBlockRequests(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	reader := &priorityBlockingReader{entered: make(chan PrioritySchedulingQuery, 10), release: make(chan struct{})}
	defer close(reader.release)
	g := &OpenAIGatewayService{usageLogRepo: reader}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(1); i <= 40; i++ {
			id := i
			g.priorityHistory(OpenAIAccountScheduleRequest{RequestedModel: "test", GroupID: &id}, c, pool)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request waited on history SQL")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-reader.entered:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	select {
	case <-reader.entered:
		t.Fatal("more than two simultaneous refresh workers")
	default:
	}
	g.priorityScheduling.mu.Lock()
	defer g.priorityScheduling.mu.Unlock()
	require.Equal(t, 2, g.priorityScheduling.active)
	require.LessOrEqual(t, len(g.priorityScheduling.entries), priorityHistoryMaxScopes)
}

func BenchmarkPriorityHistoryWarm(b *testing.B) {
	c := DefaultPrioritySchedulingConfig()
	g := priorityGateway(c, &priorityReaderStub{})
	req := OpenAIAccountScheduleRequest{RequestedModel: "benchmark"}
	pool := make([]openAIAccountCandidateScore, 100)
	signals := make(map[int64]PrioritySchedulingSignal, 100)
	accounts := make(map[int64]time.Time, 100)
	for i := range pool {
		id := int64(i + 1)
		pool[i] = priorityCandidate(id, .1, 0)
		signals[id] = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 10})
		accounts[id] = time.Now()
	}
	g.priorityScheduling.entries = map[string]*prioritySignalEntry{priorityHistoryKey(req, c): {signals: signals, accounts: accounts, observed: time.Now(), expires: time.Now().Add(time.Hour)}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.priorityHistory(req, c, pool)
	}
}

func TestPriorityBreakEvenRoundingIsNotLoss(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	item := priorityCandidate(1, .1, 0)
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{ProfitSamples: 10, Revenue: .3, BaseCost: 3})
	score := scorePriorityCandidate(c, item, signal, time.Now())
	require.NotNil(t, score.Profit)
	require.Zero(t, *score.Profit)
	require.NotContains(t, score.Reasons, "historical_loss")
	signal.Revenue = .299999
	score = scorePriorityCandidate(c, item, signal, time.Now())
	require.Less(t, *score.Profit, 0.0)
	require.Contains(t, score.Reasons, "historical_loss", "real small losses must remain visible")
}
