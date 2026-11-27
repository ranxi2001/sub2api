//go:build unit

package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func bindSMTPRequest[T any](t *testing.T, body string) T {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	var payload T
	require.NoError(t, c.ShouldBindJSON(&payload))
	return payload
}

func TestTestSMTPRequestUseTLSOmissionFallsBackToSavedSetting(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{}`)

	require.Nil(t, req.SMTPUseTLS)
	require.True(t, resolveSMTPUseTLS(req.SMTPUseTLS, &service.SMTPConfig{UseTLS: true}))
}

func TestTestSMTPRequestExplicitFalseOverridesSavedUseTLS(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{"smtp_use_tls":false}`)

	require.NotNil(t, req.SMTPUseTLS)
	require.False(t, resolveSMTPUseTLS(req.SMTPUseTLS, &service.SMTPConfig{UseTLS: true}))
}

func TestTestSMTPRequestExplicitTrueOverridesMissingSavedUseTLS(t *testing.T) {
	req := bindSMTPRequest[TestSMTPRequest](t, `{"smtp_use_tls":true}`)

	require.NotNil(t, req.SMTPUseTLS)
	require.True(t, resolveSMTPUseTLS(req.SMTPUseTLS, nil))
}

func TestSendTestEmailRequestPreservesUseTLSOmissionSemantics(t *testing.T) {
	omitted := bindSMTPRequest[SendTestEmailRequest](t, `{"email":"admin@example.com"}`)
	explicitFalse := bindSMTPRequest[SendTestEmailRequest](t, `{"email":"admin@example.com","smtp_use_tls":false}`)
	saved := &service.SMTPConfig{UseTLS: true}

	require.True(t, resolveSMTPUseTLS(omitted.SMTPUseTLS, saved))
	require.False(t, resolveSMTPUseTLS(explicitFalse.SMTPUseTLS, saved))
}

func TestUpdateSettingsResendKeyIsEncryptedAndBlankKeyPreservesExisting(t *testing.T) {
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	email := service.NewEmailService(repo, nil)
	email.SetSecretEncryptor(settingsTestEncryptor{})
	h := NewSettingHandler(svc, email, nil, nil, nil, nil, nil)

	rec := doUpdateSettings(t, h, map[string]any{"email_provider": "resend", "resend_api_key": "re_test-secret"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEqual(t, "re_test-secret", repo.values[service.SettingKeyResendAPIKey])
	require.Equal(t, "resend", repo.values[service.SettingKeyEmailProvider])
	require.Contains(t, rec.Body.String(), `"resend_api_key_configured":true`)
	require.NotContains(t, rec.Body.String(), "re_test-secret")
	require.NotContains(t, rec.Body.String(), `"resend_api_key":`)

	stored := repo.values[service.SettingKeyResendAPIKey]
	rec = doUpdateSettings(t, h, map[string]any{"email_provider": "smtp"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, stored, repo.values[service.SettingKeyResendAPIKey])
	require.Equal(t, "smtp", repo.values[service.SettingKeyEmailProvider])
}

func TestUpdateSettingsRejectsResendWithoutSavedOrNewKey(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	rec := doUpdateSettings(t, h, map[string]any{"email_provider": "resend"}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, repo.values[service.SettingKeyEmailProvider])
}
