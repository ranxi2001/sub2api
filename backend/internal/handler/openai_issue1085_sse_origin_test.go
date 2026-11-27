//go:build unit && issue1085

package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const issue1085Code = "codex_session_failover_context_required"
const issue1085Message = "fixture session requires complete context"
const issue1085Error = `{"type":"invalid_request_error","code":"codex_session_failover_context_required","message":"fixture session requires complete context"}`
const issue1085Created = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
const issue1085Failed = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"failed\",\"error\":" + issue1085Error + "}}\n\n"

func issue1085FlushRecords() {
	for len(opsErrorLogQueue) > 0 {
		flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
	}
}

func TestIssue1085UpstreamTerminalAttribution(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, mode := range []string{"http400", "sse", "stale_account_auth", "stale_skip", "current_skip", "request_override", "malformed", "unknown", "success_after_attempt", "hidden_history_success", "privacy"} {
			t.Run(fmt.Sprintf("passthrough=%v/%s", passthrough, mode), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 8)
				var sends atomic.Int32
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sends.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil || !strings.Contains(string(body), "fixture input") {
						t.Error("invalid fixture request")
					}
					if mode == "http400" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":`+issue1085Error+`}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					terminal := issue1085Failed
					switch mode {
					case "malformed":
						terminal = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture output\"}\n\n" +
							"event: response.failed\ndata: {broken\n\n"
					case "unknown":
						terminal = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture output\"}\n\n" +
							"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"fixture_unknown\",\"message\":\"fixture unknown failure\"}}}\n\n"
					case "success_after_attempt":
						terminal = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"
					case "hidden_history_success":
						terminal = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"
					case "privacy":
						terminal = strings.Replace(issue1085Failed, `"status":"failed"`, `"status":"failed","authorization":"Bearer fixture-canary-1085-secret","api_key":"fixture-canary-1085-secret"`, 1)
					}
					_, _ = io.WriteString(w, issue1085Created+terminal)
				}))
				defer provider.Close()
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				accounts := issue715AccountsOfType(service.AccountTypeAPIKey)[:1]
				accounts[0].Credentials = map[string]any{"base_url": provider.URL, "api_key": "fixture-upstream"}
				accounts[0].Extra = map[string]any{"openai_passthrough": passthrough, "openai_apikey_responses_websockets_v2_mode": "off"}
				h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, testutil.NewRealHTTPUpstream(cfg), nil, nil, &readable409UsageRepo{}, accounts...)
				h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				h.maxAccountSwitches = 0
				if mode == "current_skip" {
					h.errorPassthroughService = service.NewErrorPassthroughService(&errorPassthroughRepoStub{rules: []*model.ErrorPassthroughRule{{
						ID: 1085, Name: "fixture current terminal skip", Enabled: true, Priority: 1,
						Keywords: []string{issue1085Code}, MatchMode: model.MatchModeAny,
						Platforms: []string{service.PlatformOpenAI}, PassthroughCode: true, PassthroughBody: true, SkipMonitoring: true,
					}}}, nil)
				}
				repo := &ingressRejectOpsRepo{}
				ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				router.Use(OpsErrorLoggerMiddleware(ops))
				router.POST("/v1/responses", func(c *gin.Context) {
					groupID := int64(3131)
					c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, Key: "fixture-client", GroupID: &groupID,
						Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}, User: &service.User{ID: 100}})
					c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100})
					if mode == "stale_account_auth" || mode == "success_after_attempt" || mode == "request_override" || mode == "stale_skip" || mode == "hidden_history_success" {
						c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
							AccountID: 42, Stage: string(service.GatewayFailureStageAccountAuth),
							Message: "fixture earlier credential failure", Detail: "fixture earlier detail",
							SkipMonitoring: mode == "stale_skip" || mode == "hidden_history_success",
						}})
						if mode == "stale_skip" || mode == "hidden_history_success" {
							c.Set(service.OpsSkipPassthroughKey, true)
						}
					}
					h.Responses(c)
					if mode == "request_override" {
						service.MarkOpsRequestScopedError(c, "invalid_request_error")
					}
				})
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","input":"fixture input","stream":true}`))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(rec, req)
				require.Equal(t, int32(1), sends.Load(), "attribution must not introduce replay")
				issue1085FlushRecords()
				if mode == "hidden_history_success" || mode == "current_skip" {
					require.Empty(t, repo.entries, "historical skip policy still applies to recovered history")
					return
				}
				require.Len(t, repo.entries, 1)
				entry := repo.entries[0]
				if mode == "success_after_attempt" {
					require.Equal(t, 200, entry.StatusCode, "history must not become a final failure")
					require.Equal(t, "provider", entry.ErrorOwner)
					require.Equal(t, "upstream_http", entry.ErrorSource)
					require.Equal(t, "account_auth", entry.ErrorPhase)
					require.NotNil(t, entry.UpstreamErrorMessage)
					require.NotNil(t, entry.UpstreamStatusCode)
					require.Equal(t, "fixture earlier credential failure", *entry.UpstreamErrorMessage)
					require.Equal(t, 0, *entry.UpstreamStatusCode)
					require.NotNil(t, entry.UpstreamErrorDetail)
					require.Equal(t, "fixture earlier detail", *entry.UpstreamErrorDetail)
					require.NotNil(t, entry.UpstreamErrorsJSON)
					history, err := service.ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
					require.NoError(t, err)
					require.Len(t, history, 1)
					require.Equal(t, int64(42), history[0].AccountID)
					require.NotContains(t, entry.ErrorBody, issue1085Code)
					return
				}
				if mode == "malformed" || mode == "unknown" {
					require.Equal(t, 502, entry.StatusCode, "unknown terminal keeps the existing status fallback")
					require.Contains(t, rec.Body.String(), "response.failed")
					return
				}
				if mode == "http400" {
					require.Equal(t, 400, rec.Code)
				} else {
					require.Equal(t, 200, rec.Code)
					require.Contains(t, rec.Body.String(), "event: response.failed")
					require.True(t, strings.HasSuffix(rec.Body.String(), "\n\n"), "terminal must complete without a disconnect")
				}
				require.Equal(t, 400, entry.StatusCode)
				if mode != "http400" || !passthrough {
					require.Equal(t, "invalid_request_error", entry.ErrorType)
					require.Contains(t, entry.ErrorBody, issue1085Code)
				}
				if mode == "request_override" {
					require.Equal(t, "client", entry.ErrorOwner)
					require.Equal(t, "client_request", entry.ErrorSource)
					require.Equal(t, "request", entry.ErrorPhase)
					return
				}
				require.Equal(t, "provider", entry.ErrorOwner, "ISSUE1085_TERMINAL_OWNER")
				require.Equal(t, "upstream_http", entry.ErrorSource)
				require.Equal(t, "upstream", entry.ErrorPhase)
				require.NotNil(t, entry.UpstreamStatusCode)
				require.Equal(t, 400, *entry.UpstreamStatusCode)
				if mode != "http400" {
					require.NotNil(t, entry.UpstreamErrorMessage)
					require.Equal(t, issue1085Message, *entry.UpstreamErrorMessage)
				}
				if mode == "stale_account_auth" || mode == "stale_skip" {
					require.NotNil(t, entry.UpstreamErrorsJSON)
					history, err := service.ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
					require.NoError(t, err)
					require.Len(t, history, 1)
					require.Equal(t, int64(42), history[0].AccountID)
					require.Equal(t, string(service.GatewayFailureStageAccountAuth), history[0].Stage)
					require.Nil(t, entry.UpstreamErrorDetail)
				}
				if mode == "privacy" {
					require.NotContains(t, entry.ErrorMessage, "fixture-canary-1085-secret")
					if entry.UpstreamErrorMessage != nil {
						require.NotContains(t, *entry.UpstreamErrorMessage, "fixture-canary-1085-secret")
					}
					if entry.UpstreamErrorDetail != nil {
						require.NotContains(t, *entry.UpstreamErrorDetail, "fixture-canary-1085-secret")
					}
					if entry.UpstreamErrorsJSON != nil {
						require.NotContains(t, *entry.UpstreamErrorsJSON, "fixture-canary-1085-secret")
					}
					require.NotContains(t, entry.ErrorBody, "fixture-canary-1085-secret")
					require.Contains(t, entry.ErrorBody, "[REDACTED]")
				}
			})
		}
	}
}

func TestIssue1085LocalSSEAttribution(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%v", stale), func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 8)
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				if stale {
					service.SetOpsUpstreamError(c, 503, "fixture earlier failure", "")
					service.MarkOpsStreamErrorValue(c, service.OpsStreamError{
						ErrType: "invalid_request_error", Code: issue1085Code, Message: issue1085Message,
						IntendedStatus: 400, RequestScoped: true,
					})
				}
				c.Header("Content-Type", "text/event-stream")
				_, _ = io.WriteString(c.Writer, issue1085Created+issue1085Failed)
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			issue1085FlushRecords()
			require.NotEmpty(t, repo.entries)
			entry := repo.entries[0]
			require.Equal(t, 400, entry.StatusCode)
			require.Equal(t, "client", entry.ErrorOwner)
			require.Equal(t, "client_request", entry.ErrorSource)
			require.Equal(t, "request", entry.ErrorPhase)
			if stale {
				require.Nil(t, entry.UpstreamStatusCode)
			}
		})
	}
}
