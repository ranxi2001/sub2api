package apicompat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToAnthropicMediaMIMEBlocks(t *testing.T) {
	for _, field := range []string{"input_image", "input_file"} {
		for _, mime := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "text/plain"} {
			t.Run(field+"/"+mime, func(t *testing.T) {
				data := base64.StdEncoding.EncodeToString([]byte("synthetic media"))
				part := ResponsesContentPart{Type: field}
				uri := "data:" + mime + ";base64," + data
				if field == "input_image" {
					part.ImageURL = uri
				} else {
					part.FileData = uri
				}
				content, err := json.Marshal([]ResponsesContentPart{part})
				require.NoError(t, err)
				input, err := json.Marshal([]ResponsesInputItem{{Role: "user", Content: content}})
				require.NoError(t, err)
				out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-sonnet-4-5", Input: input})
				require.NoError(t, err)
				require.Len(t, out.Messages, 1)
				blocks := parseContentBlocks(out.Messages[0].Content)
				require.Len(t, blocks, 1)
				wantType, sourceType := "image", "base64"
				if mime == "application/pdf" || mime == "text/plain" {
					wantType = "document"
				}
				if mime == "text/plain" {
					sourceType, data = "text", "synthetic media"
				}
				require.Equal(t, wantType, blocks[0].Type)
				require.Equal(t, &AnthropicImageSource{Type: sourceType, MediaType: mime, Data: data}, blocks[0].Source)
			})
		}
	}
}

func TestResponsesToAnthropicMediaRejectsSafely(t *testing.T) {
	for _, field := range []string{"input_image", "input_file"} {
		for index, uri := range []string{
			"data:video/mp4;base64,cHJpdmF0ZQ==", "data:audio/mpeg;base64,cHJpdmF0ZQ==",
			"data:application/octet-stream;base64,cHJpdmF0ZQ==", "data:image/svg+xml;base64,cHJpdmF0ZQ==",
			"data:private-header;base64,cHJpdmF0ZQ==", "data:;base64,cHJpdmF0ZQ==",
			"data:image/png,cHJpdmF0ZQ==", "data:image/png;base64,", "data:image/png;base64,private-invalid!",
			"data:image/png;charset=utf-8;base64,cHJpdmF0ZQ==", "data:text/plain;base64,/w==",
		} {
			t.Run(fmt.Sprintf("%s/case_%02d", field, index), func(t *testing.T) {
				key := "image_url"
				if field == "input_file" {
					key = "file_data"
				}
				content, err := json.Marshal([]map[string]string{{"type": field, key: uri}, {"type": "input_text", "text": "private-text"}})
				require.NoError(t, err)
				input, err := json.Marshal([]ResponsesInputItem{{Role: "user", Content: content}})
				require.NoError(t, err)
				out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-sonnet-4-5", Input: input})
				require.Nil(t, out)
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private")
				require.NotContains(t, err.Error(), "cHJpdmF0ZQ")
				require.NotContains(t, err.Error(), uri)
			})
		}
	}
}

func TestResponsesToAnthropicMediaURLPolicy(t *testing.T) {
	out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"keep"},{"type":"input_image","image_url":"https://example.invalid/private.png"},{"type":"input_file","file_data":"https://example.invalid/private.pdf"},{"type":"input_file","file_id":"file_1"}]}]`)})
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	require.JSONEq(t, `[{"type":"text","text":"keep"}]`, string(out.Messages[0].Content))
}

func TestResponsesToAnthropicToolMediaRejectsSafely(t *testing.T) {
	out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Input: json.RawMessage(`[{"type":"function_call","name":"read","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"data:video/mp4;base64,cHJpdmF0ZQ=="}]}]`)})
	require.Nil(t, out)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "cHJpdmF0ZQ")
}

func TestResponsesToAnthropicMediaLeavesTextUntouched(t *testing.T) {
	// Media-like text is ordinary customer text, including in tool output.
	const text = "literal data:video/mp4;base64,cHJpdmF0ZQ=="
	input, err := json.Marshal([]map[string]any{
		{"role": "user", "content": text},
		{"type": "function_call", "name": "read", "call_id": "call_1", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "call_1", "output": text},
	})
	require.NoError(t, err)
	out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Input: input})
	require.NoError(t, err)
	require.Len(t, out.Messages, 3)
	require.Equal(t, text, parseContentBlocks(out.Messages[0].Content)[0].Text)
	tool := parseContentBlocks(out.Messages[2].Content)[0]
	require.Equal(t, "tool_result", tool.Type)
	var output string
	require.NoError(t, json.Unmarshal(tool.Content, &output))
	require.Equal(t, text, output)
}
