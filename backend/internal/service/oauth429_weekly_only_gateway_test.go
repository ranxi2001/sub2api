//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := weekly429Headers("99", "0", false)
			h.Set("x-codex-secondary-window-minutes", "0")
			if tc.name == "body_only_no_percentage" || tc.cached {
				h = http.Header{}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			repo := &weeklyOnlyWSRepo{}
			rates := NewRateLimitService(repo, nil, cfg, nil, nil)
			upstream := &httpUpstreamRecorder{}
			gateway := &OpenAIGatewayService{accountRepo: repo, rateLimitService: rates, httpUpstream: upstream, cache: &stubGatewayCache{}, cfg: cfg, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector()}
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			pool.setClientDialerForTest(&openAIWSQuotaLocalDialer{url: "ws" + strings.TrimPrefix(server.URL, "http")})
			gateway.openaiWSPool = pool
			rates.SetAccountRuntimeBlocker(gateway)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
			c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
			_, err := gateway.Forward(t.Context(), c, a, []byte(`{"model":"gpt-5.1","stream":false,"input":"fictional test"}`))
			require.Error(t, err)
			require.Equal(t, 429, rec.Code)
			require.Nil(t, upstream.lastReq, "WS rejection must not fall back to HTTP")
			require.Equal(t, tc.want, len(repo.writes) > 0, "WS Forward must use weekly-only permanent scheduling policy")
			if tc.want {
				require.Equal(t, []bool{false}, repo.writes)
			}
		})
	}
}
