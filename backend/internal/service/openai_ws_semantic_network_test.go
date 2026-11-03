package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Both socket hops are local. Client receipt is the barrier before advancing
// the observation clock and releasing the next upstream event.
func TestWSSemanticFirstTokenNetwork(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			var tick atomic.Int64
			origin := time.Now()
			ctx, cancel := context.WithTimeout(requesttiming.WithClock(context.Background(), func() time.Time { return origin.Add(time.Duration(tick.Load()) * time.Millisecond) }), 3*time.Second)
			defer cancel()
			commands := make(chan string)
			submissions := make(chan struct{}, 3)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				for {
					if _, _, err = ws.Read(ctx); err != nil {
						return
					}
					submissions <- struct{}{}
					for i := 0; i < 4; i++ {
						select {
						case e := <-commands:
							if ws.Write(ctx, coderws.MessageText, []byte(e)) != nil {
								return
							}
						case <-ctx.Done():
							return
						}
					}
				}
			}))
			defer upstream.Close()
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			account := passthroughLifecycleAccount()
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			account.Credentials["base_url"] = upstream.URL + "/v1"
			svc := &OpenAIGatewayService{cfg: cfg, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector()}
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			svc.openaiWSPool = pool
			results := make(chan *OpenAIForwardResult, 2)
			server, done := startPassthroughLifecycleServer(t, ctx, svc, account, &OpenAIWSIngressHooks{InitialTurnStartedAt: origin, AfterTurn: func(_ int, r *OpenAIForwardResult, e error) {
				if e == nil {
					results <- r
				}
			}})
			defer server.Close()
			client := dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"synthetic","input":"local"}`)
			defer client.CloseNow()
			for turn := 0; turn < 2; turn++ {
				if turn > 0 {
					tick.Store(1000)
					require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"synthetic","input":"local"}`)))
				}
				select {
				case <-submissions:
				case <-ctx.Done():
					t.Fatal("missing local upstream submission")
				}
				events := []string{fmt.Sprintf(`{"type":"response.created","response":{"id":"resp_%d"}}`, turn), `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, `{"type":"response.output_text.delta","delta":"later body"}`, fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_%d","usage":{"input_tokens":1,"output_tokens":1}}}`, turn)}
				for i, e := range events {
					if i == 1 {
						tick.Store(int64(turn*1000 + 100))
					}
					if i == 2 {
						tick.Store(int64(turn*1000 + 500))
					}
					if i == 3 {
						tick.Store(int64(turn*1000 + 900))
					}
					select {
					case commands <- e:
					case <-ctx.Done():
						t.Fatal("upstream event blocked")
					}
					_, payload, err := client.Read(ctx)
					require.NoError(t, err)
					require.JSONEq(t, e, string(payload))
				}
				select {
				case result := <-results:
					require.NotNil(t, result.FirstTokenMs)
					require.Equal(t, 100, *result.FirstTokenMs, "reasoning observed before body; first endpoint must survive later body and terminal")
					require.Equal(t, 900*time.Millisecond, result.Duration, "existing per-turn origin must remain unchanged")
				case <-ctx.Done():
					t.Fatal("missing turn result")
				}
			}
			select {
			case <-submissions:
				t.Fatal("unexpected replay")
			default:
			}
			_ = client.CloseNow()
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("forwarder did not stop")
			}
		})
	}
}
