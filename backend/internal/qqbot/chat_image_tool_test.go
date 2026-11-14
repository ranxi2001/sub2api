package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func imageToolTestResponse(protocol, args string) map[string]any {
	if protocol == "responses" {
		return map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "name": chatImageToolName, "arguments": args, "call_id": "call-1"}}, "usage": map[string]int{"input_tokens": 11, "output_tokens": 7}}
	}
	return map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"content": nil, "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": chatImageToolName, "arguments": args}}}}}}, "usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 7}}
}

func imageToolTestJSON(t *testing.T, protocol, args string) []byte {
	t.Helper()
	raw, err := json.Marshal(imageToolTestResponse(protocol, args))
	require.NoError(t, err)
	return raw
}

func textToolTestJSON(t *testing.T, protocol, text string) []byte {
	t.Helper()
	args, err := json.Marshal(map[string]string{"text": text})
	require.NoError(t, err)
	return []byte(strings.ReplaceAll(string(imageToolTestJSON(t, protocol, string(args))), `"name":"generate_image"`, `"name":"reply_text"`))
}

func imageToolTestStream(t *testing.T, protocol, args string) []byte {
	t.Helper()
	if protocol == "responses" {
		return []byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":" + string(imageToolTestJSON(t, protocol, args)) + "}\n\n")
	}
	var b strings.Builder
	runes := []rune(args)
	for i, part := range []string{string(runes[:len(runes)/2]), string(runes[len(runes)/2:])} {
		fn := map[string]string{"arguments": part}
		call := map[string]any{"index": 0, "function": fn}
		if i == 0 {
			call["id"], call["type"], fn["name"] = "call-1", "function", chatImageToolName
		}
		event, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}}}})
		require.NoError(t, err)
		fmt.Fprintf(&b, "data: %s\n\n", event)
	}
	_, _ = b.WriteString("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n")
	return []byte(b.String())
}

func TestChatTurnModelChoosesImageAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, "Bearer chat-key", r.Header.Get("Authorization"))
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, "required", body["tool_choice"])
					require.Equal(t, false, body["parallel_tool_calls"])
					require.Equal(t, false, body["stream"])
					tools, ok := body["tools"].([]any)
					require.True(t, ok)
					require.Len(t, tools, 3)
					tool, ok := tools[0].(map[string]any)
					require.True(t, ok)
					require.Equal(t, "function", tool["type"])
					key := "messages"
					if protocol == "responses" {
						key = "input"
						require.Equal(t, "/responses", r.URL.Path)
					} else {
						tool, ok = tool["function"].(map[string]any)
						require.True(t, ok)
						require.Equal(t, "/chat/completions", r.URL.Path)
					}
					require.Equal(t, chatImageToolName, tool["name"])
					require.Equal(t, true, tool["strict"])
					messages, err := json.Marshal(body[key])
					require.NoError(t, err)
					for _, want := range []string{"赛博朋克猫咪海报", "就这个吧", "data:image/png;base64,dGVzdA=="} {
						require.Contains(t, string(messages), want)
					}
					const args = `{"prompt":"赛博朋克猫咪海报，蓝紫色霓虹"}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write(imageToolTestStream(t, protocol, args))
					} else {
						_, _ = w.Write(imageToolTestJSON(t, protocol, args))
					}
				}))
				defer server.Close()
				s := New(nil, nil, false, nil)
				c := DefaultConfig()
				c.BaseURL, c.APIKey, c.ChatProtocol, c.ImageEnabled = server.URL, "chat-key", protocol, true
				got, err := s.chatTurn(context.Background(), c, []Message{{Role: "assistant", Content: "赛博朋克猫咪海报"}, {Role: "user", Content: "就这个吧", Images: []string{"data:image/png;base64,dGVzdA=="}}})
				require.NoError(t, err)
				require.Equal(t, chatTurnResult{ImagePrompt: "赛博朋克猫咪海报，蓝紫色霓虹", InputTokens: 11, OutputTokens: 7}, got)
			})
		}
	}
}

func TestChatImageToolRejectsInvalidAndPartialCalls(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, args := range []string{`{`, `{}`, `{"prompt":null}`, `{"prompt":123}`, `{"prompt":" "}`, `{"prompt":"cat","url":"https://example.invalid"}`, `{"prompt":"cat"}{}`, `{"prompt":"` + strings.Repeat("字", 6001) + `"}`} {
			got, err := parseChatTurn(imageToolTestJSON(t, protocol, args), "application/json")
			require.Error(t, err)
			require.Empty(t, got.ImagePrompt)
			require.EqualValues(t, 11, got.InputTokens)
		}
		raw := string(imageToolTestJSON(t, protocol, `{"prompt":"cat"}`))
		got, err := parseChatTurn([]byte(strings.ReplaceAll(raw, chatImageToolName, "delete_account")), "application/json")
		require.Error(t, err)
		require.Empty(t, got.ImagePrompt)
	}
	for _, raw := range []string{
		`{"choices":[{"message":{"tool_calls":[{"type":"function","function":{"name":"generate_image","arguments":"{\"prompt\":\"cat\"}"}},{"type":"function","function":{"name":"generate_image","arguments":"{\"prompt\":\"dog\"}"}}]}}]}`,
		`{"status":"incomplete","output":[{"type":"function_call","name":"generate_image","arguments":"{\"prompt\":\"cat\"}"}]}`,
		`{"choices":[{"finish_reason":"length","message":{"tool_calls":[{"type":"function","function":{"name":"generate_image","arguments":"{\"prompt\":\"cat\"}"}}]}}]}`,
	} {
		got, err := parseChatTurn([]byte(raw), "application/json")
		require.Error(t, err)
		require.Empty(t, got.ImagePrompt)
	}
	stream := string(imageToolTestStream(t, "chat_completions", `{"prompt":"cat"}`))
	for _, raw := range []string{
		strings.ReplaceAll(stream, "data: [DONE]", ""),
		strings.ReplaceAll(stream, `"finish_reason":"tool_calls"`, `"finish_reason":"length"`),
		strings.ReplaceAll(stream, `"index":0`, `"index":1`),
		stream + "data: {\"error\":{\"message\":\"upstream failed\"}}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"partial\"}\n\ndata: [DONE]\n\n",
	} {
		got, err := parseChatTurn([]byte(raw), "text/event-stream")
		require.Error(t, err)
		require.Empty(t, got.ImagePrompt)
	}
}

func TestChatTurnTextIsNeverExecutedAsTool(t *testing.T) {
	text := `{"name":"generate_image","arguments":{"prompt":"example only"}}`
	raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": text}}}})
	require.NoError(t, err)
	got, err := parseChatTurn(raw, "application/json")
	require.ErrorIs(t, err, errChatActionMissing)
	require.Empty(t, got.Text)
	require.Empty(t, got.ImagePrompt)
}

func TestChatTurnDisabledAndAutonomousHaveNoTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.NotContains(t, body, "tools")
		require.NotContains(t, body, "tool_choice")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"text only"}}]}`))
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	c := DefaultConfig()
	c.BaseURL = server.URL
	got, err := s.chatTurn(context.Background(), c, []Message{{Role: "user", Content: "来张图"}})
	require.NoError(t, err)
	require.Equal(t, "text only", got.Text)
	c.ImageEnabled = true
	_, _, _, err = s.chat(context.Background(), c, []Message{{Role: "user", Content: "来张图"}})
	require.NoError(t, err)
}

func TestChatTurnExplicitTextChoiceAndPersonaOrder(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, text := range []string{"你好！", "你想修改图片的哪个部分？", "这是图片里的内容。", "这个请求我无法处理。", "今天只聊聊图片构图，不生成。"} {
			t.Run(protocol+"/"+text, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, "required", body["tool_choice"])
					key := "messages"
					if protocol == "responses" {
						key = "input"
					}
					rawMessages, ok := body[key].([]any)
					require.True(t, ok)
					require.Len(t, rawMessages, 4)
					messages := make([]map[string]any, 0, len(rawMessages))
					for _, item := range rawMessages {
						message, ok := item.(map[string]any)
						require.True(t, ok)
						messages = append(messages, message)
					}
					require.Equal(t, "你只能聊天", messages[0]["content"])
					require.Equal(t, groupHistoryInstructions, messages[1]["content"])
					contract := messages[2]
					require.Equal(t, "system", contract["role"])
					require.Contains(t, contract["content"], chatActionInstructions)
					require.Equal(t, "user", messages[3]["role"])
					toolNames := make([]string, 0, 2)
					tools, ok := body["tools"].([]any)
					require.True(t, ok)
					for _, item := range tools {
						tool, ok := item.(map[string]any)
						require.True(t, ok)
						if protocol == "chat_completions" {
							tool, ok = tool["function"].(map[string]any)
							require.True(t, ok)
						}
						name, ok := tool["name"].(string)
						require.True(t, ok)
						toolNames = append(toolNames, name)
					}
					require.ElementsMatch(t, []string{chatTextToolName, chatImageToolName}, toolNames)
					_, _ = w.Write(textToolTestJSON(t, protocol, text))
				}))
				defer server.Close()
				s := New(nil, nil, false, nil)
				c := DefaultConfig()
				c.BaseURL, c.ImageEnabled, c.ChatProtocol = server.URL, true, protocol
				got, err := s.chatTurn(context.Background(), c, []Message{
					{Role: "system", Content: "你只能聊天"},
					{Role: "system", Content: groupHistoryInstructions},
					{Role: "user", Content: "你好"},
				})
				require.NoError(t, err)
				require.Equal(t, text, got.Text)
				require.Empty(t, got.ImagePrompt)
				require.Zero(t, got.EditImageIndex)
				require.Equal(t, 1, calls, "normal chat needs only one model request")
			})
		}
	}
}

func TestChatTextChoiceValidationAndStreams(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, args := range []string{`{"text":"正常回答"}`, `{}`, `{"text":null}`, `{"text":" "}`, `{"text":123}`, `{"text":"hi","prompt":"cat"}`, `{"text":"hi"}{}`, `{"text":"` + strings.Repeat("字", 32001) + `"}`} {
				raw, ct := imageToolTestJSON(t, protocol, args), "application/json"
				if stream {
					raw, ct = imageToolTestStream(t, protocol, args), "text/event-stream"
				}
				raw = []byte(strings.ReplaceAll(string(raw), chatImageToolName, chatTextToolName))
				got, err := parseChatTurn(raw, ct)
				if args == `{"text":"正常回答"}` {
					require.NoError(t, err)
					require.Equal(t, "正常回答", got.Text)
				} else {
					require.Error(t, err)
					require.Empty(t, got.Text)
				}
				require.Empty(t, got.ImagePrompt)
				require.Zero(t, got.EditImageIndex)
				require.EqualValues(t, 11, got.InputTokens)
				require.EqualValues(t, 7, got.OutputTokens)
			}
		}
	}
}

func TestChatTurnRejectsUnstructuredPromises(t *testing.T) {
	for _, tc := range []struct{ ct, raw string }{
		{"application/json", `{"choices":[{"finish_reason":"stop","message":{"content":"我给你安排一张。"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`},
		{"application/json", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"我给你安排一张。"}]}],"usage":{"input_tokens":11,"output_tokens":7}}`},
		{"text/event-stream", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"我给你安排一张。\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n"},
		{"text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output_text\":\"我给你安排一张。\",\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n"},
	} {
		got, err := parseChatTurn([]byte(tc.raw), tc.ct)
		require.ErrorIs(t, err, errChatActionMissing)
		require.Empty(t, got.Text)
		require.Empty(t, got.ImagePrompt)
		require.EqualValues(t, 11, got.InputTokens)
		require.EqualValues(t, 7, got.OutputTokens)
	}
}
