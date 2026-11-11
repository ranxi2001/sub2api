package service

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityFairDistributionIncludesIdleAccount(t *testing.T) {
	scheduler := &defaultOpenAIAccountScheduler{}
	pool := []openAIAccountCandidateScore{
		priorityCandidate(1, 0.1, 0), priorityCandidate(2, 0.1, 0),
		priorityCandidate(3, 0.1, 0), priorityCandidate(4, 0.1, 0),
	}
	// Four usable accounts in the same tier/priority. A few early failures
	// lower the fourth account's score without making it ineligible.
	for i := range pool {
		pool[i].score = 282 - float64(i)
		pool[i].account.Concurrency = 25
		factor := 10000
		pool[i].account.LoadFactor = &factor
	}
	pool[3].score = 265
	counts := make(map[int64]int)
	for i := 0; i < 4000; i++ {
		req := OpenAIAccountScheduleRequest{SessionHash: fmt.Sprintf("new-session-%d", i), RequestedModel: "gpt-test"}
		order := scheduler.buildOpenAISelectionOrder(req, openAIAccountLoadPlan{priorityScheduling: true, topK: 1, candidates: pool})
		require.Len(t, order, 4, "all candidates must remain available for slot races")
		counts[order[0].account.ID]++
	}
	for _, account := range pool {
		require.Greater(t, counts[account.account.ID], 600, "usable accounts need recovery traffic: %v", counts)
		require.Less(t, counts[account.account.ID], 1400, "a small score difference must not monopolize traffic: %v", counts)
	}
	t.Logf("new-session distribution: %v", counts)
}

func TestPriorityScoringUsesRealCapacity(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Mode = "profit"
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualityPassed: 10, QualitySamples: 10})
	candidate := priorityCandidate(1, 0.1, 60)
	normal := scorePriorityCandidate(cfg, candidate, signal, time.Now())
	factor := 10000
	candidate.account.LoadFactor = &factor
	oversized := scorePriorityCandidate(cfg, candidate, signal, time.Now())
	require.Equal(t, normal.Score, oversized.Score, "an oversized load factor must not hide occupied slots")
}

func TestPriorityFairDistributionFavorsSpareCapacity(t *testing.T) {
	busy, idle := priorityCandidate(1, 0.1, 70), priorityCandidate(2, 0.1, 0)
	busy.score, idle.score = 285, 265
	counts := map[int64]int{}
	for i := 0; i < 3000; i++ {
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{busy, idle}, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprint(i)})
		counts[order[0].account.ID]++
	}
	require.Greater(t, counts[2], 2000, "real spare slots must overcome a modest score advantage: %v", counts)
	require.Zero(t, counts[1], "an idle peer must be used before materially busier capacity: %v", counts)
}

func TestPriorityQuotaHeadroomAndUnknownData(t *testing.T) {
	now := time.Now()
	account := priorityCandidate(1, 0.1, 0)
	account.score = 280
	unknown := prioritySelectionWeight(account, now)
	account.account.Extra["codex_usage_updated_at"] = now.Format(time.RFC3339)
	account.account.Extra["codex_7d_reset_at"] = now.Add(24 * time.Hour).Format(time.RFC3339)
	account.account.Extra["codex_7d_used_percent"] = 0.0
	fresh := prioritySelectionWeight(account, now)
	account.account.Extra["codex_7d_used_percent"] = 90.0
	nearlyUsed := prioritySelectionWeight(account, now)
	require.Greater(t, fresh, unknown)
	require.Greater(t, unknown, nearlyUsed)
	require.Greater(t, nearlyUsed, 0.0, "quota is a soft preference; existing quota gates remain authoritative")
	account.account.Extra["codex_usage_updated_at"] = now.Add(-9 * time.Hour).Format(time.RFC3339)
	require.Equal(t, unknown, prioritySelectionWeight(account, now), "stale data is neutral")
}

func TestPriorityColdStartExplorationIsBounded(t *testing.T) {
	healthy, cold, bad := priorityCandidate(1, 0.1, 0), priorityCandidate(2, 0.1, 0), priorityCandidate(3, 0.01, 0)
	healthy.score, cold.score, bad.score = 490, 250, 99
	cold.priorityExploration, bad.priorityExploration = true, true
	counts := map[int64]int{}
	for i := 0; i < 5000; i++ {
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{healthy, cold, bad}, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprintf("explore-%d", i)})
		counts[order[0].account.ID]++
		require.Equal(t, int64(3), order[2].account.ID, "known degraded accounts cannot use the exploration lane")
	}
	require.InDelta(t, 500, counts[2], 100, "cold-start share should stay near 10%%: %v", counts)
	require.Zero(t, counts[3])
	// Explicit account priorities remain authoritative within the same tier.
	cold.account.Priority = 1
	for i := 0; i < 100; i++ {
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{healthy, cold}, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprint(i)})
		require.Equal(t, int64(1), order[0].account.ID)
	}
}

func TestPriorityCapacityBandUsesObservedLoad(t *testing.T) {
	low := priorityCandidate(1, 0.1, 0)
	low.account.Concurrency = 10
	low.loadKnown = true
	low.loadInfo.CurrentConcurrency = 0
	high := low
	high.loadInfo = &AccountLoadInfo{AccountID: 1, CurrentConcurrency: 1}
	require.Equal(t, 0, priorityCapacityBand(low))
	require.Equal(t, 1, priorityCapacityBand(high))
	low.rpmEnabled, low.rpmLimit, low.rpmCurrent = true, 10, 0
	high.rpmEnabled, high.rpmLimit, high.rpmCurrent = true, 10, 1
	require.Equal(t, 0, priorityCapacityBand(low))
	require.Equal(t, 1, priorityCapacityBand(high))
}

func TestPriorityExplorationIncludesQualityReadyAPIKey(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	item := priorityCandidate(1, 0.1, 0)
	item.account.Type = AccountTypeAPIKey
	item.loadKnown = true
	item.loadInfo.CurrentConcurrency = 0
	score := applyPriorityCandidate(cfg, &item, withAccountPerformanceFixture(PrioritySchedulingSignal{
		QualityPassed: 9, QualitySamples: 10,
	}), time.Now())
	require.Equal(t, "insufficient", score.Tier)
	require.True(t, item.priorityExploration)
}

func TestPriorityExplorationRejectsUnknownQuality(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	item := priorityCandidate(1, 0.1, 0)
	item.account.Type = AccountTypeAPIKey
	item.loadKnown = true
	item.loadInfo.CurrentConcurrency = 0
	applyPriorityCandidate(cfg, &item, PrioritySchedulingSignal{}, time.Now())
	require.False(t, item.priorityExploration)
}

func TestPriorityExplorationRequiresSafeUnknownAccount(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{
		1: withAccountPerformanceFixture(PrioritySchedulingSignal{QualityPassed: 10, QualitySamples: 10}),
	}}
	gateway := priorityGateway(cfg, reader)
	scheduler := &defaultOpenAIAccountScheduler{service: gateway}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, 0.1, 0), priorityCandidate(2, 0.1, 90), priorityCandidate(3, 0.1, 0), priorityCandidate(4, 0.1, 0)}
	for i := range pool {
		pool[i].account.Type = AccountTypeOAuth
	}
	pool[2].errorRate = 0.8
	pool[3].loadKnown = false
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test", UseUpstreamTokenCost: true}
	require.Eventually(t, func() bool { _, ready := gateway.prioritySignals(req, cfg, pool); return ready }, time.Second, time.Millisecond)
	plan := openAIAccountLoadPlan{candidates: pool, topK: 1}
	scheduler.applyPriorityScheduling(req, &plan)
	require.True(t, plan.candidates[0].priorityExploration)
	for _, candidate := range plan.candidates[1:] {
		require.False(t, candidate.priorityExploration)
	}
	snapshot := gateway.PrioritySchedulingSnapshot()
	require.Equal(t, "capacity_first", snapshot.SelectionPolicy)
	for _, candidate := range snapshot.Candidates {
		require.Greater(t, candidate.SelectionWeight, 0.0)
	}
}

func TestOpenAIAccountRuntimeStatsIdleErrorsRecover(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	now := time.Now()
	ttft := 16000
	for i := 0; i < 3; i++ {
		stats.reportAt(1, false, &ttft, now)
	}
	rate, _, _ := stats.snapshotAt(1, now)
	require.InDelta(t, 0.488, rate, 1e-10)
	half, latency, known := stats.snapshotAt(1, now.Add(2*time.Minute))
	require.InDelta(t, rate/2, half, 1e-10)
	require.True(t, known)
	require.Equal(t, float64(ttft), latency, "idle recovery must not invent faster latency samples")
	recovered, _, _ := stats.snapshotAt(1, now.Add(6*time.Minute))
	require.InDelta(t, rate/8, recovered, 1e-10)
	stats.reportAt(1, false, nil, now.Add(6*time.Minute))
	after, _, _ := stats.snapshotAt(1, now.Add(6*time.Minute))
	require.InDelta(t, 0.2+0.8*recovered, after, 1e-10, "new failure must still be penalized")
	stats.reportAt(1, true, nil, now.Add(5*time.Minute))
	afterOlder, _, _ := stats.snapshotAt(1, now.Add(6*time.Minute))
	require.InDelta(t, 0.8*after, afterOlder, 1e-10, "out-of-order completion cannot reverse time decay")
}

func TestPrioritySelectionDeterminismAndOverflow(t *testing.T) {
	pool := []openAIAccountCandidateScore{priorityCandidate(1, 0.1, 0), priorityCandidate(2, 0.1, 0), priorityCandidate(3, 0.1, 0)}
	for i := range pool {
		pool[i].score = 280
	}
	req := OpenAIAccountScheduleRequest{SessionHash: "repeatable", RequestedModel: "gpt-test"}
	first := buildPrioritySelectionOrder(pool, req)
	require.Equal(t, first, buildPrioritySelectionOrder(pool, req))
	acquired := []int64{}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{*pool[0].account, *pool[1].account, *pool[2].account}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired, acquireResults: map[int64]bool{1: false, 2: false, 3: false}}),
	}}
	_, _, err := scheduler.tryAcquireOpenAISelectionOrder(context.Background(), req, first)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{1, 2, 3}, acquired, "slot races must keep all overflow candidates")
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		pool[0].score = value
		weight := prioritySelectionWeight(pool[0], time.Now())
		require.False(t, math.IsNaN(weight) || math.IsInf(weight, 0))
		require.Greater(t, weight, 0.0)
	}
}

func TestPriorityOAuthGroupCompetitionKeepsAllAccountsActive(t *testing.T) {
	pool := []openAIAccountCandidateScore{priorityCandidate(1, 0.1, 0), priorityCandidate(2, 0.1, 0), priorityCandidate(3, 0.1, 0), priorityCandidate(4, 0.1, 0)}
	for i := range pool {
		pool[i].account.Type = AccountTypeOAuth
		pool[i].account.GroupIDs = []int64{3, 5, 11, 13, 15}
		pool[i].score = 280
	}
	pool[3].account.GroupIDs = []int64{11, 15}
	groupID := int64(11)
	counts := map[int64]int{}
	for i := 0; i < 5500; i++ {
		order := buildPrioritySelectionOrder(pool, OpenAIAccountScheduleRequest{GroupID: &groupID, SessionHash: fmt.Sprintf("group-session-%d", i)})
		counts[order[0].account.ID]++
	}
	for _, id := range []int64{1, 2, 3} {
		require.InDelta(t, 1000, counts[id], 150, "broader bindings must still receive traffic: %v", counts)
	}
	require.InDelta(t, 2500, counts[4], 200, "fewer competing groups deserve proportionally more spare capacity: %v", counts)
	t.Logf("five/five/five/two group bindings: %v", counts)
	// More headroom on a broader account can overcome the binding preference.
	pool[3].loadInfo.CurrentConcurrency = 9
	require.Greater(t, prioritySelectionWeight(pool[0], time.Now()), prioritySelectionWeight(pool[3], time.Now()))
	// Both binding representations can coexist without double counting.
	pool[3].account.AccountGroups = []AccountGroup{{GroupID: 11}, {GroupID: 15}, {GroupID: 15}}
	require.Equal(t, 2, priorityAccountGroupCount(pool[3].account))
	pool[3].account.GroupIDs = nil
	require.Equal(t, 2, priorityAccountGroupCount(pool[3].account))
	pool[3].account.AccountGroups = nil
	require.Equal(t, 1, priorityAccountGroupCount(pool[3].account), "unknown bindings must remain neutral")
}

func TestPriorityStickyRebalancesWithoutChangingBinding(t *testing.T) {
	for _, test := range []struct {
		name                        string
		preserve, disable, previous bool
		otherGroup                  int64
		expectEscape                bool
	}{
		{name: "movable", expectEscape: true},
		{name: "task_owner", preserve: true},
		{name: "escape_disabled", disable: true},
		{name: "response_owner", previous: true},
		{name: "outside_group", otherGroup: 99},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultPrioritySchedulingConfig()
			cfg.Enabled = true
			groupID := int64(11)
			accounts := []Account{
				{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 30, GroupIDs: []int64{3, 5, 11, 13, 15}},
				{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 20, GroupIDs: []int64{11, 15}},
			}
			if test.otherGroup != 0 {
				accounts[1].GroupIDs = []int64{test.otherGroup}
			}
			gateway := priorityGateway(cfg, &priorityReaderStub{})
			gateway.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:existing-session": 1}}
			gateway.cache = cache
			gateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 18}, 2: {AccountID: 2}}})
			scheduler := &defaultOpenAIAccountScheduler{service: gateway, stats: newOpenAIAccountRuntimeStats()}
			req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: &groupID, SessionHash: "existing-session", StickyAccountID: 1, UseUpstreamTokenCost: true, PreserveStickyBinding: test.preserve, DisableStickyEscape: test.disable}
			if test.previous {
				req.PreviousResponseID = "resp-owner"
			}
			require.Equal(t, test.expectEscape, scheduler.shouldRebalancePrioritySticky(context.Background(), req, &accounts[0]))
			selected, escaped, err := scheduler.selectBySessionHash(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, test.expectEscape, escaped)
			if test.expectEscape {
				require.Nil(t, selected)
			} else {
				require.NotNil(t, selected)
				if selected.ReleaseFunc != nil {
					selected.ReleaseFunc()
				}
			}
			require.Equal(t, int64(1), cache.sessionBindings["openai:existing-session"])
		})
	}
}

func TestPriorityStickyLoadFailureAndHealthyBindingStayPut(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	groupID := int64(11)
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 20, GroupIDs: []int64{11}},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 20, GroupIDs: []int64{11}},
	}
	gateway := priorityGateway(cfg, &priorityReaderStub{})
	gateway.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	gateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 2}, 2: {AccountID: 2}}})
	scheduler := &defaultOpenAIAccountScheduler{service: gateway, stats: newOpenAIAccountRuntimeStats()}
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: &groupID, UseUpstreamTokenCost: true}
	require.False(t, scheduler.shouldRebalancePrioritySticky(context.Background(), req, &accounts[0]))
	gateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadBatchErr: fmt.Errorf("offline")})
	require.False(t, scheduler.shouldRebalancePrioritySticky(context.Background(), req, &accounts[0]))
}
