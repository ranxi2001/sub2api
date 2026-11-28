//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise the periodic entry point, including readiness, preference suppression
// and persisted delivery keys. No HTTP request leaves the injected transport.
func TestSubscriptionExpiryService_SelectedProviderDeliversAndPreservesSuppressionAndDedup(t *testing.T) {
	for _, provider := range []string{EmailProviderResend, EmailProviderSMTP} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			settings := newNotificationEmailMemorySettingRepo()
			email := NewEmailService(settings, nil)
			var deliveries func() int64
			if provider == EmailProviderResend {
				require.NoError(t, settings.SetMultiple(ctx, map[string]string{
					SettingKeyEmailProvider: provider,
					SettingKeyResendAPIKey:  "encrypted:re_fixture",
					SettingKeySMTPFrom:      "mail@example.com",
				}))
				email.SetSecretEncryptor(resendTestEncryptor{})
				var calls int64
				email.SetResendHTTPTransport(resendRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					var payload resendSendEmailRequest
					require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
					require.Equal(t, []string{"due@example.net"}, payload.To)
					require.Contains(t, payload.HTML, "Fixture group")
					calls++
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"fixture"}`))}, nil
				}))
				deliveries = func() int64 { return calls }
			} else {
				// Omitted provider must retain legacy SMTP delivery.
				smtp := startNotificationEmailTestSMTPServer(t)
				require.NoError(t, settings.SetMultiple(ctx, smtp.settings()))
				deliveries = smtp.messageCount
			}
			sub := func(id int64, recipient string, remaining time.Duration) UserSubscription {
				return UserSubscription{
					ID: id, UserID: id, ExpiresAt: time.Now().Add(remaining),
					User: &User{ID: id, Email: recipient}, Group: &Group{Name: "Fixture group"},
				}
			}
			repo := &subscriptionExpiryRepoStub{subs: []UserSubscription{
				sub(1, "due@example.net", 7*24*time.Hour-time.Hour),
				sub(2, "optout@example.net", 3*24*time.Hour-time.Hour),
				sub(3, "already@example.net", 24*time.Hour-time.Hour),
				sub(4, "notdue@example.net", 5*24*time.Hour-time.Hour),
			}}
			event := NotificationEmailEventSubscriptionExpiryReminder
			require.NoError(t, settings.Set(ctx, notificationEmailPreferenceKey(event, "optout@example.net"), "unsubscribed"))
			require.NoError(t, settings.Set(ctx, legacyNotificationEmailDeliveryKey(event, "user_subscription", "3", "already@example.net", "1d"), "sent"))
			svc := NewSubscriptionExpiryService(repo, time.Minute)
			svc.SetSettingRepository(settings)
			svc.SetNotificationEmailService(NewNotificationEmailService(settings, email))

			svc.sendExpiryReminders(ctx)
			require.EqualValues(t, 1, deliveries())
			key := notificationEmailDeliveryKey(event, "user_subscription", strconv.FormatInt(repo.subs[0].ID, 10), "due@example.net", "7d")
			_, err := settings.GetValue(ctx, key)
			require.NoError(t, err)
			svc.sendExpiryReminders(ctx)
			require.Equal(t, 2, repo.listCalls)
			require.EqualValues(t, 1, deliveries(), "second scan must not resend")

			require.NoError(t, settings.Set(ctx, SettingKeySubscriptionExpiryNotifyEnabled, "false"))
			svc.sendExpiryReminders(ctx)
			require.Equal(t, 2, repo.listCalls, "disabled reminders must not scan")
		})
	}
}

func TestEmailCheckConfiguredUsesSelectedProviderWithoutSMTP(t *testing.T) {
	ctx := context.Background()
	settings := newNotificationEmailMemorySettingRepo()
	email := NewEmailService(settings, nil)
	email.SetSecretEncryptor(resendTestEncryptor{})
	require.NoError(t, settings.SetMultiple(ctx, map[string]string{
		SettingKeyEmailProvider: EmailProviderResend,
		SettingKeyResendAPIKey:  "encrypted:re_fixture",
		SettingKeySMTPFrom:      "mail@example.com",
	}))
	require.NoError(t, email.CheckConfigured(ctx))
	require.NoError(t, settings.Delete(ctx, SettingKeyResendAPIKey))
	require.ErrorIs(t, email.CheckConfigured(ctx), ErrEmailNotConfigured)
	require.NoError(t, settings.Set(ctx, SettingKeySMTPHost, "smtp.example.com"))
	require.ErrorIs(t, email.CheckConfigured(ctx), ErrEmailNotConfigured, "selected Resend must not fall back to SMTP")
	require.NoError(t, settings.Set(ctx, SettingKeyEmailProvider, EmailProviderSMTP))
	require.NoError(t, email.CheckConfigured(ctx))
}
