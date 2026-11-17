//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Synthetic provider contract, not a reproduction of the private supplier's
// owner store. Exercise the real handler, two socket hops and Ops persistence.
func TestIssue905WSFollowup(t *testing.T) {
	for _, outcome := range []string{"completed", "error_event", "close_only", "unknown_close"} {
		for _, pointer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pointer=%v", outcome, pointer), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 8)
				const code = "encrypted_agent_recovery_owner_unavailable"
				const tools = `[{"type":"function","name":"fixture_once","parameters":{"type":"object","properties":{}}}]`
				const call = `{"type":"function_call","call_id":"call_fixture_once","name":"fixture_once","arguments":"{}","status":"completed"}`
				result := `{"type":"function_call_output","call_id":"call_fixture_once","output":"fixture receipt 1"}`
				input := "[" + result + "]"
				anchor := `,"previous_response_id":"resp_fixture_first"`
				if !pointer {
					input = "[" + call + "," + result + "]"
					anchor = ""
				}
				second := `{"type":"response.create","model":"gpt-5.1","tools":` + tools + `,"input":` + input + anchor + `}`
				var sends, connections atomic.Int32
				seen := make(chan []byte, 4)
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					connections.Add(1)
					defer conn.CloseNow()
					for {
						_, body, err := conn.Read(r.Context())
						if err != nil {
							return
						}
						n := sends.Add(1)
						seen <- body
						if n == 1 {
							if err := conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_fixture_first","status":"completed","output":[`+call+`],"usage":{"input_tokens":2,"output_tokens":1}}}`)); err != nil {
								return
							}
							continue
						}
						if n != 2 {
							t.Error("generation was replayed")
							return
						}
						if outcome == "completed" {
							_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_fixture_second","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture receipt accepted"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}`))
							_, _, _ = conn.Read(r.Context())
							return
						}
						if outcome == "error_event" {
							_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"error","status":409,"error":{"type":"invalid_request_error","code":"`+code+`","message":"fixture owner unavailable"}}`))
						}
						reason := code + ": fixture-private-close-canary"
						if outcome == "unknown_close" {
							reason = "fixture-private-close-canary"
						}
						_ = conn.Close(coderws.StatusPolicyViolation, reason)
						return
					}
				}))
				defer provider.Close()
				repo := &ingressRejectOpsRepo{}
				h := newOpenAIWSPassthroughHandlerHarnessWithAuth(t, provider.URL, http.Header{"Session_id": {"fixture-905"}, "Authorization": {"Bearer sk-handler-cyber-test"}},
					map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}, nil, nil,
					func(h *OpenAIGatewayHandler) {
						h.opsService = service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
						h.maxAccountSwitches = 2
					},
					func(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
						key.Status, key.AllowLongContext = service.StatusActive, true
						key.User.Concurrency = 1
						key.Group = &service.Group{ID: *key.GroupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
						return service.NewAPIKeyService(&wsRevalidationKeyRepoStub{current: key}, nil, nil, nil, nil, nil, nil, cfg)
					})
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","tools":`+tools+`,"input":"Run the fixture once"}`)))
				_, first, err := h.clientConn.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
				require.Equal(t, "call_fixture_once", gjson.GetBytes(first, "response.output.0.call_id").String())
				// Submit the synthetic result for the call actually delivered above.
				require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(second)))
				completed := false
				for {
					_, raw, err := h.clientConn.Read(ctx)
					if err != nil {
						require.Equal(t, coderws.StatusInternalError, coderws.CloseStatus(err))
						break
					}
					if outcome == "completed" {
						require.Equal(t, "response.completed", gjson.GetBytes(raw, "type").String())
						require.Equal(t, "fixture receipt accepted", gjson.GetBytes(raw, "response.output.0.content.0.text").String())
						completed = true
						_ = h.clientConn.CloseNow()
						break
					}
					require.Equal(t, code, gjson.GetBytes(raw, "error.code").String())
				}
				select {
				case <-h.handlerDone:
				case <-ctx.Done():
					t.Fatal("handler did not finish")
				}
				require.EqualValues(t, 2, sends.Load())
				require.EqualValues(t, 1, connections.Load())
				<-seen
				forwarded := <-seen
				require.JSONEq(t, input, gjson.GetBytes(forwarded, "input").Raw)
				require.JSONEq(t, tools, gjson.GetBytes(forwarded, "tools").Raw)
				require.Equal(t, gjson.Get(second, "previous_response_id").String(), gjson.GetBytes(forwarded, "previous_response_id").String())
				for len(opsErrorLogQueue) > 0 {
					flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
				}
				if outcome == "completed" {
					require.True(t, completed, "second turn must actually complete")
					require.Empty(t, repo.entries)
					return
				}
				require.Len(t, repo.entries, 1, "only the second turn fails; event + close must deduplicate")
				entry := repo.entries[0]
				require.Equal(t, "upstream_transport", entry.ErrorSource, "ISSUE905_UPSTREAM_SOURCE_LOST")
				require.Equal(t, "provider", entry.ErrorOwner)
				require.False(t, entry.IsBusinessLimited)
				require.Nil(t, entry.UpstreamStatusCode, "WS business status is not an HTTP status")
				expectedStatus, expectedCode := 409, code
				if outcome == "unknown_close" {
					expectedStatus, expectedCode = 502, "upstream_websocket_closed"
				}
				require.Equal(t, expectedStatus, entry.StatusCode)
				require.Equal(t, expectedCode, gjson.Get(entry.ErrorBody, "error.code").String())
				require.EqualValues(t, 2, gjson.Get(entry.ErrorBody, "error.turn").Int())
				require.EqualValues(t, 101, gjson.Get(entry.ErrorBody, "error.wire_status").Int())
				require.Equal(t, "unknown", gjson.Get(entry.ErrorBody, "error.execution_state").String())
				require.False(t, gjson.Get(entry.ErrorBody, "error.request_written").Exists(), "lack of current-turn write proof must not become false")
				require.NotContains(t, entry.ErrorBody, "fixture-private-close-canary")
			})
		}
	}
}
