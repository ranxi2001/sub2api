//go:build unit

package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Use the actual Forward transport observer and dashboard reader, with a CC
// capability for a Responses request. No paid upstream or projected counters.
func TestPriorityObservationResponsesEndpoint(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"mock","stream":false}`)
	upstream := &httpUpstreamRecorder{resp: priorityMockResponse(200, `{"id":"resp_mock","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`, false)}
	svc := newOpenAIRejectedFieldTestService(upstream)
	account := newOpenAIRejectedFieldTestAccount()
	ctx := WithPrioritySchedulingObservation(context.Background(), nil, "gpt-5.5", "/v1/responses")
	_, err := svc.Forward(ctx, newOpenAIRejectedFieldTestContext(body), account, body)
	require.NoError(t, err)
	req := OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.5", HistoryModel: "gpt-5.5", InboundEndpoint: "/v1/responses", RequiredCapability: OpenAIEndpointCapabilityChatCompletions}
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: svc.openaiAccountStats}
	evidence := scheduler.priorityErrorEvidence(req, DefaultPrioritySchedulingConfig(), account.ID, time.Now())
	require.Equal(t, int64(1), evidence.Successes, "Responses capability fallback must read actual Responses ingress")
	require.Equal(t, "/v1/responses", evidence.Endpoint)
	svc.priorityScheduling.latestInput = newPrioritySnapshotInput(req, DefaultPrioritySchedulingConfig(), []openAIAccountCandidateScore{{account: account}}, time.Now())
	snapshot := svc.PrioritySchedulingSnapshot()
	require.Zero(t, snapshot.Candidates[0].Successes, "passive endpoint evidence must not substitute for account performance")
	require.Nil(t, snapshot.Candidates[0].Performance)
}

type observationQueryReader struct {
	UsageLogRepository
	mu      sync.Mutex
	queries []PrioritySchedulingQuery
}

func (r *observationQueryReader) ReadPrioritySchedulingSignals(_ context.Context, q PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, q)
	return map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 6, P90TTFTMs: 1234})}, nil
}

func TestPriorityObservationPublicHistoryModel(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	reader := &observationQueryReader{}
	svc := &OpenAIGatewayService{usageLogRepo: reader}
	group := int64(11)
	req := OpenAIAccountScheduleRequest{RequestedModel: "route-model", HistoryModel: "public-alias", GroupID: &group}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	require.Eventually(t, func() bool { return svc.priorityHistory(req, c, pool).ready() }, time.Second, time.Millisecond)
	reader.mu.Lock()
	require.Len(t, reader.queries, 1)
	require.Equal(t, "public-alias", reader.queries[0].Model)
	require.Equal(t, "route-model", reader.queries[0].QualityModel)
	require.Equal(t, group, *reader.queries[0].GroupID)
	reader.mu.Unlock()
	other := req
	other.HistoryModel = "distinct-public-alias"
	require.Empty(t, svc.priorityHistory(other, c, pool).signals)
	other = req
	other.GroupID = nil
	require.Empty(t, svc.priorityHistory(other, c, pool).signals)
}

func TestPriorityObservationAttemptGroupAndAliasIsolation(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	svc := &OpenAIGatewayService{openaiAccountStats: stats}
	group := int64(11)
	ctx := WithPrioritySchedulingObservation(context.Background(), &group, " public-alias ", "/v1/chat/completions")
	ctx, scope := withPriorityAttemptScope(ctx, " padded-route ", "/v1/chat/completions")
	require.Equal(t, "public-alias", scope.model)
	require.Equal(t, int64(11), scope.groupID)
	_ = ctx
	req := OpenAIAccountScheduleRequest{RequestedModel: "padded-route", HistoryModel: "public-alias", InboundEndpoint: "/v1/chat/completions", GroupID: &group}
	stats.attempts.report(priorityRequestAttemptKey(req, 1), true, time.Now())
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: stats}
	cfg := DefaultPrioritySchedulingConfig()
	require.Equal(t, int64(1), scheduler.priorityErrorEvidence(req, cfg, 1, time.Now()).Successes)
	other := req
	other.HistoryModel = "other-alias"
	require.Zero(t, scheduler.priorityErrorEvidence(other, cfg, 1, time.Now()).Successes)
	other = req
	other.GroupID = nil
	require.Zero(t, scheduler.priorityErrorEvidence(other, cfg, 1, time.Now()).Successes)
	group = 12
	require.Equal(t, int64(11), scope.groupID, "observer owns group value")
	require.Zero(t, scheduler.priorityErrorEvidence(req, cfg, 1, time.Now()).Successes)
}

func TestPriorityObservationCompletedCallFreshness(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: {}}}
	svc := priorityGateway(cfg, reader)
	req := OpenAIAccountScheduleRequest{RequestedModel: "gpt-test", HistoryModel: "public-alias"}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, time.Second, time.Millisecond)
	svc.priorityScheduling.mu.Lock()
	entry := svc.priorityScheduling.entries[priorityHistoryKey(req, cfg)]
	require.LessOrEqual(t, entry.expires.Sub(entry.observed), 5*time.Second, "completed persistence after an empty read must become visible within the cold refresh budget")
	entry.expires = time.Now().Add(-time.Second)
	svc.priorityScheduling.launches = [2]time.Time{}
	svc.priorityScheduling.mu.Unlock()
	// The reader now represents completed, persisted usage; no live/in-flight
	// timing is injected into the cache and nil TTFT stays unknown.
	reader.mu.Lock()
	reader.signal = map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 1, P90TTFTMs: 1200, LatencyUnknownSamples: 1, ProfitSamples: 2})}
	reader.mu.Unlock()
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).signals[1].Samples == 1 }, time.Second, time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); svc.priorityHistory(req, cfg, pool) }()
	}
	wg.Wait()
	reader.mu.Lock()
	require.Equal(t, 2, reader.calls, "polls share two real completed reads")
	reader.mu.Unlock()
	got := svc.priorityHistory(req, cfg, pool).signals[1]
	require.Equal(t, 1, got.LatencyUnknownSamples)
}

func TestPriorityObservationStickyPenaltyAndExpiry(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.Models = []string{"public-alias"}
	group := int64(11)
	cfg.GroupIDs = []int64{group}
	badAt := time.Now().Add(-time.Minute)
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{
		1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 20000, LatestBadLatencyAt: &badAt, QualitySamples: 10, QualityPassed: 10}),
		2: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualitySamples: 10, QualityPassed: 10}),
	}}
	svc := priorityGateway(cfg, reader)
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0), priorityCandidate(2, 1, 0)}
	accounts := make([]Account, 0, 2)
	for _, item := range pool {
		item.account.Status = StatusActive
		item.account.Schedulable = true
		item.account.GroupIDs = []int64{group}
		accounts = append(accounts, *item.account)
	}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sticky-penalty": 1}}
	svc.cache = cache
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	svc.cfg = newSchedulerTestOpenAIWSV2Config()
	svc.cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = false
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: &group, RequestedModel: "gpt-test", HistoryModel: "public-alias", InboundEndpoint: "/v1/responses", UseUpstreamTokenCost: true}
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, time.Second, time.Millisecond)
	ctx := WithPrioritySchedulingObservation(context.Background(), &group, "public-alias", "/v1/responses")
	selectTurn := func(session, previous string, excluded map[int64]struct{}) (*AccountSelectionResult, OpenAIAccountScheduleDecision) {
		t.Helper()
		selection, decision, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, previous, session, "gpt-test", excluded, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true, PlatformOpenAI)
		require.NoError(t, err)
		require.NotNil(t, selection)
		if selection.ReleaseFunc != nil {
			t.Cleanup(selection.ReleaseFunc)
		}
		return selection, decision
	}
	selected, decision := selectTurn("sticky-penalty", "", nil)
	require.Equal(t, int64(2), selected.Account.ID, "ordinary sticky cannot bypass measured penalty even with EWMA escape disabled")
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, int64(2), cache.sessionBindings["openai:sticky-penalty"], "an admitted replacement becomes the session's durable affinity")
	// A separate weighted shortcut must not undo the scored selection policy.
	weightedReq := req
	weightedReq.StickyWeighted = true
	weightedReq.StickyAccountID = 1
	weightedReq.SessionHash = "weighted-penalty"
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: svc.openaiAccountStats}
	fallback, fallbackErr := scheduler.tryFallbackToWeightedSticky(ctx, weightedReq)
	require.NoError(t, fallbackErr)
	require.Nil(t, fallback, "weighted fallback cannot reintroduce the active measured penalty")
	for _, ownerReq := range []OpenAIAccountScheduleRequest{
		func() OpenAIAccountScheduleRequest { r := req; r.DisableStickyEscape = true; return r }(),
		func() OpenAIAccountScheduleRequest { r := req; r.GuardianParentAccountID = 1; return r }(),
		func() OpenAIAccountScheduleRequest { r := req; r.EncryptedReasoning = true; return r }(),
		func() OpenAIAccountScheduleRequest { r := req; r.PreserveStickyBinding = true; return r }(),
	} {
		require.False(t, scheduler.shouldYieldPriorityLatencySticky(ownerReq, pool[0].account))
	}
	// No global ban: the same slow account remains fallback capacity.
	selected, _ = selectTurn("penalty-fallback", "", map[int64]struct{}{2: {}})
	require.Equal(t, int64(1), selected.Account.ID)
	// Provider response ownership wins, even during the measured penalty.
	require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, "resp_penalty_owner", 1, time.Hour))
	selected, decision = selectTurn("penalty-owner", "resp_penalty_owner", nil)
	require.Equal(t, int64(1), selected.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	// An old timestamp alone does not recover the shared rolling mean.
	svc.priorityScheduling.mu.Lock()
	entry := svc.priorityScheduling.entries[priorityHistoryKey(req, cfg)]
	expired := time.Now().Add(-11 * time.Minute)
	signal := entry.signals[1]
	signal.LatestBadLatencyAt = &expired
	entry.signals[1] = signal
	svc.priorityScheduling.mu.Unlock()
	selected, decision = selectTurn("sticky-penalty", "", nil)
	require.Equal(t, int64(2), selected.Account.ID, "expiry must not move the escaped session back to its previous account")
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.Equal(t, int64(2), cache.sessionBindings["openai:sticky-penalty"])
	// A newly observed healthy mean restores the fallback without moving the
	// escaped session back to its former owner.
	svc.priorityScheduling.mu.Lock()
	signal = entry.signals[1]
	signal.Performance = cloneAccountPerformance(signal.Performance)
	recoveredTTFT := 500.0
	signal.Performance.AvgTTFTMs = &recoveredTTFT
	entry.signals[1] = signal
	svc.priorityScheduling.mu.Unlock()
	selected, _ = selectTurn("penalty-recovered", "", map[int64]struct{}{2: {}})
	require.Equal(t, int64(1), selected.Account.ID, "the recovered account remains available to a new session")
	fallback, fallbackErr = scheduler.tryFallbackToWeightedSticky(ctx, weightedReq)
	require.NoError(t, fallbackErr)
	require.NotNil(t, fallback, "a recovered shared mean restores the weighted sticky fallback")
	require.Equal(t, int64(1), fallback.Account.ID)
	require.Equal(t, int64(1), cache.sessionBindings["openai:weighted-penalty"])
	require.Equal(t, int64(2), cache.sessionBindings["openai:sticky-penalty"], "recovery in another session must not undo the replacement binding")
	if fallback.ReleaseFunc != nil {
		fallback.ReleaseFunc()
	}
}

type observationMissingReader struct{ priorityReaderStub }

func (r *observationMissingReader) ReadPrioritySchedulingSignals(context.Context, PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return map[int64]PrioritySchedulingSignal{}, nil
}

func TestPriorityObservationQueryBudgetAndMissingData(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	reader := &observationMissingReader{}
	svc := priorityGateway(cfg, &priorityReaderStub{})
	svc.usageLogRepo = reader
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := OpenAIAccountScheduleRequest{RequestedModel: fmt.Sprintf("model-%d", i)}
			svc.priorityHistory(req, cfg, pool)
		}(i)
	}
	wg.Wait()
	require.Eventually(t, func() bool {
		svc.priorityScheduling.mu.Lock()
		defer svc.priorityScheduling.mu.Unlock()
		return svc.priorityScheduling.active == 0
	}, time.Second, time.Millisecond)
	reader.mu.Lock()
	require.Equal(t, 2, reader.calls, "global rolling budget permits two initial workers and bounds cross-scope bursts")
	reader.mu.Unlock()
	svc.priorityScheduling.mu.Lock()
	defer svc.priorityScheduling.mu.Unlock()
	require.LessOrEqual(t, len(svc.priorityScheduling.entries), priorityHistoryMaxScopes)
	for _, entry := range svc.priorityScheduling.entries {
		require.Empty(t, entry.signals, "an absent reader row must not be fabricated as zero observations")
	}
}

func TestPriorityObservationScopeCompatibility(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	group := int64(11)
	cfg.GroupIDs = []int64{group}
	req := OpenAIAccountScheduleRequest{GroupID: &group, RequestedModel: "route-model", HistoryModel: "public-alias"}
	for _, model := range []string{"public-alias", "route-model"} {
		cfg.Models = []string{model}
		require.True(t, priorityApplies(cfg, req))
	}
	cfg.Models = []string{"distinct-model"}
	require.False(t, priorityApplies(cfg, req))
	cfg.Models = nil
	other := int64(12)
	req.GroupID = &other
	require.False(t, priorityApplies(cfg, req))
}

// Compatibility helper for passive transport collector regressions.
func (s *defaultOpenAIAccountScheduler) priorityErrorEvidence(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig, id int64, now time.Time) PriorityErrorEvidence {
	key := priorityRequestAttemptKey(req, id)
	if s.stats == nil {
		return PriorityErrorEvidence{WindowMinutes: c.ErrorRateWindowMinutes, PriorSamples: max(1, c.ErrorRatePriorSamples), ObservedAt: now, Endpoint: key.endpoint}
	}
	return s.stats.attempts.snapshot(key, now, c.ErrorRateWindowMinutes, c.ErrorRatePriorSamples)
}
