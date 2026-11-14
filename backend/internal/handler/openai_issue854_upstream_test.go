//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIssue854UpstreamOwnerRateLimit(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, code := range []string{"encrypted_agent_recovery_owner_rate_limited", "encrypted_agent_recovery_untrusted_fixture"} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, code), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 4)
				var sends atomic.Int32
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sends.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil || gjson.GetBytes(body, "model").String() != "gpt-5.1" {
						t.Error("invalid fixture provider request")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = fmt.Fprintf(w, `{"error":{"type":"rate_limit_error","code":%q,"message":"fixture-private-provider-diagnostic","param":"fixture-private-param"}}`, code)
				}))
				defer provider.Close()
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				accounts := issue715AccountsOfType(service.AccountTypeAPIKey)
				for i := range accounts {
					accounts[i].Credentials = map[string]any{"base_url": provider.URL, "api_key": "fixture-owner-limit"}
				}
				h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, testutil.NewRealHTTPUpstream(cfg), nil, nil, &readable409UsageRepo{}, accounts...)
				h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				h.maxAccountSwitches = 1
				key := &service.APIKey{ID: 99, UserID: 100, Key: "fixture-issue854-client", Status: service.StatusActive, AllowLongContext: true,
					User:  &service.User{ID: 100, Status: service.StatusActive},
					Group: &service.Group{ID: 3132, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}}
				key.GroupID = &key.Group.ID
				h.apiKeyService = service.NewAPIKeyService(&wsRevalidationKeyRepoStub{current: key}, nil, nil, nil, nil, nil, cfg)
				repo := &ingressRejectOpsRepo{}
				ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				done := make(chan struct{}, 1)
				router := gin.New()
				router.Use(func(c *gin.Context) { c.Next(); done <- struct{}{} })
				router.Use(OpsErrorLoggerMiddleware(ops))
				router.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(h.apiKeyService, nil, cfg)))
				router.POST("/v1/responses", h.Responses)
				gateway := httptest.NewServer(router)
				defer gateway.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"gpt-5.1","stream":%v,"input":"fixture continuation"}`, stream)))
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+key.Key)
				req.Header.Set("Content-Type", "application/json")
				res, err := gateway.Client().Do(req)
				require.NoError(t, err)
				body, err := io.ReadAll(res.Body)
				_ = res.Body.Close()
				require.NoError(t, err)
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("rate limit handler did not finish")
				}
				require.Equal(t, 429, res.StatusCode)
				require.GreaterOrEqual(t, sends.Load(), int32(1))
				require.LessOrEqual(t, sends.Load(), int32(2), "rejection recovery is bounded")
				if code == "encrypted_agent_recovery_owner_rate_limited" {
					require.Equal(t, code, gjson.GetBytes(body, "error.code").String(), "ISSUE854_OWNER_LIMIT_CODE_LOST")
				} else {
					require.NotContains(t, string(body), code, "arbitrary recovery-prefixed codes are not trusted")
				}
				require.NotContains(t, string(body), "fixture-private")
				for len(opsErrorLogQueue) > 0 {
					flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
				}
				require.Len(t, repo.entries, 1)
				require.Equal(t, 429, repo.entries[0].StatusCode)
				if code == "encrypted_agent_recovery_owner_rate_limited" {
					require.Equal(t, code, gjson.Get(repo.entries[0].ErrorBody, "error.code").String())
				}
			})
		}
	}
}
