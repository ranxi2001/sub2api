package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountOpsBalanceNotificationScopedSave(t *testing.T) {
	svc, settings := opsBatchFixture(t)
	ctx := context.Background()
	before, err := svc.GetConfig(ctx)
	require.NoError(t, err)
	after, err := svc.SetBalanceLowNotification(ctx, 41, false)
	require.NoError(t, err)
	require.Equal(t, []int64{41}, after.BalanceLowDisabledAccountIDs)
	require.Equal(t, before.BalanceThresholds, after.BalanceThresholds)
	require.Equal(t, before.QuotaThresholds, after.QuotaThresholds)
	require.Equal(t, before.Webhooks, after.Webhooks)
	require.True(t, after.BalanceLow)
	require.True(t, after.WeeklyQuota)
	_, err = svc.SetBalanceLowNotification(ctx, 41, false)
	require.NoError(t, err)
	_, err = svc.SetBalanceLowNotification(ctx, 99, false)
	require.NoError(t, err)
	// Saving an old client payload must retain overrides unknown to that client.
	after.BalanceLowDisabledAccountIDs = nil
	require.NoError(t, svc.SaveConfig(ctx, after))
	after, err = svc.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{41, 99}, after.BalanceLowDisabledAccountIDs)
	after, err = svc.SetBalanceLowNotification(ctx, 41, true)
	require.NoError(t, err)
	require.Equal(t, []int64{99}, after.BalanceLowDisabledAccountIDs)
	previous := settings.raw
	_, err = svc.SetBalanceLowNotification(ctx, 123456, false)
	require.ErrorIs(t, err, ErrAccountNotFound)
	require.Equal(t, previous, settings.raw)
	_, err = svc.SetBalanceLowNotification(ctx, 99, true)
	require.NoError(t, err)
	after, err = svc.GetConfig(ctx)
	require.NoError(t, err)
	require.Empty(t, after.BalanceLowDisabledAccountIDs)
}

func TestAccountOpsBalanceNotificationObserveAndQueuedDelivery(t *testing.T) {
	ctx := context.Background()
	repo, sender := &accountOpsRepoStub{}, &accountOpsSenderStub{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, sender)
	cfg := defaultAccountOpsConfig()
	cfg.Enabled, cfg.Recipient = true, "ops@example.test"
	cfg.GroupsInitialized = true
	cfg.Groups = []AccountOpsGroup{{ID: manualAccountOpsGroupID("example"), Name: "Example", AccountIDs: []int64{7, 8}}}
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	account := &Account{ID: 7, Platform: PlatformOpenAI}
	body := []byte(`{"error":{"code":"insufficient_balance"}}`)
	svc.Observe(account, 402, nil, body)
	event := <-svc.queue
	cfg.BalanceLowDisabledAccountIDs = []int64{7}
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	svc.deliverNotification(ctx, &event)
	require.Zero(t, sender.calls)
	require.Equal(t, "suppressed", repo.state)
	svc.Observe(account, 402, nil, body)
	require.Empty(t, svc.queue)
	// Another account in the same operational group still produces a signal.
	svc.Observe(&Account{ID: 8, Platform: PlatformOpenAI}, 402, nil, body)
	require.Len(t, svc.queue, 1)
	other := <-svc.queue
	svc.deliverNotification(ctx, &other)
	require.Equal(t, 1, sender.calls)
	svc.Observe(account, 429, nil, []byte(`{"error":{"code":"weekly_limit_exceeded"}}`))
	require.Len(t, svc.queue, 1)
	require.Equal(t, "weekly_quota", (<-svc.queue).Kind)
	cfg.BalanceLowDisabledAccountIDs = []int64{}
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	svc.Observe(account, 402, nil, body)
	require.Len(t, svc.queue, 1)
	<-svc.queue
	cfg.BalanceLow = false
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	svc.Observe(account, 402, nil, body)
	require.Empty(t, svc.queue)
}

type opsMutingSender struct {
	calls int
	mute  func()
}

func (s *opsMutingSender) SendEmail(context.Context, string, string, string) error {
	s.calls++
	s.mute()
	return nil
}

func TestAccountOpsBalanceNotificationRecheckedBetweenRecipients(t *testing.T) {
	ctx := context.Background()
	repo, sender := &accountOpsRepoStub{}, &opsMutingSender{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, sender)
	cfg := defaultAccountOpsConfig()
	cfg.Enabled, cfg.Recipient = true, "one@example.test,two@example.test"
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	sender.mute = func() {
		cfg.BalanceLowDisabledAccountIDs = []int64{7}
		require.NoError(t, svc.SaveConfig(ctx, cfg))
	}
	svc.deliverNotification(ctx, &AccountOpsEvent{AccountID: 7, Kind: "balance_low", Attempts: 1})
	require.Equal(t, 1, sender.calls)
	require.Equal(t, "suppressed", repo.state)
}

func TestAccountOpsBalanceNotificationIDsValidation(t *testing.T) {
	for _, ids := range [][]int64{{0}, {-1}, {7, 7}, make([]int64, 10001)} {
		cfg := defaultAccountOpsConfig()
		cfg.BalanceLowDisabledAccountIDs = ids
		require.Error(t, ValidateAccountOpsConfig(cfg))
	}
}
