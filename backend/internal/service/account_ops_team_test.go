package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestAccountOpsRecipientListValidationAndNormalization(t *testing.T) {
	recipients, err := parseAccountOpsRecipients(" one@example.test; two@example.test\nONE@example.test ")
	require.NoError(t, err)
	require.Equal(t, []string{"one@example.test", "two@example.test"}, recipients)
	for _, bad := range []string{"Name <one@example.test>", "one@example.test\r\nBcc: x@example.test", "one@example.test\nBcc: x@example.test", ";", strings.Repeat("a", 5121)} {
		_, err = parseAccountOpsRecipients(bad)
		require.Error(t, err)
	}
	cfg := defaultAccountOpsConfig()
	cfg.Recipient = " one@example.test;two@example.test\nONE@example.test "
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	got, err := svc.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "one@example.test, two@example.test", got.Recipient)
	list := []string{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		list = append(list, name+"@example.test")
	}
	_, err = parseAccountOpsRecipients(strings.Join(list, ","))
	require.Error(t, err)
}

type opsTeamSender struct {
	calls  map[string]int
	failed string
	after  func(string)
}

func (s *opsTeamSender) SendEmail(_ context.Context, to, _, _ string) error {
	s.calls[to]++
	if s.after != nil {
		s.after(to)
	}
	if to == s.failed {
		return errors.New("synthetic delivery failure")
	}
	return nil
}
func TestAccountOpsMultipleRecipientsRetryOnlyFailedRecipient(t *testing.T) {
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "one@example.test,two@example.test,ONE@example.test"
	repo := &accountOpsRepoStub{}
	sender := &opsTeamSender{calls: map[string]int{}, failed: "two@example.test"}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, sender)
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	event := &AccountOpsEvent{AccountID: 1, GroupID: "manual-shared123", GroupName: "Shared supplier", Kind: "balance_low", Attempts: 1}
	svc.deliverNotification(context.Background(), event)
	require.Equal(t, "failed", repo.state)
	require.Len(t, event.Deliveries, 2)
	sender.failed = ""
	event.Attempts = 2
	svc.deliverNotification(context.Background(), event)
	require.Equal(t, "sent", repo.state)
	require.Equal(t, map[string]int{"one@example.test": 1, "two@example.test": 2}, sender.calls)
	require.Equal(t, "sent", event.Deliveries[opsEmailIdentity("one@example.test")].Status)
}
func TestAccountOpsRemovedEmailDoesNotReceivePendingNotification(t *testing.T) {
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "one@example.test,two@example.test"
	sender := &opsTeamSender{calls: map[string]int{}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, sender)
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	sender.after = func(to string) {
		cfg.Recipient = "one@example.test"
		require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	}
	svc.deliverNotification(context.Background(), &AccountOpsEvent{AccountID: 1, Kind: "balance_low"})
	require.Equal(t, map[string]int{"one@example.test": 1}, sender.calls)
}

type opsLifecycleRepo struct {
	accountOpsGroupAccounts
	updated map[string]any
}

func (r *opsLifecycleRepo) UpdateExtra(_ context.Context, _ int64, extra map[string]any) error {
	r.updated = extra
	return nil
}
func TestAccountOpsOAuthLifecycleDoesNotChangeSchedulingOrCredentials(t *testing.T) {
	a := &Account{ID: 1, Type: AccountTypeOAuth, Status: "active", Schedulable: true, Extra: map[string]any{"import_source": "codex_session"}}
	require.Equal(t, "temporary", accountOpsOAuthLifecycle(a))
	repo := &opsLifecycleRepo{accountOpsGroupAccounts: accountOpsGroupAccounts{items: map[int64]*Account{1: a}}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(repo, nil, false, "UTC")
	require.NoError(t, svc.SetOAuthLifecycle(context.Background(), 1, "long_term"))
	require.Equal(t, map[string]any{"oauth_lifecycle": "long_term"}, repo.updated)
	require.True(t, a.Schedulable)
	require.Equal(t, "active", a.Status)
	require.Error(t, svc.SetOAuthLifecycle(context.Background(), 1, "unknown"))
	a.Type = AccountTypeAPIKey
	require.Error(t, svc.SetOAuthLifecycle(context.Background(), 1, "temporary"))
}
