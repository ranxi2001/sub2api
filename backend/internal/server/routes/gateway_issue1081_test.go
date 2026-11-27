//go:build unit

package routes

import (
	"context"

	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type issue1081RoutePolicy struct{ enabled bool }

func (p *issue1081RoutePolicy) APIKeyMultiGroupEnabled(context.Context) (bool, error) {
	return p.enabled, nil
}

// Real registered routes and real auth run before a terminal, offline transport
// boundary. It inspects the auth result and never invokes paid upstreams.
func TestIssue1081RegisteredProtocolAuthUsesPrimaryOnly(t *testing.T) {
	for _, tc := range []struct{ method, path, platform string }{
		{"POST", "/v1/responses", service.PlatformOpenAI}, {"GET", "/v1/responses", service.PlatformOpenAI},
		{"POST", "/responses", service.PlatformOpenAI}, {"GET", "/responses", service.PlatformOpenAI},
		{"POST", "/backend-api/codex/responses", service.PlatformOpenAI}, {"GET", "/backend-api/codex/responses", service.PlatformOpenAI},
		{"POST", "/v1/messages", service.PlatformAnthropic}, {"POST", "/v1/chat/completions", service.PlatformOpenAI},
		{"POST", "/chat/completions", service.PlatformOpenAI}, {"POST", "/v1/images/generations", service.PlatformOpenAI},
		{"POST", "/v1/videos", service.PlatformOpenAI}, {"POST", "/v1beta/models/gemini-fixture:generateContent", service.PlatformGemini},
		{"POST", "/v1beta/models/gemini-fixture:streamGenerateContent", service.PlatformGemini},
		{"POST", "/antigravity/v1/messages", service.PlatformAntigravity}, {"POST", "/antigravity/v1beta/models/gemini-fixture:generateContent", service.PlatformAntigravity},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			f := newUnionFixture()
			f.groups.groups[1].Platform = tc.platform
			cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{MaxBodySize: 1 << 20, TextMaxBodySize: 1 << 20}}
			keys := service.NewAPIKeyService(&unionKeys{key: f.key}, nil, f.groups, f.subs, f.rates, nil, cfg)
			keys.SetGroupPolicy(&issue1081RoutePolicy{})
			gateway := service.NewGatewayService(f.accounts, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h := &handler.Handlers{Gateway: handler.NewGatewayHandler(gateway, nil, nil, nil, nil, nil, nil, nil, keys, nil, nil, nil, nil, cfg, nil), OpenAIGateway: handler.NewOpenAIGatewayHandler(nil, nil, nil, nil, nil, nil, nil, nil, cfg), AsyncImage: handler.NewAsyncImageHandler(nil, nil)}
			r := gin.New()
			reached := false
			r.Use(func(c *gin.Context) {
				c.Next()
				if key, ok := middleware.GetAPIKeyFromContext(c); ok {
					reached = true
					require.Equal(t, int64(1), *key.GroupID)
					require.Equal(t, []int64{1}, key.ConfiguredGroupIDs())
				}
			})
			auth := middleware.APIKeyAuthMiddleware(func(c *gin.Context) {
				middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)(c)
			})
			RegisterGatewayRoutes(r, h, auth, keys, nil, nil, nil, nil, cfg)
			// POSTs terminate at the real allowlist; GET WS handshakes terminate at
			// the real 426 upgrade-required response. Neither opens upstream transport.
			f.groups.groups[1].ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"allowed-fixture"}}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"model":"blocked-fixture","input":"fixture","messages":[]}`))
			req.Header.Set("Authorization", "Bearer fixture-key")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.True(t, reached, "real route must authenticate: %d %s", w.Code, w.Body.String())
			require.Less(t, w.Code, 500, w.Body.String())
			require.Equal(t, []int64{1, 2}, f.key.GroupIDs)
		})
	}
}

type issue1081SchedulingAccounts struct {
	service.AccountRepository
	groups         map[int64][]service.Account
	selectedGroups []int64
}

func (r *issue1081SchedulingAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, accounts := range r.groups {
		for _, a := range accounts {
			if a.ID == id {
				return &a, nil
			}
		}
	}
	return nil, service.ErrNoAvailableAccounts
}
func (r *issue1081SchedulingAccounts) GetOpenAITurnAdmission(ctx context.Context, id int64) (*service.Account, *service.Account, error) {
	a, err := r.GetByID(ctx, id)
	return a, nil, err
}
func (r *issue1081SchedulingAccounts) ListSchedulableByGroupIDAndPlatform(_ context.Context, id int64, _ string) ([]service.Account, error) {
	r.selectedGroups = append(r.selectedGroups, id)
	return r.groups[id], nil
}
func (r *issue1081SchedulingAccounts) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, id int64, _ []string) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, id, "")
}
func (r *issue1081SchedulingAccounts) ListByGroup(_ context.Context, id int64) ([]service.Account, error) {
	return r.groups[id], nil
}
func (r *issue1081SchedulingAccounts) ListSchedulableByGroupID(ctx context.Context, id int64) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, id, "")
}

type issue1081RoutingUpstream struct {
	service.HTTPUpstream
	accounts []int64
}

func (u *issue1081RoutingUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.accounts = append(u.accounts, id)
	body := `{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	contentType := "application/json"
	if !strings.HasSuffix(req.URL.Path, "/chat/completions") {
		contentType = "text/event-stream"
		body = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"done\",\"output_index\":0,\"content_index\":0}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"fixture-model\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func (u *issue1081RoutingUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestIssue1081RealRoutingAndTransportRoundTrip(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses", "/v1/chat/completions", "/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			f := newUnionFixture()
			f.groups.groups[1].AllowMessagesDispatch = true
			f.groups.groups[2].AllowMessagesDispatch = true
			cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{MaxBodySize: 1 << 20, TextMaxBodySize: 1 << 20}}
			accounts := &issue1081SchedulingAccounts{groups: map[int64][]service.Account{}}
			for groupID := int64(1); groupID <= 2; groupID++ {
				accounts.groups[groupID] = []service.Account{{ID: groupID + 100, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Credentials: map[string]any{"api_key": "fixture-upstream", "model_mapping": map[string]any{"fixture-model": "fixture-model"}}, Extra: map[string]any{"openai_responses_supported": true}}}
			}
			upstream := &issue1081RoutingUpstream{}
			keys := service.NewAPIKeyService(&unionKeys{key: f.key}, nil, f.groups, f.subs, f.rates, nil, cfg)
			policy := &issue1081RoutePolicy{}
			keys.SetGroupPolicy(policy)
			concurrency := service.NewConcurrencyService(nil)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			// Account selection must honor the group chosen by the policy.
			schedulerCfg := *cfg
			schedulerCfg.RunMode = config.RunModeStandard
			openAI := service.NewOpenAIGatewayService(accounts, nil, nil, nil, nil, nil, nil, nil, &schedulerCfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
			gateway := service.NewGatewayService(accounts, f.groups, nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeStandard}, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h := &handler.Handlers{Gateway: handler.NewGatewayHandler(gateway, openAI, nil, nil, nil, concurrency, billing, nil, keys, nil, nil, nil, nil, cfg, nil), OpenAIGateway: handler.NewOpenAIGatewayHandler(openAI, concurrency, billing, keys, nil, nil, nil, nil, cfg), AsyncImage: handler.NewAsyncImageHandler(nil, nil)}
			r := gin.New()
			RegisterGatewayRoutes(r, h, middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg), keys, nil, nil, nil, nil, cfg)
			// ConfigureAPIKeyGroupRouting installed the real GatewayService prober.
			for i, enabled := range []bool{false, true, false} {
				policy.enabled = enabled
				body := `{"model":"fixture-model","input":"fixture","messages":[{"role":"user","content":"fixture"}],"max_tokens":32,"stream":false}`
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer fixture-key")
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Len(t, upstream.accounts, i+1, "one real transport invocation per request")
				expected := int64(101)
				if enabled {
					expected = 102
				}
				require.Equal(t, expected, upstream.accounts[i])
			}
			require.Equal(t, []int64{1, 2}, f.key.GroupIDs)
		})
	}
}
