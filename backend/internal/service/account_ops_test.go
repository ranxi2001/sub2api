package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountOpsFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"balance code", `{"error":{"code":"insufficient_balance","message":"secret-token"}}`, "balance_low", 402},
		{"Chinese balance", `{"error":{"message":"账户余额不足，请充值"}}`, "balance_low", 403},
		{"weekly code", `{"error":{"code":"weekly_limit_exceeded"}}`, "weekly_quota", 429},
		{"weekly message", `{"error":{"message":"本周额度已用尽"}}`, "weekly_quota", 429},
		{"generic rate limit", `{"error":{"code":"rate_limit_exceeded","message":"too many requests"}}`, "", 429},
		{"generic quota", `{"error":{"code":"insufficient_quota"}}`, "", 429},
		{"scheduler failure", `{"error":{"message":"no available accounts"}}`, "", 503},
		{"normal response", `{"message":"余额不足"}`, "", 200},
		{"HTML", `<html>余额不足</html>`, "", 403},
		{"oversized", strings.Repeat("x", 33000) + "余额不足", "", 402},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, _ := classifyAccountOpsFailure(PlatformOpenAI, tc.status, nil, []byte(tc.body))
			require.Equal(t, tc.want, kind)
		})
	}
	h := http.Header{}
	h.Set("x-codex-secondary-used-percent", "100")
	h.Set("x-codex-secondary-window-minutes", "300")
	kind, _ := classifyAccountOpsFailure(PlatformOpenAI, 429, h, nil)
	require.Empty(t, kind)
	h.Set("x-codex-secondary-window-minutes", "10080")
	kind, _ = classifyAccountOpsFailure(PlatformOpenAI, 429, h, nil)
	require.Equal(t, "weekly_quota", kind)
	kind, _ = classifyAccountOpsFailure(PlatformAnthropic, 429, h, nil)
	require.Empty(t, kind)
}

type accountOpsSettingsStub struct {
	SettingRepository
	raw string
}

func (s *accountOpsSettingsStub) GetValue(context.Context, string) (string, error) { return s.raw, nil }
func (s *accountOpsSettingsStub) Set(_ context.Context, _ string, v string) error {
	s.raw = v
	return nil
}

type accountOpsRepoStub struct {
	AccountOpsRepository
	state string
	delay time.Duration
	event *AccountOpsEvent
}

func (r *accountOpsRepoStub) SuppressDisabled(context.Context, AccountOpsConfig) error { return nil }
func (r *accountOpsRepoStub) Complete(_ context.Context, e *AccountOpsEvent, state string, delay time.Duration) error {
	r.state = state
	r.delay = delay
	r.event = e
	return nil
}

type accountOpsSenderStub struct {
	calls    int
	body, to string
	err      error
}

func (s *accountOpsSenderStub) SendEmail(_ context.Context, to, _ string, body string) error {
	s.calls++
	s.to = to
	s.body = body
	return s.err
}
func TestAccountOpsExplicitOptInAndRedaction(t *testing.T) {
	repo := &accountOpsRepoStub{}
	sender := &accountOpsSenderStub{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, sender)
	a := &Account{ID: 7, Name: "example", Platform: PlatformOpenAI}
	body := []byte(`{"error":{"code":"insufficient_balance","message":"Authorization: secret; prompt private"}}`)
	svc.Observe(a, 402, nil, body)
	require.Empty(t, svc.queue)
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "ops@example.com"
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	svc.Observe(a, 402, nil, body)
	event := <-svc.queue
	require.Equal(t, "balance_low", event.Kind)
	require.NotContains(t, event.Signal, "secret")
	event.Attempts = 1
	event.AccountName = "<script>"
	svc.deliverNotification(context.Background(), &event)
	require.Equal(t, 1, sender.calls)
	require.Equal(t, "ops@example.com", sender.to)
	require.Equal(t, "sent", repo.state)
	require.NotContains(t, sender.body, "secret")
	require.NotContains(t, sender.body, "<script>")
	require.Contains(t, sender.body, "&lt;script&gt;")
	cfg.Enabled = false
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	svc.deliverNotification(context.Background(), &event)
	require.Equal(t, 1, sender.calls)
	require.Equal(t, "suppressed", repo.state)
}
func TestAccountOpsMailFailureDoesNotBecomeSent(t *testing.T) {
	repo := &accountOpsRepoStub{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, &accountOpsSenderStub{err: errors.New("smtp unavailable")})
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "ops@example.com"
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	e := &AccountOpsEvent{AccountID: 7, Kind: "weekly_quota", Attempts: 1}
	svc.deliverNotification(context.Background(), e)
	require.Equal(t, "failed", repo.state)
	require.Equal(t, 5*time.Minute, repo.delay)
	e.Attempts = 3
	svc.deliverNotification(context.Background(), e)
	require.Equal(t, time.Hour, repo.delay)
}
func TestAccountOpsRejectsInvalidRecipients(t *testing.T) {
	c := defaultAccountOpsConfig()
	c.Enabled = true
	for _, recipient := range []string{"", "Name <one@example.com>", "one@example.com\r\nBcc: other@example.com"} {
		c.Recipient = recipient
		require.Error(t, ValidateAccountOpsConfig(c))
	}
	c.Recipient = "one@example.com"
	require.NoError(t, ValidateAccountOpsConfig(c))
}

func TestAccountOpsBoundedQueueAndIgnoredGenericErrors(t *testing.T) {
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, &accountOpsSenderStub{})
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "ops@example.com"
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	account := &Account{ID: 1, Name: "Account", Platform: PlatformOpenAI}
	for i := 0; i < 300; i++ {
		svc.Observe(account, 429, nil, []byte(`{"error":{"message":"too many requests"}}`))
	}
	require.Empty(t, svc.queue)
	for i := 0; i < 300; i++ {
		svc.Observe(account, 402, nil, []byte(`{"error":{"code":"insufficient_balance"}}`))
	}
	require.Len(t, svc.queue, 256)
	dropped, _ := svc.RuntimeCounters()
	require.EqualValues(t, 44, dropped)
}
