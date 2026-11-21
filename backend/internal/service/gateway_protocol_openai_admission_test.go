//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Both production reader capabilities are required; a supplied-account-only
// fixture cannot exercise the final authoritative OpenAI send boundary.
type gatewayProtocolOpenAIRepo struct {
	*gatewayProtocolAdmissionRepo
	turnReads int
	changeAt  int
	change    func(*Account)
}

func (r *gatewayProtocolOpenAIRepo) GetOpenAITurnAdmission(context.Context, int64) (*Account, *Account, error) {
	r.turnReads++
	a := *r.account
	a.Credentials = maps.Clone(a.Credentials)
	if r.change != nil && r.turnReads >= r.changeAt {
		r.change(&a)
	}
	return &a, nil, nil
}

func gatewayProtocolOpenAIForward(t *testing.T, route string, stream bool, repo *gatewayProtocolOpenAIRepo, ctx context.Context) (*OpenAIForwardResult, *gatewayProtocolTransport, *httptest.ResponseRecorder, error) {
	t.Helper()
	path := "/v1/responses"
	body := []byte(`{"model":"public-model","input":"hello","stream":false}`)
	if stream {
		body = []byte(`{"model":"public-model","input":"hello","stream":true}`)
	}
	if route == "compact" {
		path += "/compact"
	}
	if route == "chat" || route == "messages" {
		path = "/v1/chat/completions"
		body = []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}],"max_tokens":16,"stream":false}`)
		if route == "messages" {
			path = "/v1/messages"
		}
	}
	content := `{"id":"resp_fixture","object":"response","status":"completed","model":"upstream-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`
	contentType := "application/json"
	// Chat/Messages bridges always request upstream Responses SSE, including
	// buffered downstream clients. Their fixture must supply a terminal event.
	if stream || route == "chat" || route == "messages" {
		contentType = "text/event-stream"
		content = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":" + content + "}\n\ndata: [DONE]\n\n"
	}
	if route == "compact" {
		if mapped, ok := repo.account.ResolveCompactMappedModel("public-model"); ok {
			content = strings.ReplaceAll(content, "upstream-model", mapped)
		}
	}
	upstream := &gatewayProtocolTransport{anthropicHTTPUpstreamRecorder: anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(content))}}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
	groupID := repo.group.ID
	c.Set("api_key", &APIKey{GroupID: &groupID})
	cfg := rawChatCompletionsTestConfig()
	svc := &OpenAIGatewayService{cfg: cfg, accountRepo: repo, httpUpstream: upstream,
		requireLatestTurnAdmission: true, rateLimitService: &RateLimitService{}, deferredService: &DeferredService{}, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	var result *OpenAIForwardResult
	var err error
	switch route {
	case "chat":
		result, err = svc.ForwardAsChatCompletions(ctx, c, repo.account, body, "", "")
	case "messages":
		result, err = svc.ForwardAsAnthropic(ctx, c, repo.account, body, "", "")
	default:
		result, err = svc.Forward(ctx, c, repo.account, body)
	}
	return result, upstream, recorder, err
}

func TestGatewayProtocolOpenAIAuthoritativeAdmission(t *testing.T) {
	for _, route := range []string{"responses", "chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			base, ctx := gatewayProtocolFixture(PlatformOpenAI)
			base.account.Credentials["model_mapping"] = map[string]any{"public-model": "upstream-model"}
			base.account.AccountGroups[0].AllowedModels = []string{"public-model"}
			proxyID := int64(95)
			base.account.ProxyID = &proxyID
			base.account.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.invalid", Port: 8080}
			repo := &gatewayProtocolOpenAIRepo{gatewayProtocolAdmissionRepo: base}
			result, upstream, _, err := gatewayProtocolOpenAIForward(t, route, false, repo, ctx)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.GreaterOrEqual(t, repo.turnReads, 2, "initial and actual-send admission must both read authority")
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, "upstream-model", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, route != "responses", gjson.GetBytes(upstream.lastBody, "stream").Bool())
			require.False(t, result.Stream, "buffered downstream stays buffered while bridges consume upstream SSE")
			require.Equal(t, "public-model", result.Model)
			require.Equal(t, "upstream-model", result.UpstreamModel)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, int64(959), upstream.accountID)
			require.Equal(t, 2, upstream.concurrency)
			require.Equal(t, "http://proxy.invalid:8080", upstream.proxyURL)
			require.Equal(t, PlatformGateway, repo.account.Platform)
			require.False(t, repo.account.IsSchedulable(), "the scheduler stays fail closed")
		})
	}
}

func TestGatewayProtocolOpenAIRejectsFinalAuthorityChanges(t *testing.T) {
	for _, route := range []string{"responses", "chat", "messages"} {
		for _, change := range []string{"membership", "disabled", "credential", "platform", "cooldown", "disabled-group", "oauth-only"} {
			t.Run(route+"/"+change, func(t *testing.T) {
				base, ctx := gatewayProtocolFixture(PlatformOpenAI)
				repo := &gatewayProtocolOpenAIRepo{gatewayProtocolAdmissionRepo: base, changeAt: 2, change: func(a *Account) {
					switch change {
					case "membership":
						a.GroupIDs, a.AccountGroups = nil, nil
					case "disabled":
						a.Status = StatusDisabled
					case "credential":
						a.Credentials["api_key"] = "rotated-synthetic-only"
					case "platform":
						a.Platform, a.Type = PlatformOpenAI, AccountTypeAPIKey
					case "cooldown":
						a.Schedulable = false
					case "disabled-group":
						base.group.Status = StatusDisabled
					case "oauth-only":
						base.group.RequireOAuthOnly = true
					}
				}}
				_, upstream, _, err := gatewayProtocolOpenAIForward(t, route, false, repo, ctx)
				require.Error(t, err)
				require.GreaterOrEqual(t, repo.turnReads, 2)
				require.Nil(t, upstream.lastReq, "a later authoritative rejection must precede transport")
			})
		}
	}
}

func TestGatewayProtocolOpenAILegacyPassthroughKeepsWireModel(t *testing.T) {
	base, ctx := gatewayProtocolFixture(PlatformOpenAI)
	base.account.Platform, base.account.Type = PlatformOpenAI, AccountTypeAPIKey
	base.account.Extra = map[string]any{"openai_passthrough": true}
	base.account.Credentials["model_mapping"] = map[string]any{"public-model": "upstream-model"}
	repo := &gatewayProtocolOpenAIRepo{gatewayProtocolAdmissionRepo: base}
	result, upstream, _, err := gatewayProtocolOpenAIForward(t, "responses", false, repo, ctx)
	require.NoError(t, err)
	require.Equal(t, "public-model", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "public-model", result.Model)
	require.Empty(t, result.UpstreamModel)
}

func TestGatewayProtocolOpenAIResponsesMappedWireModel(t *testing.T) {
	for _, scenario := range []struct {
		name, route string
		stream      bool
	}{
		{"nonstream", "responses", false}, {"stream", "responses", true}, {"compact", "compact", false}, {"compact-override", "compact", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			base, ctx := gatewayProtocolFixture(PlatformOpenAI)
			base.account.Credentials["model_mapping"] = map[string]any{"public-model": "upstream-model"}
			wantModel := "upstream-model"
			if scenario.name == "compact-override" {
				wantModel = "compact-upstream-model"
				base.account.Credentials["compact_model_mapping"] = map[string]any{"public-model": wantModel}
			}
			repo := &gatewayProtocolOpenAIRepo{gatewayProtocolAdmissionRepo: base}
			result, upstream, recorder, err := gatewayProtocolOpenAIForward(t, scenario.route, scenario.stream, repo, ctx)
			require.NoError(t, err)
			require.Equal(t, wantModel, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, scenario.stream, gjson.GetBytes(upstream.lastBody, "stream").Bool())
			require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "input").String())
			wantPath := "/tenant/v1/responses"
			if scenario.route == "compact" {
				wantPath += "/compact"
			}
			require.Equal(t, wantPath, upstream.lastReq.URL.Path)
			require.Equal(t, "public-model", result.Model)
			require.Equal(t, wantModel, result.UpstreamModel)
			require.Empty(t, result.BillingModel, "retain existing passthrough public-model billing")
			require.Contains(t, recorder.Body.String(), "public-model")
			require.Equal(t, 10, result.Usage.InputTokens)
		})
	}
}
