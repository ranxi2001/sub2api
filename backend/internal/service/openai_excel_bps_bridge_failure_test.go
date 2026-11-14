package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSContinuationUnknownTargetKeepsNativeHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, visible := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/visible=%t", stream, visible), func(t *testing.T) {
				wire := excelBPSRepairWire(t, "original", "Run", `{"name":"collaboration.send_message","arguments":{"message":"PRIVATE_ARGUMENT"}}`)
				if visible {
					wire = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"already delivered\"}\n\n" + wire
				}
				first := &excelBPSRepairBody{Reader: strings.NewReader(wire)}
				fixed := &excelBPSRepairBody{Reader: strings.NewReader(excelBPSRepairWire(t, "fixed", "Run", `{"name":"shell","arguments":{"cmd":"next-operation"}}`))}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					{StatusCode: 200, Header: http.Header{}, Body: first},
					{StatusCode: 200, Header: http.Header{}, Body: fixed},
				}}
				checked := &excelBPSRepairUpstream{httpUpstreamRecorder: upstream, bodies: []*excelBPSRepairBody{first, fixed}}
				svc := openAIClientToolsTestService(upstream)
				svc.httpUpstream = checked
				body := []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","stream":%t,"input":[{"role":"user","content":"continue"},{"type":"function_call","call_id":"old","name":"shell","arguments":"{\"cmd\":\"previous-operation\"}"},{"type":"function_call_output","call_id":"old","output":"already executed"}],"tools":[{"type":"function","name":"shell"}]}`, stream))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				account := excelAccount()
				account.Concurrency = 1
				result, err := svc.Forward(context.Background(), c, account, body)
				require.NotNil(t, result)
				require.True(t, first.closed.Load())
				require.NotContains(t, rec.Body.String(), "PRIVATE_ARGUMENT")
				if stream && visible {
					require.Error(t, err)
					require.Len(t, upstream.requests, 1)
					require.Contains(t, rec.Body.String(), `"repair_skipped_reason":"visible_content"`)
					require.Equal(t, http.StatusOK, rec.Code)
					observed, ok := GetOpsStreamError(c)
					require.True(t, ok)
					require.NotNil(t, observed.BridgeFailure)
					require.Equal(t, http.StatusOK, observed.UpstreamStatus)
					require.Equal(t, http.StatusBadGateway, observed.IntendedStatus)
					require.Equal(t, "collaboration.send_message", observed.BridgeFailure.RejectedTool)
				} else {
					require.NoError(t, err)
					require.Len(t, upstream.requests, 2)
					require.Equal(t, "resp_original", result.ResponseID)
					require.EqualValues(t, 20, result.Usage.InputTokens)
					require.EqualValues(t, 4, result.Usage.OutputTokens)
					require.Contains(t, rec.Body.String(), `"name":"shell"`)
					require.NotContains(t, rec.Body.String(), "basispoints_protocol_error")
					require.True(t, fixed.closed.Load())
					var calls, results int
					for _, item := range gjson.GetBytes(upstream.bodies[1], "input").Array() {
						switch item.Get("type").String() {
						case "function_call":
							calls++
						case "function_call_output":
							results++
							if item.Get("call_id").String() == "call_original" {
								require.Equal(t, false, gjson.Get(item.Get("output").String(), "executed").Bool())
								require.Equal(t, "unknown_client_tool", gjson.Get(item.Get("output").String(), "error.code").String())
							}
						}
					}
					require.Equal(t, 2, calls)
					require.Equal(t, 2, results)
				}
			})
		}
	}
}

func TestExcelBPSLocalProtocolFailureKeepsHTTPAndDiagnostic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			wire := excelBPSRepairWire(t, "original", "Run", `{"name":"missing","arguments":{}}`)
			// A second unknown target exhausts first-turn correction.
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				bpsCompletionResponse(http.StatusOK, wire), bpsCompletionResponse(http.StatusOK, wire),
			}}
			svc := openAIClientToolsTestService(upstream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","stream":%t,"input":"test","tools":[{"type":"function","name":"shell"}]}`, stream)))
			require.Error(t, err)
			observed, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.NotNil(t, observed.BridgeFailure)
			require.Equal(t, http.StatusOK, observed.UpstreamStatus)
			require.Equal(t, "response.completed", observed.BridgeFailure.UpstreamTerminalEvent)
			require.Equal(t, "repair_exhausted", observed.BridgeFailure.RepairSkippedReason)
			require.Equal(t, !stream, observed.NonStream)
			require.Contains(t, rec.Body.String(), `"failure_stage":"bridge_tool_validation"`)
			require.Contains(t, rec.Body.String(), `"rejected_tool":"missing"`)
			if !stream {
				require.Equal(t, http.StatusBadGateway, rec.Code)
			}
		})
	}
}

func TestExcelBPSToolArgumentsDiagnosticJSONAndSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			wire := excelBPSRepairWire(t, "fixture", "Run", `{"name":"fixture_tool","arguments":{"count":"PRIVATE_ARGUMENT"}}`)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{bpsCompletionResponse(http.StatusOK, wire)}}
			svc := openAIClientToolsTestService(upstream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","stream":%t,"input":"test","tools":[{"type":"function","name":"fixture_tool","parameters":{"type":"object","properties":{"count":{"type":"integer"}}}}]}`, stream)))
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
			observed, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.NotNil(t, observed.BridgeFailure)
			require.NotNil(t, observed.BridgeFailure.ToolArguments)
			require.Equal(t, "type", observed.BridgeFailure.ToolArguments.Rule)
			require.Equal(t, "/count", observed.BridgeFailure.ToolArguments.Path)
			require.Contains(t, rec.Body.String(), `"tool_arguments":`)
			require.Contains(t, rec.Body.String(), `"rule":"type"`)
			require.NotContains(t, rec.Body.String(), "PRIVATE_ARGUMENT")
			if !stream {
				require.Equal(t, http.StatusBadGateway, rec.Code)
			}
		})
	}
}
