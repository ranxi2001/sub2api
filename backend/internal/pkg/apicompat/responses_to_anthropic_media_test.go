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
				out, err := ResponsesToAnthropicRequestWithOptions(&ResponsesRequest{Model: "claude-sonnet-4-5", Input: input}, ResponsesToAnthropicOptions{StrictMedia: true})
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
				out, err := ResponsesToAnthropicRequestWithOptions(&ResponsesRequest{Model: "claude-sonnet-4-5", Input: input}, ResponsesToAnthropicOptions{StrictMedia: true})
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
	out, err := ResponsesToAnthropicRequestWithOptions(&ResponsesRequest{Input: json.RawMessage(`[{"type":"function_call","name":"read","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"data:video/mp4;base64,cHJpdmF0ZQ=="}]}]`)}, ResponsesToAnthropicOptions{StrictMedia: true})
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

func TestResponsesToAnthropicMediaPolicyDefaultsRemainCompatible(t *testing.T) {
	for _, mime := range []string{"video/mp4", "audio/mpeg", "application/octet-stream"} {
		t.Run(mime, func(t *testing.T) {
			input := json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"data:` + mime + `;base64,c3ludGhldGlj"}]}]`)
			request := &ResponsesRequest{Input: input}
			out, err := ResponsesToAnthropicRequest(request)
			require.NoError(t, err)
			block := parseContentBlocks(out.Messages[0].Content)[0]
			require.Equal(t, "image", block.Type)
			require.Equal(t, mime, block.Source.MediaType)
			require.Equal(t, "c3ludGhldGlj", block.Source.Data)
			_, err = ResponsesToAnthropicRequestWithOptions(request, ResponsesToAnthropicOptions{StrictMedia: true})
			var mediaErr *AnthropicMediaError
			require.ErrorAs(t, err, &mediaErr)
		})
	}
}

func TestChatAnthropicToolMediaPreservesParallelAndStringOutputs(t *testing.T) {
	var chat ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"messages":[
		{"role":"user","content":"read"},
		{"role":"assistant","tool_calls":[{"id":"call_A","type":"function","function":{"name":"read","arguments":"{}"}},{"id":"call_B","type":"function","function":{"name":"read","arguments":"{}"}},{"id":"call_C","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_A","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,c3ludGhldGlj"}}]},
		{"role":"tool","tool_call_id":"call_B","content":"literal data:video/mp4;base64,c3ludGhldGlj"},
		{"role":"tool","tool_call_id":"call_C","content":[{"type":"file","file":{"file_data":"data:text/plain;base64,c3ludGhldGlj"}}]}
	]}`), &chat))
	require.NoError(t, ValidateChatCompletionsAnthropicMedia(&chat))
	responses, err := ChatCompletionsToResponses(&chat)
	require.NoError(t, err)
	require.NoError(t, PreserveChatCompletionsAnthropicToolMedia(&chat, responses))
	out, err := ResponsesToAnthropicRequestWithOptions(responses, ResponsesToAnthropicOptions{StrictMedia: true})
	require.NoError(t, err)
	require.Len(t, out.Messages, 3)
	blocks := parseContentBlocks(out.Messages[2].Content)
	require.Len(t, blocks, 3)
	require.Equal(t, "call_A", blocks[0].ToolUseID)
	require.Equal(t, "image", parseContentBlocks(blocks[0].Content)[0].Type)
	require.Equal(t, "call_B", blocks[1].ToolUseID)
	var text string
	require.NoError(t, json.Unmarshal(blocks[1].Content, &text))
	require.Equal(t, "literal data:video/mp4;base64,c3ludGhldGlj", text)
	require.Equal(t, "call_C", blocks[2].ToolUseID)
	require.Equal(t, "synthetic", parseContentBlocks(blocks[2].Content)[0].Source.Data)
}
