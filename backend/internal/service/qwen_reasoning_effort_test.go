package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The service builds the actual outbound request; only the HTTP transport is
// a loopback client. No fixture substitutes for routing or protocol conversion.
type qwenEffortHTTPUpstream struct{ client *http.Client }

func (u qwenEffortHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(req)
}

func (u qwenEffortHTTPUpstream) DoWithTLS(req *http.Request, p string, a int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, a, n)
}

func TestQwenEffortAliasesForwardWireAndResult(t *testing.T) {
	for _, route := range []struct {
		name, ingress, protocol string
		responsesShape          bool
	}{
		{"native-responses", "/v1/responses", APIProtocolResponses, true},
		{"responses-to-chat", "/v1/responses", APIProtocolChatCompletions, true},
		{"native-chat", "/v1/chat/completions", APIProtocolChatCompletions, false},
		{"chat-to-responses", "/v1/chat/completions", APIProtocolResponses, false},
		{"responses-shaped-chat-to-responses", "/v1/chat/completions", APIProtocolResponses, true},
		{"responses-shaped-chat-to-chat", "/v1/chat/completions", APIProtocolAdaptive, true},
	} {
		for _, tc := range []struct{ name, fields, requested, want string }{
			{"flat-none", `"reasoning_effort":"none"`, "none", "none"},
			{"flat-low", `"reasoning_effort":"low"`, "low", "low"},
			{"nested-none", `"reasoning":{"effort":"none","summary":"auto"}`, "none", "none"},
			{"nested-low", `"reasoning":{"effort":"low","summary":"auto"}`, "low", "low"},
			{"nested-wins-conflict", `"reasoning":{"effort":"minimal","summary":"auto"},"reasoning_effort":"none"`, "minimal", "low"},
			{"empty-nested-falls-back", `"reasoning":{"effort":"","summary":"auto"},"reasoning_effort":"none"`, "none", "none"},
			{"mapped-max", `"reasoning_effort":"max"`, "max", "xhigh"},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				type capturedRequest struct {
					path string
					body []byte
				}
				captured := make(chan capturedRequest, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					wire, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					select {
					case captured <- capturedRequest{r.URL.Path, wire}:
					default:
						t.Error("unexpected extra upstream request")
					}
					w.Header().Set("Content-Type", "application/json")
					response := `{"id":"resp_effort","object":"response","status":"completed","model":"qwen4-max","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
					if r.URL.Path == "/v1/chat/completions" {
						response = `{"id":"chatcmpl_effort","object":"chat.completion","model":"qwen4-max","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
					} else if gjson.GetBytes(wire, "stream").Bool() || route.name == "responses-shaped-chat-to-responses" {
						// Responses-shaped Chat uses the existing buffered SSE reader
						// even with stream:false. Match that reader's fixture contract;
						// this does not assert real provider behavior for stream:false.
						w.Header().Set("Content-Type", "text/event-stream")
						response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
					}
					_, _ = io.WriteString(w, response)
				}))
				defer server.Close()
				account := &Account{ID: 91, Platform: PlatformQwen, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "fixture-key", "base_url": server.URL + "/v1", "api_protocol": route.protocol,
						"model_mapping": map[string]any{"client-model": "qwen4-max"}}}
				// Qwen's existing native Responses route is opt-in through its
				// session-cache setting; api_protocol alone does not enable it.
				if route.protocol == APIProtocolResponses {
					account.Credentials[qwenSessionCacheCredential] = true
				}
				input := `"messages":[{"role":"user","content":"hi"}]`
				if route.responsesShape {
					input = `"input":"hi"`
				}
				body := []byte(fmt.Sprintf(`{"model":"client-model","stream":false,%s,%s}`, input, tc.fields))
				original := bytes.Clone(body)
				requested := CanonicalRequestedReasoningEffort(body, "client-model")
				require.NotNil(t, requested)
				require.Equal(t, tc.requested, *requested)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = WithRequestedReasoningEffort(ctx, *requested)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, route.ingress, bytes.NewReader(body)).WithContext(ctx)
				c.Request.Header.Set("Content-Type", "application/json")
				svc := &OpenAIGatewayService{cfg: deepSeekChatFallbackTestConfig(), httpUpstream: qwenEffortHTTPUpstream{client: server.Client()}}
				var result *OpenAIForwardResult
				var err error
				if route.ingress == "/v1/responses" {
					result, err = svc.Forward(ctx, c, account, body)
				} else {
					result, err = svc.ForwardAsChatCompletions(ctx, c, account, body, "", "")
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, captured, 1)
				outbound := <-captured
				wantPath, effortPath, alias := "/v1/responses", "reasoning.effort", "reasoning_effort"
				if route.protocol == APIProtocolChatCompletions || route.name == "responses-shaped-chat-to-chat" {
					wantPath, effortPath, alias = "/v1/chat/completions", "reasoning_effort", "reasoning.effort"
				}
				require.Equal(t, wantPath, outbound.path)
				require.Equal(t, "qwen4-max", gjson.GetBytes(outbound.body, "model").Str)
				require.Equal(t, tc.want, gjson.GetBytes(outbound.body, effortPath).Str, string(outbound.body))
				require.False(t, gjson.GetBytes(outbound.body, alias).Exists(), string(outbound.body))
				if (route.name == "native-responses" || route.name == "native-chat") && gjson.GetBytes(body, "reasoning.summary").Exists() {
					require.Equal(t, "auto", gjson.GetBytes(outbound.body, "reasoning.summary").Str)
				}
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, tc.want, *result.ReasoningEffort)
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				require.Equal(t, tc.requested, *RequestedReasoningEffortFromContext(c.Request.Context()))
				require.Equal(t, requested, CanonicalRequestedReasoningEffort(body, "client-model"))
				require.Equal(t, original, body, "an account attempt must preserve the original requested effort and retry body")
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Contains(t, recorder.Body.String(), "ok")
			})
		}
	}
}

func TestQwenEffortAliasForwardLeavesNonQwenUnchanged(t *testing.T) {
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case captured <- wire:
		default:
			t.Error("unexpected extra upstream request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_other","object":"response","status":"completed","model":"qwen4-max","output":[],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer server.Close()
	body := []byte(`{"model":"client-model","input":"hi","stream":false,"reasoning_effort":"low","reasoning":{"effort":"high","summary":"auto"}}`)
	account := &Account{ID: 92, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "fixture-key", "base_url": server.URL + "/v1", "api_protocol": APIProtocolResponses,
		"model_mapping": map[string]any{"client-model": "qwen4-max"},
	}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	svc := &OpenAIGatewayService{cfg: deepSeekChatFallbackTestConfig(), httpUpstream: qwenEffortHTTPUpstream{client: server.Client()}}
	result, err := svc.Forward(ctx, c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, captured, 1)
	wire := <-captured
	require.Equal(t, "qwen4-max", gjson.GetBytes(wire, "model").Str)
	require.Equal(t, "low", gjson.GetBytes(wire, "reasoning_effort").Str)
	require.JSONEq(t, gjson.GetBytes(body, "reasoning").Raw, gjson.GetBytes(wire, "reasoning").Raw)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
}

func TestQwenEffortUsesMappedModelAndKeepsRequestedValue(t *testing.T) {
	for _, tc := range []struct{ model, request, wire string }{
		{"qwen3.8-max", "max", "xhigh"},
		{"qwen3.8-max-0902", "high", "xhigh"},
		{"qwen3.8-flash", "minimal", "low"},
		{"qwen3.8-27b", "medium", "medium"},
		{"qwen3.8-omni-flash", "none", "none"},
		{"qwen4-max", "max", "xhigh"},
		{"qwen4-max", "medium", "high"},
		{"qwen4-max", "minimal", "low"},
		{"qwen4-max-preview", "medium", "medium"},
		{"qwen3.7-plus", "high", "high"},
	} {
		t.Run(tc.model+"/"+tc.request, func(t *testing.T) {
			account := &Account{Platform: PlatformQwen, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"model_mapping": map[string]any{"client-model": tc.model},
			}}
			for path, template := range map[string]string{
				"reasoning.effort":     `{"model":"client-model","reasoning":{"effort":"%s"}}`,
				"reasoning_effort":     `{"model":"client-model","reasoning_effort":"%s"}`,
				"output_config.effort": `{"model":"client-model","output_config":{"effort":"%s"}}`,
			} {
				in := []byte(fmt.Sprintf(template, tc.request))
				out := normalizeQwenRequestEffort(account, in, "", path)
				require.Equal(t, tc.request, gjson.GetBytes(in, path).Str)
				require.Equal(t, tc.wire, gjson.GetBytes(out, path).Str)
			}
		})
	}
}

func TestQwenExplicitNoneSurvivesResponsesAndWSBridge(t *testing.T) {
	account := &Account{Platform: PlatformQwen, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"qwen3.8-max","reasoning":{"effort":"none","summary":"auto"}}`)
	filtered, err := filterOpenAIResponsesNoneReasoningEffortForAccount(account, body)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(filtered))
	bridged, err := prepareOpenAIWSHTTPBridgeSessionBody(account, body, false)
	require.NoError(t, err)
	require.Equal(t, "none", gjson.GetBytes(bridged, "reasoning.effort").Str)
}

func TestQwenBridgedMessagesWireAndUsage(t *testing.T) {
	account := &Account{Platform: PlatformQwen, Type: AccountTypeAPIKey}
	service := &OpenAIGatewayService{}
	body := []byte(`{"model":"qwen3.8-max","output_config":{"effort":"max","format":{"type":"json_schema"}},"thinking":{"type":"enabled","budget_tokens":32768}}`)
	for _, effort := range []string{"high", "none"} {
		source := []byte(fmt.Sprintf(`{"reasoning_effort":"%s"}`, effort))
		_, wire, err := service.buildNativeAnthropicUpstreamRequest(context.Background(), nil, account, body, "mock", "https://example.invalid/v1/messages", source)
		require.NoError(t, err)
		want := "xhigh"
		if effort == "none" {
			want = "none"
			require.Equal(t, "disabled", gjson.GetBytes(wire, "thinking.type").Str)
			require.False(t, gjson.GetBytes(wire, "output_config.effort").Exists())
		} else {
			require.Equal(t, want, gjson.GetBytes(wire, "output_config.effort").Str)
		}
		require.False(t, gjson.GetBytes(wire, "thinking.budget_tokens").Exists())
		require.Equal(t, "json_schema", gjson.GetBytes(wire, "output_config.format.type").Str)
		require.Equal(t, want, *nativeMessagesUsageEffort(account, wire, "qwen3.8-max"))
	}
}
