//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

const codexPageContextInput = `{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","id":"context-open","output":"{\"page_id\":null}","internal_chat_message_metadata_passthrough":{"create_time":1,"turn_id":"test-turn"}},` +
	`{"type":"function_call_output","namespace":"agent","name":"codex_apps_selected_page_part_1_of_1","id":"context-snapshot","output":"Companion Page snapshot part 1 of 1:\n{\"page_id\":null,\"document_view\":null}"}`

type codexPageContextUpstream struct {
	service.HTTPUpstream
	body []byte
}

func (u *codexPageContextUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	var err error
	u.body, err = io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	response := `{"id":"resp_page_context","model":"` + gjson.GetBytes(u.body, "model").String() + `","status":"completed",` +
		`"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`
	contentType := "application/json"
	if gjson.GetBytes(u.body, "stream").Bool() {
		contentType = "text/event-stream"
		response = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(response)),
	}, nil
}

func TestOpenAIResponsesCodexPageContextReachesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_stream_%t", accountType, stream), func(t *testing.T) {
				model := "gpt-5.5"
				if accountType == service.AccountTypeOAuth {
					model = "gpt-5.6-luna"
				}
				upstream := &codexPageContextUpstream{}
				h := newOpenAIResponsesFailoverTestHandler(t, upstream, service.Account{
					ID: 1, Platform: service.PlatformOpenAI, Type: accountType, Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"api_key": "synthetic", "access_token": "synthetic"},
				})
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
					fmt.Sprintf(`{"model":%q,"stream":%t,"input":[%s,{"role":"user","content":"hello"}]}`, model, stream, codexPageContextInput)))
				c.Request.Header.Set("Content-Type", "application/json")
				groupID := int64(3132)
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
					ID: 99, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
					User: &service.User{ID: 100},
				})
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 100})

				h.Responses(c)

				require.Equal(t, http.StatusOK, w.Code, "%s; upstream error: %s", w.Body.String(), c.GetString(service.OpsUpstreamErrorMessageKey))
				require.NotEmpty(t, upstream.body)
				require.Equal(t, "user", gjson.GetBytes(upstream.body, "input.0.role").String())
				require.Equal(t, "<external_codex_apps_open_page>\n"+`{"page_id":null}`+"\n</external_codex_apps_open_page>", gjson.GetBytes(upstream.body, "input.0.content.0.text").String())
				require.Equal(t, "<external_codex_apps_selected_page_part_1_of_1>\nCompanion Page snapshot part 1 of 1:\n{\"page_id\":null,\"document_view\":null}\n</external_codex_apps_selected_page_part_1_of_1>", gjson.GetBytes(upstream.body, "input.1.content.0.text").String())
				require.False(t, gjson.GetBytes(upstream.body, `input.#(type=="function_call_output")`).Exists())
				require.Contains(t, w.Body.String(), "resp_page_context")
			})
		}
	}
}

func TestNormalizeCodexPageContextPreservesHistoryAndOrder(t *testing.T) {
	const history = `{"type":"function_call","name":"lookup","call_id":"call-1","arguments":"{}"},{"type":"function_call_output","call_id":"call-1","output":"done"},{"type":"item_reference","id":"item-1"}`
	body := []byte(`{"model":"gpt-5.5","previous_response_id":"resp-1","metadata":{"integer":9007199254740993},"input":[{"role":"developer","content":"before"},` + codexPageContextInput + `,` + history + `,{"role":"user","content":"after"}]}`)
	got, changed := normalizeCodexPageContext(body)
	require.True(t, changed)
	require.Equal(t, "resp-1", gjson.GetBytes(got, "previous_response_id").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(got, "metadata.integer").Raw)
	require.Equal(t, 7, len(gjson.GetBytes(got, "input").Array()))
	for _, index := range []string{"0", "3", "4", "5", "6"} {
		require.JSONEq(t, gjson.GetBytes(body, "input."+index).Raw, gjson.GetBytes(got, "input."+index).Raw)
	}
	for _, index := range []string{"1", "2"} {
		item := gjson.GetBytes(got, "input."+index)
		require.Equal(t, "message", item.Get("type").String())
		require.Equal(t, "user", item.Get("role").String())
		require.Equal(t, "input_text", item.Get("content.0.type").String())
		require.Equal(t, "<external_"+gjson.GetBytes(body, "input."+index+".name").String()+">\n"+gjson.GetBytes(body, "input."+index+".output").String()+"\n</external_"+gjson.GetBytes(body, "input."+index+".name").String()+">", item.Get("content.0.text").String())
		require.False(t, item.Get("id").Exists())
		require.False(t, item.Get("internal_chat_message_metadata_passthrough").Exists())
	}
	again, changedAgain := normalizeCodexPageContext(got)
	require.False(t, changedAgain)
	require.Equal(t, got, again)
}

func TestNormalizeCodexPageContextRecognizesOnlyKnownEnvelopes(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		output any
		change bool
	}{
		{name: "no page", tool: "codex_apps_open_page", output: `{"page_id":null}`, change: true},
		{name: "open page", tool: "codex_apps_open_page", output: `{"page_id":"page-test","title":"Example"}`, change: true},
		{name: "snapshot", tool: "codex_apps_selected_page_part_1_of_1", output: "Companion Page snapshot part 1 of 1:\n{\"page_id\":null,\"document_view\":null}", change: true},
		{name: "first chunk", tool: "codex_apps_selected_page_part_1_of_2", output: "Companion Page snapshot part 1 of 2:\n{\"document_view\":\"Example", change: true},
		{name: "second chunk", tool: "codex_apps_selected_page_part_2_of_2", output: "Companion Page snapshot part 2 of 2:\n content\"}", change: true},
		{name: "other tool", tool: "codex_apps_other", output: `{"page_id":null}`},
		{name: "invalid open JSON", tool: "codex_apps_open_page", output: `{"page_id":null`},
		{name: "missing page id", tool: "codex_apps_open_page", output: `{}`},
		{name: "empty page id", tool: "codex_apps_open_page", output: `{"page_id":" "}`},
		{name: "invalid page id", tool: "codex_apps_open_page", output: `{"page_id":42}`},
		{name: "duplicate page id", tool: "codex_apps_open_page", output: `{"page_id":"page-test","page_id":null}`},
		{name: "non string output", tool: "codex_apps_open_page", output: map[string]any{"page_id": nil}},
		{name: "null output", tool: "codex_apps_open_page"},
		{name: "mismatched snapshot header", tool: "codex_apps_selected_page_part_1_of_2", output: "Companion Page snapshot part 2 of 2:\n{}"},
		{name: "empty snapshot", tool: "codex_apps_selected_page_part_1_of_1", output: "Companion Page snapshot part 1 of 1:\n "},
		{name: "ordinary snapshot tool result", tool: "codex_apps_selected_page_part_1_of_1", output: "done"},
		{name: "zero part", tool: "codex_apps_selected_page_part_0_of_1", output: "Companion Page snapshot part 0 of 1:\n{}"},
		{name: "part out of range", tool: "codex_apps_selected_page_part_2_of_1", output: "Companion Page snapshot part 2 of 1:\n{}"},
		{name: "noncanonical part", tool: "codex_apps_selected_page_part_01_of_1", output: "Companion Page snapshot part 01 of 1:\n{}"},
		{name: "invalid total", tool: "codex_apps_selected_page_part_1_of_x", output: "Companion Page snapshot part 1 of x:\n{}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"input": []any{map[string]any{
				"type": "function_call_output", "namespace": "agent", "name": tt.tool, "output": tt.output,
			}}})
			require.NoError(t, err)
			got, changed := normalizeCodexPageContext(body)
			require.Equal(t, tt.change, changed)
			if changed {
				require.Equal(t, "<external_"+tt.tool+">\n"+tt.output.(string)+"\n</external_"+tt.tool+">", gjson.GetBytes(got, "input.0.content.0.text").String())
			} else {
				require.Equal(t, body, got)
			}
		})
	}
}

func TestNormalizeCodexPageContextPreservesCallIdentities(t *testing.T) {
	for _, field := range []string{"call_id", "tool_call_id"} {
		for _, id := range []any{"call-real", 42, nil, false} {
			body, err := sjson.SetBytes([]byte(`{"input":[`+codexPageContextInput+`]}`), "input.0."+field, id)
			require.NoError(t, err)
			got, changed := normalizeCodexPageContext(body)
			require.True(t, changed, "the independent snapshot still normalizes")
			require.JSONEq(t, gjson.GetBytes(body, "input.0").Raw, gjson.GetBytes(got, "input.0").Raw)
		}
		for _, id := range []string{"", "  "} {
			body, err := sjson.SetBytes([]byte(`{"input":[`+codexPageContextInput+`]}`), "input.0."+field, id)
			require.NoError(t, err)
			got, changed := normalizeCodexPageContext(body)
			require.True(t, changed)
			require.Equal(t, "message", gjson.GetBytes(got, "input.0.type").String())
		}
	}
}

func TestNormalizeCodexPageContextLeavesUnrecognizedRequestsUnchanged(t *testing.T) {
	body := `{"input":[` + codexPageContextInput + `]}`
	for _, invalid := range []string{
		`{"input":"hello"}`,
		strings.ReplaceAll(body, `"namespace":"agent"`, `"namespace":"other"`),
		strings.ReplaceAll(body, `"type":"function_call_output"`, `"type":"custom_tool_call_output"`),
		strings.Replace(body, `"input":`, `"input":[],"input":`, 1),
		strings.Replace(body, `"name":"codex_apps_open_page"`, `"name":"other","name":"codex_apps_open_page"`, 1),
		strings.Replace(body, `"id":"context-open"`, `"call_id":"real","call_id":""`, 1),
		body + `trailing`,
	} {
		got, changed := normalizeCodexPageContext([]byte(invalid))
		require.False(t, changed, invalid)
		require.Equal(t, invalid, string(got))
	}
}

func TestNormalizeCodexPageContextDoesNotBypassToolOutputValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"input":[` + codexPageContextInput + `,{"type":"function_call_output","name":"ordinary_tool","output":"done"}]}`)
	got, changed := normalizeCodexPageContext(body)
	require.True(t, changed)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	require.False(t, (&OpenAIGatewayHandler{}).validateFunctionCallOutputRequest(c, got, zap.NewNop()))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "function_call_output requires call_id")
}

func TestNormalizeCodexPageContextCoexistsWithBootstrapMessages(t *testing.T) {
	for _, bootstrap := range []struct {
		item      string
		normalize func([]byte) ([]byte, bool)
	}{
		{`{"type":"function_call_output","namespace":"codex_app","name":"send_message_to_thread","output":` + mustJSON(t, delegationEnvelope) + `}`, normalizeCodexDelegationBootstrap},
		{`{"type":"function_call_output","namespace":"codex_app","name":"automation_update","output":"<heartbeat><automation_id>test</automation_id></heartbeat>"}`, normalizeCodexAutomationBootstrap},
	} {
		got, changed := normalizeCodexPageContext([]byte(`{"input":[` + codexPageContextInput + `,` + bootstrap.item + `]}`))
		require.True(t, changed)
		got, changed = bootstrap.normalize(got)
		require.True(t, changed)
		require.False(t, gjson.GetBytes(got, `input.#(type=="function_call_output")`).Exists())
	}
}
