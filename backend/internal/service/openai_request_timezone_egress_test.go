package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type egressTimezoneProber struct {
	mu    sync.Mutex
	calls []string
	probe func(context.Context, string) (*ProxyExitInfo, int64, error)
}

func (p *egressTimezoneProber) ProbeProxy(ctx context.Context, proxy string) (*ProxyExitInfo, int64, error) {
	p.mu.Lock()
	p.calls = append(p.calls, proxy)
	p.mu.Unlock()
	return p.probe(ctx, proxy)
}
func timezoneAutoService(enabled, auto bool, probe *egressTimezoneProber) *OpenAIGatewayService {
	return &OpenAIGatewayService{requestTimezoneProber: probe, settingService: NewSettingService(&codexTicketLifecycleSettings{get: func(_ context.Context, key string) (string, error) {
		if key == SettingKeyOpenAIRequestTimezoneEnabled && enabled || key == SettingKeyOpenAIRequestTimezoneAuto && auto {
			return "true", nil
		}
		return "false", nil
	}}, &config.Config{})}
}
func TestRequestTimezoneHeadersManualAndAuto(t *testing.T) {
	for _, tc := range []struct {
		name string
		auto bool
		want string
	}{{"manual", false, "Asia/Tokyo"}, {"automatic", true, "Asia/Shanghai"}} {
		t.Run(tc.name, func(t *testing.T) {
			svc := timezoneAutoService(true, tc.auto, nil)
			source := http.Header{"X-Timezone": []string{"Asia/Shanghai"}}
			target := http.Header{}
			account := &Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIRequestTimezoneExtraKey: "Asia/Tokyo"}}
			svc.requestTimezoneHeaders(context.Background(), account, source, target)
			require.Equal(t, tc.want, target.Get("X-Timezone"))
			require.Empty(t, target.Get("X-Openai-Timezone"), "do not invent timezone headers")
		})
	}
}

func TestRequestTimezoneEgressModesAndSwitch(t *testing.T) {
	original := localeTestBody([]string{"<environment_context><current_date>2001-01-01</current_date><timezone>Asia/Shanghai</timezone></environment_context>", "ordinary Asia/Shanghai"}, nil)
	for _, tc := range []struct {
		name          string
		enabled, auto bool
		want          string
		calls         int
	}{
		{"disabled", false, true, "Asia/Shanghai", 0}, {"manual", true, false, "Asia/Singapore", 0}, {"automatic", true, true, "Asia/Tokyo", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &egressTimezoneProber{probe: func(context.Context, string) (*ProxyExitInfo, int64, error) {
				return &ProxyExitInfo{IP: "203.0.113.10", Timezone: "Asia/Tokyo"}, 0, nil
			}}
			svc := timezoneAutoService(tc.enabled, tc.auto, probe)
			account := &Account{ID: 1, Platform: PlatformOpenAI}
			ctx := svc.withRequestTimezoneEgress(context.Background(), account)
			body := svc.normalizeRequestTimezone(ctx, account, original, "http")
			req, err := http.NewRequestWithContext(ctx, "POST", "https://example.com/v1/responses", strings.NewReader(string(body)))
			require.NoError(t, err)
			req.Header.Set("X-Timezone", "Asia/Shanghai")
			svc.requestTimezoneHeaders(ctx, account, nil, req.Header)
			got, err := BindOpenAIRequestTimezone(req, "http://proxy-a:8080")
			require.NoError(t, err)
			data, err := io.ReadAll(got.Body)
			require.NoError(t, err)
			require.NoError(t, got.Body.Close())
			require.Contains(t, gjson.GetBytes(data, "input.0.content.0.text").String(), tc.want)
			require.Contains(t, string(data), "2001-01-01")
			require.Contains(t, string(data), "ordinary Asia/Shanghai")
			require.Equal(t, tc.want, got.Header.Get("X-Timezone"))
			require.Len(t, probe.calls, tc.calls)
		})
	}
}
func TestRequestTimezoneEgressDefaultSettings(t *testing.T) {
	original := localeTestBody([]string{"<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"}, nil)
	for _, tc := range []struct {
		name          string
		settings      map[string]string
		readErr       error
		enabled, auto bool
		want          string
		calls         int
	}{
		{name: "missing", enabled: true, auto: true, want: "Asia/Tokyo", calls: 1},
		{name: "empty", settings: map[string]string{SettingKeyOpenAIRequestTimezoneEnabled: "", SettingKeyOpenAIRequestTimezoneAuto: ""}, enabled: true, auto: true, want: "Asia/Tokyo", calls: 1},
		{name: "whitespace", settings: map[string]string{SettingKeyOpenAIRequestTimezoneEnabled: " ", SettingKeyOpenAIRequestTimezoneAuto: " "}, enabled: true, auto: true, want: "Asia/Tokyo", calls: 1},
		{name: "saved disabled", settings: map[string]string{SettingKeyOpenAIRequestTimezoneEnabled: "false"}, auto: true, want: "Asia/Shanghai"},
		{name: "saved manual", settings: map[string]string{SettingKeyOpenAIRequestTimezoneAuto: "false"}, enabled: true, want: "Asia/Singapore"},
		{name: "read failure", readErr: errors.New("offline"), want: "Asia/Shanghai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &egressTimezoneProber{probe: func(context.Context, string) (*ProxyExitInfo, int64, error) {
				return &ProxyExitInfo{IP: "203.0.113.10", Timezone: "Asia/Tokyo"}, 0, nil
			}}
			settings := NewSettingService(&codexTicketLifecycleSettings{get: func(_ context.Context, key string) (string, error) {
				if tc.readErr != nil {
					return "", tc.readErr
				}
				if value, ok := tc.settings[key]; ok {
					return value, nil
				}
				return "", ErrSettingNotFound
			}}, &config.Config{})
			if tc.readErr == nil {
				parsed := settings.parseSettings(tc.settings)
				require.Equal(t, tc.enabled, parsed.OpenAIRequestTimezoneEnabled)
				require.Equal(t, tc.auto, parsed.OpenAIRequestTimezoneAuto)
			}
			svc := &OpenAIGatewayService{settingService: settings, requestTimezoneProber: probe}
			account := &Account{ID: 1, Platform: PlatformOpenAI}
			ctx := svc.withRequestTimezoneEgress(context.Background(), account)
			body := svc.normalizeRequestTimezone(ctx, account, original, "http")
			req, err := http.NewRequestWithContext(ctx, "POST", "https://example.com/v1/responses", strings.NewReader(string(body)))
			require.NoError(t, err)
			req.Header.Set("X-Timezone", "Asia/Shanghai")
			svc.requestTimezoneHeaders(ctx, account, nil, req.Header)
			got, err := BindOpenAIRequestTimezone(req, "http://proxy-a:8080")
			require.NoError(t, err)
			data, err := io.ReadAll(got.Body)
			require.NoError(t, err)
			require.NoError(t, got.Body.Close())
			require.Contains(t, gjson.GetBytes(data, "input.0.content.0.text").String(), tc.want)
			require.Equal(t, tc.want, got.Header.Get("X-Timezone"))
			require.Len(t, probe.calls, tc.calls)
		})
	}
}

func TestRequestTimezoneEgressProxyChangeAndFailure(t *testing.T) {
	probe := &egressTimezoneProber{probe: func(_ context.Context, proxy string) (*ProxyExitInfo, int64, error) {
		if proxy == "bad" {
			return nil, 0, errors.New("offline")
		}
		zone := "Asia/Tokyo"
		if proxy == "proxy-b" {
			zone = "America/New_York"
		}
		return &ProxyExitInfo{IP: "203.0.113.10", Timezone: zone}, 0, nil
	}}
	svc := timezoneAutoService(true, true, probe)
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	ctx := svc.withRequestTimezoneEgress(context.Background(), account)
	original := localeTestBody([]string{"<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"}, nil)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://example.com", strings.NewReader(string(original)))
	require.NoError(t, err)
	req.Header.Set("X-Timezone", "Asia/Shanghai")
	for _, tc := range []struct{ proxy, want string }{{"proxy-a", "Asia/Tokyo"}, {"proxy-b", "America/New_York"}, {"proxy-a", "Asia/Tokyo"}, {"bad", "Asia/Shanghai"}} {
		got, err := BindOpenAIRequestTimezone(req, tc.proxy)
		require.NoError(t, err)
		data, err := io.ReadAll(got.Body)
		require.NoError(t, err)
		require.Contains(t, gjson.GetBytes(data, "input.0.content.0.text").String(), tc.want)
		require.Equal(t, tc.want, got.Header.Get("X-Timezone"))
	}
	require.Equal(t, "Asia/Shanghai", req.Header.Get("X-Timezone"))
	require.Equal(t, []string{"proxy-a", "proxy-b", "bad"}, probe.calls)
}
func TestRequestTimezoneEgressCacheExpiryAndValidation(t *testing.T) {
	for _, tc := range []struct {
		ip, zone string
		valid    bool
	}{{"203.0.113.10", "Asia/Tokyo", true}, {"", "Asia/Tokyo", false}, {"203.0.113.10", "Unknown/Zone", false}, {"203.0.113.10", "", false}} {
		probe := &egressTimezoneProber{probe: func(context.Context, string) (*ProxyExitInfo, int64, error) {
			return &ProxyExitInfo{IP: tc.ip, Timezone: tc.zone}, 0, nil
		}}
		svc := timezoneAutoService(true, true, probe)
		name := svc.egressRequestTimezone(context.Background(), "proxy")
		require.Equal(t, tc.valid, name != "")
		require.Equal(t, name, svc.egressRequestTimezone(context.Background(), "proxy"))
		require.Len(t, probe.calls, 1)
		svc.requestTimezoneEgress.mu.Lock()
		svc.requestTimezoneEgress.entries[sha256.Sum256([]byte("proxy"))] = egressTimezoneEntry{name, time.Now().Add(-time.Second)}
		svc.requestTimezoneEgress.mu.Unlock()
		svc.egressRequestTimezone(context.Background(), "proxy")
		require.Len(t, probe.calls, 2)
	}
}
func TestRequestTimezoneEgressConcurrentCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	probe := &egressTimezoneProber{probe: func(ctx context.Context, _ string) (*ProxyExitInfo, int64, error) {
		close(entered)
		select {
		case <-release:
			return &ProxyExitInfo{IP: "203.0.113.10", Timezone: "Asia/Tokyo"}, 0, nil
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}}
	svc := timezoneAutoService(true, true, probe)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- svc.egressRequestTimezone(ctx, "proxy") }()
	<-entered
	cancel()
	require.Empty(t, <-done)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.Equal(t, "Asia/Tokyo", svc.egressRequestTimezone(context.Background(), "proxy"))
		}()
	}
	close(release)
	wg.Wait()
	require.Len(t, probe.calls, 1)
}
func TestRequestTimezoneEgressWebSocketFrames(t *testing.T) {
	received := make(chan []byte, 2)
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for i := 0; i < 2; i++ {
			_, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			received <- payload
		}
	}))
	defer server.Close()
	probe := &egressTimezoneProber{probe: func(context.Context, string) (*ProxyExitInfo, int64, error) {
		return &ProxyExitInfo{IP: "203.0.113.10", Timezone: "Asia/Tokyo"}, 0, nil
	}}
	svc := timezoneAutoService(true, true, probe)
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	ctx, cancel := context.WithTimeout(svc.withRequestTimezoneEgress(context.Background(), account), 5*time.Second)
	defer cancel()
	conn, _, _, err := newDefaultOpenAIWSClientDialer().Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"X-Timezone": []string{"Asia/Shanghai"}}, "")
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.Equal(t, "Asia/Tokyo", (<-headers).Get("X-Timezone"))
	body := localeTestBody([]string{"<environment_context><current_date>2001-01-01</current_date><timezone>Asia/Shanghai</timezone></environment_context>"}, nil)
	require.NoError(t, conn.WriteJSON(ctx, json.RawMessage(body)))
	client, ok := conn.(*coderOpenAIWSClientConn)
	require.True(t, ok)
	err = client.WriteFrame(ctx, coderws.MessageText, body)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		select {
		case data := <-received:
			require.Contains(t, string(data), "Asia/Tokyo")
			require.Contains(t, string(data), "2001-01-01")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	require.Len(t, probe.calls, 1)
}

// Observe actual HTTP/WS bytes, rather than only the service's pre-send digest.
func TestOpenAI409PhysicalTimezonePreservesReadableTask(t *testing.T) {
	original := localeTestBody([]string{"<environment_context><timezone>Asia/Shanghai</timezone></environment_context>", "keep the already completed tool result"}, nil)
	probe := &egressTimezoneProber{probe: func(context.Context, string) (*ProxyExitInfo, int64, error) {
		return &ProxyExitInfo{IP: "203.0.113.10", Timezone: "Asia/Tokyo"}, 0, nil
	}}
	svc := timezoneAutoService(true, true, probe)
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	for _, transport := range []string{"http", "ws"} {
		for _, preserved := range []bool{false, true} {
			t.Run(transport+"/preserved="+fmt.Sprint(preserved), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = svc.withRequestTimezoneEgress(ctx, account)
				if preserved {
					ctx = context.WithValue(ctx, openAIAgentRecoveryContextKey{}, &openAIAgentRecoveryRequest{omittedHistory: true})
				}
				received := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if transport == "http" {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						received <- body
						w.WriteHeader(http.StatusNoContent)
						return
					}
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.CloseNow() }()
					_, body, err := conn.Read(ctx)
					if err != nil {
						t.Error(err)
						return
					}
					received <- body
					<-ctx.Done()
				}))
				defer server.Close()
				if transport == "http" {
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(original))
					require.NoError(t, err)
					req.Header.Set("X-Timezone", "Asia/Shanghai")
					bound, err := BindOpenAIRequestTimezone(req, "")
					require.NoError(t, err)
					require.Equal(t, "Asia/Tokyo", bound.Header.Get("X-Timezone"))
					response, err := server.Client().Do(bound)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
				} else {
					conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
					require.NoError(t, err)
					defer func() { _ = conn.CloseNow() }()
					wrapped := &coderOpenAIWSClientConn{conn: conn, timezoneBinding: &requestTimezoneBinding{svc, account}, requestTimezone: "Asia/Tokyo"}
					require.NoError(t, wrapped.WriteFrame(ctx, coderws.MessageText, original))
				}
				select {
				case body := <-received:
					if preserved {
						require.JSONEq(t, string(original), string(body), "physical send must preserve the rebuilt task")
					} else {
						require.Contains(t, string(body), "Asia/Tokyo", "ordinary egress normalization remains active")
					}
				case <-ctx.Done():
					t.Fatal("provider did not receive a frame")
				}
				cancel()
			})
		}
	}
}
