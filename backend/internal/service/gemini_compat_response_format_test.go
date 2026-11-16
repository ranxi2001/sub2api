package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const geminiResponseFormatTestSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"score":{"type":"integer","minimum":1,"maximum":10},"note":{"anyOf":[{"type":"string","maxLength":600},{"type":"null"}]}},"required":["score","note"],"additionalProperties":false}`

func TestApplyGeminiChatResponseFormat(t *testing.T) {
	base := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":32000,"thinkingConfig":{"thinkingLevel":"high"}}}`)
	withTools := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"lookup"}]}],"generationConfig":{"maxOutputTokens":100}}`)
	noConfig := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	tests := []struct {
		name       string
		req        []byte
		format     string
		model      string
		wantMime   bool
		wantSchema string
		wantError  string
	}{
		{name: "json_schema writes mime and schema", req: base, format: `{"type":"json_schema","json_schema":{"name":"grade","strict":true,"schema":` + geminiResponseFormatTestSchema + `}}`, wantMime: true, wantSchema: geminiResponseFormatTestSchema},
		{name: "json_schema without schema is rejected", req: base, format: `{"type":"json_schema","json_schema":{"name":"grade"}}`, wantError: "response_format.json_schema.schema"},
		{name: "null schema is rejected", req: base, format: `{"type":"json_schema","json_schema":{"schema":null}}`, wantError: "response_format.json_schema.schema"},
		{name: "non-object schema is rejected", req: base, format: `{"type":"json_schema","json_schema":{"schema":[]}}`, wantError: "response_format.json_schema.schema"},
		{name: "json_object writes mime only", req: base, format: `{"type":"json_object"}`, wantMime: true},
		{name: "json_object creates generationConfig", req: noConfig, format: `{"type":"json_object"}`, wantMime: true},
		{name: "text leaves request unchanged", req: base, format: `{"type":"text"}`},
		{name: "null leaves request unchanged", req: base, format: `null`},
		{name: "empty leaves request unchanged", req: base, format: ``},
		{name: "invalid json is rejected", req: base, format: `{"type":"json_object"`, wantError: "response_format"},
		{name: "non-object is rejected", req: base, format: `"json_object"`, wantError: "response_format"},
		{name: "unknown format is rejected", req: base, format: `{"type":"yaml"}`, wantError: "response_format.type"},
		{name: "missing type is rejected", req: base, format: `{}`, wantError: "response_format.type"},
		{name: "image model is rejected", req: base, format: `{"type":"json_object"}`, model: "gemini-2.5-flash-image", wantError: "image output"},
		{name: "unlisted image model is rejected", req: base, format: `{"type":"json_object"}`, model: "models/gemini-3.5-flash-image-preview", wantError: "image output"},
		{name: "text model writes mime", req: base, format: `{"type":"json_object"}`, model: "gemini-4-argon", wantMime: true},
		{name: "tools with schema is rejected", req: withTools, format: `{"type":"json_schema","json_schema":{"name":"grade","schema":` + geminiResponseFormatTestSchema + `}}`, wantError: "with tools"},
		{name: "text with tools leaves request unchanged", req: withTools, format: `{"type":"text"}`},
		{name: "text with image model leaves request unchanged", req: base, format: `{"type":"text"}`, model: "gemini-2.5-flash-image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyGeminiChatResponseFormat(tt.req, json.RawMessage(tt.format), tt.model)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.True(t, json.Valid(got), string(got))
			if !tt.wantMime {
				require.Equal(t, string(tt.req), string(got))
				return
			}
			require.Equal(t, "application/json", gjson.GetBytes(got, "generationConfig.responseMimeType").String())
			schema := gjson.GetBytes(got, "generationConfig.responseJsonSchema")
			if tt.wantSchema == "" {
				require.False(t, schema.Exists(), string(got))
			} else {
				require.JSONEq(t, tt.wantSchema, schema.Raw)
			}
			require.False(t, gjson.GetBytes(got, "generationConfig.responseSchema").Exists(), string(got))
			require.Equal(t, gjson.GetBytes(tt.req, "contents").Raw, gjson.GetBytes(got, "contents").Raw)
			for _, path := range []string{"generationConfig.maxOutputTokens", "generationConfig.thinkingConfig"} {
				require.Equal(t, gjson.GetBytes(tt.req, path).Raw, gjson.GetBytes(got, path).Raw, path)
			}
		})
	}
}

func TestGeminiForwardAsChatCompletions_ForwardsResponseFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		stream     bool
		format     string
		wantSchema string
	}{
		{name: "non-stream json_schema", format: `{"type":"json_schema","json_schema":{"name":"grade","strict":true,"schema":` + geminiResponseFormatTestSchema + `}}`, wantSchema: geminiResponseFormatTestSchema},
		{name: "stream json_schema", stream: true, format: `{"type":"json_schema","json_schema":{"name":"grade","strict":true,"schema":` + geminiResponseFormatTestSchema + `}}`, wantSchema: geminiResponseFormatTestSchema},
		{name: "non-stream json_object", format: `{"type":"json_object"}`},
		{name: "stream json_object", stream: true, format: `{"type":"json_object"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusOK}
			if tt.stream {
				resp.Header = http.Header{"Content-Type": []string{"text/event-stream"}}
				resp.Body = io.NopCloser(strings.NewReader(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"{\"score\":7,\"note\":null}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5}}` + "\n\n"))
			} else {
				resp.Header = http.Header{"Content-Type": []string{"application/json"}}
				resp.Body = io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"{\"score\":7,\"note\":null}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5}}`))
			}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{resp}}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			body := []byte(`{"model":"gemini-4-argon","stream":` + strconv.FormatBool(tt.stream) + `,"reasoning_effort":"high","max_tokens":32000,"response_format":` + tt.format + `,"messages":[{"role":"user","content":"grade this"}]}`)
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

			_, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiCompatThinkingTestAccount(), body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Len(t, upstream.requestBodies, 1)
			sent := upstream.requestBodies[0]
			require.Equal(t, "application/json", gjson.GetBytes(sent, "generationConfig.responseMimeType").String(), string(sent))
			schema := gjson.GetBytes(sent, "generationConfig.responseJsonSchema")
			if tt.wantSchema == "" {
				require.False(t, schema.Exists(), string(sent))
			} else {
				require.JSONEq(t, tt.wantSchema, schema.Raw)
			}
			require.Equal(t, "high", gjson.GetBytes(sent, "generationConfig.thinkingConfig.thinkingLevel").String())
			require.Equal(t, int64(32000), gjson.GetBytes(sent, "generationConfig.maxOutputTokens").Int())
			if !tt.stream {
				require.Equal(t, `{"score":7,"note":null}`, gjson.Get(recorder.Body.String(), "choices.0.message.content").String())
			}
		})
	}
}

func TestGeminiForwardAsChatCompletions_ResponseFormatRejectedBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name, model, format, extra, wantError string
	}{
		{name: "missing schema", format: `{"type":"json_schema","json_schema":{"name":"grade"}}`, wantError: "response_format.json_schema.schema"},
		{name: "invalid schema", format: `{"type":"json_schema","json_schema":{"schema":"object"}}`, wantError: "response_format.json_schema.schema"},
		{name: "invalid format", format: `"json_object"`, wantError: "response_format"},
		{name: "unknown format", format: `{"type":"yaml"}`, wantError: "response_format.type"},
		{name: "image model", model: "gemini-2.5-flash-image", format: `{"type":"json_object"}`, wantError: "image output"},
		{name: "tools", format: `{"type":"json_object"}`, extra: `,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]`, wantError: "with tools"},
	}
	for _, tt := range tests {
		model := tt.model
		if model == "" {
			model = "gemini-4-argon"
		}
		for _, stream := range []bool{false, true} {
			t.Run(tt.name+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				upstream := &queuedHTTPUpstreamStub{}
				svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
				body := []byte(`{"model":"` + model + `","stream":` + strconv.FormatBool(stream) + `,"response_format":` + tt.format + tt.extra + `,"messages":[{"role":"user","content":"hi"}]}`)
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)
				result, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiCompatThinkingTestAccount(), body)
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
				require.Contains(t, gjson.Get(recorder.Body.String(), "error.message").String(), tt.wantError)
				require.Empty(t, upstream.requestBodies, "invalid format must not send or retry a billable request")
			})
		}
	}
}

func TestGeminiForwardAsChatCompletions_ResponseFormatRejectedAfterQueuePing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &queuedHTTPUpstreamStub{}
	svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
	body := []byte(`{"model":"gemini-4-argon","stream":true,"response_format":{"type":"json_schema","json_schema":{"name":"grade"}},"messages":[{"role":"user","content":"hi"}]}`)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)
	c.Header("Content-Type", "text/event-stream")
	_, err := io.WriteString(c.Writer, "data: {\"type\":\"ping\"}\n\n")
	require.NoError(t, err)
	c.Writer.Flush()

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, geminiCompatThinkingTestAccount(), body)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusOK, recorder.Code, "already committed queue status cannot change")
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	frames := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	require.Len(t, frames, 3)
	require.True(t, strings.HasPrefix(frames[1], "data: "))
	payload := strings.TrimPrefix(frames[1], "data: ")
	require.True(t, json.Valid([]byte(payload)))
	require.Equal(t, "invalid_request_error", gjson.Get(payload, "error.type").String())
	require.Equal(t, "invalid_response_format", gjson.Get(payload, "error.code").String())
	require.Contains(t, gjson.Get(payload, "error.message").String(), "response_format.json_schema.schema")
	require.Equal(t, "data: [DONE]", frames[2])
	require.Empty(t, upstream.requestBodies)
}

func TestGeminiForwardAsChatCompletions_CodeAssistKeepsResponseSchemaBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"{\"reasoning\":\"r\",\"answer\":\"a\"}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5}}}` + "\n\n")),
	}}}
	svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{
		ID:          612,
		Platform:    PlatformGemini,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "ya29.test-token", "project_id": "project-1"},
		Concurrency: 1,
	}
	schema := `{"type":"object","properties":{"reasoning":{"type":"string"},"answer":{"type":"string"},"big":{"type":"integer","maximum":9007199254740993}},"required":["reasoning","answer"]}`
	body := []byte(`{"model":"gemini-4-argon","response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + schema + `}},"messages":[{"role":"user","content":"hi"}]}`)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, upstream.requestBodies, 1)
	sent := upstream.requestBodies[0]
	require.Equal(t, "gemini-4-argon", gjson.GetBytes(sent, "model").String())
	require.Equal(t, "project-1", gjson.GetBytes(sent, "project").String())
	require.Equal(t, "application/json", gjson.GetBytes(sent, "request.generationConfig.responseMimeType").String(), string(sent))
	require.Equal(t, schema, gjson.GetBytes(sent, "request.generationConfig.responseJsonSchema").Raw)
	require.Equal(t, `{"reasoning":"r","answer":"a"}`, gjson.Get(recorder.Body.String(), "choices.0.message.content").String())
}
