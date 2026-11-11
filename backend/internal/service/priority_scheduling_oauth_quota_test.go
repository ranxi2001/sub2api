package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func quotaPriorityFixture(now time.Time) (PrioritySchedulingConfig, openAIAccountCandidateScore, PrioritySchedulingSignal) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.TargetTTFTMs = 3000
	cfg.Enabled, cfg.OAuthQuotaPriority = true, true
	item := priorityCandidate(1, .1, 0)
	item.account.Type, item.account.Status, item.account.Schedulable = AccountTypeOAuth, StatusActive, true
	item.account.Extra["codex_usage_updated_at"] = now.Format(time.RFC3339Nano)
	for _, w := range []string{"5h", "7d"} {
		item.account.Extra["codex_"+w+"_used_percent"] = 20.0
		item.account.Extra["codex_"+w+"_reset_at"] = now.Add(time.Hour).Format(time.RFC3339)
	}
	healthy := true
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{QualitySamples: 10, QualityPassed: 10, LatestQualityPassed: &healthy, Samples: 10, P90TTFTMs: 500, ProfitSamples: 10, Revenue: 10, BaseCost: 10})
	return cfg, item, signal
}

func TestPriorityOAuthQuotaEvidenceAndCapacity(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		mutate func(*PrioritySchedulingConfig, *openAIAccountCandidateScore, *PrioritySchedulingSignal)
		want   int
	}{
		{"single_slot", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Concurrency = 1
		}, 1},
		{"healthy", func(_ *PrioritySchedulingConfig, _ *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {}, 8},
		{"off", func(c *PrioritySchedulingConfig, _ *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			c.OAuthQuotaPriority = false
		}, 0},
		{"cutoff", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Extra["codex_7d_used_percent"] = 90.0
		}, 0},
		{"below_cutoff", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Extra["codex_7d_used_percent"] = 89.9
		}, 8},
		{"five_hour_exhausted", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Extra["codex_5h_used_percent"] = 100.0
		}, 0},
		{"unknown_quota", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			delete(a.account.Extra, "codex_7d_used_percent")
		}, 0},
		{"unknown_reset", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			delete(a.account.Extra, "codex_7d_reset_at")
		}, 0},
		{"reset", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Extra["codex_7d_reset_at"] = now.Add(-time.Second).Format(time.RFC3339)
		}, 0},
		{"stale", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Extra["codex_usage_updated_at"] = now.Add(-5 * time.Minute).Format(time.RFC3339Nano)
		}, 0},
		{"future", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Extra["codex_usage_updated_at"] = now.Add(time.Minute).Format(time.RFC3339)
		}, 0},
		{"nan", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Extra["codex_7d_used_percent"] = math.NaN()
		}, 0},
		{"unknown_quality", func(_ *PrioritySchedulingConfig, _ *openAIAccountCandidateScore, s *PrioritySchedulingSignal) {
			s.QualitySamples = 0
		}, 0},
		{"latest_degraded_despite_good_average", func(_ *PrioritySchedulingConfig, _ *openAIAccountCandidateScore, s *PrioritySchedulingSignal) {
			bad := false
			s.LatestQualityPassed = &bad
			s.QualityPassed = 0
		}, 0},
		{"bad_average", func(_ *PrioritySchedulingConfig, _ *openAIAccountCandidateScore, s *PrioritySchedulingSignal) {
			s.QualityPassed = 1
		}, 0},
		{"latency", func(_ *PrioritySchedulingConfig, _ *openAIAccountCandidateScore, s *PrioritySchedulingSignal) {
			s.P90TTFTMs = 9999
		}, 0},
		{"latency_recovery_cannot_displace_healthy_api_capacity", func(_ *PrioritySchedulingConfig, _ *openAIAccountCandidateScore, s *PrioritySchedulingSignal) {
			s.P90TTFTMs = 9999
			at := now.Add(-11 * time.Minute)
			s.LatestLatencyAt = &at
			s.LatestBadLatencyAt = &at
		}, 0},
		{"cold_financial_samples", func(_ *PrioritySchedulingConfig, _ *openAIAccountCandidateScore, s *PrioritySchedulingSignal) {
			s.Samples = 0
			s.ProfitSamples = 0
		}, 8},
		{"manual_disabled", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Schedulable = false
		}, 0},
		{"api", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.Type = AccountTypeAPIKey
		}, 0},
		{"busy", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.loadInfo.CurrentConcurrency = 8
		}, 0},
		{"queue", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.loadInfo.WaitingCount = 1
		}, 0},
		{"unknown_load", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.loadKnown = false
		}, 0},
		{"errors", func(c *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			c.ErrorRateWeight = 0 // Existing account EWMA contract.
			a.errorRate = .5
		}, 8},
		{"rpm", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.rpmEnabled = true
			a.rpmLimit = 10
			a.rpmCurrent = 7
		}, 3},
		{"rpm_exhausted", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.rpmEnabled = true
			a.rpmLimit = 10
			a.rpmCurrent = 10
		}, 0},
		{"shared_groups", func(_ *PrioritySchedulingConfig, a *openAIAccountCandidateScore, _ *PrioritySchedulingSignal) {
			a.account.GroupIDs = []int64{1, 2}
		}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, item, signal := quotaPriorityFixture(now)
			tc.mutate(&cfg, &item, &signal)
			signal = withAccountPerformanceFixture(signal)
			applyPriorityCandidate(cfg, &item, signal, now)
			require.Equal(t, tc.want, item.priorityOAuthSpare)
		})
	}
}

func TestPriorityOAuthStandbyTailCapacityAndRestore(t *testing.T) {
	cfg, oauth, signal := quotaPriorityFixture(time.Now())
	applyPriorityCandidate(cfg, &oauth, signal, time.Now())
	api1, api2, api3 := priorityCandidate(2, .1, 0), priorityCandidate(3, .1, 0), priorityCandidate(4, .1, 0)
	for _, a := range []*openAIAccountCandidateScore{&api1, &api2, &api3} {
		a.account.Concurrency = 4
	}
	bad := priorityCandidate(5, .1, 0)
	bad.account.Type = AccountTypeOAuth
	// Already ranked: three API keys, healthy OAuth, then degraded OAuth.
	order := applyPriorityOAuthStandby([]openAIAccountCandidateScore{api1, api2, api3, oauth, bad})
	ids := func(pool []openAIAccountCandidateScore) []int64 {
		out := []int64{}
		for _, a := range pool {
			out = append(out, a.account.ID)
		}
		return out
	}
	require.Equal(t, []int64{2, 1, 3, 4, 5}, ids(order))
	require.Equal(t, []int64{1, 2, 5}, ids(applyPriorityOAuthStandby([]openAIAccountCandidateScore{api1, bad, oauth})), "unqualified OAuth must stay behind an API it originally ranked below")
	require.False(t, order[0].priorityAPIStandby)
	require.True(t, order[2].priorityAPIStandby)
	require.True(t, order[3].priorityAPIStandby)
	// Losing half of OAuth capacity restores the better API key first.
	oauth.priorityOAuthSpare = 4
	order = applyPriorityOAuthStandby([]openAIAccountCandidateScore{api1, api2, api3, oauth, bad})
	require.Equal(t, []int64{2, 3, 1, 4, 5}, ids(order))
	// The same original ranking is restored when quota is exhausted.
	oauth.account.Extra["codex_7d_used_percent"] = 90.0
	applyPriorityCandidate(cfg, &oauth, signal, time.Now())
	order = applyPriorityOAuthStandby([]openAIAccountCandidateScore{api1, api2, api3, oauth, bad})
	require.Equal(t, []int64{2, 3, 4, 1, 5}, ids(order))
	for _, a := range order {
		require.False(t, a.priorityAPIStandby)
	}
	oauth.account.Extra["codex_7d_used_percent"] = 0.0
	applyPriorityCandidate(cfg, &oauth, signal, time.Now())
	require.Equal(t, 8, oauth.priorityOAuthSpare, "fresh reset evidence re-arms preference")
}

func TestPriorityOAuthStandbyUsesActualRankAndSlotFallback(t *testing.T) {
	cfg, oauth, signal := quotaPriorityFixture(time.Now())
	oauth.loadInfo.CurrentConcurrency = 2
	applyPriorityCandidate(cfg, &oauth, signal, time.Now())
	api := priorityCandidate(2, .1, 0)
	api.account.Status, api.account.Schedulable = StatusActive, true
	applyPriorityCandidate(cfg, &api, signal, time.Now())
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test", UseUpstreamTokenCost: true}
	order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{api, oauth}, req)
	require.Equal(t, int64(1), order[0].account.ID, "OAuth overrides a lower-load API only when the quota gate qualifies")
	require.True(t, order[1].priorityAPIStandby)
	var acquired []int64
	gateway := priorityGateway(cfg, &priorityReaderStub{})
	gateway.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*oauth.account, *api.account}}
	gateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired, acquireResults: map[int64]bool{1: false, 2: true}})
	scheduler := &defaultOpenAIAccountScheduler{service: gateway}
	result, _, err := scheduler.tryAcquireOpenAISelectionOrder(context.Background(), req, order)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Acquired)
	require.Equal(t, int64(2), result.Account.ID)
	require.Equal(t, []int64{1, 2}, acquired, "standby is tried in the same selection after an OAuth slot race")
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
	scheduler.recordPrioritySnapshot(req, cfg, order, time.Now())
	snapshot := gateway.PrioritySchedulingSnapshot()
	require.Equal(t, "oauth_quota_priority", snapshot.SelectionPolicy)
	for _, row := range snapshot.Candidates {
		if row.AccountID == 2 {
			require.Equal(t, "standby", row.OAuthQuotaRole)
		}
	}
	require.True(t, api.account.Schedulable, "policy must not mutate manual state")
	// On a retry, excluded OAuth is no longer present: API restores automatically.
	order = buildPrioritySelectionOrder([]openAIAccountCandidateScore{api}, req)
	require.False(t, order[0].priorityAPIStandby)
}

func TestPriorityOAuthQuotaConfigCompatibility(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true}`), &cfg))
	require.False(t, cfg.OAuthQuotaPriority)
	require.Equal(t, 90, cfg.OAuthQuotaThreshold)
	for _, threshold := range []int{0, 101, -1} {
		cfg.OAuthQuotaThreshold = threshold
		require.Error(t, ValidatePrioritySchedulingConfig(cfg))
	}
}

func TestPriorityOAuthQuotaStickyEscapeRespectsOwnershipAndScope(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		preserve, disable, previous, otherGroup bool
		want                                    bool
	}{
		{name: "movable_api", want: true}, {name: "hard_binding", preserve: true}, {name: "escape_disabled", disable: true}, {name: "previous_response", previous: true}, {name: "other_group", otherGroup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, oauth, signal := quotaPriorityFixture(time.Now())
			groupID := int64(11)
			oauth.account.GroupIDs = []int64{groupID}
			if tc.otherGroup {
				oauth.account.GroupIDs = []int64{99}
			}
			api := priorityCandidate(2, .1, 0)
			api.account.Status, api.account.Schedulable, api.account.GroupIDs = StatusActive, true, []int64{groupID}
			gateway := priorityGateway(cfg, &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: signal, 2: signal}})
			gateway.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*oauth.account, *api.account}}
			gateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{1: oauth.loadInfo, 2: api.loadInfo}})
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:quota-session": 2}}
			gateway.cache = cache
			req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test", GroupID: &groupID, UseUpstreamTokenCost: true, SessionHash: "quota-session", StickyAccountID: 2, PreserveStickyBinding: tc.preserve, DisableStickyEscape: tc.disable}
			if tc.previous {
				req.PreviousResponseID = "resp-owner"
			}
			require.Eventually(t, func() bool {
				_, ready := gateway.prioritySignals(req, cfg, []openAIAccountCandidateScore{oauth, api})
				return ready
			}, time.Second, time.Millisecond)
			scheduler := &defaultOpenAIAccountScheduler{service: gateway, stats: newOpenAIAccountRuntimeStats()}
			require.Equal(t, tc.want, scheduler.shouldRebalancePrioritySticky(context.Background(), req, api.account))
			require.Equal(t, int64(2), cache.sessionBindings["openai:quota-session"], "checking escape never rewrites the binding")
		})
	}
}

func TestPriorityOAuthQuotaScopeAndPartitions(t *testing.T) {
	cfg, oauth, signal := quotaPriorityFixture(time.Now())
	cfg.GroupIDs = []int64{11}
	cfg.Models = []string{"gpt-test"}
	api := priorityCandidate(2, .1, 0)
	gateway := priorityGateway(cfg, &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: signal, 2: signal}})
	scheduler := &defaultOpenAIAccountScheduler{service: gateway}
	group := int64(11)
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: &group, RequestedModel: "gpt-test", UseUpstreamTokenCost: true}
	require.Eventually(t, func() bool {
		_, ready := gateway.prioritySignals(req, cfg, []openAIAccountCandidateScore{oauth, api})
		return ready
	}, time.Second, time.Millisecond)
	for _, other := range []OpenAIAccountScheduleRequest{
		{Platform: PlatformOpenAI, RequestedModel: "gpt-test", UseUpstreamTokenCost: true},
		{Platform: PlatformOpenAI, GroupID: &group, RequestedModel: "other", UseUpstreamTokenCost: true},
		{Platform: PlatformAnthropic, GroupID: &group, RequestedModel: "gpt-test", UseUpstreamTokenCost: true},
		{Platform: PlatformOpenAI, GroupID: &group, RequestedModel: "gpt-test", UseUpstreamTokenCost: true, RequiredImageCapability: OpenAIImagesCapabilityNative},
	} {
		plan := openAIAccountLoadPlan{candidates: []openAIAccountCandidateScore{oauth, api}}
		require.False(t, scheduler.applyPriorityScheduling(other, &plan))
		require.Zero(t, plan.candidates[0].priorityOAuthSpare)
	}
	// With protocol balancing off, BPS OAuth cannot suppress a native API pool.
	cfg.BalanceProtocols = false
	gateway = priorityGateway(cfg, &priorityReaderStub{})
	scheduler.service = gateway
	oauth.account.Extra["openai_excel_bps"] = true
	applyPriorityCandidate(cfg, &oauth, signal, time.Now())
	applyPriorityCandidate(cfg, &api, signal, time.Now())
	plan := openAIAccountLoadPlan{priorityScheduling: true, topK: 2, candidates: []openAIAccountCandidateScore{api, oauth}}
	order := scheduler.buildOpenAISelectionOrder(req, plan)
	require.Equal(t, int64(1), order[0].account.ID)
	require.Equal(t, int64(2), order[1].account.ID)
	require.False(t, order[1].priorityAPIStandby, "BPS quota cannot suppress the native pool")
	delete(oauth.account.Extra, "openai_excel_bps")
	api.account.Extra["openai_compact_supported"] = true
	req.RequireCompact = true
	order = scheduler.buildOpenAISelectionOrder(req, plan)
	require.Equal(t, int64(2), order[0].account.ID)
	require.False(t, order[0].priorityAPIStandby, "unknown compact OAuth cannot suppress supported API")
}

func TestPriorityExplorationRequiresQualityForAllAccountTypes(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	api := priorityCandidate(1, .1, 0)
	applyPriorityCandidate(cfg, &api, PrioritySchedulingSignal{}, time.Now())
	require.False(t, api.priorityExploration, "unknown quality must not enter the exploration lane")
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{QualitySamples: 10, QualityPassed: 10})
	applyPriorityCandidate(cfg, &api, signal, time.Now())
	require.True(t, api.priorityExploration, "quality-ready API keys can receive bounded exploration")
	api.account.Type = AccountTypeOAuth
	applyPriorityCandidate(cfg, &api, signal, time.Now())
	require.True(t, api.priorityExploration, "safe OAuth cold starts retain exploration")
}
