package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type stickyEscapeRebindFixture struct {
	service     *OpenAIGatewayService
	scheduler   *defaultOpenAIAccountScheduler
	request     OpenAIAccountScheduleRequest
	accounts    []Account
	cache       *schedulerTestGatewayCache
	concurrency schedulerTestConcurrencyCache
}

func newStickyEscapeRebindFixture() *stickyEscapeRebindFixture {
	groupID := int64(947001)
	accounts := []Account{
		{ID: 947101, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{groupID}},
		{ID: 947102, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 500, GroupIDs: []int64{groupID}},
	}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:rebind-session": 947101}}
	concurrency := schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{947101: false, 947102: true},
		loadMap: map[int64]*AccountLoadInfo{
			947101: {AccountID: 947101},
			947102: {AccountID: 947102},
		},
		waitCounts: map[int64]int{947101: 1000},
	}
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = true
	cfg.Gateway.OpenAIScheduler.StickyEscapeTTFTMs = 15000
	cfg.Gateway.OpenAIScheduler.StickyEscapeErrorRate = 0.5
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = time.Minute
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	stats := newOpenAIAccountRuntimeStats()
	service := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrency),
		openaiAccountStats: stats,
	}
	return &stickyEscapeRebindFixture{
		service: service, scheduler: &defaultOpenAIAccountScheduler{service: service, stats: stats},
		request: OpenAIAccountScheduleRequest{
			GroupID: &groupID, Platform: PlatformOpenAI, SessionHash: "rebind-session",
			RequestedModel: "gpt-5.1", UseUpstreamTokenCost: true,
		},
		accounts: accounts, cache: cache, concurrency: concurrency,
	}
}

func (f *stickyEscapeRebindFixture) recoverOriginalAccount() {
	f.concurrency.acquireResults[947101] = true
	*f.concurrency.loadMap[947101] = AccountLoadInfo{AccountID: 947101}
	f.concurrency.waitCounts[947101] = 0
	f.scheduler.stats = newOpenAIAccountRuntimeStats()
	f.service.openaiAccountStats = f.scheduler.stats
}

func releaseStickyEscapeRebindSelection(selection *AccountSelectionResult) {
	if selection != nil && selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIStickyEscapeRebindKeepsReplacementAccount(t *testing.T) {
	for _, reason := range []string{"concurrency", "error_rate", "ttft", "priority_capacity"} {
		t.Run(reason, func(t *testing.T) {
			f := newStickyEscapeRebindFixture()
			switch reason {
			case "error_rate":
				for range 5 {
					f.scheduler.stats.report(947101, false, nil)
				}
			case "ttft":
				slowTTFT := 25000
				for range 5 {
					f.scheduler.stats.report(947101, true, &slowTTFT)
				}
			case "priority_capacity":
				priority := DefaultPrioritySchedulingConfig()
				priority.Enabled = true
				f.service.settingService = priorityGateway(priority, &priorityReaderStub{}).settingService
				*f.concurrency.loadMap[947101] = AccountLoadInfo{AccountID: 947101, CurrentConcurrency: 6, LoadRate: 60}
			}

			selected, decision, err := f.scheduler.Select(context.Background(), f.request)
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(947102), selected.Account.ID)
			require.True(t, selected.Acquired)
			require.Nil(t, selected.WaitPlan, "escape must still use the available peer immediately")
			require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
			require.Equal(t, int64(947102), f.cache.sessionBindings["openai:rebind-session"])
			releaseStickyEscapeRebindSelection(selected)

			f.recoverOriginalAccount()
			selected, decision, err = f.scheduler.Select(context.Background(), f.request)
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(947102), selected.Account.ID, "recovered original account must not reclaim the migrated session")
			require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
			require.True(t, decision.StickySessionHit)
			releaseStickyEscapeRebindSelection(selected)
		})
	}
}

func TestOpenAIStickyPriorityLatencyYieldRebindsWithEscapeDisabled(t *testing.T) {
	f := newStickyEscapeRebindFixture()
	f.service.cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = false
	priority := DefaultPrioritySchedulingConfig()
	priority.Enabled = true
	priority.TargetTTFTMs = 15000
	badAt := time.Now().Add(-time.Minute)
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{
		947101: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 25000, LatestBadLatencyAt: &badAt, QualitySamples: 10, QualityPassed: 10}),
		947102: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualitySamples: 10, QualityPassed: 10}),
	}}
	f.service.settingService = priorityGateway(priority, reader).settingService
	f.service.usageLogRepo = reader
	pool := []openAIAccountCandidateScore{{account: &f.accounts[0]}, {account: &f.accounts[1]}}
	require.Eventually(t, func() bool {
		return f.service.priorityHistory(f.request, priority, pool).ready()
	}, time.Second, time.Millisecond)

	selected, _, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, int64(947102), selected.Account.ID)
	require.Equal(t, int64(947102), f.cache.sessionBindings["openai:rebind-session"])
	releaseStickyEscapeRebindSelection(selected)

	f.recoverOriginalAccount()
	f.service.priorityScheduling.mu.Lock()
	entry := f.service.priorityScheduling.entries[priorityHistoryKey(f.request, priority)]
	entry.signals[947101] = entry.signals[947102]
	f.service.priorityScheduling.mu.Unlock()
	selected, decision, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, int64(947102), selected.Account.ID)
	require.True(t, decision.StickySessionHit)
	releaseStickyEscapeRebindSelection(selected)
}

type stickyEscapeListFailureRepo struct {
	schedulerTestOpenAIAccountRepo
	err error
}

func (r stickyEscapeListFailureRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return nil, r.err
}

func TestOpenAIStickyEscapeDoesNotRebindBeforeAcquiringReplacement(t *testing.T) {
	for _, scenario := range []string{"selection_error", "wait_plan"} {
		t.Run(scenario, func(t *testing.T) {
			f := newStickyEscapeRebindFixture()
			for range 5 {
				f.scheduler.stats.report(947101, false, nil)
			}
			listErr := errors.New("account list unavailable")
			if scenario == "selection_error" {
				f.service.accountRepo = stickyEscapeListFailureRepo{
					schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: f.accounts}, err: listErr,
				}
			} else {
				f.concurrency.acquireResults[947102] = false
			}
			selected, _, err := f.scheduler.Select(context.Background(), f.request)
			if scenario == "selection_error" {
				require.ErrorIs(t, err, listErr)
				require.Nil(t, selected)
			} else {
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.False(t, selected.Acquired)
				require.NotNil(t, selected.WaitPlan)
			}
			require.Equal(t, int64(947101), f.cache.sessionBindings["openai:rebind-session"])
			require.Zero(t, f.cache.deletedSessions["openai:rebind-session"], "a failed escape must retain the original binding")
		})
	}
}

func TestOpenAIStickyEscapePreservesExplicitOwnerBinding(t *testing.T) {
	f := newStickyEscapeRebindFixture()
	f.request.PreserveStickyBinding = true
	selected, _, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, int64(947102), selected.Account.ID)
	require.Equal(t, int64(947101), f.cache.sessionBindings["openai:rebind-session"], "explicit ownership must not be replaced by an ordinary escape binding")
	releaseStickyEscapeRebindSelection(selected)
}

func TestOpenAIStickyEscapeDoesNotMoveProviderContinuationOwner(t *testing.T) {
	for _, existingSession := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_session", true: "existing_session"}[existingSession], func(t *testing.T) {
			f := newStickyEscapeRebindFixture()
			if !existingSession {
				delete(f.cache.sessionBindings, "openai:rebind-session")
			}
			f.accounts[0].Extra = map[string]any{"openai_oauth_responses_websockets_v2_enabled": true}
			require.Equal(t, OpenAIUpstreamTransportResponsesWebsocketV2, f.service.getOpenAIWSProtocolResolver().Resolve(&f.accounts[0]).Transport, "OAuth continuation ownership requires a WSv2-capable account")
			f.concurrency.acquireResults[947101] = true
			f.request.PreviousResponseID = "resp-rebind-owner"
			f.request.PreviousResponseCanMove = false
			require.NoError(t, f.service.getOpenAIWSStateStore().BindResponseAccount(context.Background(), *f.request.GroupID, f.request.PreviousResponseID, 947101, time.Hour))
			for range 5 {
				f.scheduler.stats.report(947101, false, nil)
			}
			selected, decision, err := f.scheduler.Select(context.Background(), f.request)
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(947101), selected.Account.ID)
			require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
			require.True(t, decision.StickyPreviousHit)
			require.Equal(t, int64(947101), f.cache.sessionBindings["openai:rebind-session"])
			releaseStickyEscapeRebindSelection(selected)
		})
	}
}

func TestOpenAIWeightedStickyCreatesBindingForNewSession(t *testing.T) {
	f := newStickyEscapeRebindFixture()
	delete(f.cache.sessionBindings, "openai:rebind-session")
	f.request.StickyWeighted = true
	selected, _, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.True(t, selected.Acquired)
	require.Equal(t, int64(947102), selected.Account.ID)
	require.Equal(t, int64(947102), f.cache.sessionBindings["openai:rebind-session"], "weighted selection must establish affinity for a previously unseen session")
	releaseStickyEscapeRebindSelection(selected)
}

func TestOpenAIStickyEscapeRebindsLegacySessionKey(t *testing.T) {
	for _, dualWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "primary_only", true: "dual_write"}[dualWrite], func(t *testing.T) {
			f := newStickyEscapeRebindFixture()
			delete(f.cache.sessionBindings, "openai:rebind-session")
			f.cache.sessionBindings["openai:legacy-session"] = 947101
			f.service.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
			f.service.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = dualWrite
			ctx := withOpenAILegacySessionHash(context.Background(), "legacy-session")
			selected, _, err := f.scheduler.Select(ctx, f.request)
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(947102), selected.Account.ID)
			require.Equal(t, int64(947102), f.cache.sessionBindings["openai:rebind-session"])
			if dualWrite {
				require.Equal(t, int64(947102), f.cache.sessionBindings["openai:legacy-session"])
			} else {
				require.NotContains(t, f.cache.sessionBindings, "openai:legacy-session", "disabled dual writes must not leave a stale owner available to fallback readers")
			}
			releaseStickyEscapeRebindSelection(selected)

			f.recoverOriginalAccount()
			selected, decision, err := f.scheduler.Select(ctx, f.request)
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(947102), selected.Account.ID)
			require.True(t, decision.StickySessionHit)
			releaseStickyEscapeRebindSelection(selected)

			delete(f.cache.sessionBindings, "openai:rebind-session")
			accountID, err := f.service.getStickySessionAccountID(ctx, f.request.GroupID, f.request.SessionHash)
			if dualWrite {
				require.NoError(t, err)
				require.Equal(t, int64(947102), accountID)
			} else {
				require.ErrorIs(t, err, ErrStickySessionNotFound)
				require.Zero(t, accountID, "missing primary must not resurrect the pre-migration account")
			}
		})
	}
}

func TestOpenAIStickyEscapeLateWaitPlanCannotRestoreOriginalBinding(t *testing.T) {
	f := newStickyEscapeRebindFixture()
	queuedRequest := f.request
	queuedRequest.DisableStickyEscape = true
	queued, _, err := f.scheduler.Select(context.Background(), queuedRequest)
	require.NoError(t, err)
	require.NotNil(t, queued)
	require.NotNil(t, queued.WaitPlan)
	require.Equal(t, int64(947101), queued.WaitPlan.AccountID)
	require.Equal(t, int64(947101), f.cache.sessionBindings["openai:rebind-session"])
	queuedContext := ContextWithSelectionProfitGate(context.Background(), queued)

	// Another turn escapes and acquires the peer while the old turn still waits.
	replacement, _, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.NotNil(t, replacement)
	require.True(t, replacement.Acquired)
	require.Equal(t, int64(947102), replacement.Account.ID)
	require.Equal(t, int64(947102), f.cache.sessionBindings["openai:rebind-session"])
	releaseStickyEscapeRebindSelection(replacement)

	// The older waiter's eventual admission must not rewind the migrated route.
	require.NoError(t, f.service.BindStickySessionAfterProfitAdmission(queuedContext, f.request.GroupID, f.request.SessionHash, 947101))
	require.Equal(t, int64(947102), f.cache.sessionBindings["openai:rebind-session"])
	f.recoverOriginalAccount()
	next, decision, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, int64(947102), next.Account.ID)
	require.True(t, decision.StickySessionHit)
	releaseStickyEscapeRebindSelection(next)
}

func TestOpenAILegacyStickySpilloverRebindKeepsReplacement(t *testing.T) {
	for _, scenario := range []string{"queue_full", "load_read_failure", "rpm_headroom"} {
		t.Run(scenario, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			f := newStickyEscapeRebindFixture()
			f.service.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("false")
			var rpm *openAIRPMTestCache
			if scenario == "load_read_failure" {
				f.concurrency.loadBatchErr = errors.New("load cache unavailable")
				f.service.concurrencyService = NewConcurrencyService(f.concurrency)
			}
			if scenario == "rpm_headroom" {
				for i := range f.accounts {
					f.accounts[i].Extra = map[string]any{"base_rpm": 10}
				}
				f.concurrency.acquireResults[947101] = true
				rpm = &openAIRPMTestCache{counts: map[int64]int{947101: 8, 947102: 0}}
				f.service.rpmCache = rpm
			}
			selectAccount := func() (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
				return f.service.SelectAccountWithScheduler(context.Background(), f.request.GroupID, "", f.request.SessionHash, f.request.RequestedModel, nil, OpenAIUpstreamTransportAny, false)
			}
			selected, _, err := selectAccount()
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(947102), selected.Account.ID)
			require.True(t, selected.Acquired)
			require.Equal(t, int64(947102), f.cache.sessionBindings["openai:rebind-session"])
			releaseStickyEscapeRebindSelection(selected)

			f.recoverOriginalAccount()
			if rpm != nil {
				rpm.counts[947101] = 0
			}
			selected, decision, err := selectAccount()
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(947102), selected.Account.ID)
			require.True(t, decision.StickySessionHit)
			releaseStickyEscapeRebindSelection(selected)
		})
	}
}
