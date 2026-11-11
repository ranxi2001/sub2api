package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newStrictSessionFixture() *stickyEscapeRebindFixture {
	f := newStickyEscapeRebindFixture()
	f.service.cfg.Gateway.OpenAIScheduler.StrictSessionAffinityGroupIDs = []int64{*f.request.GroupID}
	return f
}

func TestStrictSessionAffinityWaitsWithoutChangingOwner(t *testing.T) {
	f := newStrictSessionFixture()
	f.request.StickyWeighted = true
	for range 5 {
		f.scheduler.stats.report(947101, false, nil)
	}
	selected, decision, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.Equal(t, int64(947101), selected.Account.ID)
	require.NotNil(t, selected.WaitPlan)
	require.True(t, decision.StickySessionHit)
	require.Equal(t, int64(947101), f.cache.sessionBindings["openai:rebind-session"])
}

func TestStrictSessionAffinityFailureNeverFallsBack(t *testing.T) {
	for _, scenario := range []string{"excluded", "inactive", "missing", "model"} {
		t.Run(scenario, func(t *testing.T) {
			f := newStrictSessionFixture()
			switch scenario {
			case "excluded":
				f.request.ExcludedIDs = map[int64]struct{}{947101: {}}
			case "inactive":
				f.accounts[0].Schedulable = false
			case "missing":
				f.service.accountRepo = schedulerTestOpenAIAccountRepo{accounts: f.accounts[1:]}
			case "model":
				f.accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"another-model": "another-model"}}
			}
			selected, _, err := f.scheduler.Select(context.Background(), f.request)
			require.Error(t, err)
			require.Nil(t, selected)
			require.Equal(t, int64(947101), f.cache.sessionBindings["openai:rebind-session"])
		})
	}
}

func TestStrictSessionAffinityCannotStartWithoutIdentity(t *testing.T) {
	f := newStrictSessionFixture()
	f.request.SessionHash = ""
	selected, _, err := f.scheduler.Select(context.Background(), f.request)
	require.ErrorContains(t, err, "session identity")
	require.Nil(t, selected)
}

func TestStrictSessionAffinityRetainsNormalBehaviorElsewhere(t *testing.T) {
	f := newStrictSessionFixture()
	f.service.cfg.Gateway.OpenAIScheduler.StrictSessionAffinityGroupIDs = []int64{*f.request.GroupID + 1}
	selected, _, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.Equal(t, int64(947102), selected.Account.ID)
	releaseStickyEscapeRebindSelection(selected)
}

type strictSessionClaimRaceCache struct {
	*schedulerTestGatewayCache
	winner int64
}

func (c *strictSessionClaimRaceCache) CompareAndSwapSessionAccountID(_ context.Context, _ int64, swap SessionAccountBindingSwap) (bool, error) {
	c.sessionBindings[swap.SessionHash] = c.winner
	return false, nil
}

func TestStrictSessionAffinityLosingFirstTurnReleasesSlot(t *testing.T) {
	f := newStrictSessionFixture()
	f.cache.sessionBindings = map[string]int64{}
	f.service.cache = &strictSessionClaimRaceCache{schedulerTestGatewayCache: f.cache, winner: 947101}
	released := []int64{}
	f.concurrency.releasedIDs = &released
	f.service.concurrencyService = NewConcurrencyService(f.concurrency)
	selected, _, err := f.scheduler.Select(context.Background(), f.request)
	require.ErrorContains(t, err, "another request owns")
	require.Nil(t, selected)
	require.Contains(t, released, int64(947102))
	require.Equal(t, int64(947101), f.cache.sessionBindings["openai:rebind-session"])
}

func TestStrictSessionAffinityWorksWithAdvancedSchedulerOff(t *testing.T) {
	f := newStrictSessionFixture()
	selected, decision, err := f.service.SelectAccountWithScheduler(context.Background(), f.request.GroupID, "", f.request.SessionHash, f.request.RequestedModel, nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(947101), selected.Account.ID)
	require.True(t, decision.StickySessionHit)
	require.NotNil(t, selected.WaitPlan)
}

func TestQwenAccountStrictSessionAffinityIsOptIn(t *testing.T) {
	f := newStickyEscapeRebindFixture()
	f.request.Platform = PlatformQwen
	for i := range f.accounts {
		f.accounts[i].Platform = PlatformQwen
		f.accounts[i].Type = AccountTypeAPIKey
		f.accounts[i].Credentials = map[string]any{qwenStrictSessionCredential: true}
	}
	selected, _, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.Equal(t, int64(947101), selected.Account.ID)
	require.NotNil(t, selected.WaitPlan)
	f.request.ExcludedIDs = map[int64]struct{}{947101: {}}
	selected, _, err = f.scheduler.Select(context.Background(), f.request)
	require.Error(t, err)
	require.Nil(t, selected)
}

func TestQwenNativeContinuationStaysWithResponseOwnerWithoutSessionHeader(t *testing.T) {
	f := newStickyEscapeRebindFixture()
	f.request.Platform = PlatformQwen
	f.request.SessionHash = ""
	f.request.PreviousResponseID = "resp_qwen_owner"
	f.cache.sessionBindings = map[string]int64{}
	for i := range f.accounts {
		f.accounts[i].Platform = PlatformQwen
		f.accounts[i].Type = AccountTypeAPIKey
		f.accounts[i].Credentials = map[string]any{qwenSessionCacheCredential: true}
	}
	require.NoError(t, f.service.getOpenAIWSStateStore().BindResponseAccount(context.Background(), *f.request.GroupID, f.request.PreviousResponseID, 947101, time.Hour))
	selected, _, err := f.scheduler.Select(context.Background(), f.request)
	require.NoError(t, err)
	require.Equal(t, int64(947101), selected.Account.ID)
	require.NotNil(t, selected.WaitPlan)
	f.request.ExcludedIDs = map[int64]struct{}{947101: {}}
	selected, _, err = f.scheduler.Select(context.Background(), f.request)
	require.Error(t, err)
	require.Nil(t, selected)
}

type qwenConcurrentFirstTurnCache struct {
	*schedulerTestGatewayCache
	firstRead bool
}

func (c *qwenConcurrentFirstTurnCache) GetSessionAccountID(ctx context.Context, groupID int64, key string) (int64, error) {
	if !c.firstRead {
		c.firstRead = true
		c.sessionBindings[key] = 947101 // Another first turn claims immediately after our miss.
		return 0, ErrStickySessionNotFound
	}
	return c.schedulerTestGatewayCache.GetSessionAccountID(ctx, groupID, key)
}

func (c *qwenConcurrentFirstTurnCache) CompareAndSwapSessionAccountID(_ context.Context, _ int64, swap SessionAccountBindingSwap) (bool, error) {
	if c.sessionBindings[swap.SessionHash] != swap.ExpectedAccountID {
		return false, nil
	}
	c.sessionBindings[swap.SessionHash] = swap.AccountID
	return true, nil
}

func TestQwenStrictConcurrentFirstTurnDoesNotEscapeNewOwner(t *testing.T) {
	for _, peerStrict := range []bool{false, true} {
		f := newStickyEscapeRebindFixture()
		f.request.Platform = PlatformQwen
		f.cache.sessionBindings = map[string]int64{}
		f.service.cache = &qwenConcurrentFirstTurnCache{schedulerTestGatewayCache: f.cache}
		for i := range f.accounts {
			f.accounts[i].Platform = PlatformQwen
			f.accounts[i].Type = AccountTypeAPIKey
			f.accounts[i].Credentials = map[string]any{qwenStrictSessionCredential: true}
		}
		f.accounts[1].Credentials[qwenStrictSessionCredential] = peerStrict
		selected, _, err := f.scheduler.Select(context.Background(), f.request)
		require.Error(t, err, "peer strict=%v", peerStrict)
		require.Nil(t, selected)
		require.Equal(t, int64(947101), f.cache.sessionBindings["openai:rebind-session"])
	}
}
