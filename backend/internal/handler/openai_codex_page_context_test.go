package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponses_CodexPageContextDoesNotRequireCallID(t *testing.T) {
	response := serveCodexPageContextRequest(t, `{"model":"gpt-5.1","stream":false,"input":[{"type":"function_call_output","id":"fco_page_1","name":"codex_apps_open_page","namespace":"agent","output":"{\"page_id\":null}"}]}`)
	require.NotEqual(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "function_call_output requires call_id")
}

func TestOpenAIResponses_CodexSelectedPageContextDoesNotRequireCallID(t *testing.T) {
	response := serveCodexPageContextRequest(t, `{"model":"gpt-5.1","stream":false,"input":[{"type":"function_call_output","name":"codex_apps_selected_page_part_2_of_2","namespace":"agent","output":"Companion Page snapshot part 2 of 2"}]}`)
	require.NotEqual(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "function_call_output requires call_id")
}

func TestOpenAIResponses_CodexPageContextCoexistsWithDelegation(t *testing.T) {
	body := `{"model":"gpt-5.1","stream":false,"input":[{"type":"function_call_output","name":"codex_apps_open_page","namespace":"agent","output":"{\"page_id\":null}"},{"type":"function_call_output","name":"send_message_to_thread","namespace":"codex_app","output":` + mustJSON(t, delegationEnvelope) + `}]}`
	response := serveCodexPageContextRequest(t, body)
	require.NotEqual(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "function_call_output requires call_id")
}

func serveCodexPageContextRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	groupID := int64(2)
	require.NoError(t, h.gatewayService.BindOpenAIHTTPResponseOwner(context.Background(), groupID, "resp_page_context", 1, 101))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 101, UserID: 1, GroupID: &groupID, User: &service.User{ID: 1}, AllowLongContext: true})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
		c.Next()
	})
	router.POST("/responses", h.Responses)
	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestOpenAIResponses_CodexPageContextContinuation(t *testing.T) {
	tests := []struct {
		name      string
		previous  string
		rejection string
	}{
		{name: "owned", previous: `"resp_page_context"`},
		{name: "null", previous: `null`},
		{name: "blank", previous: `""`},
		{name: "unowned", previous: `"resp_unknown"`, rejection: "previous_response_id is not available for this user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"gpt-5.1","stream":false,"previous_response_id":` + tt.previous + `,"input":[{"type":"function_call_output","name":"codex_apps_open_page","output":"page context"}]}`
			response := serveCodexPageContextRequest(t, body)
			if tt.rejection != "" {
				require.Equal(t, http.StatusBadRequest, response.Code)
				require.Contains(t, response.Body.String(), tt.rejection)
				return
			}
			require.NotEqual(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
}

func TestOpenAIResponses_CodexPageContextRetainsToolValidation(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "ordinary output", input: `{"type":"function_call_output","name":"ordinary_tool","output":"done"}`},
		{name: "other namespace", input: `{"type":"function_call_output","name":"codex_apps_open_page","namespace":"ordinary","output":"done"}`},
		{name: "unrecognized part name", input: `{"type":"function_call_output","name":"codex_apps_selected_page_part_other","output":"done"}`},
		{name: "numeric output", input: `{"type":"function_call_output","name":"codex_apps_open_page","output":0}`},
		{name: "boolean output", input: `{"type":"function_call_output","name":"codex_apps_open_page","output":false}`},
		{name: "mixed ordinary result", input: `{"type":"function_call_output","name":"codex_apps_open_page","output":"context"},{"type":"function_call_output","name":"ordinary_tool","output":"done"}`},
		{name: "real call id still needs context", input: `{"type":"function_call_output","name":"codex_apps_open_page","call_id":"call_real","output":"done"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := serveCodexPageContextRequest(t, `{"model":"gpt-5.1","stream":false,"input":[`+tt.input+`]}`)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Contains(t, response.Body.String(), "function_call_output requires")
		})
	}
}

func TestNormalizeCodexPageContextPreservesHistoryAndExactJSON(t *testing.T) {
	body := []byte(`{
 "model":"gpt-5.1", "previous_response_id":null,
 "metadata": { "integer":9007199254740993, "decimal":0.123456789012345678901, "flag":false },
 "input":[
  { "type":"message", "role":"user", "content":"before" },
  {"type":"function_call","call_id":"call_real","name":"ordinary_tool","arguments":"{}"},
  {"type":"function_call_output","call_id":"call_real","output":"done"},
  {"type":"item_reference","id":"item_existing"},
  {"type":"function_call_output","id":"fco_page","name":"codex_apps_open_page","namespace":"agent","output":"页面上下文\n{\"page_id\":null}","internal_chat_message_metadata_passthrough":{"turn_id":"turn_example","create_time":"example"}},
  {"type":"function_call_output","name":"codex_apps_selected_page_part_1_of_1","output":"selected context"},
  { "type":"message", "role":"user", "content":"after" }
 ]}`)
	got, changed := normalizeCodexPageContext(body)
	require.True(t, changed)
	require.True(t, gjson.ValidBytes(got))
	for _, path := range []string{"model", "previous_response_id", "metadata", "input.0", "input.1", "input.2", "input.3", "input.6"} {
		require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(got, path).Raw, path)
	}
	require.JSONEq(t, `{"type":"message","role":"user","content":[{"type":"input_text","text":"页面上下文\n{\"page_id\":null}"}]}`, gjson.GetBytes(got, "input.4").Raw)
	require.JSONEq(t, `{"type":"message","role":"user","content":[{"type":"input_text","text":"selected context"}]}`, gjson.GetBytes(got, "input.5").Raw)
	again, changedAgain := normalizeCodexPageContext(got)
	require.False(t, changedAgain)
	require.Equal(t, got, again)
}

func TestNormalizeCodexPageContextAcceptsOnlySyntheticShapes(t *testing.T) {
	tests := []struct {
		name    string
		item    string
		changed bool
	}{
		{name: "agent namespace", item: `{"type":"function_call_output","name":"codex_apps_open_page","namespace":"agent","output":"context"}`, changed: true},
		{name: "omitted namespace", item: `{"type":"function_call_output","name":"codex_apps_open_page","output":"context"}`, changed: true},
		{name: "empty output", item: `{"type":"function_call_output","name":"codex_apps_open_page","output":""}`, changed: true},
		{name: "blank call id", item: `{"type":"function_call_output","name":"codex_apps_open_page","call_id":"  ","output":"context"}`, changed: true},
		{name: "real call id", item: `{"type":"function_call_output","name":"codex_apps_open_page","call_id":"call_real","output":"done"}`},
		{name: "null call id", item: `{"type":"function_call_output","name":"codex_apps_open_page","call_id":null,"output":"context"}`},
		{name: "numeric call id", item: `{"type":"function_call_output","name":"codex_apps_open_page","call_id":0,"output":"context"}`},
		{name: "foreign namespace", item: `{"type":"function_call_output","name":"codex_apps_open_page","namespace":"tools","output":"context"}`},
		{name: "blank namespace", item: `{"type":"function_call_output","name":"codex_apps_open_page","namespace":"","output":"context"}`},
		{name: "null namespace", item: `{"type":"function_call_output","name":"codex_apps_open_page","namespace":null,"output":"context"}`},
		{name: "numeric output", item: `{"type":"function_call_output","name":"codex_apps_open_page","output":0}`},
		{name: "boolean output", item: `{"type":"function_call_output","name":"codex_apps_open_page","output":false}`},
		{name: "object output", item: `{"type":"function_call_output","name":"codex_apps_open_page","output":{}}`},
		{name: "array output", item: `{"type":"function_call_output","name":"codex_apps_open_page","output":[]}`},
		{name: "null output", item: `{"type":"function_call_output","name":"codex_apps_open_page","output":null}`},
		{name: "missing output", item: `{"type":"function_call_output","name":"codex_apps_open_page"}`},
		{name: "other type", item: `{"type":"custom_tool_call_output","name":"codex_apps_open_page","output":"context"}`},
		{name: "padded type", item: `{"type":" function_call_output ","name":"codex_apps_open_page","output":"context"}`},
		{name: "padded name", item: `{"type":"function_call_output","name":" codex_apps_open_page ","output":"context"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.1","input":[` + tt.item + `]}`)
			got, changed := normalizeCodexPageContext(body)
			require.Equal(t, tt.changed, changed)
			if !tt.changed {
				require.Equal(t, body, got)
				return
			}
			require.Equal(t, "message", gjson.GetBytes(got, "input.0.type").String())
			require.Equal(t, gjson.GetBytes(body, "input.0.output").String(), gjson.GetBytes(got, "input.0.content.0.text").String())
		})
	}
}

func TestNormalizeCodexPageContextValidatesSelectedPartName(t *testing.T) {
	tests := []struct {
		name    string
		changed bool
	}{
		{name: "codex_apps_selected_page_part_1_of_1", changed: true},
		{name: "codex_apps_selected_page_part_2_of_3", changed: true},
		{name: "codex_apps_selected_page_part_0_of_1"},
		{name: "codex_apps_selected_page_part_1_of_0"},
		{name: "codex_apps_selected_page_part_2_of_1"},
		{name: "codex_apps_selected_page_part_01_of_2"},
		{name: "codex_apps_selected_page_part_1_of_02"},
		{name: "codex_apps_selected_page_part_-1_of_2"},
		{name: "codex_apps_selected_page_part_+1_of_2"},
		{name: "codex_apps_selected_page_part_other"},
		{name: "codex_apps_selected_page_part_1_of_1_extra"},
		{name: "codex_apps_selected_page_part_99999999999999999999999999999999_of_99999999999999999999999999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.1","input":[{"type":"function_call_output","name":` + mustJSON(t, tt.name) + `,"output":"context"}]}`)
			got, changed := normalizeCodexPageContext(body)
			require.Equal(t, tt.changed, changed)
			if !tt.changed {
				require.Equal(t, body, got)
			}
		})
	}
}

func TestNormalizeCodexPageContextRejectsAmbiguousJSON(t *testing.T) {
	tests := []string{
		`{"input":[{"type":"function_call_output","type":"message","name":"codex_apps_open_page","output":"context"}]}`,
		`{"input":[{"type":"function_call_output","name":"codex_apps_open_page","output":"context","output":"other"}]}`,
		`{"input":[{"type":"function_call_output","name":"codex_apps_open_page","call_id":"","call_id":"call_real","output":"context"}]}`,
		`{"input":[{"type":"function_call_output","name":"codex_apps_open_page","namespace":"agent","namespace":"tools","output":"context"}]}`,
		`{"previous_response_id":null,"previous_response_id":"resp_real","input":[{"type":"function_call_output","name":"codex_apps_open_page","output":"context"}]}`,
		`{"metadata":{"key":1,"key":2},"input":[{"type":"function_call_output","name":"codex_apps_open_page","output":"context"}]}`,
		`{"input":[{"type":"function_call_output","name":"codex_apps_open_page","output":"context"}]`,
		`{"input":[{"type":"function_call_output","name":"codex_apps_open_page","output":"context"}]} {}`,
		`{"input":"plain user message"}`,
		`{"input":{}}`,
	}
	for _, body := range tests {
		got, changed := normalizeCodexPageContext([]byte(body))
		require.False(t, changed, body)
		require.Equal(t, []byte(body), got)
	}
}

func TestNormalizeCodexPageContextPreservesContinuationField(t *testing.T) {
	for _, value := range []string{`null`, `"resp_existing"`, `"  "`, `0`, `false`, `{}`, `[]`} {
		t.Run(value, func(t *testing.T) {
			body := []byte(`{"previous_response_id":` + value + `,"input":[{"type":"function_call_output","name":"codex_apps_open_page","output":"context"}]}`)
			got, changed := normalizeCodexPageContext(body)
			require.True(t, changed)
			require.Equal(t, value, gjson.GetBytes(got, "previous_response_id").Raw)
		})
	}
}
