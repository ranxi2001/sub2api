package routes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestGatewayRoutesGroupStreamOnlyMountedOnEveryGatewayChain 按源码断言每条网关链都在模型白名单之后、
// 合成路由改写（或分组校验）之前挂上 groupStreamOnly。rootRoute 与 codexDirect 两条链的完整顺序
// 由 TestGatewayRoutesGroupModelAllowlistMountedOnEveryGatewayRoute 逐字断言。
func TestGatewayRoutesGroupStreamOnlyMountedOnEveryGatewayChain(t *testing.T) {
	routeSource, err := os.ReadFile("gateway.go")
	require.NoError(t, err)
	source := string(routeSource)

	for _, chain := range []struct{ group, next string }{
		{group: "gateway", next: "gateway.Use(compositeTarget)"},
		{group: "gemini", next: "gemini.Use(compositeGeminiTarget)"},
		{group: "antigravityV1", next: "antigravityV1.Use(requireGroupAnthropic)"},
		{group: "antigravityV1Beta", next: "antigravityV1Beta.Use(requireGroupGoogle)"},
	} {
		allowlistChain := chain.group + ".Use(groupModelAllowlist)"
		if chain.group == "gateway" || chain.group == "antigravityV1" {
			allowlistChain = chain.group + ".Use(groupModelAllowlist, apiKeyModelPolicy)"
		}
		re := regexp.MustCompile(
			regexp.QuoteMeta(allowlistChain) +
				`\s*` + regexp.QuoteMeta(chain.group+".Use(groupCodexCLIOnly, groupBlockSubagents)") +
				`\s*` + regexp.QuoteMeta(chain.group+".Use(groupStreamOnly)") +
				`[\s\S]{0,200}?` + regexp.QuoteMeta(chain.next))
		require.Regexp(t, re, source, "%s chain must mount groupStreamOnly right after the allowlist", chain.group)
	}
}

// TestGatewayRoutesGroupStreamOnlyRejectsNonStreamRequests 走真实路由表：非流式请求在调度前被拒绝，
// 合成分组也在改写之前就拒绝。
func TestGatewayRoutesGroupStreamOnlyRejectsNonStreamRequests(t *testing.T) {
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformOpenAI, service.PlatformComposite} {
		router := newGatewayRoutesTestRouterWithGroup(&service.Group{Platform: platform, StreamOnly: true})
		for _, tc := range []struct{ path, body, want string }{
			{"/v1/messages", `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[]}`, `"type":"error"`},
			{"/antigravity/v1/messages", `{"model":"claude-sonnet-4-5","stream":false}`, `"type":"error"`},
			{"/v1/chat/completions", `{"model":"gpt-5.4","stream":false,"messages":[]}`, "stream_required"},
			{"/chat/completions", `{"model":"gpt-5.4","messages":[]}`, "stream_required"},
			{"/v1/responses", `{"model":"gpt-5.4","input":"hi"}`, "stream_required"},
			{"/responses", `{"model":"gpt-5.4","stream":"true"}`, "stream_required"},
			{"/backend-api/codex/responses", `{"model":"gpt-5.4","stream":true,"stream":false}`, "stream_required"},
		} {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusBadRequest, w.Code, "%s %s", platform, tc.path)
			require.Contains(t, w.Body.String(), tc.want, "%s %s", platform, tc.path)
			require.Contains(t, w.Body.String(), "only accepts streaming requests", "%s %s", platform, tc.path)
		}
	}
}
