//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type tempUnschedWSForwardRepo struct {
	openAIWSRateLimitSignalRepo
	modelReasons []string
	errors       int
	scheduling   []bool
}

func (r *tempUnschedWSForwardRepo) SetModelRateLimit(_ context.Context, _ int64, _ string, _ time.Time, reason ...string) error {
	r.modelReasons = append(r.modelReasons, strings.Join(reason, ""))
	return nil
}

func (r *tempUnschedWSForwardRepo) SetError(context.Context, int64, string) error {
	r.errors++
	return nil
}

func (r *tempUnschedWSForwardRepo) SetSchedulable(_ context.Context, _ int64, value bool) error {
	r.scheduling = append(r.scheduling, value)
	return nil
}

type tempUnschedWSForwardCounter struct {
	inner *memoryTempUnschedTriggerCounter
	hits  int
}

func (c *tempUnschedWSForwardCounter) Increment(ctx context.Context, key string, window int) (int64, string, error) {
	c.hits++
	return c.inner.Increment(ctx, key, window)
}

func (c *tempUnschedWSForwardCounter) Reset(ctx context.Context, key, epoch string) error {
	return c.inner.Reset(ctx, key, epoch)
}

// Both cases enter a public gateway forwarding method and send a real upstream
// output delta before semantic 429. No phase flag or side-effect helper is called
// by the fixture. The before/after controls share the identical transport.
func TestTempUnschedWSForwardAfterOutputSemantic429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, entry := range []string{"v2", "ingress"} {
		for _, timing := range []string{TempUnschedStreamBefore, TempUnschedStreamAfter} {
			t.Run(entry+"/"+timing, func(t *testing.T) {
				var peers atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					var request map[string]any
					if err := conn.ReadJSON(&request); err != nil {
						t.Error(err)
						return
					}
					peers.Add(1)
					for _, frame := range []string{
						`{"type":"response.output_text.delta","response_id":"resp_phase_fixture","delta":"visible-token"}`,
						`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"fixture rate limit reached"}}`,
					} {
						if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
							t.Error(err)
							return
						}
					}
				}))
				defer upstream.Close()
				cfg := newOpenAIWSV2TestConfig()
				cfg.Security.URLAllowlist.Enabled = false
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				account := tempUnschedTimingAccount(map[string]any{
					"error_code": float64(429), "keywords": []any{"rate_limit_exceeded"},
					"scope": "model", "duration_minutes": float64(5), "stream_timing": timing,
					"trigger_count": float64(3), "trigger_window_minutes": float64(5),
				})
				account.Concurrency = 1
				account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
				for key, value := range openAIWSQuotaTestCredentials(AccountTypeAPIKey, upstream.URL) {
					account.Credentials[key] = value
				}
				repo := &tempUnschedWSForwardRepo{openAIWSRateLimitSignalRepo: openAIWSRateLimitSignalRepo{
					stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*account}},
				}}
				rates := NewRateLimitService(repo, nil, cfg, nil, nil)
				counter := &tempUnschedWSForwardCounter{inner: newMemoryTempUnschedTriggerCounter(time.Now)}
				rates.SetTempUnschedTriggerCounter(counter)
				httpFallback := &httpUpstreamRecorder{}
				pool := newOpenAIWSConnPool(cfg)
				defer pool.Close()
				pool.setClientDialerForTest(&openAIWSQuotaLocalDialer{url: "ws" + strings.TrimPrefix(upstream.URL, "http")})
				gateway := &OpenAIGatewayService{accountRepo: repo, rateLimitService: rates, httpUpstream: httpFallback,
					cache: &stubGatewayCache{}, cfg: cfg, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
					toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
				for attempt := 1; attempt <= 3; attempt++ {
					if entry == "v2" {
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
						c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
						_, err := gateway.Forward(t.Context(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"fixture"}`))
						require.Error(t, err)
						require.Contains(t, recorder.Body.String(), "visible-token", "Forward must deliver output before semantic429")
						require.Contains(t, recorder.Body.String(), "rate_limit_exceeded")
					} else {
						tempUnschedWSForwardIngress(t, gateway, account)
					}
					wantHits, wantCooldowns := 0, 0
					if timing == TempUnschedStreamAfter {
						wantHits = attempt
						if attempt == 3 {
							wantCooldowns = 1
						}
					}
					require.Equal(t, wantHits, counter.hits, "semantic429 must respect the real output phase and count each response once")
					require.Len(t, repo.modelReasons, wantCooldowns, "pending matches cannot cool before the third independent failure")
					require.Zero(t, repo.errors, "pending model failures cannot permanently disable the account")
					require.Empty(t, repo.scheduling, "semantic429 cannot change permanent scheduling for an API key")
					require.EqualValues(t, attempt, peers.Load(), "each attempt must reach the real upstream")
					require.Nil(t, httpFallback.lastReq, "the fixture must stay on its WebSocket forwarding entry")
				}
			})
		}
	}
}

func tempUnschedWSForwardIngress(t *testing.T, gateway *OpenAIGatewayService, account *Account) {
	t.Helper()
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		_, first, err := conn.Read(ctx)
		if err != nil {
			done <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(ctx)
		c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
		done <- gateway.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "fictional-key", first, nil)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"fixture"}`)))
	_, output, err := client.Read(ctx)
	require.NoError(t, err)
	require.Contains(t, string(output), "visible-token", "ingress must deliver output before semantic429")
	_, failure, err := client.Read(ctx)
	require.NoError(t, err)
	require.Contains(t, string(failure), "rate_limit_exceeded")
	client.CloseNow()
	select {
	case err := <-done:
		require.NotErrorIs(t, err, context.DeadlineExceeded)
	case <-ctx.Done():
		t.Fatal("ingress forwarding did not terminate after the client closed")
	}
}
