package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Regression tests for #7978: /v1/responses with stream=false must return a
// single JSON Responses object even though the ChatGPT OAuth upstream is
// always called with stream=true and answers with SSE.

const openAINonStreamRegressionSSE = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_7978","status":"in_progress","output":[]}}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_7978","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"

func runOpenAIOAuthNonStreamRegression(t *testing.T, passthrough bool, requestBody string, upstreamSSE ...string) (*httptest.ResponseRecorder, *httpUpstreamRecorder, *OpenAIForwardResult) {
	t.Helper()
	sseBody := openAINonStreamRegressionSSE
	if len(upstreamSSE) > 0 {
		sseBody = upstreamSSE[0]
	}
	header := http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_7978"}}
	return runOpenAIOAuthNonStreamRegressionWithHeader(t, passthrough, requestBody, header, sseBody)
}

func runOpenAIOAuthNonStreamRegressionWithHeader(t *testing.T, passthrough bool, requestBody string, upstreamHeader http.Header, sseBody string) (*httptest.ResponseRecorder, *httpUpstreamRecorder, *OpenAIForwardResult) {
	t.Helper()
	return runOpenAINonStreamRegressionForAccount(t, AccountTypeOAuth, passthrough, requestBody, upstreamHeader, sseBody)
}

func runOpenAINonStreamRegressionForAccount(t *testing.T, accountType string, passthrough bool, requestBody string, upstreamHeader http.Header, responseBody string) (*httptest.ResponseRecorder, *httpUpstreamRecorder, *OpenAIForwardResult) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set("Accept", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     upstreamHeader,
		Body:       io.NopCloser(strings.NewReader(responseBody)),
	}}
	cfg := &config.Config{}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, responseHeaderFilter: compileResponseHeaderFilter(cfg)}
	account := &Account{
		ID:             7978,
		Name:           "acc",
		Platform:       PlatformOpenAI,
		Type:           accountType,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Extra:          map[string]any{"openai_passthrough": passthrough},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}
	if accountType == AccountTypeAPIKey {
		account.Credentials = map[string]any{"api_key": "fixture-api-key"}
		account.Extra[openai_compat.ExtraKeyResponsesMode] = "force_responses"
	}

	result, err := svc.Forward(context.Background(), c, account, []byte(requestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	return rec, upstream, result
}

func requireSingleJSONResponsesObject(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"), "Content-Type=%q", rec.Header().Get("Content-Type"))
	var obj map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &obj), "body=%s", rec.Body.String())
	require.Equal(t, "resp_7978", obj["id"])
	require.Equal(t, "completed", obj["status"])
	require.Equal(t, "OK", gjson.GetBytes(rec.Body.Bytes(), "output.0.content.0.text").String())
}

func TestOpenAIGatewayService_OAuthPassthrough_StreamFalseReturnsJSON(t *testing.T) {
	rec, upstream, result := runOpenAIOAuthNonStreamRegression(t, true,
		`{"model":"gpt-5.4","instructions":"x","input":"Reply with exactly OK.","stream":false}`)

	// Upstream still requires stream=true.
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, result.Stream)
	requireSingleJSONResponsesObject(t, rec)
}

func TestOpenAIGatewayService_OAuthPassthrough_StreamTrueStillStreams(t *testing.T) {
	rec, upstream, result := runOpenAIOAuthNonStreamRegression(t, true,
		`{"model":"gpt-5.4","instructions":"x","input":"Reply with exactly OK.","stream":true}`)

	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.True(t, result.Stream)
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream"), "Content-Type=%q", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "event: response.completed")
}

func TestOpenAIGatewayService_OAuthLegacy_StreamFalseReturnsJSONContentType(t *testing.T) {
	rec, upstream, result := runOpenAIOAuthNonStreamRegression(t, false,
		`{"model":"gpt-5.4","instructions":"x","input":"Reply with exactly OK.","stream":false}`)

	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, result.Stream)
	requireSingleJSONResponsesObject(t, rec)
}

const openAINonStreamIncompleteSSE = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_7978","status":"in_progress","output":[]}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"Hel"}` + "\n\n" +
	"event: response.incomplete\n" +
	`data: {"type":"response.incomplete","response":{"id":"resp_7978","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hel"}]}],"usage":{"input_tokens":5,"output_tokens":16,"total_tokens":21}}}` + "\n\n"

func TestOpenAIGatewayService_OAuthStreamFalseIncompleteReturnsJSON(t *testing.T) {
	for _, passthrough := range []bool{true, false} {
		rec, _, result := runOpenAIOAuthNonStreamRegression(t, passthrough,
			`{"model":"gpt-5.4","instructions":"x","input":"hi","max_output_tokens":16,"stream":false}`,
			openAINonStreamIncompleteSSE)

		require.False(t, result.Stream)
		require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"), "passthrough=%v Content-Type=%q", passthrough, rec.Header().Get("Content-Type"))
		var obj map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &obj), "passthrough=%v body=%s", passthrough, rec.Body.String())
		require.Equal(t, "resp_7978", obj["id"])
		require.Equal(t, "incomplete", obj["status"])
		require.Equal(t, "max_output_tokens", gjson.GetBytes(rec.Body.Bytes(), "incomplete_details.reason").String())
		require.Equal(t, "Hel", gjson.GetBytes(rec.Body.Bytes(), "output.0.content.0.text").String())
		require.Equal(t, 5, result.Usage.InputTokens)
		require.Equal(t, 16, result.Usage.OutputTokens)
	}
}

func TestOpenAIGatewayService_OAuthStreamFalseCancelledReturnsJSON(t *testing.T) {
	cancelledSSE := "event: response.cancelled\n" +
		`data: {"type":"response.cancelled","response":{"id":"resp_7978","object":"response","status":"cancelled","output":[],"usage":{"input_tokens":5,"output_tokens":0,"total_tokens":5}}}` + "\n\n"
	for _, passthrough := range []bool{true, false} {
		rec, _, _ := runOpenAIOAuthNonStreamRegression(t, passthrough,
			`{"model":"gpt-5.4","instructions":"x","input":"hi","stream":false}`, cancelledSSE)

		require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"), "passthrough=%v Content-Type=%q", passthrough, rec.Header().Get("Content-Type"))
		require.Equal(t, "cancelled", gjson.GetBytes(rec.Body.Bytes(), "status").String(), "passthrough=%v body=%s", passthrough, rec.Body.String())
	}
}

// The ChatGPT OAuth upstream can answer with SSE but no Content-Type header;
// the body framing alone must route the response through SSE-to-JSON.
func TestOpenAIGatewayService_OAuthStreamFalseWithoutUpstreamContentTypeReturnsJSON(t *testing.T) {
	for _, passthrough := range []bool{true, false} {
		header := http.Header{"X-Request-Id": []string{"rid_7978"}}
		rec, upstream, result := runOpenAIOAuthNonStreamRegressionWithHeader(t, passthrough,
			`{"model":"gpt-5.4","instructions":"x","input":"Reply with exactly OK.","stream":false}`,
			header, openAINonStreamRegressionSSE)

		require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool(), "passthrough=%v", passthrough)
		require.False(t, result.Stream, "passthrough=%v", passthrough)
		requireSingleJSONResponsesObject(t, rec)
	}
}

func TestOpenAIGatewayService_OAuthLikeNonStreamTerminalMatrix(t *testing.T) {
	for _, account := range []struct{ name, kind string }{{"OAuth", AccountTypeOAuth}, {"SetupToken", AccountTypeSetupToken}} {
		for _, passthrough := range []struct {
			name string
			on   bool
		}{{"passthrough_on", true}, {"passthrough_off", false}} {
			for _, headerCase := range []struct {
				name            string
				withContentType bool
			}{{"header_present", true}, {"header_missing", false}} {
				for _, terminal := range []string{"completed", "incomplete", "cancelled", "canceled"} {
					t.Run(account.name+"/"+passthrough.name+"/"+headerCase.name+"/"+terminal, func(t *testing.T) {
						// Every terminal carries content and usage, including cancellation.
						response := map[string]any{
							"id": "resp_7978", "object": "response", "status": terminal,
							"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "terminal content"}}}},
							"usage":  map[string]any{"input_tokens": 5, "output_tokens": 16, "total_tokens": 21},
						}
						if terminal == "incomplete" {
							response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
						}
						payload, err := json.Marshal(map[string]any{"type": "response." + terminal, "response": response})
						require.NoError(t, err)
						sse := "event: response." + terminal + "\ndata: " + string(payload) + "\n\n"
						header := http.Header{}
						header.Set("X-Request-Id", "rid_7978")
						if headerCase.withContentType {
							header.Set("Content-Type", "text/event-stream")
						}
						rec, upstream, result := runOpenAINonStreamRegressionForAccount(t, account.kind, passthrough.on,
							`{"model":"gpt-5.4","instructions":"x","input":"hi","stream":false}`, header, sse)

						require.Equal(t, http.StatusOK, rec.Code)
						require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool(), "Codex upstream must still receive stream=true")
						require.False(t, result.Stream)
						require.True(t, json.Valid(rec.Body.Bytes()), "Codex non-stream terminal must be a single JSON object; body=%s", rec.Body.String())
						var obj map[string]any
						require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &obj))
						require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"))
						require.Equal(t, "resp_7978", obj["id"])
						require.Equal(t, "response", obj["object"])
						require.Equal(t, terminal, obj["status"])
						require.Equal(t, "terminal content", gjson.GetBytes(rec.Body.Bytes(), "output.0.content.0.text").String())
						if terminal == "incomplete" {
							require.Equal(t, "max_output_tokens", gjson.GetBytes(rec.Body.Bytes(), "incomplete_details.reason").String())
						}
						require.Equal(t, int64(5), gjson.GetBytes(rec.Body.Bytes(), "usage.input_tokens").Int())
						require.Equal(t, int64(16), gjson.GetBytes(rec.Body.Bytes(), "usage.output_tokens").Int())
						require.Equal(t, int64(21), gjson.GetBytes(rec.Body.Bytes(), "usage.total_tokens").Int())
						require.Equal(t, "rid_7978", result.RequestID)
						require.Equal(t, "resp_7978", result.ResponseID)
						require.Equal(t, 5, result.Usage.InputTokens)
						require.Equal(t, 16, result.Usage.OutputTokens)
					})
				}
			}
		}
	}
}

func TestOpenAIGatewayService_OAuthLikeStreamTrueStillStreams(t *testing.T) {
	for _, kind := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, passthrough := range []bool{true, false} {
			t.Run(kind+"/"+map[bool]string{true: "passthrough_on", false: "passthrough_off"}[passthrough], func(t *testing.T) {
				rec, upstream, result := runOpenAINonStreamRegressionForAccount(t, kind, passthrough,
					`{"model":"gpt-5.4","instructions":"x","input":"hi","stream":true}`,
					http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_7978"}}, openAINonStreamRegressionSSE)
				require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
				require.True(t, result.Stream)
				require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream"))
				require.Contains(t, rec.Body.String(), "event: response.completed")
			})
		}
	}
}

func TestOpenAIGatewayService_OAuthLikeNonStreamJSONLiteralFramingPreserved(t *testing.T) {
	const body = `{"id":"resp_7978","object":"response","created_at":1234567890,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"literal data: foo and event: response.completed"}]}],"usage":{"input_tokens":5,"output_tokens":16,"total_tokens":21}}`
	for _, kind := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		for _, passthrough := range []bool{true, false} {
			t.Run(kind+"/"+map[bool]string{true: "passthrough_on", false: "passthrough_off"}[passthrough], func(t *testing.T) {
				rec, upstream, result := runOpenAINonStreamRegressionForAccount(t, kind, passthrough,
					`{"model":"gpt-5.4","instructions":"x","input":"hi","stream":false}`,
					http.Header{"X-Request-Id": []string{"rid_7978"}}, body)
				require.JSONEq(t, body, rec.Body.String())
				require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"))
				require.Equal(t, kind != AccountTypeAPIKey, gjson.GetBytes(upstream.lastBody, "stream").Bool())
				require.False(t, result.Stream)
				require.Equal(t, "rid_7978", result.RequestID)
				require.Equal(t, "resp_7978", result.ResponseID)
				require.Equal(t, 5, result.Usage.InputTokens)
				require.Equal(t, 16, result.Usage.OutputTokens)
			})
		}
	}
}
