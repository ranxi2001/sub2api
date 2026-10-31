package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"math"
	"sync"
	"testing"
	"time"
)

func priorityCandidate(id int64, rate float64, load int) openAIAccountCandidateScore {
	return openAIAccountCandidateScore{account: &Account{ID: id, Name: "test", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: &rate, Extra: map[string]any{AccountCostMultiplierExtraKey: rate}, Concurrency: 10}, loadKnown: true, loadInfo: &AccountLoadInfo{AccountID: id, LoadRate: load, CurrentConcurrency: load / 10}}
}
func TestPriorityScoringExperienceAndCost(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.TargetTTFTMs = 3000
	c.Mode = "profit"
	good := PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualityPassed: 10, QualitySamples: 10}
	cheap := scorePriorityCandidate(c, priorityCandidate(1, 0.2, 10), good, time.Now())
	expensive := scorePriorityCandidate(c, priorityCandidate(2, 1, 10), good, time.Now())
	require.Greater(t, cheap.Score, expensive.Score)
	require.Equal(t, "eligible", cheap.Tier)
	slow := good
	slow.P90TTFTMs = 5000
	require.Equal(t, "degraded", scorePriorityCandidate(c, priorityCandidate(1, 0, 10), slow, time.Now()).Tier)
	bad := good
	bad.QualityPassed = 5
	require.Equal(t, "degraded", scorePriorityCandidate(c, priorityCandidate(1, 0, 10), bad, time.Now()).Tier)
	require.Equal(t, "degraded", scorePriorityCandidate(c, priorityCandidate(1, 0, 90), good, time.Now()).Tier)
	noLoad := priorityCandidate(1, 0, 0)
	noLoad.loadKnown = false
	require.Equal(t, "insufficient", scorePriorityCandidate(c, noLoad, good, time.Now()).Tier)
	missing := scorePriorityCandidate(c, priorityCandidate(1, 0.2, 10), PrioritySchedulingSignal{}, time.Now())
	require.Equal(t, "insufficient", missing.Tier)
	require.Contains(t, missing.Reasons, "latency_insufficient")
}
func TestPriorityConfigValidationAndScope(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	require.NoError(t, ValidatePrioritySchedulingConfig(c))
	require.False(t, c.applies(nil, "m"))
	c.Enabled = true
	c.GroupIDs = []int64{5}
	c.Models = []string{"m"}
	id := int64(5)
	require.True(t, c.applies(&id, "m"))
	require.False(t, c.applies(nil, "m"))
	require.False(t, c.applies(&id, "other"))
	for _, mutate := range []func(*PrioritySchedulingConfig){func(c *PrioritySchedulingConfig) { c.Mode = "oops" }, func(c *PrioritySchedulingConfig) { c.TargetTTFTMs = 0 }, func(c *PrioritySchedulingConfig) { c.WindowMinutes = 1441 }, func(c *PrioritySchedulingConfig) { c.CostWeight = math.NaN() }, func(c *PrioritySchedulingConfig) { c.GroupIDs = []int64{-1} }, func(c *PrioritySchedulingConfig) {
		c.QualityWeight = 0
		c.LatencyWeight = 0
		c.LoadWeight = 0
		c.CostWeight = 0
	}} {
		bad := c
		mutate(&bad)
		require.Error(t, ValidatePrioritySchedulingConfig(bad))
	}
}

type priorityReaderStub struct {
	UsageLogRepository
	mu     sync.Mutex
	calls  int
	signal map[int64]PrioritySchedulingSignal
	err    error
}

func (r *priorityReaderStub) ReadPrioritySchedulingSignals(_ context.Context, query PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	// The SQL reader returns one aggregate row per requested account, including
	// explicitly empty history. Keep this test adapter faithful to that contract.
	signals := make(map[int64]PrioritySchedulingSignal, len(query.AccountIDs))
	for _, id := range query.AccountIDs {
		signals[id] = r.signal[id]
	}
	return signals, r.err
}
func priorityGateway(c PrioritySchedulingConfig, r *priorityReaderStub) *OpenAIGatewayService {
	settings := &SettingService{settingRepo: &prioritySettingStub{}}
	settings.prioritySchedulingConfig.config = c
	settings.prioritySchedulingConfig.expires = time.Now().Add(time.Hour)
	return &OpenAIGatewayService{settingService: settings, usageLogRepo: r}
}
func TestPrioritySchedulingOrderingAndFallback(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.TargetTTFTMs = 3000
	c.Enabled = true
	c.Mode = "profit"
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: {Samples: 20, P90TTFTMs: 6000, QualityPassed: 10, QualitySamples: 10}, 2: {Samples: 20, P90TTFTMs: 1000, QualityPassed: 10, QualitySamples: 10}}}
	gateway := priorityGateway(c, r)
	scheduler, ok := newDefaultOpenAIAccountScheduler(gateway, nil).(*defaultOpenAIAccountScheduler)
	require.True(t, ok)
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "m", UseUpstreamTokenCost: true}
	candidates := []openAIAccountCandidateScore{priorityCandidate(1, 0.01, 10), priorityCandidate(2, 2, 10), priorityCandidate(3, 0.1, 10)}
	_, ready := gateway.prioritySignals(req, c, candidates)
	require.False(t, ready, "cold history must not block request")
	require.Eventually(t, func() bool { _, ready := gateway.prioritySignals(req, c, candidates); return ready }, time.Second, time.Millisecond)
	plan := openAIAccountLoadPlan{candidates: candidates, topK: 1}
	scheduler.applyPriorityScheduling(req, &plan)
	require.True(t, plan.priorityScheduling)
	order := scheduler.buildOpenAISelectionOrder(req, plan)
	require.Len(t, order, 3, "overflow remains available if best account loses its slot")
	require.Equal(t, int64(2), order[0].account.ID, "expensive eligible account must beat cheap degraded account")
	require.Equal(t, int64(3), order[1].account.ID)
	require.Equal(t, int64(1), order[2].account.ID)
	snap := gateway.PrioritySchedulingSnapshot()
	require.True(t, snap.HistoryReady)
	require.Equal(t, PriorityLatencyDeprioritize, snap.LatencyPenaltyMode)
	require.False(t, snap.LatencyFallback)
	require.Equal(t, int64(2), snap.Candidates[0].AccountID)
	for _, other := range []OpenAIAccountScheduleRequest{{Platform: PlatformOpenAI, RequestedModel: "m", RequiredImageCapability: OpenAIImagesCapabilityNative}, {Platform: PlatformGemini, RequestedModel: "m", UseUpstreamTokenCost: true}, {Platform: PlatformOpenAI, RequestedModel: "m"}} {
		p := openAIAccountLoadPlan{candidates: candidates}
		scheduler.applyPriorityScheduling(other, &p)
		require.False(t, p.priorityScheduling)
	}
	gateway.priorityScheduling.mu.Lock()
	for _, e := range gateway.priorityScheduling.entries {
		e.signals = nil
		e.observed = time.Time{}
		e.expires = time.Now().Add(time.Hour)
	}
	gateway.priorityScheduling.mu.Unlock()
	plan.priorityScheduling = false
	scheduler.applyPriorityScheduling(req, &plan)
	require.True(t, plan.priorityScheduling, "missing history must retain live capacity balancing")
	require.False(t, gateway.PrioritySchedulingSnapshot().HistoryReady)
}
func TestPrioritySignalRefreshCoalescesAndErrorsFallBack(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.Enabled = true
	r := &priorityReaderStub{err: errors.New("offline")}
	gateway := priorityGateway(c, r)
	req := OpenAIAccountScheduleRequest{RequestedModel: "m"}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, 1, 0)}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, ready := gateway.prioritySignals(req, c, pool); require.False(t, ready) }()
	}
	wg.Wait()
	require.Eventually(t, func() bool {
		gateway.priorityScheduling.mu.Lock()
		defer gateway.priorityScheduling.mu.Unlock()
		return gateway.priorityScheduling.active == 0
	}, time.Second, time.Millisecond)
	r.mu.Lock()
	require.Equal(t, 1, r.calls)
	r.mu.Unlock()
}

type prioritySettingStub struct {
	SettingRepository
	mu    sync.Mutex
	value string
	err   error
}

func (r *prioritySettingStub) GetValue(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return "", r.err
	}
	if r.value == "" {
		return "", ErrSettingNotFound
	}
	return r.value, nil
}
func (r *prioritySettingStub) Set(_ context.Context, _, v string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.value = v
	return nil
}
func TestPriorityConfigPersistenceAndColdStart(t *testing.T) {
	repo := &prioritySettingStub{}
	settings := &SettingService{settingRepo: repo}
	require.False(t, settings.prioritySchedulingRuntimeConfig().Enabled)
	c := DefaultPrioritySchedulingConfig()
	c.Enabled = true
	require.NoError(t, settings.SavePrioritySchedulingConfig(context.Background(), c))
	require.True(t, settings.prioritySchedulingRuntimeConfig().Enabled)
	got, err := settings.GetPrioritySchedulingConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, c, got)
	c.Enabled = false
	require.NoError(t, settings.SavePrioritySchedulingConfig(context.Background(), c))
	require.False(t, settings.prioritySchedulingRuntimeConfig().Enabled)
	repo.mu.Lock()
	repo.value = "{"
	repo.mu.Unlock()
	_, err = settings.GetPrioritySchedulingConfig(context.Background())
	require.Error(t, err)
}

func (r *prioritySettingStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestPrioritySchedulingGatewayEntryAndDisable(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	c := DefaultPrioritySchedulingConfig()
	// This fixture compares 8s against 0.5s under the legacy 3s target.
	// The production default is now 15s; using it makes both accounts eligible
	// and allows the cheap account to win instead of testing the degraded tier.
	c.TargetTTFTMs = 3000
	c.Enabled = true
	c.Mode = "profit"
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: {Samples: 20, P90TTFTMs: 8000, QualityPassed: 10, QualitySamples: 10}, 2: {Samples: 20, P90TTFTMs: 500, QualityPassed: 10, QualitySamples: 10}}}
	svc := priorityGateway(c, reader)
	candidates := []openAIAccountCandidateScore{priorityCandidate(1, 0.01, 0), priorityCandidate(2, 1, 0)}
	accounts := make([]Account, 0, 2)
	for _, candidate := range candidates {
		candidate.account.Status = StatusActive
		candidate.account.Schedulable = true
		accounts = append(accounts, *candidate.account)
	}
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	svc.cache = &schedulerTestGatewayCache{}
	svc.cfg = newSchedulerTestOpenAIWSV2Config()
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test", UseUpstreamTokenCost: true}
	require.Eventually(t, func() bool { _, ready := svc.prioritySignals(req, c, candidates); return ready }, time.Second, time.Millisecond)
	selected, _, err := svc.SelectAccountWithScheduler(context.Background(), nil, "", "", "gpt-test", nil, OpenAIUpstreamTransportHTTPSSE, false)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, int64(2), selected.Account.ID)
	if selected.ReleaseFunc != nil {
		selected.ReleaseFunc()
	}
	require.NotNil(t, svc.PrioritySchedulingSnapshot())
	c.Enabled = false
	require.NoError(t, svc.settingService.SavePrioritySchedulingConfig(context.Background(), c))
	require.Nil(t, svc.getOpenAIAccountScheduler(context.Background(), svc.prioritySchedulingRuntimeConfig().Enabled))
}

func TestPriorityLoadFactorDoesNotHideActualConcurrency(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	good := PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualityPassed: 10, QualitySamples: 10}
	a := priorityCandidate(1, 1, 90)
	a.account.Concurrency = 100
	a.loadInfo.CurrentConcurrency = 90
	factor := 10000
	a.account.LoadFactor = &factor
	a.loadInfo.LoadRate = 0
	scored := scorePriorityCandidate(c, a, good, time.Now())
	require.Equal(t, "degraded", scored.Tier)
	require.Equal(t, 90, *scored.LoadPercent)
	a.loadInfo.CurrentConcurrency = 20
	boosted := scorePriorityCandidate(c, a, good, time.Now())
	a.account.LoadFactor = nil
	normal := scorePriorityCandidate(c, a, good, time.Now())
	require.Equal(t, boosted.Score, normal.Score, "load-factor overrides must not inflate actual capacity")
}
func TestPriorityExplicitPriorityWithinExperienceTier(t *testing.T) {
	a, b, poor := priorityCandidate(1, 1, 0), priorityCandidate(2, 1, 0), priorityCandidate(3, 0, 0)
	a.account.Priority = 1
	b.account.Priority = 2
	poor.account.Priority = 0
	a.score = 410
	b.score = 490
	poor.score = 99
	s, ok := newDefaultOpenAIAccountScheduler(&OpenAIGatewayService{}, nil).(*defaultOpenAIAccountScheduler)
	require.True(t, ok)
	got := s.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{}, openAIAccountLoadPlan{priorityScheduling: true, topK: 1, candidates: []openAIAccountCandidateScore{b, poor, a}})
	require.Equal(t, int64(1), got[0].account.ID)
	require.Equal(t, int64(2), got[1].account.ID)
	require.Equal(t, int64(3), got[2].account.ID)
}

func TestPriorityOAuthProfitUsesUserChargeAndTheoreticalCost(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.Mode = "profit"
	a := priorityCandidate(1, 0.001, 0)
	a.account.Type = AccountTypeOAuth
	a.account.Extra[AccountCostMultiplierExtraKey] = 0.1
	signal := PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualityPassed: 10, QualitySamples: 10, ProfitSamples: 10, Revenue: 10, BaseCost: 60}
	score := scorePriorityCandidate(c, a, signal, time.Now())
	require.Equal(t, "usage", score.EconomicsSource)
	require.Equal(t, 4.0, *score.Profit)
	require.InDelta(t, 0.4, *score.Margin, 0.0001)
	differentRate := 10.0
	a.account.RateMultiplier = &differentRate
	require.Equal(t, score.Score, scorePriorityCandidate(c, a, signal, time.Now()).Score, "OAuth profit must not be inferred from current account multiplier")
	signal.BaseCost = 120
	loss := scorePriorityCandidate(c, a, signal, time.Now())
	require.Equal(t, -2.0, *loss.Profit)
	require.Equal(t, "degraded", loss.Tier)
	signal.Revenue = 0
	zero := scorePriorityCandidate(c, a, signal, time.Now())
	require.Nil(t, zero.Margin)
	require.Equal(t, -12.0, *zero.Profit)
	require.False(t, math.IsNaN(zero.Score))
	signal.ProfitSamples = 0
	unknown := scorePriorityCandidate(c, a, signal, time.Now())
	require.Equal(t, "unknown", unknown.EconomicsSource)
	require.Nil(t, unknown.Profit)
	require.Equal(t, "insufficient", unknown.Tier)
}
