//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAILegacyAdmissionRebind(t *testing.T) {
	for _, batch := range []bool{false, true} {
		mode := "priority_lru"
		if batch {
			mode = "load_batch"
		}
		for _, scenario := range []string{"new_session", "rpm_spillover", "capacity_spillover", "load_read_failure"} {
			// The priority/LRU engine waits on a full sticky account; RPM headroom
			// supplies its existing, movable spillover path without changing policy.
			if !batch && (scenario == "capacity_spillover" || scenario == "load_read_failure") {
				continue
			}
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
				ctx := context.Background()
				groupID := int64(948001)
				const session = "legacy-admission"
				const a, b, c int64 = 948101, 948102, 948103
				accounts := []Account{
					{ID: b, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
						Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID},
						Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": false}},
					{ID: c, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
						Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID},
						Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}},
				}
				cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{}}
				var acquired, released []int64
				concurrency := schedulerTestConcurrencyCache{
					acquiredIDs: &acquired, releasedIDs: &released,
					acquireResults: map[int64]bool{}, loadMap: map[int64]*AccountLoadInfo{}, waitCounts: map[int64]int{},
				}
				if scenario != "new_session" {
					accounts = append(accounts, Account{
						ID: a, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
						Concurrency: 1, Priority: 10, GroupIDs: []int64{groupID},
						Extra: map[string]any{"openai_oauth_responses_websockets_v2_enabled": true},
					})
					cache.sessionBindings["openai:"+session] = a
				}
				if scenario == "capacity_spillover" || scenario == "load_read_failure" {
					concurrency.acquireResults[a] = false
					concurrency.loadMap[a] = &AccountLoadInfo{AccountID: a, CurrentConcurrency: 1, LoadRate: 100}
					concurrency.waitCounts[a] = 3
				}
				if scenario == "load_read_failure" {
					concurrency.loadBatchErr = errors.New("synthetic load read failure")
				}
				cfg := newSchedulerTestOpenAIWSV2Config()
				cfg.Gateway.Scheduling.LoadBatchEnabled = batch
				cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
				svc := &OpenAIGatewayService{
					accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: cache, cfg: cfg,
					concurrencyService: NewConcurrencyService(concurrency),
				}
				if scenario == "rpm_spillover" {
					accounts[len(accounts)-1].Extra["base_rpm"] = 10
					svc.rpmCache = &openAIRPMTestCache{counts: map[int64]int{a: 8}}
				}
				require.Nil(t, svc.getOpenAIAccountScheduler(ctx, false), "LEGACY_ADMISSION_FIXTURE: advanced scheduler must be off")
				selected, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", session, "gpt-5.1", nil,
					OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityChatCompletions, false, false, true)
				require.NoError(t, err, "LEGACY_ADMISSION_FIXTURE: selection must succeed")
				require.NotNil(t, selected)
				releaseStickyEscapeRebindSelection(selected)
				require.Equal(t, c, selected.Account.ID, "LEGACY_ADMISSION_FIXTURE: C must pass transport admission")
				require.True(t, selected.Acquired)
				require.Contains(t, acquired, b, "LEGACY_ADMISSION_FIXTURE: B must reach the outer rejection")
				require.Contains(t, released, b, "LEGACY_ADMISSION_FIXTURE: rejected B must release its slot")
				require.Equal(t, c, cache.sessionBindings["openai:"+session], "LEGACY_ADMISSION_REBIND: final admitted C must own sticky session")
			})
		}
	}
}
