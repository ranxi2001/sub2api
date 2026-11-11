//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var issue805AgentB = strings.ReplaceAll(issue715Agent, "fixture-task-reference-715", "fixture-task-produced-by-B")

// A network oracle supports all three real WS adapters. Cross-account
// acceptance is synthetic; it makes no claim about provider cryptography.
type issue805Oracle struct {
	mu              sync.Mutex
	ids             []string
	frames          [][]byte
	ws, http        int
	rejection       string
	rejectionParam  string
	rejectionStatus int
	disconnect      bool
}

func (u *issue805Oracle) response(r *http.Request, body []byte) []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	id := r.Header.Get("Authorization")
	u.ids = append(u.ids, id)
	u.frames = append(u.frames, append([]byte(nil), body...))
	if u.disconnect {
		return nil
	}
	if u.rejection != "" {
		return []byte(fmt.Sprintf(`{"type":"response.failed","response":{"id":"resp_805_rejected","status":"failed","error":{"code":%q,"param":%q,"message":"synthetic upstream rejected intact history"}}}`, u.rejection, u.rejectionParam))
	}
	output := "[" + issue715Agent + "]"
	if id == "Bearer fixture-ws-two" {
		output = "[" + issue805AgentB + `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"existing ledger balance 18"}]}]`
	}
	return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_805_%d","model":"gpt-5.1","status":"completed","error":null,"output":%s,"usage":{"input_tokens":5,"output_tokens":3}}}`, len(u.ids), output))
}

func (u *issue805Oracle) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			u.mu.Lock()
			u.http++
			u.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if response := u.response(r, body); response != nil {
				u.mu.Lock()
				status := u.rejectionStatus
				u.mu.Unlock()
				if status > 0 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = fmt.Fprintf(w, `{"error":%s}`, gjson.GetBytes(response, "response.error").Raw)
				} else {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", response)
				}
			}
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow() //nolint:errcheck // Fixture cleanup.
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			u.mu.Lock()
			u.ws++
			u.mu.Unlock()
			response := u.response(r, body)
			if response == nil || conn.Write(r.Context(), coderws.MessageText, response) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func issue805WSNode(t *testing.T, mode, upstream string, cache service.GatewayCache, disabled, limited bool) *openAIWSPassthroughHandlerHarness {
	t.Helper()
	return newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream,
		http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}},
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false"}, cache,
		func(key *service.APIKey) { key.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID, a.Priority = 1, 0
			a.Credentials["api_key"] = "fixture-ws-one"
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			issue805Policy(t, cfg, disabled)
			require.Equal(t, mode, a.ResolveOpenAIResponsesWebSocketV2Mode(cfg.Gateway.OpenAIWS.IngressModeDefault))
			other := *a
			other.ID, other.Priority = 2, 1
			other.Credentials = map[string]any{"api_key": "fixture-ws-two", "base_url": upstream}
			if limited {
				reset := time.Now().Add(time.Hour)
				a.RateLimitResetAt = &reset
			}
			*repo = &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, other}}}
		})
}

func TestIssue805WSCompletedHistoryAcrossNodesAndTurns(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			clients := issue805Redis(t)
			newCache := func() service.GatewayCache { return repository.NewGatewayCache(clients()) }
			u := &issue805Oracle{}
			srv := u.server(t)
			producer := issue805WSNode(t, mode, srv.URL, newCache(), false, false)
			issue715WSSend(t, producer.clientConn, issue715WSCreate("", false))
			first := issue715WSRead(t, producer.clientConn)
			require.Contains(t, string(first), "fixture-task-reference-715")
			probe := clients()
			require.Eventually(t, func() bool {
				owners := issue805Owners(t, probe)
				if len(owners) != 1 {
					return false
				}
				for _, raw := range owners {
					if gjson.Get(raw, "1").Int() != 1 {
						return false
					}
				}
				return true
			}, time.Second, 5*time.Millisecond)
			original := issue805Owners(t, probe)
			consumer := issue805WSNode(t, mode, srv.URL, newCache(), true, true)
			body := issue715WSCreate("", true)
			issue715WSSend(t, consumer.clientConn, body)
			answer := issue715WSRead(t, consumer.clientConn)
			require.Equal(t, "response.completed", gjson.GetBytes(answer, "type").String(), string(answer))
			require.Contains(t, string(answer), "existing ledger balance 18")
			require.Eventually(t, func() bool {
				owners := issue805Owners(t, probe)
				if len(owners) != 2 {
					return false
				}
				for key, raw := range owners {
					if old, ok := original[key]; ok {
						if raw != old {
							return false
						}
					} else if gjson.Get(raw, "1").Int() != 2 {
						return false
					}
				}
				return true
			}, time.Second, 5*time.Millisecond, "A must survive and the newly delivered output must commit real B")
			previous := gjson.GetBytes(answer, "response.id").String()
			mixed := strings.Replace(issue715WSCreate(previous, true), issue715Agent, issue715Agent+","+issue805AgentB, 1)
			issue715WSSend(t, consumer.clientConn, mixed)
			require.Equal(t, "response.completed", gjson.GetBytes(issue715WSRead(t, consumer.clientConn), "type").String())
			u.mu.Lock()
			ids, frames, httpCount, wsCount := append([]string(nil), u.ids...), append([][]byte(nil), u.frames...), u.http, u.ws
			u.mu.Unlock()
			require.Equal(t, []string{"Bearer fixture-ws-one", "Bearer fixture-ws-two", "Bearer fixture-ws-two"}, ids)
			for i, source := range []string{body, mixed} {
				require.JSONEq(t, gjson.Get(source, "input").Raw, gjson.GetBytes(frames[i+1], "input").Raw, "actual adapter send must preserve complete current history and tool results")
			}
			if mode == service.OpenAIWSIngressModeHTTPBridge {
				require.Equal(t, 3, httpCount)
				require.Zero(t, wsCount)
			} else {
				require.Equal(t, 3, wsCount)
				require.Zero(t, httpCount)
			}
			// Normal restart back to strict can reject mixed producers, but must
			// neither erase evidence nor pretend that A's history was made by B.
			strict := issue805WSNode(t, mode, srv.URL, newCache(), false, false)
			issue715WSSend(t, strict.clientConn, strings.Replace(mixed, fmt.Sprintf(`,"previous_response_id":%q`, previous), "", 1))
			issue805WSRefusal(t, strict.clientConn, "encrypted_agent_recovery_owner_conflict")
			u.mu.Lock()
			require.Len(t, u.ids, 3)
			u.mu.Unlock()
			for _, node := range []*openAIWSPassthroughHandlerHarness{strict, consumer, producer} {
				issue600Finish(t, node, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
			}
		})
	}
}

func issue805WSRefusal(t *testing.T, conn *coderws.Conn, expectedCode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	explicitRefusal := false
	for {
		_, event, err := conn.Read(ctx)
		if err != nil {
			require.NotErrorIs(t, err, context.DeadlineExceeded, "refusal must close promptly")
			require.True(t, explicitRefusal || coderws.CloseStatus(err) == coderws.StatusPolicyViolation,
				"local policy refusal needs %s or a policy-violation close; arbitrary EOF is not evidence: %v", expectedCode, err)
			return
		}
		require.Contains(t, []string{"error", "response.failed"}, gjson.GetBytes(event, "type").String(), string(event))
		explicitRefusal = explicitRefusal || gjson.GetBytes(event, "error.code").String() == expectedCode ||
			gjson.GetBytes(event, "response.error.code").String() == expectedCode
	}
}

// This oracle deliberately drops an in-flight upstream connection. EOF is an
// allowed transport outcome here, but proves no explicit local policy refusal.
// The caller separately asserts the exact sends and absence of warmup replay.
func issue805WSAmbiguousTermination(t *testing.T, conn *coderws.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for {
		_, event, err := conn.Read(ctx)
		if err != nil {
			require.NotErrorIs(t, err, context.DeadlineExceeded, "ambiguous turn must terminate promptly")
			return
		}
		require.Contains(t, []string{"error", "response.failed"}, gjson.GetBytes(event, "type").String(), string(event))
	}
}

func TestIssue805WSAmbiguousLaterTurnNeverReplaysWarmup(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			clients := issue805Redis(t)
			u := &issue805Oracle{}
			srv := u.server(t)
			h := issue805WSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), true, false)
			issue715WSSend(t, h.clientConn, issue715WSCreate("", false))
			require.Contains(t, string(issue715WSRead(t, h.clientConn)), "fixture-task-reference-715")
			u.mu.Lock()
			u.disconnect = true
			u.mu.Unlock()
			current := issue715WSCreate("", true)
			issue715WSSend(t, h.clientConn, current)
			issue805WSAmbiguousTermination(t, h.clientConn)
			issue600Finish(t, h, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
			u.mu.Lock()
			defer u.mu.Unlock()
			require.Len(t, u.frames, 2, "one warmup and one ambiguous current turn; never replay either")
			require.JSONEq(t, gjson.Get(current, "input").Raw, gjson.GetBytes(u.frames[1], "input").Raw)
		})
	}
}

func TestIssue805WSRejectsUnsafeHistoryBeforeNetworkSend(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		for _, state := range []string{"lost", "failed", "conflict", "foreign_pointer", "pointer_only"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				clients := issue805Redis(t)
				u := &issue805Oracle{}
				srv := u.server(t)
				cache := repository.NewGatewayCache(clients())
				h := issue805WSNode(t, mode, srv.URL, cache, true, false)
				issue715WSSend(t, h.clientConn, issue715WSCreate("", false))
				first := issue715WSRead(t, h.clientConn)
				require.Contains(t, string(first), "fixture-task-reference-715")
				probe := clients()
				require.Eventually(t, func() bool {
					owners := issue805Owners(t, probe)
					if len(owners) != 1 {
						return false
					}
					for _, raw := range owners {
						if gjson.Get(raw, "1").Int() != 1 {
							return false
						}
					}
					return true
				}, time.Second, 5*time.Millisecond)
				body := issue715WSCreate("", true)
				switch state {
				case "foreign_pointer", "pointer_only":
					require.NoError(t, service.NewOpenAIWSStateStore(cache).BindResponseAccount(context.Background(), 4301, "resp_foreign805", 2, time.Minute))
					body = issue715WSCreate("resp_foreign805", state != "pointer_only")
				default:
					for key := range issue805Owners(t, probe) {
						if state == "lost" {
							require.NoError(t, probe.Del(context.Background(), key).Err())
						} else {
							require.NoError(t, probe.Set(context.Background(), key, state, time.Minute).Err())
						}
					}
				}
				issue715WSSend(t, h.clientConn, body)
				code := "encrypted_agent_recovery_owner_unavailable"
				if state == "conflict" {
					code = "encrypted_agent_recovery_owner_conflict"
				}
				issue805WSRefusal(t, h.clientConn, code)
				issue600Finish(t, h, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
				u.mu.Lock()
				defer u.mu.Unlock()
				require.Len(t, u.frames, 1, "unsafe current history cannot be sent or replaced with the first warmup")
			})
		}
	}
}

func TestIssue805WSUpstreamRejectionNeverDeletesAndRetries(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			clients := issue805Redis(t)
			u := &issue805Oracle{}
			srv := u.server(t)
			producer := issue805WSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), false, false)
			issue715WSSend(t, producer.clientConn, issue715WSCreate("", false))
			require.Contains(t, string(issue715WSRead(t, producer.clientConn)), "fixture-task-reference-715")
			u.mu.Lock()
			u.rejection = "unsupported_encrypted_agent_message"
			u.mu.Unlock()
			consumer := issue805WSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), true, true)
			body := issue715WSCreate("", true)
			issue715WSSend(t, consumer.clientConn, body)
			answer := issue715WSRead(t, consumer.clientConn)
			require.Equal(t, "response.failed", gjson.GetBytes(answer, "type").String(), string(answer))
			require.Contains(t, string(answer), "unsupported_encrypted_agent_message")
			for _, node := range []*openAIWSPassthroughHandlerHarness{consumer, producer} {
				issue600Finish(t, node, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
			}
			u.mu.Lock()
			defer u.mu.Unlock()
			require.Equal(t, []string{"Bearer fixture-ws-one", "Bearer fixture-ws-two"}, u.ids)
			require.JSONEq(t, gjson.Get(body, "input").Raw, gjson.GetBytes(u.frames[1], "input").Raw)
		})
	}
}

func TestIssue805WSNewBOutputWaitsForCrossNodeCommit(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			clients := issue805Redis(t)
			newCache := func() service.GatewayCache { return repository.NewGatewayCache(clients()) }
			u := &issue805Oracle{}
			srv := u.server(t)
			originalNode := issue805WSNode(t, mode, srv.URL, newCache(), false, false)
			issue715WSSend(t, originalNode.clientConn, issue715WSCreate("", false))
			require.Contains(t, string(issue715WSRead(t, originalNode.clientConn)), "fixture-task-reference-715")
			probe := clients()
			require.Eventually(t, func() bool {
				owners := issue805Owners(t, probe)
				if len(owners) != 1 {
					return false
				}
				for _, raw := range owners {
					if gjson.Get(raw, "1").Int() != 1 {
						return false
					}
				}
				return true
			}, time.Second, 5*time.Millisecond)
			original := issue805Owners(t, probe)
			base := newCache()
			delivery, ok := base.(issue715WSDeliveryCache)
			require.True(t, ok)
			barrier := &issue715WSBarrierCache{GatewayCache: base, issue715WSDeliveryCache: delivery, entered: make(chan struct{}), release: make(chan struct{}), observed: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
			defer release()
			producer := issue805WSNode(t, mode, srv.URL, barrier, true, true)
			issue715WSSend(t, producer.clientConn, issue715WSCreate("", true))
			answer := issue715WSRead(t, producer.clientConn)
			require.Contains(t, string(answer), "fixture-task-produced-by-B")
			issue715Await(t, barrier.entered) // Finish is reached only after the actual downstream write.
			pending := issue805Owners(t, probe)
			require.Len(t, pending, 2)
			for key, raw := range pending {
				if old, found := original[key]; found {
					require.Equal(t, old, raw)
				} else {
					require.True(t, strings.HasPrefix(raw, "pending:"), raw)
					require.Contains(t, raw, `[1,2,`)
				}
			}
			readerBase := newCache()
			readerDelivery, ok := readerBase.(issue715WSDeliveryCache)
			require.True(t, ok)
			observed := make(chan struct{})
			var observeOnce sync.Once
			reader := &issue805ReadObserver{GatewayCache: readerBase, issue715WSDeliveryCache: readerDelivery, seen: observed, once: &observeOnce}
			consumer := issue805WSNode(t, mode, srv.URL, reader, true, true)
			mixed := strings.Replace(issue715WSCreate("", true), issue715Agent, issue715Agent+","+issue805AgentB, 1)
			issue715WSSend(t, consumer.clientConn, mixed)
			issue715Await(t, observed)
			u.mu.Lock()
			before := len(u.frames)
			u.mu.Unlock()
			require.Equal(t, 2, before, "client-visible B output cannot be consumed on another node before commit")
			release()
			require.Equal(t, "response.completed", gjson.GetBytes(issue715WSRead(t, consumer.clientConn), "type").String())
			for _, node := range []*openAIWSPassthroughHandlerHarness{consumer, producer, originalNode} {
				issue600Finish(t, node, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
			}
			u.mu.Lock()
			defer u.mu.Unlock()
			require.Equal(t, []string{"Bearer fixture-ws-one", "Bearer fixture-ws-two", "Bearer fixture-ws-two"}, u.ids)
			require.JSONEq(t, gjson.Get(mixed, "input").Raw, gjson.GetBytes(u.frames[2], "input").Raw)
			for key, raw := range issue805Owners(t, probe) {
				if old, found := original[key]; found {
					require.Equal(t, old, raw)
				} else {
					require.EqualValues(t, 2, gjson.Get(raw, "1").Int(), "new output must retain real B after commit")
				}
			}
		})
	}
}

func TestIssue805WSHTTPBridgeHTTPRejectionReportsOriginalCode(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, 520} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			clients := issue805Redis(t)
			u := &issue805Oracle{}
			srv := u.server(t)
			producer := issue805WSNode(t, service.OpenAIWSIngressModeHTTPBridge, srv.URL, repository.NewGatewayCache(clients()), false, false)
			issue715WSSend(t, producer.clientConn, issue715WSCreate("", false))
			require.Contains(t, string(issue715WSRead(t, producer.clientConn)), "fixture-task-reference-715")
			u.mu.Lock()
			u.rejection, u.rejectionStatus = "unsupported_encrypted_agent_message", status
			u.mu.Unlock()
			consumer := issue805WSNode(t, service.OpenAIWSIngressModeHTTPBridge, srv.URL, repository.NewGatewayCache(clients()), true, true)
			body := issue715WSCreate("", true)
			issue715WSSend(t, consumer.clientConn, body)
			answer := issue715WSRead(t, consumer.clientConn)
			require.Equal(t, "error", gjson.GetBytes(answer, "type").String(), string(answer))
			require.Equal(t, "unsupported_encrypted_agent_message", gjson.GetBytes(answer, "error.code").String())
			require.EqualValues(t, status, gjson.GetBytes(answer, "status").Int())
			for _, node := range []*openAIWSPassthroughHandlerHarness{consumer, producer} {
				issue600Finish(t, node, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
			}
			u.mu.Lock()
			defer u.mu.Unlock()
			require.Equal(t, []string{"Bearer fixture-ws-one", "Bearer fixture-ws-two"}, u.ids)
			require.JSONEq(t, gjson.Get(body, "input").Raw, gjson.GetBytes(u.frames[1], "input").Raw)
		})
	}
}
