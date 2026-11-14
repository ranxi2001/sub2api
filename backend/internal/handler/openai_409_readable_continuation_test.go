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
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type readable409DeliveryWriter struct {
	gin.ResponseWriter
	observe func()
	fail    bool
}

func (w *readable409DeliveryWriter) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "fixture-readable-B") {
		w.observe()
		if w.fail {
			return 0, io.ErrClosedPipe
		}
	}
	return w.ResponseWriter.Write(body)
}

func (w *readable409DeliveryWriter) WriteString(body string) (int, error) {
	return w.Write([]byte(body))
}

// Keep the real pricing/RecordUsage path observable. A recovered usage panic
// must not pass merely because the response already reached the client.
type readable409UsageRepo struct {
	service.UsageLogRepository
	mu   sync.Mutex
	rows []service.UsageLog
}

func (r *readable409UsageRepo) Create(_ context.Context, row *service.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, *row)
	return true, nil
}

func (r *readable409UsageRepo) snapshot() []service.UsageLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]service.UsageLog(nil), r.rows...)
}

// Both sides use real localhost HTTP, plus the production Redis ownership Lua.
// Authentication and persistence use isolated fixtures; pricing and usage run
// through production services. No real credentials, provider, customer history
// or tool side effects are involved.
func TestOpenAI409ReadableContinuationHTTP(t *testing.T) {
	for _, fixture := range []struct {
		name, reasoning string
		store           bool
	}{
		{"summary", `{"type":"reasoning","id":"rs_fixture_A","summary":[{"type":"summary_text","text":"Use the recorded ledger result; never apply the addition twice."}]}`, false},
		{"content", `{"type":"reasoning","id":"rs_fixture_A","status":"completed","encrypted_content":"fixture-reasoning-A","summary":[{"type":"summary_text","text":"Use the recorded ledger result; never apply the addition twice."}],"content":[{"type":"reasoning_text","text":"The readable calculation is 11 + 7 = 18."}]}`, false},
		{"stored_content", `{"type":"reasoning","id":"rs_fixture_A","encrypted_content":"fixture-reasoning-A","content":[{"type":"reasoning_text","text":"Use the recorded ledger result; never apply the addition twice."}]}`, true},
	} {
		for _, stream := range []bool{false, true} {
			for _, outcome := range []string{"completed", "rejected", "write_failed"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", fixture.name, stream, outcome), func(t *testing.T) {
					clients := issue805Redis(t)
					observer := clients()
					const system = `{"type":"message","role":"system","content":"Report the existing balance. Do not repeat completed operations."}`
					const originalTask = `{"type":"message","role":"user","content":"Keep ledger balance 11; add 7 exactly once","sequence":9007199254740993}`
					const call = `{"type":"function_call","name":"update_ledger","call_id":"fc_715_once","arguments":"{\"add\":7}"}`
					const assistant = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I will use the recorded result on continuation."}]}`
					const result = `{"type":"function_call_output","call_id":"fc_715_once","output":"ledger balance 18; applied once"}`
					const current = `{"type":"message","role":"user","content":"Continue the same task and confirm the existing balance"}`
					bAgent := strings.ReplaceAll(issue715Agent, "fixture-task-reference-715", "fixture-readable-B")
					var mu sync.Mutex
					var sent [][]byte
					var identities []string
					provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						mu.Lock()
						sent = append(sent, body)
						identities = append(identities, r.Header.Get("Authorization"))
						n := len(sent)
						mu.Unlock()
						output := "[" + issue715Agent + "," + fixture.reasoning + "," + assistant + "," + call + "]"
						if n > 1 {
							valid := r.Header.Get("Authorization") == "Bearer fixture-key-2" &&
								!strings.Contains(string(body), "fixture-task-reference-715") &&
								!strings.Contains(string(body), "fixture-reasoning-A") &&
								!strings.Contains(string(body), "rs_fixture_A") &&
								!gjson.GetBytes(body, "previous_response_id").Exists() &&
								!gjson.GetBytes(body, "conversation").Exists() &&
								strings.Contains(string(body), "Use the recorded ledger result; never apply the addition twice.") &&
								gjson.GetBytes(body, "input.6.output").String() == "ledger balance 18; applied once"
							if !valid || outcome == "rejected" {
								w.Header().Set("Content-Type", "application/json")
								w.WriteHeader(http.StatusBadRequest)
								_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"fixture refuses nonportable history or identity"}}`)
								return
							}
							output = "[" + bAgent + `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Existing balance is 18; the completed tool was not repeated."}]}]`
						}
						payload := fmt.Sprintf(`{"id":"resp_readable_%d","status":"completed","error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, n, output)
						if gjson.GetBytes(body, "stream").Bool() {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", payload)
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, payload)
						}
					}))
					defer provider.Close()
					accounts := issue715AccountsOfType(service.AccountTypeAPIKey)
					for i := range accounts {
						accounts[i].Credentials["base_url"] = provider.URL
					}
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					settings := service.NewSettingService(&omit409SettingsRepo{enabled: true}, cfg)
					usage := &readable409UsageRepo{}
					h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, testutil.NewRealHTTPUpstream(cfg), repository.NewGatewayCache(clients()), settings, usage, accounts...)
					h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					template, _ := newIssue715Context(t, "{}")
					done := make(chan struct{}, 1)
					pendingBeforeWrite := false
					var observeErr error
					router := gin.New()
					router.POST("/v1/responses", func(c *gin.Context) {
						defer func() { done <- struct{}{} }()
						for key, value := range template.Keys {
							c.Set(key, value)
						}
						c.Writer = &readable409DeliveryWriter{ResponseWriter: c.Writer, fail: outcome == "write_failed", observe: func() {
							ctx, cancel := context.WithTimeout(context.Background(), time.Second)
							defer cancel()
							var keys []string
							keys, observeErr = observer.Keys(ctx, "openai_agent_owner:*").Result()
							for _, key := range keys {
								var value string
								value, observeErr = observer.Get(ctx, key).Result()
								if observeErr != nil {
									return
								}
								pendingBeforeWrite = pendingBeforeWrite || (strings.HasPrefix(value, "pending:") && strings.Contains(value, ":[1,2,"))
							}
						}}
						h.Responses(c)
					})
					gateway := httptest.NewServer(router)
					defer gateway.Close()
					client := gateway.Client()
					client.Timeout = 10 * time.Second
					request := func(input string) (int, string) {
						body := fmt.Sprintf(`{"model":"gpt-5.1","store":%t,"stream":%t,"input":[%s]}`, fixture.store, stream, input)
						req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/responses", strings.NewReader(body))
						require.NoError(t, err)
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("session_id", "fixture-readable-same-thread")
						resp, err := client.Do(req)
						require.NoError(t, err)
						answer, readErr := io.ReadAll(resp.Body)
						_ = resp.Body.Close()
						select {
						case <-done:
						case <-time.After(10 * time.Second):
							t.Fatal("gateway handler did not finish")
						}
						require.NoError(t, readErr)
						return resp.StatusCode, string(answer)
					}
					status, first := request(system + "," + originalTask)
					require.Equal(t, http.StatusOK, status, first)
					firstUsage := usage.snapshot()
					require.Len(t, firstUsage, 1, "A usage must reach persistence through real pricing")
					require.Equal(t, int64(1), firstUsage[0].AccountID)
					require.Equal(t, 5, firstUsage[0].InputTokens)
					require.Equal(t, 3, firstUsage[0].OutputTokens)
					require.Greater(t, firstUsage[0].TotalCost, 0.0)
					response := issue758CompletedResponse(t, first, stream)
					toolExecutions, balance := 0, 11
					for _, item := range response.Get("output").Array() {
						if item.Get("type").String() == "function_call" {
							require.Equal(t, "fc_715_once", item.Get("call_id").String())
							toolExecutions++
							balance += int(gjson.Get(item.Get("arguments").String(), "add").Int())
						}
					}
					require.Equal(t, 1, toolExecutions)
					require.Equal(t, 18, balance)
					ownersA := issue805Owners(t, observer)
					require.Len(t, ownersA, 1)
					// The repository owns this backing slice. Its authoritative GetByID
					// and admission reads now see A disabled, not merely a sticky miss.
					accounts[0].Schedulable = false
					disabled, err := (openAIImagesFailoverAccountRepo{accounts: accounts}).GetByID(context.Background(), 1)
					require.NoError(t, err)
					require.False(t, disabled.Schedulable)
					history := strings.TrimSuffix(strings.TrimPrefix(response.Get("output").Raw, "["), "]")
					status, answer := request(system + "," + originalTask + "," + history + "," + result + "," + current)
					mu.Lock()
					defer mu.Unlock()
					require.Len(t, sent, 2, "readable continuation must reach B exactly once; response=%s", answer)
					require.Equal(t, []string{"Bearer fixture-key-1", "Bearer fixture-key-2"}, identities)
					rows := usage.snapshot()
					if outcome == "rejected" {
						require.Len(t, rows, 1, "upstream rejection must not invent completed usage")
					} else {
						require.Len(t, rows, 2, "B completed usage survives even a downstream write failure")
						require.Equal(t, int64(2), rows[1].AccountID)
						require.Equal(t, 5, rows[1].InputTokens)
						require.Equal(t, 3, rows[1].OutputTokens)
						require.Greater(t, rows[1].TotalCost, 0.0)
					}
					for index, expected := range map[int]string{0: system, 1: originalTask, 4: assistant, 5: call, 6: result, 7: current} {
						require.JSONEq(t, expected, gjson.GetBytes(sent[1], fmt.Sprintf("input.%d", index)).Raw)
					}
					require.Contains(t, string(sent[1]), "Historical encrypted content omitted")
					for _, field := range []string{"summary", "content"} {
						for _, part := range gjson.Get(fixture.reasoning, field).Array() {
							require.Contains(t, gjson.GetBytes(sent[1], "input.3").Raw, part.Get("text").String())
						}
					}
					owners := issue805Owners(t, observer)
					for key, owner := range ownersA {
						require.Equal(t, owner, owners[key], "old A owner must not change")
						delete(owners, key)
					}
					switch outcome {
					case "rejected":
						require.Equal(t, http.StatusBadRequest, status, answer)
						require.Contains(t, answer, "fixture refuses")
						require.Empty(t, owners, "rejection must not publish B or replay in the pool")
					default:
						require.NoError(t, observeErr)
						require.True(t, pendingBeforeWrite, "B output must be reserved before downstream exposure")
						require.Len(t, owners, 1)
						for _, owner := range owners {
							if outcome == "write_failed" {
								require.Equal(t, "failed", owner, "failed delivery cannot commit B")
							} else {
								require.True(t, strings.HasPrefix(owner, "[1,2,"), owner)
							}
						}
						if outcome == "completed" {
							require.Equal(t, http.StatusOK, status, answer)
							completed := issue758CompletedResponse(t, answer, stream)
							require.Contains(t, completed.Raw, "Existing balance is 18")
							require.NotContains(t, completed.Raw, `"type":"function_call"`, "completed tools must not run again")
						}
					}
				})
			}
		}
	}
}
