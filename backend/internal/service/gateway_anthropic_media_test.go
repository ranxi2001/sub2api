//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiCompatibilityMediaPolicyIsTargetScoped(t *testing.T) {
	testCompatibilityMediaPolicy(t, false)
}

func TestAntigravityCompatibilityMediaPolicyIsTargetScoped(t *testing.T) {
	testCompatibilityMediaPolicy(t, true)
}

// Exercise both production service entry points and capture their actual
// outbound request. A synthetic success proves conversion only, not media
// understanding by any provider.
func testCompatibilityMediaPolicy(t *testing.T, antigravity bool) {
	t.Helper()
	for _, endpoint := range []string{"chat/completions", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, field := range []string{"image", "file"} {
				for _, mime := range []string{"video/mp4", "audio/mpeg", "application/octet-stream", "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "text/plain"} {
					t.Run(fmt.Sprintf("%s/stream=%t/%s/%s", endpoint, stream, field, mime), func(t *testing.T) {
						uri := "data:" + mime + ";base64,c3ludGhldGlj"
						part := map[string]any{"type": "input_image", "image_url": uri}
						if field == "file" {
							part = map[string]any{"type": "input_file", "file_data": uri}
						}
						request := map[string]any{"model": "gemini-2.5-flash", "stream": stream}
						if antigravity {
							request["model"] = "gemini-3.1-pro-high"
						}
						if endpoint == "chat/completions" {
							part = map[string]any{"type": "image_url", "image_url": map[string]string{"url": uri}}
							if field == "file" {
								part = map[string]any{"type": "file", "file": map[string]string{"file_data": uri}}
							}
							request["messages"] = []any{map[string]any{"role": "user", "content": []any{part}}}
						} else {
							request["input"] = []any{map[string]any{"role": "user", "content": []any{part}}}
						}
						body, err := json.Marshal(request)
						require.NoError(t, err)
						upstream := &queuedHTTPUpstreamStub{
							responses: []*http.Response{compatibilityMediaSuccessResponse(stream, antigravity)},
							onCall: func(req *http.Request, _ *queuedHTTPUpstreamStub) {
								if antigravity {
									require.Equal(t, "Bearer fresh-oauth-token", req.Header.Get("Authorization"))
									require.Empty(t, req.Header.Get("x-goog-api-key"))
									require.Equal(t, "/v1internal:streamGenerateContent", req.URL.Path)
								} else {
									require.Equal(t, "synthetic-api-key", req.Header.Get("x-goog-api-key"))
									require.Empty(t, req.Header.Get("Authorization"))
									action := ":generateContent"
									if stream {
										action = ":streamGenerateContent"
									}
									require.True(t, strings.HasSuffix(req.URL.Path, action))
								}
							},
						}
						c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/"+endpoint, body)
						var result *ForwardResult
						if antigravity {
							svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
							account := newAntigravityCompatAccount(AccountTypeOAuth)
							if endpoint == "chat/completions" {
								result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
							} else {
								result, err = svc.ForwardAsResponses(context.Background(), c, account, body, nil)
							}
						} else {
							svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: upstream, cfg: &config.Config{}}
							account := &Account{ID: 9812, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1,
								Credentials: map[string]any{"api_key": "synthetic-api-key"}}
							if endpoint == "chat/completions" {
								result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body)
							} else {
								result, err = svc.ForwardAsResponses(context.Background(), c, account, body)
							}
						}
						require.NoError(t, err)
						require.NotNil(t, result)
						require.Equal(t, http.StatusOK, recorder.Code)
						require.Equal(t, 1, upstream.callCount)
						require.Len(t, upstream.requestBodies, 1)
						require.Contains(t, recorder.Body.String(), "ok")
						root := "contents"
						if antigravity {
							root = "request.contents"
							require.False(t, gjson.GetBytes(upstream.requestBodies[0], "contents").Exists())
						} else {
							require.False(t, gjson.GetBytes(upstream.requestBodies[0], "request").Exists())
						}
						wire := gjson.GetBytes(upstream.requestBodies[0], root+".0.parts.0")
						if mime == "text/plain" {
							require.Equal(t, "synthetic", wire.Get("text").String())
						} else if field == "file" && mime != "application/pdf" {
							// Both fixtures target Gemini models, so Antigravity also
							// uses the shared converter's legacy document-as-text carrier.
							// Only PDF/plain document handling is upgraded.
							legacy := gjson.Parse(wire.Get("text").String())
							require.Equal(t, "document", legacy.Get("type").String())
							require.Equal(t, mime, legacy.Get("source.media_type").String())
							require.Equal(t, "c3ludGhldGlj", legacy.Get("source.data").String())
						} else {
							require.Equal(t, mime, wire.Get("inlineData.mimeType").String())
							require.Equal(t, "c3ludGhldGlj", wire.Get("inlineData.data").String())
						}
					})
				}
			}
		}
	}
}

// Code Assist and Antigravity wrap Gemini results and always stream upstream;
// AI Studio API-key routes use plain JSON or plain SSE to match clientStream.
func compatibilityMediaSuccessResponse(clientStream, codeAssist bool) *http.Response {
	body := `{"responseId":"synthetic-media","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}`
	if codeAssist {
		body = `{"response":` + body + `}`
	}
	contentType := "application/json"
	if clientStream || codeAssist {
		contentType = "text/event-stream"
		body = "data: " + body + "\n\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestGeminiCompatibilityMediaPolicyOAuthControls(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, endpoint := range []string{"chat/completions", "responses"} {
			t.Run(fmt.Sprintf("%s/stream=%t", endpoint, stream), func(t *testing.T) {
				request := map[string]any{"model": "gemini-2.5-flash", "stream": stream}
				const uri = "data:video/mp4;base64,c3ludGhldGlj"
				if endpoint == "chat/completions" {
					request["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": uri}}}}}
				} else {
					request["input"] = []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_image", "image_url": uri}}}}
				}
				body, err := json.Marshal(request)
				require.NoError(t, err)
				upstream := &queuedHTTPUpstreamStub{
					responses: []*http.Response{compatibilityMediaSuccessResponse(stream, true)},
					onCall: func(req *http.Request, _ *queuedHTTPUpstreamStub) {
						require.Equal(t, "Bearer synthetic-token", req.Header.Get("Authorization"))
						require.Empty(t, req.Header.Get("x-goog-api-key"))
						require.Equal(t, "/v1internal:streamGenerateContent", req.URL.Path)
					},
				}
				svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: upstream, cfg: &config.Config{}}
				account := &Account{ID: 9813, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1,
					Credentials: map[string]any{"access_token": "synthetic-token", "project_id": "synthetic-project"}}
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/"+endpoint, body)
				if endpoint == "responses" {
					result, err := svc.ForwardAsResponses(context.Background(), c, account, body)
					require.Error(t, err)
					require.Nil(t, result)
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					require.Contains(t, recorder.Body.String(), "Gemini OAuth accounts do not support Responses compatibility")
					require.Zero(t, upstream.callCount)
					require.Empty(t, upstream.requestBodies)
					return
				}
				result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Contains(t, recorder.Body.String(), "ok")
				require.Equal(t, 1, upstream.callCount)
				require.Len(t, upstream.requestBodies, 1)
				wire := gjson.GetBytes(upstream.requestBodies[0], "request.contents.0.parts.0.inlineData")
				require.Equal(t, "video/mp4", wire.Get("mimeType").String())
				require.Equal(t, "c3ludGhldGlj", wire.Get("data").String())
			})
		}
	}
}

func TestGeminiDocumentsSurviveAnthropicMIMEBridge(t *testing.T) {
	for _, tc := range []struct {
		mime string
		path string
		want string
	}{
		{"text/plain", "contents.0.parts.0.text", "synthetic"},
		{"application/pdf", "contents.0.parts.0.inlineData.data", "c3ludGhldGlj"},
	} {
		t.Run(tc.mime, func(t *testing.T) {
			var req apicompat.ResponsesRequest
			input := `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:` + tc.mime + `;base64,c3ludGhldGlj"}]}]}`
			require.NoError(t, json.Unmarshal([]byte(input), &req))
			anthropic, err := apicompat.ResponsesToAnthropicRequest(&req)
			require.NoError(t, err)
			body, err := json.Marshal(anthropic)
			require.NoError(t, err)
			wire, err := convertClaudeMessagesToGeminiGenerateContent(body, "gemini-2.5-flash")
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(wire, tc.path).String())
		})
	}
	textSource := map[string]any{"type": "document", "source": map[string]any{"type": "text", "media_type": "text/plain", "data": "synthetic"}}
	require.Equal(t, "synthetic", extractClaudeContentText([]any{textSource}))
	require.Empty(t, extractClaudeToolResultImageParts([]any{textSource}))
}
