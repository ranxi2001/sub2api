package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountOpsRobotOnlyConfiguration(t *testing.T) {
	var cfg AccountOpsConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"balance_low":true,"cooldown_minutes":60,"webhooks":[{"id":"robot","provider":"wecom","enabled":true,"url":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake"}]}`), &cfg))
	require.NoError(t, ValidateAccountOpsConfig(cfg))
}

func TestAccountOpsRobotNotificationContainsFourFields(t *testing.T) {
	value, restored, threshold, used, percent := 2.973, 11.053, 3.0, 92.0, 90.0
	at := time.Date(2026, 10, 7, 5, 5, 26, 0, time.UTC)
	for _, tc := range []struct {
		name, kind, phase, title, balance, threshold string
		details                                      *AccountOpsDetails
	}{
		{"balance", "balance_threshold", "alert", "上游账户余额不足", "$2.973", "$3", &AccountOpsDetails{Balance: &value, Threshold: &threshold, Unit: "USD", ObservedAt: &at}},
		{"recovery", "balance_threshold", "recovery", "上游账户余额已恢复", "$11.053", "$3", &AccountOpsDetails{Balance: &restored, Threshold: &threshold, Unit: "USD", ObservedAt: &at}},
		{"oauth", "quota_threshold", "alert", "上游账户额度不足", "8%（7d）", "10%（已用 90%）", &AccountOpsDetails{UsedPercent: &used, ThresholdPercent: &percent, Window: "7d", ResetsAt: &at}},
		{"failure", "balance_low", "", "上游账户余额不足", "未获取", "不适用", nil},
		{"weekly", "weekly_quota", "", "上游账户周额度已用尽", "未获取", "不适用", nil},
	} {
		for _, provider := range []string{"wecom", "dingtalk", "feishu"} {
			t.Run(tc.name+"/"+provider, func(t *testing.T) {
				svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
				svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "Asia/Shanghai")
				raw := map[string]string{"wecom": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake", "dingtalk": "https://oapi.dingtalk.com/robot/send?access_token=fake", "feishu": "https://open.feishu.cn/open-apis/bot/v2/hook/fake-id"}[provider]
				cipher, err := svc.encryptor.Encrypt(raw)
				require.NoError(t, err)
				calls := 0
				svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					var payload map[string]any
					require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
					var content string
					title := tc.title
					if provider == "feishu" {
						card := requireOpsType[map[string]any](t, payload["card"])
						header := requireOpsType[map[string]any](t, card["header"])
						require.Equal(t, title, requireOpsType[map[string]any](t, header["title"])["content"])
						if tc.phase == "recovery" {
							require.Equal(t, "green", header["template"])
						}
						elements := requireOpsType[[]any](t, card["elements"])
						require.Len(t, elements, 1)
						element := requireOpsType[map[string]any](t, elements[0])
						text := requireOpsType[map[string]any](t, element["text"])
						content = requireOpsType[string](t, text["content"])
					} else {
						markdown := requireOpsType[map[string]any](t, payload["markdown"])
						key := "content"
						if provider == "dingtalk" {
							key = "text"
							require.Equal(t, title, markdown["title"])
						}
						content = requireOpsType[string](t, markdown[key])
						heading, fields, ok := strings.Cut(content, "\n")
						require.True(t, ok)
						require.Equal(t, "### "+title, heading)
						content = strings.ReplaceAll(fields, "**", "")
					}
					require.Equal(t, "账户：ExampleKey (#18)\n余额："+tc.balance+"\n阈值："+tc.threshold+"\n时间：2026-10-07 13:05:26", content)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errcode":0,"code":0}`)), Header: http.Header{}}, nil
				})}
				event := &AccountOpsEvent{AccountID: 18, AccountName: "ExampleKey", Kind: tc.kind, Phase: tc.phase, LastSeen: at, Occurrences: 17, Details: tc.details, HTTPStatus: 402, Signal: "balance_error_code"}
				message := svc.notificationMessage(event)
				require.Contains(t, message.html, "建议操作")
				if tc.phase == "recovery" {
					require.Contains(t, message.html, "恢复时余额")
				}
				require.NoError(t, svc.sendRobot(context.Background(), AccountOpsWebhook{Provider: provider, urlCipher: cipher}, message))
				require.Equal(t, 1, calls)
			})
		}
	}
}

func TestAccountOpsOfficialURLs(t *testing.T) {
	for _, raw := range []string{"http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake", "https://qyapi.weixin.qq.com.evil.test/cgi-bin/webhook/send?key=fake", "https://qyapi.weixin.qq.com:443/cgi-bin/webhook/send?key=fake", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake&redirect=evil"} {
		require.Error(t, validateAccountOpsURL("wecom", raw))
	}
	require.NoError(t, validateAccountOpsURL("wecom", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake"))
}
func TestAccountOpsEncryptedRetainedRedactedConfig(t *testing.T) {
	settings := &accountOpsSettingsStub{}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "Asia/Shanghai")
	var c AccountOpsConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"balance_low":true,"cooldown_minutes":60,"webhooks":[{"id":"one","provider":"dingtalk","enabled":true,"url":"https://oapi.dingtalk.com/robot/send?access_token=fakeonly","secret":"fake-secret"}]}`), &c))
	require.NoError(t, svc.SaveConfig(context.Background(), c))
	require.NotContains(t, settings.raw, "fakeonly")
	require.NotContains(t, settings.raw, "fake-secret")
	got, err := svc.GetConfig(context.Background())
	require.NoError(t, err)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "cipher")
	require.NotContains(t, string(raw), "fakeonly")
	require.True(t, got.Webhooks[0].URLConfigured)
	require.NoError(t, svc.SaveConfig(context.Background(), got))
	got.Webhooks[0].Provider = "feishu"
	require.Error(t, svc.SaveConfig(context.Background(), got))
	got.Webhooks[0].Provider = "dingtalk"
	got.Webhooks[0].ClearSecret = true
	require.NoError(t, svc.SaveConfig(context.Background(), got))
	got, err = svc.GetConfig(context.Background())
	require.NoError(t, err)
	require.False(t, got.Webhooks[0].SecretConfigured)
}

type opsTestEncryptor struct{}

func (opsTestEncryptor) Encrypt(s string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}
func (opsTestEncryptor) Decrypt(s string) (string, error) {
	b, e := base64.StdEncoding.DecodeString(s)
	return string(b), e
}

type opsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f opsRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAccountOpsRobotSigningAndBusinessCodes(t *testing.T) {
	for _, provider := range []string{"wecom", "dingtalk", "feishu"} {
		t.Run(provider, func(t *testing.T) {
			svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
			svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "Asia/Shanghai")
			raw := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fake"
			if provider == "dingtalk" {
				raw = "https://oapi.dingtalk.com/robot/send?access_token=fake"
			}
			if provider == "feishu" {
				raw = "https://open.feishu.cn/open-apis/bot/v2/hook/fake-id"
			}
			cipher, _ := svc.encryptor.Encrypt(raw)
			secret, _ := svc.encryptor.Encrypt("fake-secret")
			w := AccountOpsWebhook{Provider: provider, urlCipher: cipher}
			if provider != "wecom" {
				w.secretCipher = secret
			}
			responseBody := `{"errcode":0}`
			if provider == "feishu" {
				responseBody = `{"code":0}`
			}
			svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				var p map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&p))
				require.Contains(t, fmt.Sprint(p), "Sub2API")
				if provider == "dingtalk" {
					ts := r.URL.Query().Get("timestamp")
					require.NotEmpty(t, ts)
					h := hmac.New(sha256.New, []byte("fake-secret"))
					_, _ = h.Write([]byte(ts + "\nfake-secret"))
					require.Equal(t, base64.StdEncoding.EncodeToString(h.Sum(nil)), r.URL.Query().Get("sign"))
				}
				if provider == "feishu" {
					ts := requireOpsType[string](t, p["timestamp"])
					h := hmac.New(sha256.New, []byte(ts+"\nfake-secret"))
					require.Equal(t, base64.StdEncoding.EncodeToString(h.Sum(nil)), p["sign"])
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responseBody)), Header: http.Header{}}, nil
			})}
			require.NoError(t, svc.sendRobot(context.Background(), w, accountOpsMessage{title: "Sub2API test", plain: "Sub2API test", markdown: "Sub2API test"}))
			responseBody = `{"errcode":310000,"code":19024,"errmsg":"https://secret.example/token"}`
			svc.robotLast = map[string]time.Time{}
			err := svc.sendRobot(context.Background(), w, accountOpsMessage{title: "Sub2API test", plain: "Sub2API test", markdown: "Sub2API test"})
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret.example")
		})
	}
}
func TestAccountOpsChannelRetrySkipsSuccessfulDestinations(t *testing.T) {
	settings := &accountOpsSettingsStub{}
	sender := &accountOpsSenderStub{}
	repo := &accountOpsRepoStub{}
	svc := NewAccountOpsService(settings, repo, sender)
	svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "Asia/Shanghai")
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "ops@example.test"
	cfg.Webhooks = []AccountOpsWebhook{{ID: "robot", Provider: "feishu", Enabled: true, URL: "https://open.feishu.cn/open-apis/bot/v2/hook/fake-id"}}
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	calls := 0
	body := `{"code":1}`
	svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	e := &AccountOpsEvent{AccountID: 7, Kind: "balance_low", Attempts: 1}
	svc.deliverEvent(context.Background(), e)
	require.Equal(t, "failed", repo.state)
	require.Equal(t, 1, sender.calls)
	body = `{"code":0}`
	e.Attempts = 2
	svc.deliverEvent(context.Background(), e)
	require.Equal(t, 1, sender.calls)
	require.Equal(t, 2, calls)
	require.Equal(t, "sent", repo.state)
}

func TestAccountOpsProviderRequiresExplicitNumericZero(t *testing.T) {
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "Asia/Shanghai")
	cipher, _ := svc.encryptor.Encrypt("https://open.feishu.cn/open-apis/bot/v2/hook/fake-id")
	w := AccountOpsWebhook{Provider: "feishu", urlCipher: cipher}
	for _, body := range []string{`{}`, `{"code":null}`, `{"code":"0"}`, `{"code":1}`} {
		t.Run(body, func(t *testing.T) {
			svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			require.Error(t, svc.sendRobot(context.Background(), w, accountOpsMessage{title: "Sub2API"}))
		})
	}
}

type opsChangingSender struct{ change func() }

func (s opsChangingSender) SendEmail(context.Context, string, string, string) error {
	s.change()
	return nil
}
func TestAccountOpsReloadsRemovedDestinationBeforeSending(t *testing.T) {
	settings := &accountOpsSettingsStub{}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(nil, opsTestEncryptor{}, true, "Asia/Shanghai")
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "ops@example.test"
	cfg.Webhooks = []AccountOpsWebhook{{ID: "robot", Provider: "feishu", Enabled: true, URL: "https://open.feishu.cn/open-apis/bot/v2/hook/fake-id"}}
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	svc.email = opsChangingSender{change: func() { cfg.Webhooks = nil; require.NoError(t, svc.SaveConfig(context.Background(), cfg)) }}
	sends := 0
	svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		sends++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})}
	svc.deliverEvent(context.Background(), &AccountOpsEvent{AccountID: 1, Kind: "balance_low"})
	require.Zero(t, sends)
}

type opsReservationStub struct {
	accountOpsRepoStub
	duringWait func()
}

func (s *opsReservationStub) ReserveRobotDelivery(context.Context, *AccountOpsEvent, string, string, string) (bool, error) {
	s.duringWait()
	return true, nil
}
func TestAccountOpsRecoveryDuringRateWaitStopsDelivery(t *testing.T) {
	now := time.Now()
	received := now.Add(-time.Minute)
	fresh := now.Add(time.Hour)
	balance := &UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &received, FreshUntil: &fresh, Data: map[string]any{"remaining": 5.0, "unit": "USD"}}
	a := &Account{ID: 1, Type: AccountTypeAPIKey, Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: balance}}}
	repo := &opsReservationStub{duringWait: func() { balance.Data["remaining"] = 6.0 }}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, nil)
	svc.SetNotificationDependencies(&opsAccountsStub{account: a}, opsTestEncryptor{}, true, "Asia/Shanghai")
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 5, Unit: "USD"}}
	cfg.Webhooks = []AccountOpsWebhook{{ID: "robot", Provider: "feishu", Enabled: true, URL: "https://open.feishu.cn/open-apis/bot/v2/hook/fake-id"}}
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	calls := 0
	svc.robotClient = &http.Client{Transport: opsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})}
	event, trigger := svc.evaluateThreshold(context.Background(), a, "balance_threshold", cfg, now)
	require.True(t, trigger)
	svc.deliverEvent(context.Background(), event)
	require.Zero(t, calls)
	require.Equal(t, "resolved", repo.state)
}

func requireOpsType[T any](t *testing.T, value any) T {
	t.Helper()
	result, ok := value.(T)
	require.True(t, ok, "unexpected payload type %T", value)
	return result
}
