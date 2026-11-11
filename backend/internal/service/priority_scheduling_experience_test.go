package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityComparableCapacityFavorsLowerLatency(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.Mode = "profit"
	cfg.TargetTTFTMs = 9000
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{
		1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 27000, QualitySamples: 10, QualityPassed: 10}),
		2: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 42000, QualitySamples: 10, QualityPassed: 10}),
	}}
	gateway := priorityGateway(cfg, reader)
	scheduler := &defaultOpenAIAccountScheduler{service: gateway}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, 0.1, 0), priorityCandidate(2, 0.1, 0)}
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test", UseUpstreamTokenCost: true}
	require.Eventually(t, func() bool { _, ready := gateway.prioritySignals(req, cfg, pool); return ready }, time.Second, time.Millisecond)
	plan := openAIAccountLoadPlan{candidates: pool, topK: 1}
	scheduler.applyPriorityScheduling(req, &plan)
	counts := map[int64]int{}
	for i := 0; i < 2000; i++ {
		req.SessionHash = fmt.Sprint(i)
		order := scheduler.buildOpenAISelectionOrder(req, plan)
		counts[order[0].account.ID]++
	}
	require.Greater(t, counts[1], 1200, "lower P90 must matter even when both accounts miss the target: %v", counts)
	require.Greater(t, counts[2], 300, "a slow but usable peer still gets recovery/overflow opportunities: %v", counts)
}

func TestPriorityCapacityPrecedesSmallProfitDifference(t *testing.T) {
	pool := []openAIAccountCandidateScore{priorityCandidate(1, 0.1, 0), priorityCandidate(2, 0.1, 0), priorityCandidate(3, 0.1, 0)}
	for i := range pool {
		pool[i].account.Concurrency = 100
	}
	pool[0].loadInfo.CurrentConcurrency, pool[0].score = 43, 66.8
	pool[1].loadInfo.CurrentConcurrency, pool[1].score = 2, 65.9
	pool[2].loadInfo.CurrentConcurrency, pool[2].score = 0, 61.4
	counts := map[int64]int{}
	for i := 0; i < 1000; i++ {
		order := buildPrioritySelectionOrder(pool, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprint(i)})
		counts[order[0].account.ID]++
	}
	require.Zero(t, counts[1], "43%% occupied must not beat idle peers for a small score advantage: %v", counts)
	require.Greater(t, counts[2], 300)
	require.Greater(t, counts[3], 300)
}

func TestPriorityColdGatewayBalancesCompatibleProtocols(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		balance, nativeSlot, bpsSlot, restrictNative bool
		want                                         int64
		wait                                         bool
	}{
		{"idle_native_before_busy_bps", true, true, true, false, 51002, false},
		{"explicit_bps_preference", false, true, true, false, 51001, false},
		{"lost_native_slot_uses_bps_without_wait", true, false, true, false, 51001, false},
		{"both_slots_lost_waits_only_after_alternatives", true, false, false, false, 51002, true},
		{"explicit_model_allowlist_is_not_bypassed", true, true, true, true, 51001, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultPrioritySchedulingConfig()
			cfg.Enabled = true
			cfg.BalanceProtocols = tc.balance
			gateway := priorityGateway(cfg, &priorityReaderStub{})
			groupID := int64(11)
			input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			applyOAuthModelMappings(input, []OAuthModelMappingRule{{From: "gpt-5.4", To: "gpt-5.6-sol"}})
			if tc.restrictNative {
				delete(input.Credentials, OpenAIModelMappingModeKey)
			}
			accounts := []Account{
				{ID: 51001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 100, Priority: 1, GroupIDs: []int64{groupID}, Extra: map[string]any{"openai_excel_bps": true}},
				{ID: 51002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 20, Priority: 1, GroupIDs: []int64{groupID}, Credentials: input.Credentials},
			}
			acquired := []int64{}
			gateway.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
			gateway.cfg = newSchedulerTestOpenAIWSV2Config()
			gateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired, loadMap: map[int64]*AccountLoadInfo{51001: {AccountID: 51001, CurrentConcurrency: 43}, 51002: {AccountID: 51002}}, acquireResults: map[int64]bool{51001: tc.bpsSlot, 51002: tc.nativeSlot}})
			scheduler := newDefaultOpenAIAccountScheduler(gateway, nil)
			selection, _, err := scheduler.Select(context.Background(), OpenAIAccountScheduleRequest{GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-6-sol", UseUpstreamTokenCost: true})
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, tc.want, selection.Account.ID)
			if tc.wait {
				require.NotNil(t, selection.WaitPlan)
				require.Contains(t, acquired, int64(51001))
				require.Contains(t, acquired, int64(51002))
			} else {
				require.True(t, selection.Acquired)
				require.Nil(t, selection.WaitPlan)
				selection.ReleaseFunc()
			}
			if tc.restrictNative {
				require.NotContains(t, acquired, int64(51002))
			}
			require.False(t, accounts[1].IsExcelBPSEnabled(), "balancing must never enable another account protocol")
		})
	}
}

func TestPriorityRPMAndQualityRemainAuthoritative(t *testing.T) {
	healthy, bad := priorityCandidate(1, 1, 60), priorityCandidate(2, 0.01, 0)
	healthy.score, bad.score = 280, 90
	bad.priorityUnhealthy = true
	order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{bad, healthy}, OpenAIAccountScheduleRequest{SessionHash: "health"})
	require.Equal(t, int64(1), order[0].account.ID, "low load must not outrank known quality/error/loss risk")
	bad.priorityUnhealthy = false
	bad.rpmEnabled = true
	bad.rpmCurrent = 95
	bad.rpmLimit = 100
	require.Greater(t, priorityCapacityBand(bad), priorityCapacityBand(healthy), "near-exhausted RPM is not idle capacity")
}

func TestOAuthAliasModeRetainsExplicitAllowlistAndCatalog(t *testing.T) {
	mapping := map[string]any{"gpt-5.4": "gpt-5.6-sol"}
	input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": mapping}}
	applyOAuthModelMappings(input, []OAuthModelMappingRule{{From: "gpt-5.4", To: "gpt-5.6-sol"}})
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: input.Credentials, Extra: map[string]any{"auto_config_initial_revision": "legacy"}}
	require.False(t, a.IsModelSupported("gpt-6-sol"), "do not infer an alias scope from an old auto-config marker")
	a.Credentials[OpenAIModelMappingModeKey] = "aliases"
	require.True(t, a.IsModelSupported("gpt-6-sol"))
	body := []byte(`{"object":"list","data":[{"id":"gpt-6-sol"},{"id":"gpt-5.6-sol"},{"id":"deepseek-chat"}]}`)
	projected, err := projectAccountModelsBody(body, a, nil, false)
	require.NoError(t, err)
	var got struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(projected, &got))
	ids := []string{}
	for _, m := range got.Data {
		ids = append(ids, m.ID)
	}
	require.ElementsMatch(t, []string{"gpt-6-sol", "gpt-5.6-sol", "gpt-5.4"}, ids)
	a.AccountGroups = []AccountGroup{{GroupID: 11, AllowedModels: []string{"gpt-5.4"}}}
	projected, err = projectAccountModelsBody(body, a, &Group{ID: 11}, false)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(projected, &got))
	require.Len(t, got.Data, 1)
	require.Equal(t, "gpt-5.4", got.Data[0].ID)
	for _, invalid := range []any{true, 42, "all"} {
		require.Error(t, ValidateModelMappingMode(map[string]any{OpenAIModelMappingModeKey: invalid}))
	}
}

func TestPriorityNeverPrefersQueuedAccountOverFreePeer(t *testing.T) {
	queued, idle := priorityCandidate(1, 0.01, 0), priorityCandidate(2, 1, 0)
	queued.score, idle.score = 99, 20
	queued.loadInfo.WaitingCount = 1
	for i := 0; i < 200; i++ {
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{queued, idle}, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprint(i)})
		require.Equal(t, int64(2), order[0].account.ID)
	}
}

func TestOAuthAutoAliasesPreserveDefaultModelScope(t *testing.T) {
	input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{}}
	require.True(t, applyOAuthModelMappings(input, []OAuthModelMappingRule{{From: "gpt-5.4", To: "gpt-5.6-sol"}}))
	account := &Account{Platform: input.Platform, Type: input.Type, Credentials: input.Credentials, Extra: input.Extra}
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-sol", "gpt-6-astra", "gpt-5.6-luna"} {
		require.True(t, account.IsModelSupported(model), "automatic alias must preserve default model %s", model)
	}
	require.Equal(t, "gpt-5.6-sol", account.GetMappedModel("gpt-5.4"))
	require.False(t, account.IsModelSupported("deepseek-chat"), "preserve the default foreign-provider gate")
}
