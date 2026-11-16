//go:build unit && plaintextowner && followupcapture

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSFollowupErrorCapture(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			response := func() []byte {
				if calls.Add(1) == 1 {
					return []byte(`{"type":"response.completed","response":{"id":"resp_capture_first","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first success"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`)
				}
				return []byte(`{"type":"response.failed","response":{"id":"resp_capture_second","status":"failed","error":{"code":"invalid_request_error","message":"fixture second turn rejected"}}}`)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					_, _ = io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\n", response())
					return
				}
				conn, e := coderws.Accept(w, r, nil)
				if e != nil {
					t.Error(e)
					return
				}
				defer conn.CloseNow()
				for {
					_, _, e := conn.Read(r.Context())
					if e != nil {
						return
					}
					if conn.Write(r.Context(), coderws.MessageText, response()) != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream.URL, http.Header{}, map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false"}, nil, func(key *service.APIKey) {
				// This fixture tests an upstream follow-up rejection. Opaque
				// response pointers require permission even with tiny local input.
				key.AllowLongContext = true
			}, func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, _ *service.AccountRepository) {
				a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
				cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			})
			issue600Finish(t, h, http.Header{}) // close constructor's idle socket; reuse real handler dependencies
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := &recoveryCaptureStore{tasks: map[string][]byte{}, records: map[string][]byte{}}
			directory := t.TempDir()
			manager, e := requestcapture.New(store, directory, requestcapture.Config{Enabled: true, QuotaMiB: 8, RetentionDays: 1})
			require.NoError(t, e)
			defer manager.Close()
			task, e := manager.Create(ctx, requestcapture.CreateTask{TargetType: "user", TargetID: h.apiKey.UserID, DurationMinutes: 1}, "fixture")
			require.NoError(t, e)
			observedSession := make(chan *requestcapture.Session, 1)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), h.apiKey)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: h.apiKey.UserID, Concurrency: 1})
				c.Next()
			})
			router.Use(middleware.RequestCapture(manager))
			router.Use(func(c *gin.Context) {
				observedSession <- requestcapture.FromContext(c.Request.Context())
				if requestcapture.FromContext(c.Request.Context()) == nil {
					t.Error("fixture capture session was not admitted")
				}
				c.Next()
			})
			router.GET("/v1/responses", h.handler.ResponsesWebSocket)
			server := httptest.NewServer(router)
			defer server.Close()
			conn, _, e := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: http.Header{"Session_id": {"fixture-capture"}, "Thread-Id": {"fixture-capture-thread"}}})
			require.NoError(t, e)
			defer conn.CloseNow()
			session := <-observedSession
			issue715WSSend(t, conn, `{"type":"response.create","model":"gpt-5.1","store":true,"input":[{"role":"user","content":"fixture first"}]}`)
			require.Equal(t, "response.completed", gjson.GetBytes(issue715WSRead(t, conn), "type").String())
			second := `{"type":"response.create","model":"gpt-5.1","store":true,"previous_response_id":"resp_capture_first","input":[{"role":"user","content":"fixture second"}]}`
			issue715WSSend(t, conn, second)
			terminal := issue715WSRead(t, conn)
			require.Equal(t, "response.failed", gjson.GetBytes(terminal, "type").String())
			require.Equal(t, "fixture second turn rejected", gjson.GetBytes(terminal, "response.error.message").String(), "fixture must reach the fake upstream: %s", terminal)
			require.Equal(t, int64(2), calls.Load())
			_ = conn.CloseNow()
			require.Eventually(t, func() bool { return manager.Stats().ActiveRequests == 0 }, 3*time.Second, 5*time.Millisecond)
			records, e := manager.Records(ctx, task.ID, "", true, 10, 0)
			require.NoError(t, e)
			t.Logf("actual capture lifecycle: %+v", requestcapture.FollowupTestState(session))
			taskState, taskErr := manager.Task(ctx, task.ID)
			require.NoError(t, taskErr)
			require.Len(t, records, 1, "FOLLOWUP_ERROR_CAPTURE_MISSING: only second failed turn must be retained; stats=%+v task=%+v", manager.Stats(), taskState)
			record := records[0]
			require.Equal(t, 2, record.Turn, "FOLLOWUP_ERROR_CAPTURE_WRONG_TURN: the second response must not be attributed to the first turn")
			require.True(t, record.IsError)
			require.False(t, record.Partial, record.Reason)
			parts := map[string][]byte{}
			for _, part := range record.Parts {
				body, e := os.ReadFile(filepath.Join(directory, task.ID, record.ID, part.Name))
				require.NoError(t, e)
				parts[part.Stage] = body
			}
			require.Len(t, parts, 4)
			require.JSONEq(t, second, string(parts["client_request"]))
			require.Contains(t, string(parts["upstream_request"]), "fixture second")
			require.Contains(t, string(parts["client_response"]), "fixture second turn rejected")
			require.NotContains(t, string(parts["client_response"]), "first success")
			require.Equal(t, int64(2), calls.Load())
		})
	}
}
