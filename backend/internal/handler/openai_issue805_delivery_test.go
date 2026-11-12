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
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func issue805HTTPNodes(t *testing.T, upstream string, clients func() *redis.Client, wrappers ...func(service.GatewayCache) service.GatewayCache) (func(bool) *OpenAIGatewayHandler, []service.Account) {
	t.Helper()
	accounts := issue715Accounts()
	for i := range accounts {
		accounts[i].Type = service.AccountTypeAPIKey
		accounts[i].Credentials = map[string]any{"api_key": []string{"fixture-ws-one", "fixture-ws-two"}[i], "base_url": upstream, "pool_mode": true, "pool_mode_retry_count": 2, "pool_mode_retry_status_codes": []any{520}}
	}
	return func(disabled bool) *OpenAIGatewayHandler {
		cfg := &config.Config{}
		cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		cache := repository.NewGatewayCache(clients())
		for _, wrap := range wrappers {
			cache = wrap(cache)
		}
		h := newOpenAIResponsesFailoverTestHandlerWithCache(t, testutil.NewRealHTTPUpstream(cfg), cache, accounts...)
		h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		issue805Policy(t, h.cfg, disabled)
		return h
	}, accounts
}

// Observe the actual cross-node Redis lookup, rather than infer that a
// consumer reached the delivery fence because a timer happened to elapse.
type issue805ReadObserver struct {
	service.GatewayCache
	issue715WSDeliveryCache
	seen chan struct{}
	once *sync.Once
}

func (c *issue805ReadObserver) GetOpenAIAgentRecovery(ctx context.Context, key string) (string, error) {
	raw, err := c.issue715WSDeliveryCache.GetOpenAIAgentRecovery(ctx, key)
	if strings.HasPrefix(raw, "pending:") {
		c.once.Do(func() { close(c.seen) })
	}
	return raw, err
}

func TestIssue805HTTPUpstreamRejectionIsSingleIntactSend(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
	}{
		{"invalid_encrypted", "invalid_encrypted_content", 400},
		{"unsupported", "unsupported_encrypted_agent_message", 400},
		{"pool_retryable", "server_error", 520},
		{"rejected_field", "unsupported_parameter", 400},
		{"stream_failure", "unsupported_encrypted_agent_message", 0},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				clients := issue805Redis(t)
				u := &issue805Oracle{}
				srv := u.server(t)
				nodes, accounts := issue805HTTPNodes(t, srv.URL, clients)
				first := issue758Request(t, nodes(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				original := issue805Owners(t, clients())
				require.Len(t, original, 1)
				reset := time.Now().Add(time.Hour)
				accounts[0].RateLimitResetAt = &reset
				u.mu.Lock()
				u.rejection, u.rejectionStatus = tc.code, tc.status
				if tc.name == "rejected_field" {
					u.rejectionParam = "max_output_tokens"
				}
				u.mu.Unlock()
				body := issue715Body(true, stream)
				if tc.name == "rejected_field" {
					body = strings.TrimSuffix(body, "}") + `,"max_output_tokens":256}`
				}
				answer := issue758Request(t, nodes(true), body)
				require.NotContains(t, answer.Body.String(), "response.completed")
				require.NotContains(t, answer.Body.String(), "existing ledger balance")
				if tc.status == 400 {
					require.Equal(t, http.StatusBadRequest, answer.Code)
					require.Contains(t, answer.Body.String(), tc.code)
				} else {
					require.True(t, answer.Code >= 400 || strings.Contains(answer.Body.String(), "error") || strings.Contains(answer.Body.String(), "response.failed"), answer.Body.String())
				}
				u.mu.Lock()
				defer u.mu.Unlock()
				require.Equal(t, []string{"Bearer fixture-ws-one", "Bearer fixture-ws-two"}, u.ids, "upstream rejection must not delete and retry or enter pool failover")
				require.JSONEq(t, gjson.Get(body, "input").Raw, gjson.GetBytes(u.frames[1], "input").Raw)
				if tc.name == "rejected_field" {
					require.EqualValues(t, 256, gjson.GetBytes(u.frames[1], "max_output_tokens").Int(), "exercise a field that the ordinary same-account retry would remove")
				}
				require.Equal(t, original, issue805Owners(t, clients()))
			})
		}
	}
}

type issue805DeliveryWriter struct {
	gin.ResponseWriter
	entered, release chan struct{}
	once             sync.Once
	failure          string
	cancel           context.CancelFunc
}

func (w *issue805DeliveryWriter) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "fixture-task-produced-by-B") {
		w.once.Do(func() { close(w.entered); <-w.release })
		switch w.failure {
		case "error":
			return 0, io.ErrClosedPipe
		case "cancel":
			w.cancel()
		}
	}
	return w.ResponseWriter.Write(body)
}
func (w *issue805DeliveryWriter) WriteString(body string) (int, error) { return w.Write([]byte(body)) }

func TestIssue805HTTPNewOutputDeliveryAcrossNodes(t *testing.T) {
	for _, outcome := range []string{"commit", "error", "cancel"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", outcome, stream), func(t *testing.T) {
				clients := issue805Redis(t)
				u := &issue805Oracle{}
				srv := u.server(t)
				observed := make(chan struct{})
				var observeOnce sync.Once
				nodes, accounts := issue805HTTPNodes(t, srv.URL, clients, func(base service.GatewayCache) service.GatewayCache {
					delivery, ok := base.(issue715WSDeliveryCache)
					require.True(t, ok)
					return &issue805ReadObserver{GatewayCache: base, issue715WSDeliveryCache: delivery, seen: observed, once: &observeOnce}
				})
				first := issue758Request(t, nodes(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
				require.Equal(t, 200, first.Code, first.Body.String())
				probe := clients()
				original := issue805Owners(t, probe)
				require.Len(t, original, 1)
				reset := time.Now().Add(time.Hour)
				accounts[0].RateLimitResetAt = &reset
				producer, consumer := nodes(true), nodes(true)
				c, _ := newIssue715Context(t, issue715Body(true, stream))
				c.Request.Header.Set("session_id", "fixture-758-thread")
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				w := &issue805DeliveryWriter{ResponseWriter: c.Writer, entered: make(chan struct{}), release: make(chan struct{}), failure: outcome, cancel: cancel}
				c.Writer = w
				var once sync.Once
				release := func() { once.Do(func() { close(w.release) }) }
				defer release()
				done := make(chan struct{})
				go func() { defer close(done); producer.Responses(c) }()
				issue715Await(t, w.entered)
				pending := issue805Owners(t, probe)
				require.Len(t, pending, 2)
				for key, raw := range pending {
					if old, ok := original[key]; ok {
						require.Equal(t, old, raw)
					} else {
						require.True(t, strings.HasPrefix(raw, "pending:"), raw)
						require.Contains(t, raw, `[1,2,`)
					}
				}
				mixed := strings.Replace(issue715Body(true, stream), issue715Agent, issue715Agent+","+issue805AgentB, 1)
				result := make(chan *httptest.ResponseRecorder, 1)
				go func() { result <- issue758Request(t, consumer, mixed) }()
				issue715Await(t, observed)
				select {
				case answer := <-result:
					t.Fatalf("pending output was consumed before actual downstream write: %s", answer.Body.String())
				default:
				}
				u.mu.Lock()
				require.Len(t, u.ids, 2, "pending must never reach another network send")
				u.mu.Unlock()
				release()
				issue715Await(t, done)
				var answer *httptest.ResponseRecorder
				select {
				case answer = <-result:
				case <-time.After(5 * time.Second):
					t.Fatal("cross-node consumer did not resolve delivery")
				}
				if outcome == "commit" {
					require.Equal(t, 200, answer.Code, answer.Body.String())
					require.Contains(t, answer.Body.String(), "existing ledger balance 18")
				} else {
					require.Equal(t, http.StatusConflict, answer.Code, answer.Body.String())
				}
				for key, raw := range issue805Owners(t, probe) {
					if old, ok := original[key]; ok {
						require.Equal(t, old, raw)
					} else if outcome == "commit" {
						require.EqualValues(t, 2, gjson.Get(raw, "1").Int())
					} else {
						require.Equal(t, "failed", raw, "failed/canceled downstream write cannot commit B")
					}
				}
			})
		}
	}
}

func TestIssue805HTTPKeyPermissionStillRejectsBeforeSend(t *testing.T) {
	clients := issue805Redis(t)
	u := &issue805Oracle{}
	srv := u.server(t)
	nodes, accounts := issue805HTTPNodes(t, srv.URL, clients)
	first := issue758Request(t, nodes(false), strings.Replace(issue715Body(false, false), issue715Agent+",", "", 1))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	reset := time.Now().Add(time.Hour)
	accounts[0].RateLimitResetAt = &reset
	c, rec := newIssue715Context(t, issue715Body(true, false))
	c.Request.Header.Set("session_id", "fixture-758-thread")
	key, _ := c.Get(string(middleware.ContextKeyAPIKey))
	key.(*service.APIKey).AllowLongContext = false
	nodes(true).Responses(c)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "context_length_exceeded", gjson.Get(rec.Body.String(), "error.code").String())
	u.mu.Lock()
	defer u.mu.Unlock()
	require.Len(t, u.ids, 1, "policy cannot override Key permissions")
}

func TestIssue805HTTPOldPointerCannotMoveToAlternative(t *testing.T) {
	for _, fullHistory := range []bool{false, true} {
		t.Run(fmt.Sprint("full=", fullHistory), func(t *testing.T) {
			clients := issue805Redis(t)
			u := &issue805Oracle{}
			srv := u.server(t)
			nodes, accounts := issue805HTTPNodes(t, srv.URL, clients)
			first := issue758Request(t, nodes(false), strings.Replace(issue715Body(false, false), issue715Agent+",", "", 1))
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			responseID := gjson.Get(first.Body.String(), "id").String()
			require.NotEmpty(t, responseID)
			reset := time.Now().Add(time.Hour)
			accounts[0].RateLimitResetAt = &reset
			body := issue715Body(true, false)
			if !fullHistory {
				body = `{"model":"gpt-5.1","stream":false,"input":[{"type":"message","role":"user","content":"continue"}]}`
			}
			body = strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"previous_response_id":%q}`, responseID)
			answer := issue758Request(t, nodes(true), body)
			require.GreaterOrEqual(t, answer.Code, http.StatusBadRequest, answer.Body.String())
			u.mu.Lock()
			defer u.mu.Unlock()
			require.Len(t, u.ids, 1, "server state pointer must not migrate with the agent-history policy")
		})
	}
}

func TestIssue805HTTPOldDeliveryFenceCannotBeBypassed(t *testing.T) {
	for _, state := range []string{"pending", "failed", "conflict", "unknown_encoding"} {
		t.Run(state, func(t *testing.T) {
			clients := issue805Redis(t)
			u := &issue805Oracle{}
			srv := u.server(t)
			nodes, accounts := issue805HTTPNodes(t, srv.URL, clients)
			first := issue758Request(t, nodes(false), strings.Replace(issue715Body(false, false), issue715Agent+",", "", 1))
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			probe := clients()
			owners := issue805Owners(t, probe)
			require.Len(t, owners, 1)
			for key, raw := range owners {
				value := state
				if state == "pending" {
					value = "pending:fixture-uncertain:" + raw
				}
				require.NoError(t, probe.Set(context.Background(), key, value, time.Minute).Err())
			}
			reset := time.Now().Add(time.Hour)
			accounts[0].RateLimitResetAt = &reset
			answer := issue758Request(t, nodes(true), issue715Body(true, false))
			require.Equal(t, http.StatusConflict, answer.Code, answer.Body.String())
			if state == "pending" {
				require.Contains(t, answer.Body.String(), "encrypted_agent_recovery_delivery_pending")
			}
			u.mu.Lock()
			defer u.mu.Unlock()
			require.Len(t, u.ids, 1, "uncertain/failed/conflicting delivery must stop before network send")
		})
	}
}
