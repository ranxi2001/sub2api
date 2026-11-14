//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func issue842WSHistory(t *testing.T, variant string) (string, string) {
	t.Helper()
	body, want := issue842History(t, true, variant)
	var err error
	body, err = sjson.Set(body, "type", "response.create")
	require.NoError(t, err)
	body, err = sjson.Set(body, "client_metadata.thread_id", "fixture-ws715-thread")
	require.NoError(t, err)
	return body, want
}

func TestIssue842WSDeliveryAndUnknownOutcome(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		for _, state := range []string{"pending", "failed", "conflict", "foreign_pointer", "unknown"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				clients := issue805Redis(t)
				u := &issue805Oracle{}
				srv := u.server(t)
				producer := issue805WSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), false, false)
				issue715WSSend(t, producer.clientConn, issue715WSCreate("", false))
				require.Contains(t, string(issue715WSRead(t, producer.clientConn)), "fixture-task-reference-715")
				probe := clients()
				require.Eventually(t, func() bool {
					owners := issue805Owners(t, probe)
					if len(owners) != 1 {
						return false
					}
					for _, raw := range owners {
						if !issue805CompletedOwner(raw, 1) {
							return false
						}
					}
					return true
				}, time.Second, 5*time.Millisecond)
				original := issue805Owners(t, probe)
				body, want := issue842WSHistory(t, "reasoning_status")
				if state == "foreign_pointer" {
					body, _ = sjson.Set(body, "previous_response_id", "resp_fixture_foreign842")
					require.NoError(t, service.NewOpenAIWSStateStore(repository.NewGatewayCache(clients())).BindResponseAccount(context.Background(), 4301, "resp_fixture_foreign842", 1, time.Minute))
				} else if state != "unknown" {
					for key, raw := range original {
						value := state
						if state == "pending" {
							value = "pending:fixture842:" + raw
						}
						require.NoError(t, probe.Set(context.Background(), key, value, time.Minute).Err())
					}
				}
				if state == "unknown" {
					u.mu.Lock()
					u.disconnect = true
					u.mu.Unlock()
				}
				consumer := issue805WSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), true, true)
				issue715WSSend(t, consumer.clientConn, body)
				if state == "unknown" {
					issue805WSAmbiguousTermination(t, consumer.clientConn)
				} else {
					code := "encrypted_agent_recovery_owner_unavailable"
					if state == "pending" {
						code = "encrypted_agent_recovery_delivery_pending"
					} else if state == "conflict" {
						code = "encrypted_agent_recovery_owner_conflict"
					}
					issue805WSRefusal(t, consumer.clientConn, code)
				}
				for _, node := range []*openAIWSPassthroughHandlerHarness{consumer, producer} {
					issue600Finish(t, node, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
				}
				u.mu.Lock()
				defer u.mu.Unlock()
				if state == "unknown" {
					require.Equal(t, []string{"Bearer fixture-ws-one", "Bearer fixture-ws-two"}, u.ids, "unknown B write cannot replay warmup, generation or tool results")
					require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(u.frames[1], "input").Raw)
					require.Equal(t, original, issue805Owners(t, probe))
				} else {
					require.Len(t, u.frames, 1, "unsafe metadata-bearing history never reaches B")
				}
			})
		}
	}
}

func TestIssue842WSReplayMetadata(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		for _, variant := range []string{"reasoning_status", "reasoning_content", "plaintext_reasoning", "reasoning_id", "invalid_replay_id", "message_call_id"} {
			for _, known := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/known=%t", mode, variant, known), func(t *testing.T) {
					clients := issue805Redis(t)
					u := &issue805Oracle{}
					srv := u.server(t)
					original := map[string]string{}
					if known {
						producer := issue805WSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), false, false)
						issue715WSSend(t, producer.clientConn, issue715WSCreate("", false))
						require.Contains(t, string(issue715WSRead(t, producer.clientConn)), "fixture-task-reference-715")
						probe := clients()
						require.Eventually(t, func() bool {
							owners := issue805Owners(t, probe)
							if len(owners) != 1 {
								return false
							}
							for _, raw := range owners {
								if !issue805CompletedOwner(raw, 1) {
									return false
								}
							}
							return true
						}, time.Second, 5*time.Millisecond)
						original = issue805Owners(t, probe)
						issue600Finish(t, producer, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
					}
					consumer := issue805WSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), true, true)
					body, want := issue842WSHistory(t, variant)
					for turn := 0; turn < 2; turn++ {
						issue715WSSend(t, consumer.clientConn, body)
						ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
						_, answer, err := consumer.clientConn.Read(ctx)
						cancel()
						if err != nil {
							require.NotErrorIs(t, err, context.DeadlineExceeded)
							if coderws.CloseStatus(err) == coderws.StatusPolicyViolation {
								t.Log("Issue842 metadata policy refusal before frame")
							}
						}
						require.NoError(t, err, "Issue842 WS benign metadata must reach the actual adapter once")
						require.Equal(t, "response.completed", gjson.GetBytes(answer, "type").String(), "Issue842 WS benign metadata must reach the actual adapter once: %s", answer)
						require.Contains(t, string(answer), "existing ledger balance 18")
					}
					issue600Finish(t, consumer, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
					u.mu.Lock()
					defer u.mu.Unlock()
					offset := 0
					if known {
						offset = 1
					}
					ids := []string{"Bearer fixture-ws-two", "Bearer fixture-ws-two"}
					if known {
						ids = append([]string{"Bearer fixture-ws-one"}, ids...)
					}
					require.Equal(t, ids, u.ids, "no warmup or executed tool replay")
					for _, frame := range u.frames[offset:] {
						require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(frame, "input").Raw)
						require.Equal(t, "9007199254740993", gjson.GetBytes(frame, "input.1.sequence").Raw)
					}
					if mode == service.OpenAIWSIngressModeHTTPBridge {
						require.Equal(t, len(ids), u.http)
						require.Zero(t, u.ws)
					} else {
						require.Equal(t, len(ids), u.ws)
						require.Zero(t, u.http)
					}
					for key, raw := range original {
						require.Equal(t, raw, issue805Owners(t, clients())[key], "A remains the original producer")
					}
				})
			}
		}
	}
}
