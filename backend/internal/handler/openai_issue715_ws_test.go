//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A real socket oracle, not a provider encryption implementation. Only the
// producer account recognizes this reference and the complete task/tool context.
type issue715WSOracle struct {
	mu     sync.Mutex
	ids    []int64
	frames [][]byte
	ws     int
	http   int
	fail   bool
}

func (u *issue715WSOracle) response(r *http.Request, body []byte) []byte {
	id := int64(1)
	if r.Header.Get("Authorization") == "Bearer fixture-ws-two" {
		id = 2
	}
	u.mu.Lock()
	u.ids = append(u.ids, id)
	u.frames = append(u.frames, append([]byte(nil), body...))
	n, fail := len(u.ids), u.fail
	u.mu.Unlock()
	if fail {
		return nil // ambiguous execution/EOF: never automatic replay
	}
	input := gjson.GetBytes(body, "input").Raw
	output := `[` + issue715Agent + `,{"type":"function_call","call_id":"fc_715_once","name":"update_ledger","arguments":"{\"add\":7}"}]`
	if strings.Contains(input, "fixture-task-reference-715") {
		if id != 1 || !strings.Contains(input, "Keep ledger balance 11") || !strings.Contains(input, "ledger balance 18; applied once") {
			return []byte(`{"type":"response.failed","response":{"id":"resp_wrong_route","status":"failed","error":{"code":"unsupported_encrypted_agent_message"}}}`)
		}
		output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Same task: ledger balance 18; update already applied once"}]}]`
	}
	return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_ws715_%d","model":"gpt-5.1","status":"completed","error":null,"output":%s,"usage":{"input_tokens":5,"output_tokens":3}}}`, n, output))
}

func (u *issue715WSOracle) server(t *testing.T) *httptest.Server {
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
				_, _ = fmt.Fprintf(w, "data: %s\n\n", response)
			}
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
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

func issue715WSNode(t *testing.T, mode, upstream string, cache service.GatewayCache, drift bool) *openAIWSPassthroughHandlerHarness {
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
			require.Equal(t, mode, a.ResolveOpenAIResponsesWebSocketV2Mode(cfg.Gateway.OpenAIWS.IngressModeDefault), "fixture must execute its declared adapter")
			other := *a
			other.ID, other.Priority = 2, 1
			other.Credentials = map[string]any{"api_key": "fixture-ws-two", "base_url": upstream}
			if drift {
				a.Priority, other.Priority = 1, 0
			}
			*repo = &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, other}}}
		})
}

func issue715WSCreate(previous string, followup bool) string {
	input := `{"type":"message","role":"user","content":"Keep ledger balance 11; add 7 exactly once"}`
	if followup {
		input += `,` + issue715Agent + `,{"type":"function_call","name":"update_ledger","call_id":"fc_715_once","arguments":"{\"add\":7}"},{"type":"function_call_output","call_id":"fc_715_once","output":"ledger balance 18; applied once"},{"type":"message","role":"user","content":"Continue the same task"}`
	}
	prev := ""
	if previous != "" {
		prev = fmt.Sprintf(`,"previous_response_id":%q`, previous)
	}
	return fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","store":true,"client_metadata":{"thread_id":"fixture-ws715-thread"},"input":[%s]%s}`, input, prev)
}

func issue715WSRead(t *testing.T, conn *coderws.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for {
		_, event, err := conn.Read(ctx)
		require.NoError(t, err, "generated WS task must continue without rebuilding the session")
		kind := gjson.GetBytes(event, "type").String()
		if kind == "response.completed" || kind == "response.failed" || kind == "error" {
			return event
		}
	}
}

func issue715WSSend(t *testing.T, conn *coderws.Conn, body string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(body)))
}

type issue715WSDeliveryCache interface {
	issue715DeliveryOwners
	ReserveOpenAIAgentRecovery(context.Context, string, string, string, time.Duration) error
	FinishOpenAIAgentRecovery(context.Context, string, string, string, bool, time.Duration) error
}

type issue715WSBarrierCache struct {
	service.GatewayCache
	issue715WSDeliveryCache
	entered, release, observed chan struct{}
	once, readOnce             sync.Once
}

func (c *issue715WSBarrierCache) FinishOpenAIAgentRecovery(ctx context.Context, key, route, token string, commit bool, ttl time.Duration) error {
	if commit {
		c.once.Do(func() {
			close(c.entered)
			select {
			case <-c.release:
			case <-ctx.Done():
			}
		})
	}
	return c.issue715WSDeliveryCache.FinishOpenAIAgentRecovery(ctx, key, route, token, commit, ttl)
}

func (c *issue715WSBarrierCache) GetOpenAIAgentRecovery(ctx context.Context, key string) (string, error) {
	raw, err := c.issue715WSDeliveryCache.GetOpenAIAgentRecovery(ctx, key)
	if strings.HasPrefix(raw, "pending:") {
		c.readOnce.Do(func() { close(c.observed) })
	}
	return raw, err
}

func TestIssue715WSGeneratedTaskContinuation(t *testing.T) {
	issue715WSContinuation(t, false)
}

func TestIssue715WSFirstDeliveryBarrier(t *testing.T) {
	issue715WSContinuation(t, true)
}

func issue715WSContinuation(t *testing.T, useBarrier bool) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			for _, crossNode := range []bool{false, true} {
				t.Run(fmt.Sprint("cross-node=", crossNode), func(t *testing.T) {
					addr, db := os.Getenv("ISSUE715_REDIS_ADDR"), 0
					if addr == "" {
						addr = miniredis.RunT(t).Addr()
						t.Log("ownership adapter: production Lua on miniredis")
					} else {
						host, _, err := net.SplitHostPort(addr)
						require.NoError(t, err)
						require.Contains(t, []string{"127.0.0.1", "::1", "localhost"}, host)
						db = 15
						probe := redis.NewClient(&redis.Options{Addr: addr, DB: db})
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						info, err := probe.Info(ctx, "server").Result()
						require.NoError(t, err)
						require.Contains(t, info, "redis_version:", "real Redis required")
						require.NoError(t, probe.FlushDB(ctx).Err())
						t.Cleanup(func() {
							ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							defer cancel()
							_ = probe.FlushDB(ctx).Err()
							_ = probe.Close()
						})
						t.Log("ownership adapter: production Lua on real disposable Redis DB15")
					}
					newCache := func() service.GatewayCache {
						client := redis.NewClient(&redis.Options{Addr: addr, DB: db})
						t.Cleanup(func() { _ = client.Close() })
						return repository.NewGatewayCache(client)
					}
					base := newCache()
					transactional, ok := base.(issue715WSDeliveryCache)
					require.True(t, ok)
					barrier := &issue715WSBarrierCache{GatewayCache: base, issue715WSDeliveryCache: transactional, entered: make(chan struct{}), release: make(chan struct{}), observed: make(chan struct{})}
					var releaseOnce sync.Once
					release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
					defer release()
					u := &issue715WSOracle{}
					srv := u.server(t)
					producerCache := base
					if useBarrier {
						producerCache = barrier
					}
					producer := issue715WSNode(t, mode, srv.URL, producerCache, false)
					consumer := producer
					if crossNode {
						// A separate gateway, account repository, state store, pool and
						// Redis adapter; ordinary preference explicitly chooses account 2.
						consumerCache := newCache()
						consumerTransactional, ok := consumerCache.(issue715WSDeliveryCache)
						require.True(t, ok)
						observing := &issue715WSBarrierCache{GatewayCache: consumerCache, issue715WSDeliveryCache: consumerTransactional, entered: make(chan struct{}), release: barrier.release, observed: barrier.observed}
						if useBarrier {
							consumerCache = observing
						}
						consumer = issue715WSNode(t, mode, srv.URL, consumerCache, true)
					}
					issue715WSSend(t, producer.clientConn, issue715WSCreate("", false))
					first := issue715WSRead(t, producer.clientConn)
					require.Contains(t, string(first), "fixture-task-reference-715")
					toolExecutions, ledger := 0, 11
					for _, item := range gjson.GetBytes(first, "response.output").Array() {
						if item.Get("type").String() == "function_call" {
							toolExecutions++
							ledger += 7
						}
					}
					previous := gjson.GetBytes(first, "response.id").String()
					firstPrevious := previous
					if crossNode {
						// First consumption has complete context and no response anchor:
						// ordinary response stickiness must not hide missing task ownership.
						firstPrevious = ""
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/responses", nil)
						c.Request.Header.Set("session_id", "fixture-ws715-session")
						c.Request.Header.Set("thread-id", "fixture-ws715-thread")
						hash := consumer.handler.gatewayService.GenerateSessionHash(c, []byte(issue715WSCreate("", true)))
						require.NotEmpty(t, hash)
						require.NoError(t, base.SetSessionAccountID(context.Background(), 4301, "openai:"+hash, 2, time.Minute))
					}
					if crossNode && useBarrier {
						issue715WSSend(t, consumer.clientConn, issue715WSCreate(firstPrevious, true))
						select {
						case <-barrier.observed:
						case <-time.After(2 * time.Second):
							t.Fatal("client-visible WS delivery must reserve shared ownership before commit")
						}
						release()
					} else {
						release()
						issue715WSSend(t, consumer.clientConn, issue715WSCreate(firstPrevious, true))
					}
					for turn := 0; turn < 2; turn++ {
						if turn > 0 {
							issue715WSSend(t, consumer.clientConn, issue715WSCreate(previous, true))
						}
						answer := issue715WSRead(t, consumer.clientConn)
						require.Contains(t, string(answer), "Same task: ledger balance 18", "generated opaque WS task and tool result must return to producer")
						require.NotContains(t, string(answer), `"type":"function_call"`, "completed tool effects must not be requested again")
						previous = gjson.GetBytes(answer, "response.id").String()
					}
					require.Equal(t, 1, toolExecutions)
					require.Equal(t, 18, ledger)
					u.mu.Lock()
					ids, httpCount, wsCount := append([]int64(nil), u.ids...), u.http, u.ws
					u.mu.Unlock()
					require.Equal(t, []int64{1, 1, 1}, ids, "exactly three network generations, all on producer")
					if mode == service.OpenAIWSIngressModeHTTPBridge {
						require.Equal(t, 3, httpCount)
						require.Zero(t, wsCount)
					} else {
						require.Equal(t, 3, wsCount)
						require.Zero(t, httpCount)
					}
					issue600Finish(t, consumer, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
					if crossNode {
						issue600Finish(t, producer, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
					}
				})
			}
		})
	}
}

func TestIssue715WSAmbiguousToolSubmissionIsNotReplayed(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			u := &issue715WSOracle{}
			srv := u.server(t)
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			h := issue715WSNode(t, mode, srv.URL, repository.NewGatewayCache(client), false)
			issue715WSSend(t, h.clientConn, issue715WSCreate("", false))
			first := issue715WSRead(t, h.clientConn)
			require.Contains(t, string(first), "update_ledger")
			// The client has performed the synthetic ledger effect once. The
			// next submission reaches the provider before it closes the socket.
			u.mu.Lock()
			u.fail = true
			u.mu.Unlock()
			issue715WSSend(t, h.clientConn, issue715WSCreate("", true))
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			for {
				_, event, err := h.clientConn.Read(ctx)
				if err != nil {
					require.NotErrorIs(t, err, context.DeadlineExceeded, "failed turn must release the handler")
					break
				}
				require.NotEqual(t, "response.completed", gjson.GetBytes(event, "type").String(), "ambiguous tool result must never become fabricated success")
			}
			issue600Finish(t, h, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
			u.mu.Lock()
			ids := append([]int64(nil), u.ids...)
			u.mu.Unlock()
			require.Equal(t, []int64{1, 1}, ids, "one generation plus one possibly executed result submission; no automatic replay")
		})
	}
}

func TestIssue715WSPreviousResponseMustBelongToTaskOwner(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			u := &issue715WSOracle{}
			srv := u.server(t)
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			cache := repository.NewGatewayCache(client)
			h := issue715WSNode(t, mode, srv.URL, cache, false)
			issue715WSSend(t, h.clientConn, issue715WSCreate("", false))
			first := issue715WSRead(t, h.clientConn)
			require.Contains(t, string(first), "fixture-task-reference-715")
			foreignStore := service.NewOpenAIWSStateStore(cache)
			require.NoError(t, foreignStore.BindResponseAccount(context.Background(), 4301, "resp_foreign715", 2, time.Minute))
			issue715WSSend(t, h.clientConn, issue715WSCreate("resp_foreign715", true))
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			for {
				_, event, err := h.clientConn.Read(ctx)
				if err != nil {
					require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
					break
				}
				require.Equal(t, "error", gjson.GetBytes(event, "type").String(), "foreign previous response cannot start a generation")
			}
			issue600Finish(t, h, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
			u.mu.Lock()
			ids := append([]int64(nil), u.ids...)
			u.mu.Unlock()
			require.Equal(t, []int64{1}, ids, "foreign previous-response binding must reject before upstream write")
		})
	}
}

func TestIssue715WSRecoveryRefusalDoesNotPenalizeAccount(t *testing.T) {
	for _, reason := range []string{"owner_unavailable", "owner_conflict", "route_changed", "wire_changed", "store_unavailable", "delivery_pending"} {
		t.Run(reason, func(t *testing.T) {
			err := &service.OpenAIAgentRecoveryError{Reason: reason}
			require.False(t, shouldReportOpenAIWSProxyAccountFailure(err))
			closeErr := service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "fixture local refusal", err)
			require.False(t, shouldReportOpenAIWSProxyAccountFailure(fmt.Errorf("fixture adapter: %w", closeErr)), "local compatibility/store failures cannot damage account health")
		})
	}
	require.True(t, shouldReportOpenAIWSProxyAccountFailure(errors.New("fixture genuine upstream failure")))
}

type issue715WSFailedWriteCache struct {
	service.GatewayCache
	issue715WSDeliveryCache
	reserved chan string
	release  chan struct{}
}

func (c *issue715WSFailedWriteCache) ReserveOpenAIAgentRecovery(ctx context.Context, key, route, token string, ttl time.Duration) error {
	if err := c.issue715WSDeliveryCache.ReserveOpenAIAgentRecovery(ctx, key, route, token, ttl); err != nil {
		return err
	}
	c.reserved <- key
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestIssue715WSClosedDownstreamCannotCommitGeneratedTask(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			u := &issue715WSOracle{}
			srv := u.server(t)
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			base := repository.NewGatewayCache(client)
			transactional, ok := base.(issue715WSDeliveryCache)
			require.True(t, ok)
			cache := &issue715WSFailedWriteCache{GatewayCache: base, issue715WSDeliveryCache: transactional, reserved: make(chan string, 1), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(cache.release) }) }
			defer release()
			h := issue715WSNode(t, mode, srv.URL, cache, false)
			issue715WSSend(t, h.clientConn, issue715WSCreate("", false))
			var key string
			select {
			case key = <-cache.reserved:
			case <-time.After(5 * time.Second):
				t.Fatal("fixture output reservation barrier not reached")
			}
			pending, err := transactional.GetOpenAIAgentRecovery(context.Background(), key)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(pending, "pending:"))
			// Complete the real close handshake while production is blocked
			// immediately before its actual client write. The ingress reader
			// acknowledges the close independently of the output goroutine.
			_ = h.clientConn.Close(coderws.StatusNormalClosure, "fixture closes before task delivery")
			release()
			issue600Finish(t, h, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
			record, err := transactional.GetOpenAIAgentRecovery(context.Background(), key)
			require.NoError(t, err)
			require.Equal(t, "failed", record, "actual failed WS write must not register a successful generated-task owner")
			u.mu.Lock()
			ids := append([]int64(nil), u.ids...)
			u.mu.Unlock()
			require.Equal(t, []int64{1}, ids, "a failed downstream write cannot cause automatic upstream replay")
		})
	}
}
