//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
						upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
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
							account := &Account{ID: 9812, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1,
								Credentials: map[string]any{"access_token": "synthetic-token", "project_id": "synthetic-project"}}
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
						wire := gjson.GetBytes(upstream.requestBodies[0], "request.contents.0.parts.0")
						if mime == "text/plain" {
							require.Equal(t, "synthetic", wire.Get("text").String())
						} else if !antigravity && field == "file" && mime != "application/pdf" {
							// Gemini's legacy document carrier is serialized as text;
							// only supported PDF/plain document handling is upgraded.
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
