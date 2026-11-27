//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type resendRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f resendRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSendEmailWithResendConfigUsesFixedEndpointAndExpectedPayload(t *testing.T) {
	var captured *http.Request
	var payload resendSendEmailRequest
	svc := NewEmailService(nil, nil)
	svc.SetResendHTTPTransport(resendRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &payload))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"email_123"}`)), Header: make(http.Header)}, nil
	}))

	err := svc.SendEmailWithResendConfig(context.Background(), &ResendConfig{APIKey: "re_test", From: "Sub2API <mail@example.com>", FromName: "Sub2API"}, "user@example.net", "Verify", "<p>Hello</p>")
	require.NoError(t, err)
	require.Equal(t, "https://api.resend.com/emails", captured.URL.String())
	require.Equal(t, "Bearer re_test", captured.Header.Get("Authorization"))
	require.Equal(t, "application/json", captured.Header.Get("Content-Type"))
	require.Equal(t, "mail@example.com", payload.To[0])
	require.Equal(t, "Verify", payload.Subject)
	require.Equal(t, "<p>Hello</p>", payload.HTML)
}

func TestSendEmailWithResendConfigClassifiesErrorsWithoutProviderBody(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "authentication or permission"},
		{http.StatusTooManyRequests, "rate limit"},
		{http.StatusUnprocessableEntity, "rejected"},
		{http.StatusBadGateway, "unavailable"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			svc := NewEmailService(nil, nil)
			svc.SetResendHTTPTransport(resendRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(`{"message":"sensitive-provider-detail"}`)), Header: make(http.Header)}, nil
			}))
			err := svc.SendEmailWithResendConfig(context.Background(), &ResendConfig{APIKey: "re_test", From: "mail@example.com"}, "user@example.net", "Subject", "Body")
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			require.NotContains(t, err.Error(), "sensitive-provider-detail")
		})
	}
}

func TestSendEmailWithResendConfigRejectsInvalidAddressesBeforeHTTP(t *testing.T) {
	calls := 0
	svc := NewEmailService(nil, nil)
	svc.SetResendHTTPTransport(resendRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected request")
	}))
	err := svc.SendEmailWithResendConfig(context.Background(), &ResendConfig{APIKey: "re_test", From: "bad"}, "user@example.net", "Subject", "Body")
	require.Error(t, err)
	require.Zero(t, calls)
}

func TestSendEmailWithResendConfigMapsTimeoutToSafeError(t *testing.T) {
	svc := NewEmailService(nil, nil)
	svc.SetResendHTTPTransport(resendRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	}))
	err := svc.SendEmailWithResendConfig(context.Background(), &ResendConfig{APIKey: "re_test", From: "mail@example.com"}, "user@example.net", "Subject", "Body")
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out")
	require.NotContains(t, err.Error(), "re_test")
}

func TestGetResendConfigDecryptsStoredKeyAndKeepsSenderSettings(t *testing.T) {
	repo := &resendSettingsTestRepository{values: map[string]string{
		SettingKeyResendAPIKey: "encrypted:re_test",
		SettingKeySMTPFrom:     "mail@example.com",
		SettingKeySMTPFromName: "Sub2API",
	}}
	svc := NewEmailService(repo, nil)
	svc.SetSecretEncryptor(resendTestEncryptor{})
	config, err := svc.GetResendConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "re_test", config.APIKey)
	require.Equal(t, "mail@example.com", config.From)
	require.Equal(t, "Sub2API", config.FromName)
}

type resendTestEncryptor struct{}

func (resendTestEncryptor) Encrypt(value string) (string, error) { return "encrypted:" + value, nil }
func (resendTestEncryptor) Decrypt(value string) (string, error) {
	return strings.TrimPrefix(value, "encrypted:"), nil
}

func TestSendEmailUsesSMTPWhenProviderIsMissing(t *testing.T) {
	repo := &resendSettingsTestRepository{values: map[string]string{SettingKeySMTPHost: "smtp.example.com"}}
	svc := NewEmailService(repo, nil)
	err := svc.SendEmail(context.Background(), "invalid", "Subject", "Body")
	require.Error(t, err)
	require.Contains(t, err.Error(), "SMTP")
}

type resendSettingsTestRepository struct{ values map[string]string }

func (r *resendSettingsTestRepository) Get(context.Context, string) (*Setting, error) {
	panic("unexpected")
}
func (r *resendSettingsTestRepository) GetValue(context.Context, string) (string, error) {
	panic("unexpected")
}
func (r *resendSettingsTestRepository) Set(context.Context, string, string) error {
	panic("unexpected")
}
func (r *resendSettingsTestRepository) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}
func (r *resendSettingsTestRepository) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected")
}
func (r *resendSettingsTestRepository) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected")
}
func (r *resendSettingsTestRepository) Delete(context.Context, string) error { panic("unexpected") }
