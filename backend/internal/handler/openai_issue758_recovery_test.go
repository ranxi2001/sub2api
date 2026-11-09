//go:build unit

package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func issue758Request(t *testing.T, handler *OpenAIGatewayHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	c, recorder := newIssue715Context(t, body)
	c.Request.Header.Set("session_id", "fixture-758-thread")
	key, _ := c.Get(string(middleware.ContextKeyAPIKey))
	apiKey, ok := key.(*service.APIKey)
	require.True(t, ok)
	apiKey.ID = 99
	handler.Responses(c)
	return recorder
}

func issue758CompletedResponse(t *testing.T, body string, stream bool) gjson.Result {
	t.Helper()
	if !stream {
		response := gjson.Parse(body)
		require.Equal(t, "completed", response.Get("status").String(), body)
		return response
	}
	var responses []gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			event := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if event.Get("type").String() == "response.completed" {
				responses = append(responses, event.Get("response"))
			}
		}
	}
	require.Len(t, responses, 1, "must deliver exactly one completion: %s", body)
	require.Equal(t, "completed", responses[0].Get("status").String())
	return responses[0]
}

// The upstream is a synthetic compatibility oracle, not a live OAuth provider.
// Its delegated task and tool result must survive waiting and app restarts;
// access to another pool account does not grant it access to this task.
func TestIssue758HTTPLimitedOwnerWaitsAndResumesIntactTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, shared := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, stored := range []bool{false, true} {
				t.Run(fmt.Sprintf("shared=%v/stream=%v/stored=%v", shared, stream, stored), func(t *testing.T) {
					accounts := issue715Accounts()
					if stored {
						// HTTP previous_response_id is supported only for API-key
						// upstreams. The full-history cases exercise OAuth; this
						// branch covers retention of HTTP authorization anchors.
						for i := range accounts {
							accounts[i].Type = service.AccountTypeAPIKey
							accounts[i].Credentials = map[string]any{"api_key": fmt.Sprintf("fixture-key-%d", i+1)}
						}
					}
					upstream := &issue715Upstream{mode: "generated-json"}
					var cache service.GatewayCache
					var mr *miniredis.Miniredis
					if shared {
						mr = miniredis.RunT(t)
						client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
						t.Cleanup(func() { _ = client.Close() })
						cache = repository.NewGatewayCache(client)
					}
					handler := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, cache, accounts...)
					firstBody := strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1)
					first := issue758Request(t, handler, firstBody)
					require.Equal(t, http.StatusOK, first.Code, first.Body.String())
					firstResponse := issue758CompletedResponse(t, first.Body.String(), stream)
					require.JSONEq(t, issue715Agent, firstResponse.Get("output.#(type==agent_message)").Raw)

					toolExecutions, balance := 0, 11
					executeTools := func(response gjson.Result) {
						for _, output := range response.Get("output").Array() {
							if output.Get("type").String() != "function_call" {
								continue
							}
							require.Equal(t, "fc_715_once", output.Get("call_id").String())
							require.EqualValues(t, 7, gjson.Get(output.Get("arguments").String(), "add").Int())
							toolExecutions++
							balance += 7
						}
					}
					executeTools(firstResponse)
					require.Equal(t, 1, toolExecutions)
					require.Equal(t, 18, balance)
					upstream.mode = ""
					followupBody := issue715Body(true, stream)
					completed := issue758Request(t, handler, followupBody)
					require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
					executeTools(issue758CompletedResponse(t, completed.Body.String(), stream))
					require.Contains(t, completed.Body.String(), "Same task: ledger balance 18; update already applied once")
					require.Equal(t, []int64{1, 1}, upstream.ids)
					if stored {
						responseID := issue758CompletedResponse(t, completed.Body.String(), stream).Get("id").String()
						require.NotEmpty(t, responseID)
						var err error
						followupBody, err = sjson.Set(followupBody, "previous_response_id", responseID)
						require.NoError(t, err)
					}

					originalOwners := make(map[string]string)
					if shared {
						for _, key := range mr.Keys() {
							if strings.HasPrefix(key, "openai_agent_owner:") {
								value, err := mr.Get(key)
								require.NoError(t, err)
								originalOwners[key] = value
								require.Positive(t, mr.TTL(key))
								require.LessOrEqual(t, mr.TTL(key), time.Hour)
							}
						}
						require.NotEmpty(t, originalOwners, "the completed delegated task must establish shared ownership")
					}

					resetAt := time.Now().UTC().Add(5 * time.Hour).Truncate(time.Second)
					accounts[0].RateLimitResetAt = &resetAt
					accounts[0].Priority, accounts[1].Priority = 100, 0
					require.True(t, accounts[1].IsSchedulable(), "the pool has an available alternative but no compatibility evidence for it")
					assertWaiting := func(response *httptest.ResponseRecorder) {
						t.Helper()
						require.Equal(t, http.StatusTooManyRequests, response.Code, response.Body.String())
						require.Equal(t, "encrypted_agent_recovery_owner_rate_limited", gjson.Get(response.Body.String(), "error.code").String())
						require.Equal(t, "rate_limit_error", gjson.Get(response.Body.String(), "error.type").String())
						message := gjson.Get(response.Body.String(), "error.message").String()
						require.Contains(t, message, resetAt.Format(time.RFC3339))
						require.Contains(t, message, "Preserve this task and context")
						retrySeconds, err := strconv.Atoi(response.Header().Get("Retry-After"))
						require.NoError(t, err, "a pre-dispatch refusal must expose when to retry")
						require.InDelta(t, time.Until(resetAt).Seconds(), retrySeconds, 2)
						require.Equal(t, []int64{1, 1}, upstream.ids, "waiting cannot send or replay a tool result to any upstream")
					}
					assertWaiting(issue758Request(t, handler, followupBody))

					if shared {
						// Advance only the isolated Redis clock. The original one-hour
						// affinity would be gone, while the quota reset is still ahead.
						mr.FastForward(2 * time.Hour)
						for key, original := range originalOwners {
							value, err := mr.Get(key)
							require.NoError(t, err, "ownership must outlive the original TTL while the owner waits for quota")
							require.Equal(t, original, value, "retention must preserve the exact compatible route")
							require.Greater(t, mr.TTL(key), 2*time.Hour)
						}
						// No process-local history or mutable sticky is available here.
						handler = newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, cache, accounts...)
						assertWaiting(issue758Request(t, handler, followupBody))
					}

					past := time.Now().Add(-time.Minute)
					accounts[0].RateLimitResetAt = &past
					resumed := issue758Request(t, handler, followupBody)
					require.Equal(t, http.StatusOK, resumed.Code, resumed.Body.String())
					executeTools(issue758CompletedResponse(t, resumed.Body.String(), stream))
					require.Contains(t, resumed.Body.String(), "Same task: ledger balance 18; update already applied once")
					require.Equal(t, 1, toolExecutions, "an already delivered result must never request a second tool execution")
					require.Equal(t, 18, balance)
					require.Equal(t, []int64{1, 1, 1}, upstream.ids, "the original owner must resume despite another account's higher preference")
					require.Len(t, upstream.bodies, 3)
					for _, body := range upstream.bodies[1:] {
						require.JSONEq(t, issue715Agent, gjson.GetBytes(body, "input.#(type==agent_message)").Raw)
						require.Equal(t, "ledger balance 18; applied once", gjson.GetBytes(body, "input.#(type==function_call_output).output").String())
						require.Contains(t, string(body), "Keep ledger balance 11; add 7 exactly once")
					}
				})
			}
		}
	}
}

func TestIssue758HTTPOAuthPreviousResponseCannotPromiseRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, cooldown := range []string{"rate", "overload"} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, cooldown), func(t *testing.T) {
				mr := miniredis.RunT(t)
				client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
				t.Cleanup(func() { _ = client.Close() })
				cache := repository.NewGatewayCache(client)
				accounts := issue715Accounts() // Keep OAuth, including the available alternative.
				upstream := &issue715Upstream{mode: "generated-json"}
				handler := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, cache, accounts...)
				first := issue758Request(t, handler, strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				require.JSONEq(t, issue715Agent, issue758CompletedResponse(t, first.Body.String(), stream).Get("output.#(type==agent_message)").Raw)
				upstream.mode = ""
				completed := issue758Request(t, handler, issue715Body(true, stream))
				require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
				responseID := issue758CompletedResponse(t, completed.Body.String(), stream).Get("id").String()
				require.NotEmpty(t, responseID)
				body, err := sjson.Set(issue715Body(true, stream), "previous_response_id", responseID)
				require.NoError(t, err)

				type evidence struct {
					value string
					ttl   time.Duration
				}
				before := make(map[string]evidence)
				agents, anchors := 0, 0
				for _, key := range mr.Keys() {
					isAgent := strings.HasPrefix(key, "openai_agent_owner:")
					isAnchor := strings.Contains(key, "openai:http-response-owner:") || strings.Contains(key, "openai:response:")
					if !isAgent && !isAnchor {
						continue
					}
					value, err := mr.Get(key)
					require.NoError(t, err)
					require.Positive(t, mr.TTL(key))
					before[key] = evidence{value, mr.TTL(key)}
					if isAgent {
						agents++
						require.True(t, gjson.Valid(value), "must have a proven route, not pending/failed/conflict")
						require.EqualValues(t, 1, gjson.Get(value, "1").Int(), "the intact item belongs to OAuth account 1")
					} else {
						anchors++
						switch {
						case strings.Contains(key, "http-response-owner:v1:"):
							require.Equal(t, "v1:100:99", value)
						case strings.Contains(key, "http-response-owner:user:"):
							require.Equal(t, "100", value)
						case strings.Contains(key, "http-response-owner:key:"):
							require.Equal(t, "99", value)
						default:
							require.Equal(t, "1", value)
						}
					}
				}
				require.Equal(t, 1, agents)
				require.Equal(t, 4, anchors, "response account plus v1/user/key HTTP authorization must all exist")
				// A fresh handler must resolve the proven item and all authorization
				// anchors from the real shared adapter, without process-local history.
				handler = newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, cache, accounts...)
				for _, phase := range []string{"before", "during", "after"} {
					resetAt := time.Now().UTC().Add(5 * time.Hour).Truncate(time.Second)
					if phase != "during" {
						resetAt = time.Now().Add(-time.Minute)
					}
					if cooldown == "rate" {
						accounts[0].RateLimitResetAt = &resetAt
					} else {
						accounts[0].OverloadUntil = &resetAt
					}
					refused := issue758Request(t, handler, body)
					require.Equal(t, http.StatusConflict, refused.Code, "%s: %s", phase, refused.Body.String())
					require.Equal(t, "encrypted_agent_recovery_http_previous_response_unsupported", gjson.Get(refused.Body.String(), "error.code").String())
					require.Equal(t, "invalid_request_error", gjson.Get(refused.Body.String(), "error.type").String())
					message := gjson.Get(refused.Body.String(), "error.message").String()
					require.Contains(t, message, "complete original history without previous_response_id")
					require.Contains(t, message, "Waiting alone will not make this request supported")
					require.NotContains(t, message, "Wait until")
					require.Empty(t, refused.Header().Get("Retry-After"))
					require.Equal(t, []int64{1, 1}, upstream.ids, "unsupported continuation must never dispatch")
					for key, original := range before {
						value, err := mr.Get(key)
						require.NoError(t, err)
						require.Equal(t, original.value, value)
						require.Equal(t, original.ttl, mr.TTL(key), "refusal must not extend any evidence: %s", key)
					}
				}
			})
		}
	}
}

func TestIssue758HTTPWaitingErrorAfterSSECommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &OpenAIGatewayHandler{}
	for _, reason := range []string{"owner_rate_limited", "owner_overloaded", "http_previous_response_unsupported"} {
		t.Run(reason, func(t *testing.T) {
			c, recorder := newIssue715Context(t, issue715Body(true, true))
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.WriteHeaderNow()
			resetAt := time.Now().UTC().Add(5 * time.Hour).Truncate(time.Second)
			err := &service.OpenAIAgentRecoveryError{Reason: reason, RetryAt: resetAt}
			if reason == "http_previous_response_unsupported" {
				err.RetryAt = time.Time{}
			}
			require.True(t, handler.handleOpenAIAgentRecoveryError(c, err, false))
			require.Equal(t, http.StatusOK, recorder.Code, "committed SSE must report its failure in the terminal event")
			require.Empty(t, c.Writer.Header().Get("Retry-After"), "headers cannot be changed after committing the stream")
			require.Empty(t, recorder.Result().Header.Get("Retry-After"))
			require.Contains(t, recorder.Body.String(), "event: response.failed")
			require.NotContains(t, recorder.Body.String(), "event: response.completed")
			var events []gjson.Result
			for _, line := range strings.Split(recorder.Body.String(), "\n") {
				if strings.HasPrefix(line, "data: ") {
					events = append(events, gjson.Parse(strings.TrimPrefix(line, "data: ")))
				}
			}
			require.Len(t, events, 1, "only one terminal failure may be emitted")
			require.Equal(t, "response.failed", events[0].Get("type").String())
			require.Equal(t, "failed", events[0].Get("response.status").String())
			// Responses terminal errors carry code/message, unlike HTTP JSON's
			// error envelope, which also carries an error.type classification.
			require.Equal(t, "encrypted_agent_recovery_"+reason, events[0].Get("response.error.code").String())
			message := events[0].Get("response.error.message").String()
			if reason == "http_previous_response_unsupported" {
				require.Contains(t, message, "complete original history without previous_response_id")
				require.NotContains(t, message, "Wait until")
			} else {
				require.Contains(t, message, resetAt.Format(time.RFC3339))
			}
			require.Contains(t, message, "Preserve this task and context")
		})
	}
}

func TestIssue758HTTPQuotaWaitCannotHideHardRestrictions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, change := range []string{"disabled", "expired", "credentials"} {
		t.Run(change, func(t *testing.T) {
			accounts := issue715Accounts()
			upstream := &issue715Upstream{}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
			first := issue758Request(t, handler, issue715Body(false, false))
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			resetAt := time.Now().Add(5 * time.Hour)
			accounts[0].RateLimitResetAt = &resetAt
			code := "encrypted_agent_recovery_owner_unavailable"
			switch change {
			case "disabled":
				accounts[0].Schedulable = false
			case "expired":
				past := time.Now().Add(-time.Minute)
				accounts[0].AutoPauseOnExpired = true
				accounts[0].ExpiresAt = &past
			case "credentials":
				accounts[0].Credentials = map[string]any{"access_token": "fixture-changed-identity"}
				code = "encrypted_agent_recovery_route_changed"
			}
			refused := issue758Request(t, handler, issue715Body(true, false))
			require.Equal(t, http.StatusConflict, refused.Code, refused.Body.String())
			require.Equal(t, code, gjson.Get(refused.Body.String(), "error.code").String())
			require.Equal(t, "invalid_request_error", gjson.Get(refused.Body.String(), "error.type").String())
			require.Empty(t, refused.Header().Get("Retry-After"), "quota recovery cannot promise that a revoked or changed route will recover")
			require.Equal(t, []int64{1}, upstream.ids)
		})
	}
}
