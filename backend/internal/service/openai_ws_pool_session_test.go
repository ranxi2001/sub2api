//go:build unit && issue905pool

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The fake provider pins conversation identity at the HTTP upgrade, just like
// a stateful WS gateway. A payload carrying a new identity on the old socket
// is rejected. Check actual frames and completion, not just key equality.
func TestIssue905PoolSessionIsolation(t *testing.T) {
	for _, header := range []string{"session-id", "session_id", "thread-id", "thread_id", "conversation_id", "conversation-id"} {
		for _, capacity := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/capacity=%d", header, capacity), func(t *testing.T) {
				var dials atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					bound := r.Header.Get(header)
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						return
					}
					defer conn.CloseNow()
					dials.Add(1)
					for {
						_, raw, err := conn.Read(r.Context())
						if err != nil {
							return
						}
						answer := `{"type":"response.completed","response":{"id":"fixture-response","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture completed"}]}]}}`
						if gjson.GetBytes(raw, "client_metadata.identity").String() != bound {
							answer = `{"type":"error","status":400,"error":{"code":"fixture_identity_conflict"}}`
						}
						if conn.Write(r.Context(), coderws.MessageText, []byte(answer)) != nil {
							return
						}
					}
				}))
				defer upstream.Close()
				cfg := &config.Config{}
				cfg.Gateway.OpenAIWS.MaxConnsPerAccount = capacity
				cfg.Gateway.OpenAIWS.MaxIdlePerAccount = capacity
				pool := newOpenAIWSConnPool(cfg)
				defer pool.Close()
				account := &Account{ID: 905, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: capacity}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				req := openAIWSAcquireRequest{Account: account, WSURL: "ws" + strings.TrimPrefix(upstream.URL, "http"), Headers: http.Header{}}
				send := func(identity string) string {
					req.Headers.Set(header, identity)
					lease, err := pool.Acquire(ctx, req)
					require.NoError(t, err)
					defer lease.Release()
					require.NoError(t, lease.WriteJSONWithContextTimeout(ctx, map[string]any{"type": "response.create", "client_metadata": map[string]string{"identity": identity}}, time.Second))
					raw, err := lease.ReadMessage(time.Second)
					require.NoError(t, err)
					require.Equal(t, "response.completed", gjson.GetBytes(raw, "type").String(), "ISSUE905_POOL_REUSED_OTHER_SESSION: actual upstream saw the previous handshake identity")
					require.Equal(t, "fixture completed", gjson.GetBytes(raw, "response.output.0.content.0.text").String())
					return lease.ConnID()
				}
				first := send("fixture-A")
				require.Equal(t, first, send("fixture-A"), "same session keeps reusing its socket")
				second := send("fixture-B")
				require.NotEqual(t, first, second)
				require.Equal(t, second, send("fixture-B"))
				require.EqualValues(t, 2, dials.Load(), "two sessions, no generation retry")
			})
		}
	}
}

func TestIssue905PoolPreferredSessionFence(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(&openAIWSCountingDialer{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := openAIWSAcquireRequest{Account: &Account{ID: 906, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}, WSURL: "wss://fixture.invalid", Headers: http.Header{"Session_id": {"fixture-A"}}}
	first, err := pool.Acquire(ctx, req)
	require.NoError(t, err)
	id := first.ConnID()
	first.Release()
	req.PreferredConnID = id
	req.ForcePreferredConn = true
	req.Headers.Set("Session_id", "fixture-B")
	other, err := pool.Acquire(ctx, req)
	if other != nil {
		other.Release()
	}
	require.ErrorIs(t, err, errOpenAIWSPreferredConnUnavailable, "ISSUE905_PREFERRED_SESSION_FENCE: never migrate an opaque continuation onto another handshake")
	req.Headers.Set("Session_id", "fixture-A")
	same, err := pool.Acquire(ctx, req)
	require.NoError(t, err)
	same.Release()
	require.Equal(t, id, same.ConnID())
}
