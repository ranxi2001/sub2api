package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountOpsWebhookAutoDetectsAndRetainsConcreteProvider(t *testing.T) {
	for _, tc := range []struct{ provider, url string }{
		{"wecom", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=synthetic-token"},
		{"dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=synthetic-token"},
		{"feishu", "https://open.feishu.cn/open-apis/bot/v2/hook/synthetic-token"},
		{"custom", "https://hooks.example.test:8443/notify?token=synthetic-token"},
	} {
		for _, provider := range []string{"", "auto", tc.provider} {
			t.Run(tc.provider+"/"+provider, func(t *testing.T) {
				settings := &accountOpsSettingsStub{}
				svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
				svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "UTC")
				out, err := svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Provider: provider, Enabled: true, URL: tc.url})
				require.NoError(t, err)
				require.Equal(t, tc.provider, out.Webhooks[0].Provider)
				require.True(t, out.Webhooks[0].URLConfigured)
				require.NotContains(t, settings.raw, "synthetic-token")
				before := settings.raw
				out, err = svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Enabled: true})
				require.NoError(t, err)
				require.Equal(t, tc.provider, out.Webhooks[0].Provider)
				require.JSONEq(t, before, settings.raw)
				out, err = svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Provider: "auto", Enabled: false})
				require.NoError(t, err)
				require.Equal(t, tc.provider, out.Webhooks[0].Provider)
				require.False(t, out.Webhooks[0].Enabled)
			})
		}
	}
}

func TestAccountOpsWebhookURLChangeDoesNotReuseSigningSecret(t *testing.T) {
	for _, nextURL := range []string{
		"https://oapi.dingtalk.com/robot/send?access_token=new-bot",
		"https://open.feishu.cn/open-apis/bot/v2/hook/new-bot",
		"https://hooks.example.test/new-bot",
	} {
		t.Run(nextURL, func(t *testing.T) {
			settings := &accountOpsSettingsStub{}
			svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
			svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "UTC")
			oldURL := "https://oapi.dingtalk.com/robot/send?access_token=old-bot"
			_, err := svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Provider: "dingtalk", Enabled: true, URL: oldURL, Secret: "old-secret"})
			require.NoError(t, err)
			out, err := svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Provider: "dingtalk", Enabled: true, URL: oldURL})
			require.NoError(t, err)
			require.True(t, out.Webhooks[0].SecretConfigured, "resubmitting the same URL retains its own secret")
			out, err = svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Provider: "auto", Enabled: true, URL: nextURL})
			require.NoError(t, err)
			require.False(t, out.Webhooks[0].SecretConfigured)
			var stored storedOpsConfig
			require.NoError(t, json.Unmarshal([]byte(settings.raw), &stored))
			require.Empty(t, stored.StoredWebhooks[0].SecretCipher)
		})
	}
}

func TestAccountOpsWebhookRejectsUnsafeOrMalformedURLsWithoutSaving(t *testing.T) {
	for _, raw := range []string{
		"http://hooks.example.test/send", "https://localhost/send", "https://localhost./send", "https://x.localhost/send",
		"https://127.0.0.1/send", "https://10.1.2.3/send", "https://[::1]/send", "https://[::ffff:127.0.0.1]/send",
		"https://169.254.169.254/latest", "https://metadata.google.internal/send", "https://metadata.google.internal./send",
		"https://name:password@hooks.example.test/send", "https://hooks.example.test/send#fragment", "https://hooks.example.test:0/send", "https://hooks.example.test:65536/send",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=synthetic&extra=value",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=synthetic&bad=%zz",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=synthetic;extra=value",
		"https://qyapi.weixin.qq.com:443/cgi-bin/webhook/send?key=synthetic",
		"https://qyapi.weixin.qq.com./cgi-bin/webhook/send?key=synthetic",
		"https://open.feishu.cn/not-a-hook", "https://oapi.dingtalk.com/robot/send?access_token=synthetic&timestamp=123",
	} {
		t.Run(raw, func(t *testing.T) {
			settings := &accountOpsSettingsStub{}
			svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
			svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "UTC")
			_, err := svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Enabled: true, URL: raw})
			require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
			require.Empty(t, settings.raw)
			require.NotContains(t, err.Error(), "synthetic")
			require.NotContains(t, err.Error(), raw)
		})
	}
}

func TestAccountOpsCustomWebhookTemplatePreservesEscapingAndCompactFields(t *testing.T) {
	settings := &accountOpsSettingsStub{}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "UTC")
	var input AccountOpsWebhook
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"url":"https://hooks.example.test/send?token=synthetic-secret","message_template":"{\"content\":\"{{message}}\",\"embeds\":[{\"title\":\"{{title}}\",\"fields\":[\"{{account}}\",\"{{balance}}\",\"{{threshold}}\",\"{{time}}\"]}],\"n\":1234567890123456789}"}`), &input))
	out, err := svc.SaveWebhook(context.Background(), "bot", input)
	require.NoError(t, err)
	require.Equal(t, "custom", out.Webhooks[0].Provider)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(raw), "message_template")
	require.NotContains(t, string(raw), "synthetic-secret")
	value, threshold := 2.5, 3.0
	message := svc.notificationMessage(&AccountOpsEvent{AccountName: `Example "quote" {{threshold}}`, AccountID: 17, Kind: "balance_threshold", LastSeen: time.Date(2026, 10, 8, 1, 2, 3, 0, time.UTC), Details: &AccountOpsDetails{Balance: &value, Threshold: &threshold, Unit: "USD"}})
	svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded))
		require.Contains(t, string(body), `1234567890123456789`)
		require.Equal(t, "上游账户余额不足\n账户：Example \"quote\" {{threshold}} (#17)\n余额：$2.5\n阈值：$3\n时间：2026-10-08 01:02:03", decoded["content"])
		embeds, ok := decoded["embeds"].([]any)
		require.True(t, ok)
		require.Len(t, embeds, 1)
		embed, ok := embeds[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, []any{`Example "quote" {{threshold}} (#17)`, "$2.5", "$3", "2026-10-08 01:02:03"}, embed["fields"])
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	cfg, err := svc.loadConfig(context.Background())
	require.NoError(t, err)
	require.NoError(t, svc.sendRobot(context.Background(), cfg.Webhooks[0], message))
	before := settings.raw
	_, err = svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Enabled: true})
	require.NoError(t, err)
	require.JSONEq(t, before, settings.raw)
}

func TestAccountOpsCustomWebhookTemplateValidationAndReset(t *testing.T) {
	for _, template := range []string{"[]", "null", "broken", `{"text":"{{unknown}}"}`, `{"{{account}}":"value"}`, `{"text":"{{message}"}`, strings.Repeat(" ", 16385) + `{}`, `{"x":` + strings.Repeat(`[`, 65) + `0` + strings.Repeat(`]`, 65) + `}`} {
		t.Run(fmt.Sprintf("length_%d_%s", len(template), template[:min(20, len(template))]), func(t *testing.T) {
			settings := &accountOpsSettingsStub{}
			svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
			svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "UTC")
			body, err := json.Marshal(map[string]any{"enabled": true, "url": "https://hooks.example.test/send", "message_template": template})
			require.NoError(t, err)
			var input AccountOpsWebhook
			require.NoError(t, json.Unmarshal(body, &input))
			_, err = svc.SaveWebhook(context.Background(), "bot", input)
			require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
			require.Empty(t, settings.raw)
		})
	}
}

func TestAccountOpsCustomWebhookTemplateResetChangesRevisionAndUsesDefaultBody(t *testing.T) {
	settings := &accountOpsSettingsStub{}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "UTC")
	template := `{"content":"{{account}}"}`
	_, err := svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Enabled: true, URL: "https://hooks.example.test/send", MessageTemplate: &template})
	require.NoError(t, err)
	var before, after storedOpsConfig
	require.NoError(t, json.Unmarshal([]byte(settings.raw), &before))
	empty := ""
	_, err = svc.SaveWebhook(context.Background(), "bot", AccountOpsWebhook{Enabled: true, MessageTemplate: &empty})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(settings.raw), &after))
	require.NotEqual(t, before.StoredWebhooks[0].Revision, after.StoredWebhooks[0].Revision)
	require.Equal(t, before.StoredWebhooks[0].URLCipher, after.StoredWebhooks[0].URLCipher)
	message := svc.compactRobotMessage(&AccountOpsEvent{AccountName: "test", LastSeen: time.Date(2026, 10, 8, 1, 2, 3, 0, time.UTC)}, "Test")
	cfg, err := svc.loadConfig(context.Background())
	require.NoError(t, err)
	for _, status := range []int{200, 201, 204, 302, 400, 500} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			calls := 0
			svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.JSONEq(t, `{"text":"Test\n账户：test\n余额：未获取\n阈值：未获取\n时间：2026-10-08 01:02:03"}`, string(body))
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://second.example.test/private"}}, Body: io.NopCloser(strings.NewReader("private-response-error"))}, nil
			})}
			err = svc.sendRobot(context.Background(), cfg.Webhooks[0], message)
			if status >= 200 && status < 300 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private-response-error")
			}
			require.Equal(t, 1, calls, "redirect destinations must never receive notification data")
		})
	}
}

func TestAccountOpsWebhookDefaultTransportBlocksPrivateDestinations(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	response, err := accountOpsHTTPClient.Get(server.URL)
	if response != nil {
		_ = response.Body.Close()
	}
	require.Error(t, err)
	require.Zero(t, requests.Load())
}

func TestAccountOpsWebhookExplicitProviderCannotBypassValidation(t *testing.T) {
	for _, input := range []AccountOpsWebhook{
		{Provider: "custom", URL: "https://qyapi.weixin.qq.com/not-a-hook"},
		{Provider: "wecom", URL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake&bad=%zz"},
		{Provider: "dingtalk", URL: "https://hooks.example.test/send"},
		{Provider: "custom", URL: "https://hooks.example.test/send", Secret: "unused-secret"},
	} {
		svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
		svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "UTC")
		_, err := svc.SaveWebhook(context.Background(), "bot", input)
		require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
	}
}
