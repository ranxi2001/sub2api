package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeAPIKeySignatureRetryPreservesNumbers(t *testing.T) {
	const numbers = `{"record_id":9007199254740993,"negative":-9007199254740993,"decimal":0.12345678901234567890123456789,"exponent":1.234567890123456789e+40,"nested":[9007199254740995,{"value":-0}]}`
	const history = `{"model":"claude-opus-5-5","max_tokens":100,"fixture_extension":` + numbers + `,"tools":[{"name":"lookup","description":"fixture","input_schema":{"type":"object","properties":{"record_id":{"type":"integer"}},"required":["record_id"]}}],"messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"thinking","thinking":"fixture thought","signature":"fixture-signature"},{"type":"text","text":"first answer"},{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":` + numbers + `}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_fixture","content":"found"}]},{"role":"assistant","content":"done"},{"role":"user","content":"continue"}]}`
	const rejection = `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`
	const toolRejection = `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in tool_use block"}}`
	const success = `{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`

	assertNumbers := func(t *testing.T, value gjson.Result, label string) {
		t.Helper()
		for _, field := range []struct {
			path string
			raw  string
		}{
			{"record_id", "9007199254740993"},
			{"negative", "-9007199254740993"},
			{"decimal", "0.12345678901234567890123456789"},
			{"exponent", "1.234567890123456789e+40"},
			{"nested.0", "9007199254740995"},
			{"nested.1.value", "-0"},
		} {
			require.Equal(t, field.raw, value.Get(field.path).Raw, "%s_%s_exact", label, field.path)
		}
	}
	assertToolMapping := func(t *testing.T, body []byte) {
		t.Helper()
		require.Equal(t, "toolu_fixture", gjson.GetBytes(body, "messages.1.content.2.id").String())
		require.Equal(t, "lookup", gjson.GetBytes(body, "messages.1.content.2.name").String())
		require.Equal(t, "tool_use", gjson.GetBytes(body, "messages.1.content.2.type").String())
		require.Equal(t, "toolu_fixture", gjson.GetBytes(body, "messages.2.content.0.tool_use_id").String())
		require.Equal(t, "tool_result", gjson.GetBytes(body, "messages.2.content.0.type").String())
		require.Equal(t, "found", gjson.GetBytes(body, "messages.2.content.0.content").String())
	}

	for _, stage := range []string{"stage_one", "stage_two"} {
		t.Run(stage, func(t *testing.T) {
			responses := []*http.Response{{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(rejection))}}
			if stage == "stage_two" {
				responses = append(responses, &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(toolRejection))})
			}
			responses = append(responses, &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"req_fixture"}}, Body: io.NopCloser(strings.NewReader(success))})
			upstream := &httpUpstreamSequenceRecorder{responses: responses}
			cfg := &config.Config{}
			svc := &GatewayService{
				cfg:                  cfg,
				httpUpstream:         upstream,
				settingService:       NewSettingService(&panelRateLimitSettingRepo{values: map[string]string{}}, cfg),
				rateLimitService:     &RateLimitService{},
				responseHeaderFilter: compileResponseHeaderFilter(cfg),
				deferredService:      &DeferredService{},
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			account := newAnthropicAPIKeyAccountForTest()
			delete(account.Extra, "anthropic_passthrough")
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(history)), PlatformAnthropic)
			require.NoError(t, err)
			result, err := svc.Forward(c.Request.Context(), c, account, parsed)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.bodies, len(responses))
			require.Equal(t, "fixture-signature", gjson.GetBytes(upstream.bodies[0], "messages.1.content.0.signature").String())
			assertNumbers(t, gjson.GetBytes(upstream.bodies[0], "messages.1.content.2.input"), "first_tool_input")
			assertToolMapping(t, upstream.bodies[0])

			last := upstream.bodies[len(upstream.bodies)-1]
			if stage == "stage_two" {
				// Check this filter's own tool text before inspecting the first retry.
				// Both parse sites must independently demonstrate numeric corruption on RED.
				for _, body := range [][]byte{last, parsed.Body.Bytes()} {
					toolText := gjson.GetBytes(body, "messages.1.content.2.text").String()
					prefix, input, ok := strings.Cut(toolText, " input=")
					require.True(t, ok)
					require.Equal(t, "(tool_use) name=lookup id=toolu_fixture", prefix)
					require.True(t, gjson.Valid(input))
					assertNumbers(t, gjson.Parse(input), "stage_two_tool_input")
					require.Equal(t, "text", gjson.GetBytes(body, "messages.1.content.2.type").String())
					require.Equal(t, "(tool_result) tool_use_id=toolu_fixture\n\"found\"", gjson.GetBytes(body, "messages.2.content.0.text").String())
					assertNumbers(t, gjson.GetBytes(body, "fixture_extension"), "stage_two_extension")
				}
			}
			assertNumbers(t, gjson.GetBytes(upstream.bodies[1], "messages.1.content.2.input"), "stage_one_tool_input")
			assertToolMapping(t, upstream.bodies[1])
			if stage == "stage_one" {
				assertNumbers(t, gjson.GetBytes(parsed.Body.Bytes(), "messages.1.content.2.input"), "parsed_tool_input")
				assertToolMapping(t, parsed.Body.Bytes())
			}
			for _, body := range [][]byte{last, parsed.Body.Bytes()} {
				require.NotContains(t, string(body), "fixture-signature")
				require.Equal(t, "fixture thought", gjson.GetBytes(body, "messages.1.content.0.text").String())
				require.Equal(t, "first answer", gjson.GetBytes(body, "messages.1.content.1.text").String())
				require.Equal(t, "done", gjson.GetBytes(body, "messages.3.content").String())
				require.Equal(t, "continue", gjson.GetBytes(body, "messages.4.content").String())
				assertNumbers(t, gjson.GetBytes(body, "fixture_extension"), "final_extension")
			}
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "ok", gjson.Get(rec.Body.String(), "content.0.text").String())
			require.Equal(t, "req_fixture", result.RequestID)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
		})
	}
}
