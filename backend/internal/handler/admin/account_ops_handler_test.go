//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

type opsHandlerSettings struct{ service.SettingRepository }

func (opsHandlerSettings) GetValue(context.Context, string) (string, error) { return "", nil }
func TestAccountOpsConfigWithoutSMTPService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := service.NewAccountOpsService(opsHandlerSettings{}, nil, nil)
	h := NewAccountOpsHandler(svc, nil)
	r := gin.New()
	r.GET("/config", h.GetConfig)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/config", nil))
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `"smtp_configured":false`)
}

type opsHandlerWritableSettings struct {
	service.SettingRepository
	raw      string
	writes   int
	writeErr error
}

func (s *opsHandlerWritableSettings) GetValue(context.Context, string) (string, error) {
	return s.raw, nil
}
func (s *opsHandlerWritableSettings) Set(_ context.Context, _ string, value string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.raw = value
	s.writes++
	return nil
}

type opsHandlerAccounts struct {
	service.AccountRepository
	account *service.Account
	err     error
}

func (s opsHandlerAccounts) GetByID(context.Context, int64) (*service.Account, error) {
	return s.account, s.err
}

type opsHandlerQueue struct{ service.AccountOpsRepository }

func (opsHandlerQueue) SuppressDisabled(context.Context, service.AccountOpsConfig) error { return nil }

type opsHandlerEncryptor struct{ err error }

func (s opsHandlerEncryptor) Encrypt(string) (string, error) { return "synthetic-cipher", s.err }
func (s opsHandlerEncryptor) Decrypt(string) (string, error) { return "", s.err }
func TestAccountOpsInvalidConfigurationReturnsSafe400WithoutWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, fields string
		accountType  string
	}{
		{"amount rule on OAuth", `,"balance_thresholds":[{"account_id":41,"enabled":true,"threshold":5,"unit":"USD"}]`, service.AccountTypeOAuth},
		{"quota rule on API key", `,"quota_thresholds":[{"account_id":41,"enabled":true,"threshold_percent":80,"window":"any"}]`, service.AccountTypeAPIKey},
		{"invalid recipient", `,"recipient":"canary-secret@example.test,not-an-email"`, service.AccountTypeAPIKey},
		{"invalid official URL", `,"webhooks":[{"id":"robot","provider":"wecom","url":"https://bad.example/canary-secret?key=private-canary"}]`, service.AccountTypeAPIKey},
		{"negative threshold", `,"balance_thresholds":[{"account_id":41,"enabled":true,"threshold":-1,"unit":"USD"}]`, service.AccountTypeAPIKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &opsHandlerWritableSettings{raw: `{"cooldown_minutes":60}`}
			before := settings.raw
			svc := service.NewAccountOpsService(settings, opsHandlerQueue{}, nil)
			svc.SetNotificationDependencies(opsHandlerAccounts{account: &service.Account{ID: 41, Type: tc.accountType}}, opsHandlerEncryptor{}, true, "Asia/Shanghai")
			h := NewAccountOpsHandler(svc, nil)
			router := gin.New()
			router.PUT("/config", h.SaveConfig)
			request := httptest.NewRequest("PUT", "/config", strings.NewReader(`{"enabled":false,"cooldown_minutes":60`+tc.fields+`}`))
			request.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, request)
			require.Equal(t, 400, rec.Code)
			require.Equal(t, before, settings.raw)
			require.Zero(t, settings.writes)
			require.NotContains(t, rec.Body.String(), "canary-secret")
			require.NotContains(t, rec.Body.String(), "private-canary")
			require.NotContains(t, rec.Body.String(), "bad.example")
		})
	}
}
func TestAccountOpsInfrastructureFailuresReturnSafe503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"storage", "account lookup", "encryption", "stored config"} {
		t.Run(kind, func(t *testing.T) {
			settings := &opsHandlerWritableSettings{raw: `{"cooldown_minutes":60}`}
			lookup := opsHandlerAccounts{account: &service.Account{ID: 41, Type: service.AccountTypeAPIKey}}
			encryptor := opsHandlerEncryptor{}
			body := `{"enabled":false,"cooldown_minutes":60}`
			canary := errors.New("database-or-credential-private-canary")
			switch kind {
			case "stored config":
				settings.raw = `{"enabled":true,"cooldown_minutes":60}`
			case "storage":
				settings.writeErr = canary
			case "account lookup":
				lookup.err = canary
				body = `{"enabled":false,"cooldown_minutes":60,"balance_thresholds":[{"account_id":41,"enabled":true,"threshold":5,"unit":"USD"}]}`
			case "encryption":
				encryptor.err = canary
				body = `{"enabled":false,"cooldown_minutes":60,"webhooks":[{"id":"robot","provider":"wecom","url":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=private-canary"}]}`
			}
			svc := service.NewAccountOpsService(settings, opsHandlerQueue{}, nil)
			svc.SetNotificationDependencies(lookup, encryptor, true, "Asia/Shanghai")
			router := gin.New()
			router.PUT("/config", NewAccountOpsHandler(svc, nil).SaveConfig)
			request := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, request)
			require.Equal(t, 503, rec.Code)
			require.NotContains(t, rec.Body.String(), "private-canary")
			require.Zero(t, settings.writes)
		})
	}
}

func TestAccountOpsScopedRequestsIgnoreOtherScopesAndSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &opsHandlerWritableSettings{raw: `{"cooldown_minutes":60,"balance_thresholds":[{"account_id":41,"enabled":true,"threshold":5,"unit":"USD","notify_alert":false,"notify_recovery":true}],"encrypted_webhooks":[{"id":"saved","provider":"dingtalk","enabled":true,"url_cipher":"private-url-cipher","secret_cipher":"private-secret-cipher","revision":"unchanged"}]}`}
	svc := service.NewAccountOpsService(settings, opsHandlerQueue{}, nil)
	svc.SetNotificationDependencies(opsHandlerAccounts{account: &service.Account{ID: 41, Type: service.AccountTypeAPIKey}}, opsHandlerEncryptor{}, true, "UTC")
	router := gin.New()
	h := NewAccountOpsHandler(svc, nil)
	router.PUT("/notification-settings", h.SaveNotificationSettings)
	router.PUT("/rules/:id", h.SaveRule)
	router.DELETE("/rules/:id", h.DeleteRule)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := send("PUT", "/notification-settings", `{"enabled":false,"recipient":"new@example.test","balance_low":true,"weekly_quota":true,"cooldown_minutes":90,"webhooks":[],"balance_thresholds":[],"secret":"must-not-save"}`)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `"account_id":41`)
	require.Contains(t, rec.Body.String(), `"id":"saved"`)
	require.NotContains(t, rec.Body.String(), "private-")
	require.NotContains(t, settings.raw, "must-not-save")
	require.Contains(t, settings.raw, "private-secret-cipher")
	rec = send("PUT", "/rules/41", `{"metric":"balance","enabled":true,"threshold":9,"notify_recovery":false,"recipient":"overwrite@example.test","webhooks":[],"url":"must-not-save","secret":"must-not-save"}`)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `"recipient":"new@example.test"`)
	require.Contains(t, rec.Body.String(), `"notify_alert":false`)
	require.Contains(t, rec.Body.String(), `"notify_recovery":false`)
	require.Contains(t, settings.raw, "private-secret-cipher")
	require.NotContains(t, settings.raw, "must-not-save")
	rec = send("DELETE", "/rules/41?metric=balance", "")
	require.Equal(t, 200, rec.Code)
	require.NotContains(t, rec.Body.String(), `"account_id":41`)
	require.Contains(t, rec.Body.String(), `"id":"saved"`)
}
func TestAccountOpsScopedValidationAndStorageFailuresStaySafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		body    string
		storage bool
		want    int
	}{
		{"static threshold validation", `{"metric":"balance","enabled":true,"threshold":-1,"unit":"USD"}`, false, 400},
		{"database failure", `{"metric":"balance","enabled":true,"threshold":5,"unit":"USD"}`, true, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &opsHandlerWritableSettings{raw: `{"cooldown_minutes":60}`}
			if tc.storage {
				settings.writeErr = errors.New("private-database-canary")
			}
			svc := service.NewAccountOpsService(settings, opsHandlerQueue{}, nil)
			svc.SetNotificationDependencies(opsHandlerAccounts{account: &service.Account{ID: 41, Type: service.AccountTypeAPIKey}}, nil, false, "UTC")
			router := gin.New()
			router.PUT("/rules/:id", NewAccountOpsHandler(svc, nil).SaveRule)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", "/rules/41", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.want, rec.Code)
			require.Zero(t, settings.writes)
			require.NotContains(t, rec.Body.String(), "private-database-canary")
		})
	}
}

func TestAccountOpsBatchHandlerValidatesPayloadAndRedactsConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body string
		storage    bool
		want       int
	}{
		{"complete batch", `{"account_ids":[41,42],"rule":{"metric":"balance","enabled":true,"threshold":8,"unit":"USD","notify_alert":true,"notify_recovery":false},"recipient":"must-not-save@example.test","webhooks":[]}`, false, 200},
		{"malformed JSON", `{"account_ids":`, false, 400},
		{"empty IDs", `{"account_ids":[],"rule":{"metric":"balance"}}`, false, 400},
		{"partial existing rule", `{"account_ids":[41],"rule":{"metric":"balance","enabled":true,"notify_alert":true,"notify_recovery":false}}`, false, 400},
		{"duplicate IDs", `{"account_ids":[41,41],"rule":{"metric":"balance","enabled":true,"threshold":8,"unit":"USD","notify_alert":true,"notify_recovery":false}}`, false, 400},
		{"storage failure", `{"account_ids":[41,42],"rule":{"metric":"balance","enabled":true,"threshold":8,"unit":"USD","notify_alert":true,"notify_recovery":false}}`, true, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &opsHandlerWritableSettings{raw: `{"cooldown_minutes":60,"recipient":"before@example.test","balance_thresholds":[{"account_id":41,"enabled":true,"threshold":3,"unit":"USD"}],"encrypted_webhooks":[{"id":"saved","provider":"dingtalk","enabled":true,"url_cipher":"private-url-cipher","secret_cipher":"private-secret-cipher","revision":"private-revision"}]}`}
			before := settings.raw
			if tc.storage {
				settings.writeErr = errors.New("private-storage-error")
			}
			svc := service.NewAccountOpsService(settings, opsHandlerQueue{}, nil)
			svc.SetNotificationDependencies(opsHandlerAccounts{account: &service.Account{ID: 41, Type: service.AccountTypeAPIKey}}, nil, false, "UTC")
			h := NewAccountOpsHandler(svc, nil)
			router := gin.New()
			router.PUT("/rules/batch", h.SaveRulesBatch)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", "/rules/batch", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			require.Equal(t, tc.want, recorder.Code)
			require.NotContains(t, recorder.Body.String(), "private-")
			if tc.want == 200 {
				require.Contains(t, recorder.Body.String(), `"account_id":42`)
				require.Contains(t, recorder.Body.String(), `"recipient":"before@example.test"`)
				require.Contains(t, settings.raw, "private-secret-cipher")
				require.NotContains(t, settings.raw, "must-not-save")
			} else {
				require.Equal(t, before, settings.raw)
			}
		})
	}
}

func TestAccountOpsHandlerSavesCustomChannelNamesWithoutExposingCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &opsHandlerWritableSettings{raw: `{"cooldown_minutes":60,"recipient":"ops@example.test","encrypted_webhooks":[{"id":"robot","provider":"dingtalk","enabled":true,"url_cipher":"private-url-cipher","secret_cipher":"private-secret-cipher","revision":"private-revision"}]}`}
	svc := service.NewAccountOpsService(settings, opsHandlerQueue{}, nil)
	h := NewAccountOpsHandler(svc, nil)
	router := gin.New()
	router.PUT("/settings", h.SaveNotificationSettings)
	router.PUT("/webhooks/:id", h.SaveWebhook)
	for _, tc := range []struct{ path, body, expected string }{
		{"/settings", `{"email_name":"  值班邮箱  "}`, `"email_name":"值班邮箱"`},
		{"/webhooks/robot", `{"enabled":true,"name":"  值班机器人  "}`, `"name":"值班机器人"`},
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		require.Equal(t, 200, recorder.Code)
		require.Contains(t, recorder.Body.String(), tc.expected)
		require.NotContains(t, recorder.Body.String(), "private-")
		require.Contains(t, settings.raw, "private-secret-cipher")
		require.Contains(t, settings.raw, "private-revision")
	}
}

type opsHandlerGroupedAccounts struct{ service.AccountRepository }

func (opsHandlerGroupedAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	switch id {
	case 41, 42:
		return &service.Account{ID: id, Type: service.AccountTypeAPIKey}, nil
	case 61, 62:
		return &service.Account{ID: id, Type: service.AccountTypeOAuth}, nil
	default:
		return nil, service.ErrAccountNotFound
	}
}

func TestAccountOpsGroupedBatchHandlerIsAtomicAndRejectsAmbiguousPayloads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	balance := `{"metric":"balance","enabled":true,"threshold":8,"unit":"USD","notify_alert":true,"notify_recovery":false}`
	quota := `{"metric":"quota","enabled":true,"threshold_percent":90,"window":"any","notify_alert":false,"notify_recovery":true}`
	validGroups := `[{"account_ids":[41,42],"rule":` + balance + `},{"account_ids":[61,62],"rule":` + quota + `}]`
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"mixed selection", `{"groups":` + validGroups + `}`, 200},
		{"legacy still supported", `{"account_ids":[41,42],"rule":` + balance + `}`, 200},
		{"missing quota account", `{"groups":[{"account_ids":[41],"rule":` + balance + `},{"account_ids":[999],"rule":` + quota + `}]}`, 400},
		{"wrong quota account type", `{"groups":[{"account_ids":[41],"rule":` + balance + `},{"account_ids":[42],"rule":` + quota + `}]}`, 400},
		{"incomplete flags in second group", `{"groups":[{"account_ids":[41],"rule":` + balance + `},{"account_ids":[61],"rule":{"metric":"quota","enabled":true,"threshold_percent":90,"window":"any","notify_alert":true}}]}`, 400},
		{"cross-group duplicate", `{"groups":[{"account_ids":[41],"rule":` + balance + `},{"account_ids":[41],"rule":` + balance + `}]}`, 400},
		{"empty groups", `{"groups":[]}`, 400},
		{"null groups", `{"groups":null}`, 400},
		{"empty child group", `{"groups":[{"account_ids":[],"rule":` + balance + `}]}`, 400},
		{"too many groups", `{"groups":[{},{},{}]}`, 400},
		{"groups plus full legacy", `{"groups":` + validGroups + `,"account_ids":[41],"rule":` + balance + `}`, 400},
		{"groups plus null legacy IDs", `{"groups":` + validGroups + `,"account_ids":null}`, 400},
		{"groups plus null legacy rule", `{"groups":` + validGroups + `,"rule":null}`, 400},
		{"legacy plus null groups", `{"groups":null,"account_ids":[41],"rule":` + balance + `}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &opsHandlerWritableSettings{raw: `{"cooldown_minutes":60,"recipient":"ops@example.test","balance_thresholds":[{"account_id":41,"enabled":false,"threshold":3,"unit":"USD"}],"quota_thresholds":[{"account_id":61,"enabled":false,"threshold_percent":75,"window":"7d"}],"encrypted_webhooks":[{"id":"saved","provider":"dingtalk","enabled":true,"url_cipher":"private-url","secret_cipher":"private-secret","revision":"private-revision"}]}`}
			before := settings.raw
			svc := service.NewAccountOpsService(settings, opsHandlerQueue{}, nil)
			svc.SetNotificationDependencies(opsHandlerGroupedAccounts{}, nil, false, "UTC")
			router := gin.New()
			router.PUT("/rules/batch", NewAccountOpsHandler(svc, nil).SaveRulesBatch)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", "/rules/batch", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			require.Equal(t, tc.want, recorder.Code)
			require.NotContains(t, recorder.Body.String(), "private-")
			if tc.want != 200 {
				require.Zero(t, settings.writes)
				require.Equal(t, before, settings.raw)
				return
			}
			require.Equal(t, 1, settings.writes, "all groups must persist in one configuration write")
			require.Contains(t, settings.raw, "private-secret")
			var response struct {
				Data service.AccountOpsConfig `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			for _, rule := range response.Data.BalanceThresholds {
				require.Equal(t, 8.0, rule.Threshold)
				require.True(t, *rule.NotifyAlert)
				require.False(t, *rule.NotifyRecovery)
			}
			if tc.name == "mixed selection" {
				require.Len(t, response.Data.QuotaThresholds, 2)
				for _, rule := range response.Data.QuotaThresholds {
					require.Equal(t, 90.0, rule.ThresholdPercent)
					require.False(t, *rule.NotifyAlert)
					require.True(t, *rule.NotifyRecovery)
				}
			}
		})
	}
}
