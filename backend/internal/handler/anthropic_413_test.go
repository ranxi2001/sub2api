//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The fixture sends a small synthetic request. The provider rejects it with
// HTTP 413, so neither a huge allocation nor a real provider is needed.
const anthropic413ClientMessage = "Request body is too large. Anthropic Messages requests are limited to 32 MiB. Reduce the request size and try again."

// Rewrite the destination at the transport boundary, including the hardcoded
// OAuth endpoint, so every auth variant uses only this local HTTP fixture.
type anthropic413HTTPUpstream struct {
	client   *http.Client
	target   *url.URL
	mu       sync.Mutex
	accounts []int64
}

func (u *anthropic413HTTPUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accounts = append(u.accounts, accountID)
	u.mu.Unlock()
	local := req.Clone(req.Context())
	local.URL.Scheme = u.target.Scheme
	local.URL.Host = u.target.Host
	local.Host = u.target.Host
	return u.client.Do(local)
}

func (u *anthropic413HTTPUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

type anthropic413AccountRepo struct {
	service.AccountRepository
	mu        sync.Mutex
	mutations []string
}

func (r *anthropic413AccountRepo) record(kind string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutations = append(r.mutations, kind)
	return nil
}

func (r *anthropic413AccountRepo) SetError(context.Context, int64, string) error {
	return r.record("error")
}

func (r *anthropic413AccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return r.record("temp_unschedulable")
}

func (r *anthropic413AccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return r.record("model_rate_limit")
}

func TestAnthropic413MessagesHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, variant := range []struct {
		name        string
		accountType string
		passthrough bool
		omit413     bool
		cooldown    bool
		override    string
	}{
		{name: "apikey", accountType: service.AccountTypeAPIKey},
		{name: "passthrough", accountType: service.AccountTypeAPIKey, passthrough: true},
		{name: "apikey_custom_codes", accountType: service.AccountTypeAPIKey, omit413: true},
		{name: "passthrough_custom_codes", accountType: service.AccountTypeAPIKey, passthrough: true, omit413: true},
		{name: "oauth", accountType: service.AccountTypeOAuth},
		{name: "setup_token", accountType: service.AccountTypeSetupToken},
		{name: "apikey_cooldown_rule", accountType: service.AccountTypeAPIKey, cooldown: true},
		{name: "oauth_cooldown_rule", accountType: service.AccountTypeOAuth, cooldown: true},
		{name: "apikey_custom_override", accountType: service.AccountTypeAPIKey, override: "custom"},
		{name: "passthrough_body_override", accountType: service.AccountTypeAPIKey, passthrough: true, override: "body"},
	} {
		t.Run(variant.name, func(t *testing.T) {
			for _, boundary := range []string{"nonstream", "stream", "headers", "ping", "partial"} {
				t.Run(boundary, func(t *testing.T) {
					stream := boundary != "nonstream"
					committed := boundary == "headers" || boundary == "ping" || boundary == "partial"
					provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						if len(body) > 256*1024 || !strings.Contains(string(body), "synthetic-413-input") || r.URL.Path != "/v1/messages" {
							t.Errorf("unexpected synthetic request: path=%s bytes=%d", r.URL.Path, len(body))
						}
						w.Header().Set("Content-Type", "text/html")
						w.Header().Set("X-Request-Id", "fixture-must-not-leak")
						w.Header().Set("Retry-After", "999")
						w.WriteHeader(http.StatusRequestEntityTooLarge)
						_, _ = io.WriteString(w, "<html>Request body exceeds the 32 MiB limit for Messages requests; fixture-must-not-leak</html>")
					}))
					t.Cleanup(provider.Close)
					target, err := url.Parse(provider.URL)
					require.NoError(t, err)
					upstream := &anthropic413HTTPUpstream{client: provider.Client(), target: target}
					cfg := &config.Config{RunMode: config.RunModeSimple}
					groupID := int64(4130)
					group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
					var accounts []*service.Account
					for _, id := range []int64{4131, 4132} {
						account := &service.Account{
							ID: id, Name: "synthetic-413", Platform: service.PlatformAnthropic, Type: variant.accountType,
							Status: service.StatusActive, Schedulable: true, Concurrency: 1,
							Credentials:   map[string]any{"api_key": "fixture-key", "access_token": "fixture-token"},
							Extra:         map[string]any{"anthropic_passthrough": variant.passthrough},
							AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID}},
						}
						if variant.omit413 {
							account.Credentials["custom_error_codes_enabled"] = true
							account.Credentials["custom_error_codes"] = []any{float64(401), float64(429), float64(500)}
						}
						if variant.cooldown {
							account.Credentials["temp_unschedulable_enabled"] = true
							account.Credentials["temp_unschedulable_rules"] = []any{map[string]any{
								"error_code": float64(413), "keywords": []any{"32 MiB"}, "duration_minutes": float64(10),
							}}
						}
						accounts = append(accounts, account)
					}
					snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
					repo := &anthropic413AccountRepo{}
					rateLimits := service.NewRateLimitService(repo, nil, cfg, nil, nil)
					gw := service.NewGatewayService(
						nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, cfg,
						snapshot, nil, nil, rateLimits, nil, nil, upstream,
						nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
					)
					billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
					t.Cleanup(billing.Stop)
					h := &GatewayHandler{
						gatewayService: gw, billingCacheService: billing, cfg: cfg, maxAccountSwitches: 1,
						concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
					}
					if variant.override != "" {
						rule := newBodyLimitPassthroughRule(http.StatusOK, "fixture-override-must-not-leak")
						rule.Platforms = []string{service.PlatformAnthropic}
						rule.PassthroughBody = variant.override == "body"
						rule.SkipMonitoring = true
						rules := &errorPassthroughRepoStub{}
						rules.rules = append(rules.rules, rule)
						h.errorPassthroughService = service.NewErrorPassthroughService(rules, nil)
					}
					rec := httptest.NewRecorder()
					body := fmt.Sprintf(`{"model":"claude-sonnet-4-5","max_tokens":16,"stream":%t,"messages":[{"role":"user","content":"synthetic-413-input"}]}`, stream)
					req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
					apiKey := &service.APIKey{ID: 4133, UserID: 4134, GroupID: &groupID, Group: group, Status: service.StatusActive,
						User: &service.User{ID: 4134, Balance: 100, Concurrency: 10}}
					prefix := ""
					setupOpsErrorLogTestQueue(t, 1)
					ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
					router := gin.New()
					router.Use(OpsErrorLoggerMiddleware(ops))
					var c *gin.Context
					router.POST("/v1/messages", func(handlerContext *gin.Context) {
						c = handlerContext
						c.Set(string(middleware.ContextKeyAPIKey), apiKey)
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
						if committed {
							c.Header("Content-Type", "text/event-stream")
							switch boundary {
							case "ping":
								prefix = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
							case "partial":
								prefix = partialMessageStartSSE
							}
							if prefix != "" {
								_, writeErr := io.WriteString(c.Writer, prefix)
								require.NoError(t, writeErr)
							}
							c.Writer.Flush()
						}
						h.Messages(c)
					})
					// Exercise actual ops middleware, selection, Forward and wire output.
					router.ServeHTTP(rec, req)

					if !committed {
						require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "anthropic_413_wire_status: upstream HTTP 413 must reach the client")
						require.Contains(t, rec.Result().Header.Get("Content-Type"), "application/json")
						require.JSONEq(t, fmt.Sprintf(`{"type":"error","error":{"type":"request_too_large","message":%q}}`, anthropic413ClientMessage), rec.Body.String())
					} else {
						require.Equal(t, http.StatusOK, rec.Code)
						require.Equal(t, "text/event-stream", rec.Result().Header.Get("Content-Type"))
						require.True(t, strings.HasPrefix(rec.Body.String(), prefix))
						terminal := strings.TrimPrefix(rec.Body.String(), prefix)
						var data []string
						for _, line := range strings.Split(terminal, "\n") {
							if strings.HasPrefix(line, "data: ") {
								data = append(data, strings.TrimPrefix(line, "data: "))
							} else {
								require.True(t, line == "" || line == "event: error", "unexpected terminal SSE line: %s", line)
							}
						}
						require.Len(t, data, 1, "exactly one terminal SSE error")
						require.JSONEq(t, fmt.Sprintf(`{"type":"error","error":{"type":"request_too_large","message":%q}}`, anthropic413ClientMessage), data[0])
					}
					require.NotContains(t, rec.Body.String(), "fixture-must-not-leak")
					require.Empty(t, rec.Result().Header.Get("X-Request-Id"))
					require.Empty(t, rec.Result().Header.Get("Retry-After"))
					require.Equal(t, int64(1), OpsErrorLogQueueLength(), "413 must remain observable even when an override requests skip_monitoring")
					entry := (<-opsErrorLogQueue).entry
					require.Equal(t, http.StatusRequestEntityTooLarge, entry.StatusCode, "ops records logical 413 even on an already committed HTTP 200 stream")
					require.Equal(t, "request_too_large", entry.ErrorType)
					require.Equal(t, "request", entry.ErrorPhase)
					require.Equal(t, "client", entry.ErrorOwner)
					require.Equal(t, "client_request", entry.ErrorSource)
					require.Equal(t, "P3", entry.Severity)
					require.True(t, entry.IsBusinessLimited)
					require.Nil(t, entry.AccountID, "client rejection must not be attributed to account health")
					require.Nil(t, entry.UpstreamStatusCode)
					require.Empty(t, entry.UpstreamErrors)
					require.NotContains(t, entry.ErrorMessage, "must-not-leak")
					phase, businessLimited, owner, _ := classifyOpsErrorLog(c, "request_too_large", anthropic413ClientMessage, "", http.StatusRequestEntityTooLarge)
					require.Equal(t, "request", phase, "request size must not be classified as an upstream outage")
					require.Equal(t, "client", owner, "request rejection must not pollute provider/group health")
					require.True(t, businessLimited, "SLA aggregation must exclude the request rejection even with upstream diagnostics")
					upstream.mu.Lock()
					defer upstream.mu.Unlock()
					require.Len(t, upstream.accounts, 1, "413 must not replay the payload or fail over to another account")
					repo.mu.Lock()
					defer repo.mu.Unlock()
					require.Empty(t, repo.mutations, "request size rejection must not change account health")
				})
			}
		})
	}
}
