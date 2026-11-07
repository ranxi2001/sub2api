//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Mistral's allow-list for DeepSeek-named models, from the live 400:
// "reasoning_effort 'medium' is not supported for this model; supported values:
// ['none', 'low', 'high', 'xhigh', 'max']".
var mistralDeepSeekAllowedEfforts = map[string]bool{"none": true, "low": true, "high": true, "xhigh": true, "max": true}

func TestMistralDeepSeekEffortMapping(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	for _, model := range []string{"deepseek-v4-1-flash-1017", "deepseek-flash", "deepseek-v4.1-flash", "DeepSeek-V4-1-Flash-1017", "deepseek_v4_1_flash"} {
		for _, tc := range []struct{ name, field, want string }{
			{"medium", `"medium"`, `"high"`},
			{"Medium case", `"Medium"`, `"high"`},
			{"medium padded", `" medium "`, `"high"`},
			{"minimal", `"minimal"`, `"low"`},
			{"none", `"none"`, `"none"`},
			{"low", `"low"`, `"low"`},
			{"high", `"high"`, `"high"`},
			{"xhigh", `"xhigh"`, `"xhigh"`},
			{"x-high", `"x-high"`, `"xhigh"`},
			{"extra_high", `"extra_high"`, `"xhigh"`},
			{"max", `"max"`, `"max"`},
			{"MAX", `"MAX"`, `"max"`},
			{"ultra", `"ultra"`, `"max"`},
			{"off", `"off"`, `"none"`},
			{"disabled", `"disabled"`, `"none"`},
			{"empty dropped", `""`, ``},
			{"default dropped", `"default"`, ``},
			{"auto dropped", `"auto"`, ``},
			{"unknown dropped", `"future"`, ``},
			{"numeric dropped", `3`, ``},
			{"bool dropped", `true`, ``},
			{"null preserved", `null`, `null`},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"reasoning_effort":%s,"messages":[{"role":"user","content":"hi"}]}`, model, tc.field))
				out, err := normalizeMistralChatRequest(account, body)
				require.NoError(t, err)
				require.Equal(t, tc.want, gjson.GetBytes(out, "reasoning_effort").Raw)
				require.NotContains(t, string(out), `"medium"`)
				if v := gjson.GetBytes(out, "reasoning_effort"); v.Type == gjson.String {
					require.True(t, mistralDeepSeekAllowedEfforts[v.String()], v.String())
				}
				// Last-mile guard is idempotent on an already-normalized body.
				require.Equal(t, string(out), string(enforceMistralChatReasoningEffort(account, out)))
			})
		}
	}
}

func TestMistralDeepSeekNestedEffortMapped(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning":{"effort":"medium","summary":"auto"},"messages":[{"role":"user","content":"hi"}]}`)
	out, err := normalizeMistralChatRequest(account, body)
	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(out, "reasoning_effort").String())
	require.False(t, gjson.GetBytes(out, "reasoning").Exists())

	// Disabled thinking on DeepSeek becomes the native "none", not GLM's "low".
	out, err = normalizeMistralChatRequest(account, []byte(`{"model":"deepseek-v4-1-flash-1017","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.Equal(t, "none", gjson.GetBytes(out, "reasoning_effort").String())
	require.False(t, gjson.GetBytes(out, "thinking").Exists())

	// No effort at all: nothing is invented.
	out, err = normalizeMistralChatRequest(account, []byte(`{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "reasoning_effort").Exists())
}

func TestMistralGLMEffortMappingNoRegression(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, tc := range []struct{ in, want string }{
			{`"none"`, `"low"`}, {`"minimal"`, `"low"`}, {`"low"`, `"low"`},
			{`"medium"`, `"high"`}, {`"high"`, `"high"`},
			{`"xhigh"`, `"max"`}, {`"max"`, `"max"`},
			{`"future"`, `"future"`}, {`42`, `42`}, {`null`, `null`},
		} {
			body := []byte(fmt.Sprintf(`{"model":%q,"reasoning_effort":%s,"messages":[]}`, model, tc.in))
			out, err := normalizeMistralChatRequest(account, body)
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(out, "reasoning_effort").Raw, model+" "+tc.in)
		}
	}
	// GLM aliases and other Mistral models are untouched (ds.2/ds.3 behavior).
	for _, model := range []string{"zai-glm-5-3-latest", "mistral-small-latest", "magistral-medium-latest"} {
		body := []byte(fmt.Sprintf(`{"model":%q,"reasoning_effort":"medium","messages":[]}`, model))
		out, err := normalizeMistralChatRequest(account, body)
		require.NoError(t, err)
		require.Equal(t, "medium", gjson.GetBytes(out, "reasoning_effort").String(), model)
		require.Equal(t, string(out), string(enforceMistralChatReasoningEffort(account, out)))
	}
	// Direct unit check on mapMistralReasoningEffort default family branch.
	val, keep := mapMistralReasoningEffort(mistralEffortFamilyNone, "medium")
	require.True(t, keep)
	require.Equal(t, "medium", val)
}

func TestMistralEffortMappingScopedToMistralPlatform(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"medium","messages":[]}`)
	for _, platform := range []string{PlatformDeepseek, PlatformOpenAI} {
		account := &Account{Platform: platform}
		out, err := normalizeMistralChatRequest(account, body)
		require.NoError(t, err)
		require.Equal(t, string(body), string(out), platform)
		require.Equal(t, string(body), string(enforceMistralChatReasoningEffort(account, body)), platform)
	}
	require.Equal(t, string(body), string(enforceMistralChatReasoningEffort(nil, body)))
	require.Empty(t, enforceMistralChatReasoningEffort(&Account{Platform: PlatformMistral}, nil))
}

func TestMistralEnforceEffortLastMile(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	in := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"medium","messages":[{"role":"user","content":"hi"}]}`)
	out := enforceMistralChatReasoningEffort(account, in)
	require.Equal(t, "high", gjson.GetBytes(out, "reasoning_effort").String())
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.0.content").String())
	// Unparseable or null JSON bodies are left alone (the upstream reports the real error).
	require.Equal(t, "not-json", string(enforceMistralChatReasoningEffort(account, []byte("not-json"))))
	require.Equal(t, "null", string(enforceMistralChatReasoningEffort(account, []byte("null"))))
}

// ds.3 preservation together with the ds.4 effort fix on one body.
func TestMistralDeepSeekEffortAndReasoningContentTogether(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	in := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"medium","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","reasoning_content":"think"},{"role":"assistant","content":"again"},{"role":"user","content":"go"}]}`)
	normalized, err := normalizeMistralChatRequest(account, in)
	require.NoError(t, err)
	outbound := ensureDeepSeekChatReasoningPlaceholders(account, normalized)
	outbound = stripDisallowedMistralChatMessageExtras(account, outbound)
	outbound = enforceMistralChatReasoningEffort(account, outbound)
	require.Equal(t, "high", gjson.GetBytes(outbound, "reasoning_effort").String())
	require.NotContains(t, string(outbound), "reasoning_content")
	require.NotContains(t, string(outbound), "reasoning_details")
	require.NotContains(t, string(outbound), `"medium"`)
}

// End-to-end through every inbound endpoint that can reach Mistral, buffered and
// streaming, with a fake upstream. Asserts the actual outbound body and the
// effort recorded for usage/billing.
func TestMistralDeepSeekMediumAcrossEndpoints(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct{ in, want string }{{"medium", "high"}, {"minimal", "low"}, {"xhigh", "xhigh"}, {"max", "max"}, {"none", "none"}, {"low", "low"}, {"high", "high"}} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", endpoint, stream, tc.in), func(t *testing.T) {
					request := map[string]any{"model": "deepseek-flash", "stream": stream}
					switch endpoint {
					case "/v1/chat/completions":
						request["messages"] = []any{
							map[string]any{"role": "user", "content": "hello"},
							map[string]any{"role": "assistant", "content": "prior", "reasoning_content": " "},
							map[string]any{"role": "user", "content": "again"},
						}
						request["reasoning_effort"] = tc.in
					case "/v1/responses":
						request["input"] = "hello"
						request["reasoning"] = map[string]any{"effort": tc.in}
					case "/v1/messages":
						request["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
						request["max_tokens"] = 64
						request["output_config"] = map[string]any{"effort": tc.in}
					}
					if endpoint == "/v1/messages" && (tc.in == "minimal" || tc.in == "none") {
						t.Skip("Anthropic output_config.effort has no minimal/none level")
					}
					body, err := json.Marshal(request)
					require.NoError(t, err)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
					contentType := "application/json"
					response := `{"id":"fixture","model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
					if stream {
						contentType = "text/event-stream"
						response = "data: {\"id\":\"fixture\",\"model\":\"deepseek-v4-1-flash-1017\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\ndata: [DONE]\n\n"
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}}
					account := rawChatCompletionsTestAccount()
					account.Platform = PlatformMistral
					account.Credentials["api_protocol"] = APIProtocolAdaptive
					account.Credentials["model_mapping"] = map[string]any{"deepseek-flash": "deepseek-v4-1-flash-1017"}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					var result *OpenAIForwardResult
					switch endpoint {
					case "/v1/chat/completions":
						result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					case "/v1/responses":
						result, err = svc.Forward(context.Background(), c, account, body)
					case "/v1/messages":
						result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 1)
					require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
					require.Equal(t, "deepseek-v4-1-flash-1017", gjson.GetBytes(upstream.lastBody, "model").String())
					require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "reasoning_effort").String(), string(upstream.lastBody))
					require.True(t, mistralDeepSeekAllowedEfforts[gjson.GetBytes(upstream.lastBody, "reasoning_effort").String()])
					require.NotContains(t, string(upstream.lastBody), `"medium"`)
					require.NotContains(t, string(upstream.lastBody), "reasoning_content")
					require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning").Exists())
					require.NotNil(t, result.ReasoningEffort)
					require.Equal(t, tc.want, *result.ReasoningEffort)
				})
			}
		}
	}
}

func TestMistralAccountTestMapsDeepSeekEffort(t *testing.T) {
	c, _ := mistralTestContext()
	c.Request = c.Request.WithContext(withPelicanTestOptions(c.Request.Context(), pelicanTestOptions{reasoningEffort: "medium"}))
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}}
	svc := &AccountTestService{httpUpstream: upstream}
	account := &Account{ID: 901, Platform: PlatformMistral, Type: AccountTypeAPIKey}
	require.NoError(t, svc.testOpenAIChatCompletionsConnection(c, account, "deepseek-v4-1-flash-1017", "hello", "https://upstream.example/v1", "fixture-key"))
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
}
