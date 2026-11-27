//go:build unit

package routes

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
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
			cfg := &config.Config{RunMode: config.RunModeSimple}
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
