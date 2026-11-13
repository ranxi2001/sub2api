//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestOAuth429WeeklyOnlyHTTPForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, weekly  string
				reverse, want bool
			}{
				{"primary_zero_neighbor", "99", false, true},
				{"secondary_zero_neighbor", "99", true, true},
				{"weekly_equality", "98", false, false},
				{"5h_only_100", "", false, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					h := weekly429Headers(tc.weekly, "0", tc.reverse)
					short := "secondary"
					if tc.reverse {
						short = "primary"
					}
					h.Set("x-codex-"+short+"-window-minutes", "0")
					if tc.name == "5h_only_100" {
						h = http.Header{}
						h.Set("x-codex-primary-window-minutes", "300")
						h.Set("x-codex-primary-used-percent", "100")
					}
					h.Set("Content-Type", "application/json")
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"usage_limit_reached","message":"fictional quota rejection","resets_in_seconds":7200}}`))}}
					repo := &weeklyOnly429Repo{}
					cfg := &config.Config{}
					rates := NewRateLimitService(repo, nil, cfg, nil, nil)
					gateway := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rates, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector()}
					rates.SetAccountRuntimeBlocker(gateway)
					a := scheduling429Account()
					a.Status = StatusActive
					a.Concurrency = 1
					a.Credentials = map[string]any{"access_token": "fictional-oauth-token", "chatgpt_account_id": "fictional-owner"}
					a.Extra["openai_passthrough"] = passthrough
					a.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
					delete(a.Extra, "codex_7d_used_percent")
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
					_, err := gateway.Forward(t.Context(), c, a, []byte(`{"model":"gpt-5.2","stream":true,"input":"fictional test"}`))
					require.Error(t, err)
					require.NotNil(t, upstream.lastReq, "must reach the actual HTTP transport boundary")
					require.Equal(t, tc.want, len(repo.ids) > 0, "HTTP Forward must use weekly-only permanent scheduling policy")
					if tc.want {
						require.Equal(t, []bool{false}, repo.values)
					}
				})
			}
		})
	}
}

type weeklyOnlyWSRepo struct {
	openAIWSRateLimitSignalRepo
	writes []bool
}

func (r *weeklyOnlyWSRepo) SetSchedulable(_ context.Context, _ int64, value bool) error {
	r.writes = append(r.writes, value)
	return nil
}

// A real loopback upstream handshake/error frame enters via Forward. These are
// gateway entry-point tests, not direct calls to the shared side-effect helper.
func TestOAuth429WeeklyOnlyWSForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                string
		frame, cached, want bool
	}{
		{"handshake_weekly_zero_neighbor", false, false, true},
		{"frame_with_weekly_handshake", true, false, true},
		{"body_only_no_percentage", true, false, false},
		{"frame_with_explicit_cache", true, true, true},
		{"frame_low_overrides_high_cache", true, true, false},
		{"frame_invalid_overrides_high_cache", true, true, false},
		{"frame_elapsed_handshake_reset", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := weekly429Headers("99", "0", false)
			h.Set("x-codex-secondary-window-minutes", "0")
			if tc.name == "body_only_no_percentage" || tc.name == "frame_with_explicit_cache" {
				h = http.Header{}
			}
			if tc.name == "frame_low_overrides_high_cache" {
				h.Set("x-codex-primary-used-percent", "20")
			}
			if tc.name == "frame_invalid_overrides_high_cache" {
				h.Set("x-codex-primary-used-percent", "invalid")
			}
			// A long handshake reset must not leak into ordinary-model temporary cooldown.
			h.Set("x-codex-primary-reset-after-seconds", "500000")
			if tc.name == "body_only_no_percentage" || tc.name == "frame_with_explicit_cache" {
				h = http.Header{}
			}
			if tc.name == "frame_elapsed_handshake_reset" {
				h.Set("x-codex-primary-reset-after-seconds", "1")
			}
			var poolRef atomic.Pointer[openAIWSConnPool]
			var peers atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peers.Add(1)
				if !tc.frame {
					for k, v := range h {
						w.Header()[k] = v
					}
					w.WriteHeader(429)
					_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":7200}}`))
					return
				}
				upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
				conn, err := upgrader.Upgrade(w, r, h)
				if err != nil {
					t.Errorf("upgrade: %v", err)
					return
				}
				defer conn.Close()
				var request map[string]any
				if err := conn.ReadJSON(&request); err != nil {
					t.Errorf("read: %v", err)
					return
				}
				if tc.name == "frame_elapsed_handshake_reset" {
					// Simulate elapsed connection time after the actual peer read,
					// before the error frame; no sleep or alternate helper path.
					ap := poolRef.Load().getOrCreateAccountPool(42)
					ap.mu.Lock()
					for _, pooled := range ap.conns {
						pooled.createdAtNano.Store(time.Now().Add(-time.Minute).UnixNano())
					}
					ap.mu.Unlock()
				}
				_ = conn.WriteJSON(map[string]any{"type": "error", "error": map[string]any{"type": "usage_limit_reached", "code": "rate_limit_exceeded", "message": "fictional quota rejection", "limit_window_minutes": 10080, "resets_in_seconds": 7200}})
			}))
			defer server.Close()
			cfg := newOpenAIWSV2TestConfig()
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			a := scheduling429Account()
			a.Status = StatusActive
			a.Concurrency = 1
			a.Credentials = openAIWSQuotaTestCredentials(AccountTypeOAuth, server.URL)
			a.Extra["responses_websockets_v2_enabled"] = true
			if tc.cached {
				a.Extra["codex_7d_window_minutes"] = 10080
			} else {
				delete(a.Extra, "codex_7d_used_percent")
			}
			repo := &weeklyOnlyWSRepo{openAIWSRateLimitSignalRepo: openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*a}}}}
			rates := NewRateLimitService(repo, nil, cfg, nil, nil)
			upstream := &httpUpstreamRecorder{}
			gateway := &OpenAIGatewayService{accountRepo: repo, rateLimitService: rates, httpUpstream: upstream, cache: &stubGatewayCache{}, cfg: cfg, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector()}
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			pool.setClientDialerForTest(&openAIWSQuotaLocalDialer{url: "ws" + strings.TrimPrefix(server.URL, "http")})
			gateway.openaiWSPool = pool
			poolRef.Store(pool)
			rates.SetAccountRuntimeBlocker(gateway)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
			c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
			_, err := gateway.Forward(t.Context(), c, a, []byte(`{"model":"gpt-5.1","stream":false,"input":"fictional test"}`))
			require.Error(t, err)
			require.Equal(t, 429, rec.Code)
			require.Positive(t, peers.Load(), "must observe the actual upstream peer")
			if tc.frame {
				require.NotEmpty(t, repo.rateLimitCalls)
				require.WithinDuration(t, time.Now().Add(2*time.Hour), repo.rateLimitCalls[len(repo.rateLimitCalls)-1], 10*time.Second, "temporary cooldown must use the event reset, not handshake quota")
			}
			require.Nil(t, upstream.lastReq, "WS rejection must not fall back to HTTP")
			require.Equal(t, tc.want, len(repo.writes) > 0, "WS Forward must use weekly-only permanent scheduling policy")
			if tc.want {
				require.Equal(t, []bool{false}, repo.writes)
			}
		})
	}
}

func TestOAuth429WeeklyOnlyWSClientIngress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	h := weekly429Headers("99", "0", true)
	h.Set("x-codex-primary-window-minutes", "0")
	capture := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"error","error":{"type":"usage_limit_reached","code":"rate_limit_exceeded","message":"fictional rejection","resets_in_seconds":7200}}`)}}
	dialer := &openAIWSCaptureDialer{conn: capture, handshake: h}
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(dialer)
	a := scheduling429Account()
	a.Status = StatusActive
	a.Concurrency = 1
	a.Credentials = openAIWSQuotaTestCredentials(AccountTypeOAuth, "")
	a.Extra["responses_websockets_v2_enabled"] = true
	delete(a.Extra, "codex_7d_used_percent")
	repo := &weeklyOnlyWSRepo{openAIWSRateLimitSignalRepo: openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*a}}}}
	rates := NewRateLimitService(repo, nil, cfg, nil, nil)
	gateway := &OpenAIGatewayService{accountRepo: repo, rateLimitService: rates, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, cfg: cfg, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	rates.SetAccountRuntimeBlocker(gateway)
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, first, err := conn.Read(ctx)
		if err != nil {
			done <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(ctx)
		c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
		done <- gateway.ProxyResponsesWebSocketFromClient(ctx, c, conn, a, "fictional-token", first, nil)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`)))
	select {
	case err := <-done:
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.Equal(t, 429, failover.StatusCode)
		require.Positive(t, dialer.DialCount(), "client ingress must reach the upstream adapter")
		require.Equal(t, []bool{false}, repo.writes, "client WebSocket ingress must preserve explicit secondary weekly evidence")
	case <-ctx.Done():
		t.Fatal("client ingress did not finish")
	}
}

type weeklyOnlyLoopbackHTTP struct {
	client *http.Client
	target string
}

func (u *weeklyOnlyLoopbackHTTP) Do(request *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	local := request.Clone(request.Context())
	parsed, err := url.Parse(u.target)
	if err != nil {
		return nil, err
	}
	local.URL.Scheme, local.URL.Host = parsed.Scheme, parsed.Host
	local.Host = parsed.Host
	return u.client.Do(local)
}

func (u *weeklyOnlyLoopbackHTTP) DoWithTLS(request *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(request, proxy, id, concurrency)
}

func TestOAuth429WeeklyOnlyHTTPStreamExpiredObservation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"native_stream", "passthrough_stream", "native_buffered", "passthrough_buffered", "ws_http_bridge"} {
		t.Run(mode, func(t *testing.T) {
			var peers atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peers.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("x-codex-primary-window-minutes", "10080")
				w.Header().Set("x-codex-primary-used-percent", "99")
				w.Header().Set("x-codex-primary-reset-after-seconds", "1")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, ": fictional keepalive\n\n")
				w.(http.Flusher).Flush()
				// A real HTTP transport receives headers before this reset elapses.
				time.Sleep(1500 * time.Millisecond)
				_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"fictional-response\",\"error\":{\"type\":\"usage_limit_reached\",\"code\":\"rate_limit_exceeded\",\"message\":\"fictional quota rejection\",\"resets_in_seconds\":7200}}}\n\n")
			}))
			defer server.Close()
			a := scheduling429Account()
			a.Status, a.Concurrency = StatusActive, 1
			a.Credentials = map[string]any{"access_token": "fictional-token", "chatgpt_account_id": "fictional-owner"}
			a.Extra["codex_7d_used_percent"] = 20.0
			a.Extra["openai_passthrough"] = strings.HasPrefix(mode, "passthrough")
			a.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
			repo := &weeklyOnly429Repo{}
			cfg := &config.Config{}
			rates := NewRateLimitService(repo, nil, cfg, nil, nil)
			gateway := &OpenAIGatewayService{cfg: cfg, httpUpstream: &weeklyOnlyLoopbackHTTP{client: server.Client(), target: server.URL}, rateLimitService: rates, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector()}
			rates.SetAccountRuntimeBlocker(gateway)
			gateway.openaiOAuth429RetryStartedAt.Store(a.ID, time.Now().Add(-openAIOAuth429RetryWindow-time.Second))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
			var err error
			if mode == "ws_http_bridge" {
				payload := []byte(`{"type":"response.create","model":"gpt-5.2","stream":true,"input":"fictional test"}`)
				_, err = gateway.proxyOpenAIWSHTTPBridgeTurn(t.Context(), c, a, "fictional-token", payload, len(payload), "gpt-5.2", "", "", "", "", 1, func([]byte) error { return nil })
			} else {
				stream := !strings.HasSuffix(mode, "buffered")
				_, err = gateway.Forward(t.Context(), c, a, []byte(fmt.Sprintf(`{"model":"gpt-5.2","stream":%t,"input":"fictional test"}`, stream)))
			}
			require.Error(t, err)
			require.Positive(t, peers.Load(), "must reach the real loopback HTTP peer")
			require.Empty(t, repo.ids, "expired HTTP response quota must not permanently disable on a later stream 429")
			require.Positive(t, repo.rateLimitedCalls, "semantic 429 must preserve temporary cooldown handling")
			require.Less(t, time.Until(repo.lastRateLimitedAt), time.Minute, "nested-event fallback cooldown must remain bounded")
		})
	}
}
