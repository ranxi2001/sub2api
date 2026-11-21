//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

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
