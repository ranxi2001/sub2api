//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const mistralMixed = `[{"type":"text","text":"Hello "},{"type":"thinking","thinking":[{"type":"text","text":"reason "},{"type":"text","text":"two"}],"closed":true,"signature":"fictional"},{"type":"text","text":"world"}]`

func TestMistralRequestCompatibility(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	for _, tc := range []struct{ name, input, want string }{
		{"glm none", `"model":"zai-glm-5-3","reasoning_effort":"none"`, `"low"`},
		{"glm minimal", `"model":"zai-glm-5-3","reasoning_effort":"minimal"`, `"low"`},
		{"glm medium", `"model":"zai-glm-5-3","reasoning_effort":"medium"`, `"high"`},
		{"glm xhigh", `"model":"zai-glm-5-3","reasoning_effort":"xhigh"`, `"max"`},
		{"glm low", `"model":"zai-glm-5-3","reasoning_effort":"low"`, `"low"`},
		{"glm high", `"model":"zai-glm-5-3","reasoning_effort":"high"`, `"high"`},
		{"glm max", `"model":"zai-glm-5-3","reasoning_effort":"max"`, `"max"`},
		{"glm absent", `"model":"zai-glm-5-3"`, ``},
		{"unknown effort", `"model":"zai-glm-5-3","reasoning_effort":"future"`, `"future"`},
		{"invalid effort preserved", `"model":"zai-glm-5-3","reasoning_effort":42`, `42`},
		{"other model", `"model":"mistral-small-latest","reasoning_effort":"none"`, `"none"`},
		{"alias untouched", `"model":"zai-glm-5-3-latest","reasoning_effort":"none"`, `"none"`},
		{"explicit wins", `"model":"zai-glm-5-3","reasoning_effort":"max","store":true,"thinking":{"type":"disabled"},"enable_thinking":false`, `"max"`},
		{"null explicit wins", `"model":"zai-glm-5-3","reasoning_effort":null,"thinking":{"type":"disabled"}`, `null`},
		{"disabled type", `"model":"zai-glm-5-3","thinking":{"type":"disabled"},"enable_thinking":true`, `"low"`},
		{"disabled bool", `"model":"zai-glm-5-3","thinking":{"type":"enabled"},"enable_thinking":false`, `"low"`},
		{"enabled default", `"model":"zai-glm-5-3","store":false,"thinking":{"type":"enabled"},"enable_thinking":true`, ``},
		{"invalid flags", `"model":"zai-glm-5-3","thinking":"disabled","enable_thinking":"false"`, ``},
		{"null flag", `"model":"zai-glm-5-3","enable_thinking":null`, ``},
		{"other disabled", `"model":"mistral-small-latest","enable_thinking":false`, `"none"`},
		{"deepseek none", `"model":"deepseek-v4-1-flash-1017","reasoning_effort":"none"`, `"none"`},
		{"deepseek minimal", `"model":"deepseek-v4-1-flash-1017","reasoning_effort":"minimal"`, `"low"`},
		{"deepseek low", `"model":"deepseek-v4-1-flash-1017","reasoning_effort":"low"`, `"low"`},
		{"deepseek medium", `"model":"deepseek-v4-1-flash-1017","reasoning_effort":"medium"`, `"high"`},
		{"deepseek high", `"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high"`, `"high"`},
		{"deepseek xhigh", `"model":"deepseek-v4-1-flash-1017","reasoning_effort":"xhigh"`, `"xhigh"`},
		{"deepseek max", `"model":"deepseek-v4-1-flash-1017","reasoning_effort":"max"`, `"max"`},
		{"glm flash medium", `"model":"zai-glm-5-3-flash","reasoning_effort":"medium"`, `"high"`},
		{"glm flash none", `"model":"zai-glm-5-3-flash","reasoning_effort":"none"`, `"low"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{` + tc.input + `,"messages":[{"role":"user","content":"hello"}],"metadata":{"keep":true},"reasoning":{"keep":true}}`)
			out, err := normalizeMistralChatRequest(account, body)
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(out, "reasoning_effort").Raw)
			for _, key := range []string{"store", "thinking", "enable_thinking"} {
				require.False(t, gjson.GetBytes(out, key).Exists(), key)
			}
			require.Equal(t, `{"keep":true}`, gjson.GetBytes(out, "metadata").Raw)
			require.False(t, gjson.GetBytes(out, "reasoning").Exists())
			again, err := normalizeMistralChatRequest(account, out)
			require.NoError(t, err)
			require.Equal(t, out, again)
		})
	}
}

func TestMistralRequestCacheCompatibility(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	for _, role := range []string{"user", "system", "tool", "assistant"} {
		body := []byte(`{"messages":[{"role":"` + role + `","cache_control":{"type":"ephemeral"},"content":[{"type":"text","text":"a","cache_control":{}},{"type":"text","text":"b"}]}]}`)
		out, err := normalizeMistralChatRequest(account, body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(out, "messages.0.cache_control").Exists())
		if role == "assistant" {
			require.JSONEq(t, `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, gjson.GetBytes(out, "messages.0.content").Raw)
		} else {
			require.Equal(t, "ab", gjson.GetBytes(out, "messages.0.content").String())
		}
	}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"look","cache_control":{}},{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}]},{"role":"user","content":[{"type":"text","text":"extended","extension":true,"cache_control":{}}]},{"role":"assistant","reasoning_content":"duplicate","content":[{"type":"thinking","signature":"signed","closed":true,"cache_control":{},"thinking":[{"type":"text","text":"thought","cache_control":{}},{"type":"thinking","thinking":[{"type":"text","text":"nested","cache_control":{}}]},{"type":"reference","reference_ids":["ref"],"cache_control":{}}]}],"tool_calls":[{"function":{"arguments":"{\"cache_control\":true}"}}]},{"role":"tool","content":"{\"cache_control\":true}"}],"tools":[{"type":"function","cache_control":{},"function":{"name":"f","cache_control":{},"parameters":{"type":"object","properties":{"cache_control":{"type":"string"}},"cache_control":"keep"}}}]}`)
	out, err := normalizeMistralChatRequest(account, body)
	require.NoError(t, err)
	for _, path := range []string{"messages.0.content.0.cache_control", "messages.1.content.0.cache_control", "messages.2.content.0.cache_control", "messages.2.content.0.thinking.0.cache_control", "messages.2.content.0.thinking.1.thinking.0.cache_control", "messages.2.content.0.thinking.2.cache_control", "tools.0.cache_control", "tools.0.function.cache_control", "messages.2.reasoning_content"} {
		require.False(t, gjson.GetBytes(out, path).Exists(), path)
	}
	for _, path := range []string{"messages.0.content.1", "messages.1.content.0.extension", "messages.2.content.0.signature", "messages.2.content.0.closed", "messages.2.content.0.thinking.2.reference_ids", "messages.2.tool_calls", "messages.3.content", "tools.0.function.parameters"} {
		require.Equal(t, gjson.GetBytes(body, path).Value(), gjson.GetBytes(out, path).Value(), path)
	}
	require.True(t, gjson.GetBytes(out, "messages.1.content").IsArray())
	again, err := normalizeMistralChatRequest(account, out)
	require.NoError(t, err)
	require.Equal(t, out, again)
}

func TestMistralRequestNoopAndBoundaries(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	body := []byte("{ \"model\": \"zai-glm-5-3\", \"messages\": [{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"keep array\"}]}] }")
	out, err := normalizeMistralChatRequest(account, body)
	require.NoError(t, err)
	require.Equal(t, body, out)
	for _, foreign := range []*Account{nil, {Platform: PlatformOpenAI}} {
		body := []byte(`{"store":true,"model":"zai-glm-5-3","reasoning_effort":"none","messages":[]}`)
		out, err := normalizeMistralChatRequest(foreign, body)
		require.NoError(t, err)
		require.Equal(t, body, out)
	}
	for _, body := range []string{`{"messages":[{"role":"user","content":["private"]}]}`, `{"messages":[{"role":"user","content":[{"type":"thinking","thinking":"private","cache_control":{}}]}]}`, `{"messages":`, `null`} {
		_, err := normalizeMistralChatRequest(account, []byte(body))
		require.ErrorIs(t, err, errMistralContent)
		require.NotContains(t, err.Error(), "private")
	}
}

func TestMistralRequestMultiTurnNativeReplay(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	response, err := normalizeMistralChatResponse(account, []byte(`{"choices":[{"message":{"role":"assistant","content":`+mistralMixed+`}}]}`), false)
	require.NoError(t, err)
	msg := gjson.GetBytes(response, "choices.0.message").Raw
	body := []byte(`{"store":true,"messages":[{"role":"user","content":"first"},` + msg + `,{"role":"user","content":"second"},` + msg + `]}`)
	out, err := normalizeMistralChatRequest(account, body)
	require.NoError(t, err)
	for _, path := range []string{"messages.1.content", "messages.3.content"} {
		require.JSONEq(t, mistralMixed, gjson.GetBytes(out, path).Raw)
	}
	again, err := normalizeMistralChatRequest(account, out)
	require.NoError(t, err)
	require.Equal(t, out, again)
}

func TestMistralResponseNormalization(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	for _, field := range []string{"message", "delta"} {
		body := []byte(`{"extension":{"keep":true},"choices":[{"index":2,"finish_reason":"tool_calls","` + field + `":{"content":` + mistralMixed + `,"tool_calls":[{"index":0,"id":"call_fake","function":{"arguments":"{\""}}],"other":12}}],"usage":{"prompt_tokens":8,"completion_tokens":4}}`)
		normalized, err := normalizeMistralChatResponse(account, body, false)
		require.NoError(t, err)
		prefix := "choices.0." + field + "."
		require.Equal(t, "Hello world", gjson.GetBytes(normalized, prefix+"content").String())
		require.Equal(t, "reason two", gjson.GetBytes(normalized, prefix+"reasoning_content").String())
		require.JSONEq(t, mistralMixed, strings.TrimSpace(gjson.GetBytes(normalized, prefix+mistralContentField).String()))
		for _, path := range []string{"extension", "usage", "choices.0.index", "choices.0.finish_reason", prefix + "tool_calls", prefix + "other"} {
			require.JSONEq(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(normalized, path).Raw)
		}
	}
	for _, body := range []string{`{"choices":[{"delta":{"content":"text"}}]}`, `{"choices":[{"delta":{"content":null}}]}`, `{"choices":[],"usage":{"prompt_tokens":4}}`, "[DONE]"} {
		out, err := normalizeMistralChatResponse(account, []byte(body), true)
		require.NoError(t, err)
		require.Equal(t, body, string(out))
	}
}

func TestMistralUnknownAndReferencePolicy(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	for _, block := range []string{`{"type":"unknown","secret":"do not leak"}`, `{"type":"thinking","thinking":[{"type":"unknown"}]}`, `{"type":"text","text":42}`} {
		body := []byte(`{"choices":[{"delta":{"content":[` + block + `]}}]}`)
		for _, bridge := range []bool{false, true} {
			_, err := normalizeMistralChatResponse(account, body, bridge)
			require.ErrorIs(t, err, errMistralContent)
			require.NotContains(t, err.Error(), "do not leak")
		}
	}
	body := []byte(`{"choices":[{"message":{"content":[{"type":"thinking","closed":true,"signature":"fake","thinking":[{"type":"text","text":"thought"},{"type":"reference","reference_ids":["fake"]},{"type":"tool_reference","tool":"fake"}]}]}}]}`)
	out, err := normalizeMistralChatResponse(account, body, false)
	require.NoError(t, err)
	require.JSONEq(t, gjson.GetBytes(body, "choices.0.message.content").Raw, strings.TrimSpace(gjson.GetBytes(out, "choices.0.message.mistral_content").String()))
	_, err = normalizeMistralChatResponse(account, body, true)
	require.ErrorIs(t, err, errMistralContent)
}

func TestMistralRoundTripAndForeignAccounts(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	response, err := normalizeMistralChatResponse(account, []byte(`{"choices":[{"message":{"role":"assistant","content":`+mistralMixed+`,"tool_calls":[{"id":"fake"}]}}]}`), false)
	require.NoError(t, err)
	request := []byte(`{"messages":[` + gjson.GetBytes(response, "choices.0.message").Raw + `]}`)
	out, err := normalizeMistralChatRequest(account, request)
	require.NoError(t, err)
	require.JSONEq(t, mistralMixed, gjson.GetBytes(out, "messages.0.content").Raw)
	require.False(t, gjson.GetBytes(out, "messages.0.reasoning_content").Exists())
	require.False(t, gjson.GetBytes(out, "messages.0.mistral_content").Exists())
	require.Equal(t, "fake", gjson.GetBytes(out, "messages.0.tool_calls.0.id").String())
	for _, content := range []string{`"answer"`, `null`, mistralMixed} {
		input := []byte(`{"messages":[{"role":"assistant","content":` + content + `,"reasoning_content":"thought","tool_calls":[{"id":"fake"}]}]}`)
		out, err := normalizeMistralChatRequest(account, input)
		require.NoError(t, err)
		require.Equal(t, "fake", gjson.GetBytes(out, "messages.0.tool_calls.0.id").String())
		if content == mistralMixed {
			require.JSONEq(t, content, gjson.GetBytes(out, "messages.0.content").Raw)
		} else {
			require.Equal(t, "thought", gjson.GetBytes(out, "messages.0.content.0.thinking.0.text").String())
		}
	}
	for _, foreign := range []*Account{nil, {Platform: PlatformOpenAI}, {Platform: "deepseek"}} {
		for _, body := range []string{string(request), string(response), "invalid JSON"} {
			out, err := normalizeMistralChatResponse(foreign, []byte(body), true)
			require.NoError(t, err)
			require.Equal(t, body, string(out))
			out, err = normalizeMistralChatRequest(foreign, []byte(body))
			require.NoError(t, err)
			require.Equal(t, body, string(out))
			line, err := normalizeMistralChatSSELine(foreign, body)
			require.NoError(t, err)
			require.Equal(t, body, line)
		}
	}
}

func TestMistralTextArrayHistoryPreservesReasoning(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"answer","extension":true}],"reasoning_content":"thought","tool_calls":[{"id":"fake"}]}]}`)
	out, err := normalizeMistralChatRequest(account, body)
	require.NoError(t, err)
	require.Equal(t, "thought", gjson.GetBytes(out, "messages.0.content.0.thinking.0.text").String())
	require.Equal(t, "answer", gjson.GetBytes(out, "messages.0.content.1.text").String())
	require.True(t, gjson.GetBytes(out, "messages.0.content.1.extension").Bool())
	require.Equal(t, "fake", gjson.GetBytes(out, "messages.0.tool_calls.0.id").String())
}

func TestMistralFailedChunkPreservesUsage(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"unknown\"}]}}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n"
	c, w := mistralTestContext()
	s := &OpenAIGatewayService{}
	result, err := s.streamRawChatCompletions(c, mistralTestResponse(stream), account, "fake", "fake", "fake", nil, nil, time.Now(), 1)
	require.Error(t, err)
	require.NotNil(t, result)
	require.Equal(t, 8, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.NotContains(t, w.Body.String(), "[DONE]")
}

func TestMistralAccountTestParsesArrays(t *testing.T) {
	account := &Account{Platform: PlatformMistral}
	for _, valid := range []bool{true, false} {
		c, w := mistralTestContext()
		content := mistralMixed
		if !valid {
			content = `[{"type":"unknown"}]`
		}
		stream := "data: {\"choices\":[{\"delta\":{\"content\":" + content + "},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		err := (&AccountTestService{}).processOpenAIChatCompletionsStream(c, strings.NewReader(stream), account)
		if valid {
			require.NoError(t, err)
			require.Contains(t, w.Body.String(), "Hello world")
			require.NotContains(t, w.Body.String(), "reason two")
			require.Contains(t, w.Body.String(), `"success":true`)
		} else {
			require.Error(t, err)
			require.NotContains(t, w.Body.String(), `"success":true`)
		}
	}
}

func mistralTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c, w
}
func mistralTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestMistralBridgeAndRawReaders(t *testing.T) {
	s := &OpenAIGatewayService{}
	account := &Account{Platform: PlatformMistral}
	body := `{"id":"fake","model":"fake","choices":[{"index":0,"message":{"role":"assistant","content":` + mistralMixed + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":4}}`
	for _, writer := range []compatErrorWriter{writeOpenAIResponsesFallbackError, writeAnthropicError} {
		c, _ := mistralTestContext()
		resp, usage, err := s.readCCUpstreamJSONResponse(c, mistralTestResponse(body), account, writer)
		require.NoError(t, err)
		require.Equal(t, 8, usage.InputTokens)
		data, err := json.Marshal(resp)
		require.NoError(t, err)
		require.Equal(t, "Hello world", gjson.GetBytes(data, "choices.0.message.content").String())
	}
	c, w := mistralTestContext()
	_, err := s.bufferChatCompletionsAsAnthropic(c, mistralTestResponse(body), account, "fake", "fake", "fake", nil, nil, time.Now())
	require.NoError(t, err)
	require.Contains(t, w.Body.String(), `"thinking":"reason two"`)
	require.Contains(t, w.Body.String(), `"text":"Hello world"`)
	c, w = mistralTestContext()
	_, err = s.bufferChatCompletionsAsResponses(c, mistralTestResponse(body), account, "fake", nil, nil, false, nil, "fake", "fake", nil, nil, false, time.Now(), false, nil)
	require.NoError(t, err)
	require.Contains(t, w.Body.String(), "reason two")
	require.Contains(t, w.Body.String(), "Hello world")
	c, w = mistralTestContext()
	_, err = s.bufferRawChatCompletions(c, mistralTestResponse(body), account, "fake", "fake", "fake", nil, nil, time.Now())
	require.NoError(t, err)
	require.Equal(t, "reason two", gjson.Get(w.Body.String(), "choices.0.message.reasoning_content").String())
	stream := "data: " + `{"choices":[{"index":0,"delta":{"content":` + mistralMixed + `}}]}` + "\n\ndata: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"fragment"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: " + `{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":4}}` + "\n\ndata: [DONE]\n\n"
	// Exercise both complete bridge stream writers with a text/reasoning stream.
	bridgeStream := "data: " + `{"id":"fake","choices":[{"index":0,"delta":{"content":` + mistralMixed + `},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	c, w = mistralTestContext()
	_, err = s.streamChatCompletionsAsAnthropic(c, mistralTestResponse(bridgeStream), account, "fake", "fake", "fake", nil, nil, time.Now())
	require.NoError(t, err)
	require.Contains(t, w.Body.String(), "reason two")
	require.Contains(t, w.Body.String(), "Hello world")
	c, w = mistralTestContext()
	_, err = s.streamChatCompletionsAsResponses(c, mistralTestResponse(bridgeStream), account, "fake", nil, nil, false, nil, "fake", "fake", nil, nil, false, time.Now(), nil)
	require.NoError(t, err)
	require.Contains(t, w.Body.String(), "reason two")
	require.Contains(t, w.Body.String(), "Hello world")
	c, _ = mistralTestContext()
	var chunks []*apicompat.ChatCompletionsChunk
	state := s.scanCCStream(c, mistralTestResponse(stream), account, "test", "fake", time.Now(), func(ch *apicompat.ChatCompletionsChunk) { chunks = append(chunks, ch) })
	require.NoError(t, state.Err)
	require.True(t, state.SawDone)
	require.Len(t, chunks, 3)
	require.Equal(t, 8, state.Usage.InputTokens)
	data, _ := json.Marshal(chunks[0])
	require.Equal(t, "reason two", gjson.GetBytes(data, "choices.0.delta.reasoning_content").String())
	c, w = mistralTestContext()
	result, err := s.streamRawChatCompletions(c, mistralTestResponse(stream), account, "fake", "fake", "fake", nil, nil, time.Now(), 1)
	require.NoError(t, err)
	require.Equal(t, 8, result.Usage.InputTokens)
	require.Contains(t, w.Body.String(), "data: [DONE]")
	require.Contains(t, w.Body.String(), `"mistral_content"`)
	bad := "data: " + `{"choices":[{"delta":{"content":[{"type":"secret-unknown"}]}}]}` + "\n\ndata: [DONE]\n\n"
	c, _ = mistralTestContext()
	state = s.scanCCStream(c, mistralTestResponse(bad), account, "test", "fake", time.Now(), func(*apicompat.ChatCompletionsChunk) { t.Fatal("unexpected emission") })
	require.Error(t, state.Err)
	require.False(t, state.SawDone)
	c, w = mistralTestContext()
	_, err = s.streamRawChatCompletions(c, mistralTestResponse(bad), account, "fake", "fake", "fake", nil, nil, time.Now(), 1)
	require.Error(t, err)
	require.NotContains(t, w.Body.String(), "[DONE]")
	require.NotContains(t, w.Body.String(), "secret-unknown")
	// A malformed chunk after a finish_reason must still fail, never emit DONE.
	c, w = mistralTestContext()
	_, err = s.streamRawChatCompletions(c, mistralTestResponse(strings.TrimSuffix(bridgeStream, "data: [DONE]\n\n")+bad), account, "fake", "fake", "fake", nil, nil, time.Now(), 1)
	require.Error(t, err)
	require.Contains(t, w.Body.String(), "Hello world")
	require.NotContains(t, w.Body.String(), "[DONE]")
	c, w = mistralTestContext()
	_, err = s.bufferRawChatCompletions(c, mistralTestResponse(`{"choices":[{"message":{"content":[{"type":"secret-unknown"}]}}]}`), account, "fake", "fake", "fake", nil, nil, time.Now())
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.NotContains(t, w.Body.String(), "secret-unknown")
}
