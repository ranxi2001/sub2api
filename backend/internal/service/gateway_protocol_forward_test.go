//go:build unit

package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayProtocolTransport struct {
	anthropicHTTPUpstreamRecorder
	accountID   int64
	concurrency int
	proxyURL    string
}

func (u *gatewayProtocolTransport) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.accountID, u.concurrency = id, concurrency
	u.proxyURL = proxy
	return u.anthropicHTTPUpstreamRecorder.Do(req, proxy, id, concurrency)
}

func (u *gatewayProtocolTransport) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestGatewayProtocolAnthropicBaseVersionsReachNativeBuilder(t *testing.T) {
	for _, target := range []string{PlatformAnthropic, PlatformAntigravity} {
		for _, prefix := range []string{"", "/tenant"} {
			for _, suffix := range []string{"", "/v1", "/v1/", "/v1beta"} {
				t.Run(target+prefix+suffix, func(t *testing.T) {
					repo, ctx := gatewayProtocolFixture(target)
					base := "https://gateway.example" + prefix + suffix
					repo.account.Credentials["base_url"] = base
					view, err := prepareGatewayProtocolAccount(ctx, repo.account, repo, domain.PlatformGatewayAnthropic, "public-model")
					require.NoError(t, err)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					svc := &GatewayService{cfg: rawChatCompletionsTestConfig()}
					req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, view, []byte(`{"model":"public-model","messages":[]}`), "synthetic-only")
					require.NoError(t, err)
					require.Equal(t, prefix+"/v1/messages", req.URL.Path)
					require.Equal(t, "gateway.example", req.URL.Host)
					require.Equal(t, base, repo.account.GetCredential("base_url"), "stored site and tenant prefix stay intact")
				})
			}
		}
	}
}

func TestGatewayProtocolForwardUsesNativeBuildersAndSharedIdentity(t *testing.T) {
	for _, route := range []string{"anthropic", "openai", "gemini", "antigravity-messages", "antigravity-gemini"} {
		t.Run(route, func(t *testing.T) {
			target := route
			if strings.HasPrefix(route, "antigravity-") {
				target = PlatformAntigravity
			}
			repo, ctx := gatewayProtocolFixture(target)
			repo.account.Credentials["model_mapping"] = map[string]any{"public-model": "upstream-model"}
			body := []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}],"max_tokens":16,"stream":false}`)
			path, content := "/v1/messages", `{"id":"fixture","type":"message","model":"upstream-model","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":2}}`
			if route == "openai" {
				path = "/v1/responses"
				body = []byte(`{"model":"public-model","input":"hello","stream":false}`)
				content = `{"id":"resp_fixture","object":"response","status":"completed","model":"upstream-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`
			}
			if route == "gemini" || route == "antigravity-gemini" {
				path = "/v1beta/models/public-model:generateContent"
				body = []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
				content = `{"candidates":[{"content":{"parts":[{"text":"OK"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`
			}
			if strings.HasPrefix(route, "antigravity-") {
				path = "/antigravity" + path
			}
			upstream := &gatewayProtocolTransport{anthropicHTTPUpstreamRecorder: anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"fixture-id"}}, Body: io.NopCloser(strings.NewReader(content))}}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
			c.Request.Host = "inbound-only.invalid"
			c.Request.Header.Set("Authorization", "Bearer inbound-must-not-leak")
			cfg := &config.Config{}
			var err error
			switch route {
			case "anthropic":
				svc := &GatewayService{cfg: cfg, accountRepo: repo, httpUpstream: upstream,
					responseHeaderFilter: compileResponseHeaderFilter(cfg), rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}}
				_, err = svc.Forward(ctx, c, repo.account, &ParsedRequest{Body: NewRequestBodyRef(body), Model: "public-model"})
			case "openai":
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), accountRepo: &gatewayProtocolOpenAIRepo{gatewayProtocolAdmissionRepo: repo}, httpUpstream: upstream, requireLatestTurnAdmission: true,
					rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
				_, err = svc.Forward(ctx, c, repo.account, body)
			case "gemini":
				svc := &GeminiMessagesCompatService{cfg: cfg, accountRepo: repo, httpUpstream: upstream, rateLimitService: &RateLimitService{}}
				_, err = svc.ForwardNative(ctx, c, repo.account, "public-model", "generateContent", false, body)
			case "antigravity-messages":
				svc := &AntigravityGatewayService{accountRepo: repo, httpUpstream: upstream, rateLimitService: &RateLimitService{}, settingService: &SettingService{cfg: cfg}}
				_, err = svc.Forward(ctx, c, repo.account, body, false)
			case "antigravity-gemini":
				svc := &AntigravityGatewayService{accountRepo: repo, httpUpstream: upstream, rateLimitService: &RateLimitService{}, settingService: &SettingService{cfg: cfg}}
				_, err = svc.ForwardGemini(ctx, c, repo.account, "public-model", "generateContent", false, body, false)
			}
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq, "real service entry must reach its existing native upstream builder")
			require.Equal(t, "gateway.example", upstream.lastReq.URL.Host)
			if route == "gemini" || route == "antigravity-gemini" {
				require.Equal(t, "/tenant/v1beta/models/upstream-model:generateContent", upstream.lastReq.URL.Path)
				require.Equal(t, "synthetic-only", upstream.lastReq.Header.Get("x-goog-api-key"))
			} else {
				want := "/tenant/v1/messages"
				if route == "openai" {
					want = "/tenant/v1/responses"
				}
				require.Equal(t, want, upstream.lastReq.URL.Path)
				require.Contains(t, string(upstream.lastBody), "upstream-model")
				require.NotContains(t, upstream.lastReq.Header.Get("Authorization"), "inbound-must-not-leak")
			}
			require.Equal(t, AccountTypeGateway, repo.account.Type)
			require.Equal(t, PlatformGateway, repo.account.Platform)
			require.Equal(t, int64(959), repo.account.ID)
			require.Equal(t, int64(959), upstream.accountID, "all native transports share one account key and connection budget")
			require.Equal(t, 2, upstream.concurrency)
			require.NotEmpty(t, recorder.Body.String())
		})
	}
}

func TestGatewayProtocolForwardRejectsRemovedMembershipBeforeTransport(t *testing.T) {
	for _, route := range []string{"anthropic", "openai", "gemini", "antigravity-messages", "antigravity-gemini"} {
		t.Run(route, func(t *testing.T) {
			target := route
			if strings.HasPrefix(route, "antigravity-") {
				target = PlatformAntigravity
			}
			repo, ctx := gatewayProtocolFixture(target)
			selected := *repo.account
			repo.account = &Account{ID: selected.ID, Platform: PlatformGateway, Type: AccountTypeGateway,
				Status: StatusActive, Schedulable: true, Credentials: selected.Credentials}
			upstream := &gatewayProtocolTransport{}
			body := []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)).WithContext(ctx)
			var err error
			switch route {
			case "anthropic":
				_, err = (&GatewayService{accountRepo: repo, httpUpstream: upstream}).Forward(ctx, c, &selected, &ParsedRequest{Model: "public-model", Body: NewRequestBodyRef(body)})
			case "openai":
				_, err = (&OpenAIGatewayService{accountRepo: repo, httpUpstream: upstream}).Forward(ctx, c, &selected, body)
			case "gemini":
				_, err = (&GeminiMessagesCompatService{accountRepo: repo, httpUpstream: upstream}).ForwardNative(ctx, c, &selected, "public-model", "generateContent", false, body)
			case "antigravity-messages":
				_, err = (&AntigravityGatewayService{accountRepo: repo, httpUpstream: upstream}).Forward(ctx, c, &selected, body, false)
			case "antigravity-gemini":
				_, err = (&AntigravityGatewayService{accountRepo: repo, httpUpstream: upstream}).ForwardGemini(ctx, c, &selected, "public-model", "generateContent", false, body, false)
			}
			require.ErrorIs(t, err, ErrGatewayAccountGroup)
			require.Nil(t, upstream.lastReq, "stale selection cannot leak a key after membership removal")
		})
	}
}
