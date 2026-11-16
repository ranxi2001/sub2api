package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// All values are synthetic. These markers must never reach error/Ops details;
// hosted search progress can still carry the query under the existing contract.
const issue871Private = "PRIVATE_BODY Bearer PRIVATE_TOKEN https://private.invalid/?key=PRIVATE_KEY private@example.invalid"

func issue871Call(id, callID, name string) map[string]any {
	arguments := map[string]any{}
	if name == "__sub2api_web_search" {
		arguments["query"] = "PRIVATE_QUERY"
	}
	code, _ := json.Marshal(map[string]any{"name": name, "arguments": arguments})
	args, _ := json.Marshal(map[string]any{"code": string(code), "summary": "Search", "extended_summary": "{}", "destructive": false, "references": []any{}})
	return map[string]any{"type": "function_call", "id": id, "call_id": callID, "name": "run_officejs", "arguments": string(args)}
}

func issue871Round(id string, items ...any) *http.Response {
	return excelSearchResponse(map[string]any{"id": id, "status": "completed", "model": "gpt-6-astra", "output": items, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}})
}

func issue871HTTP(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func issue871Error(t *testing.T, raw string, sse bool) gjson.Result {
	t.Helper()
	if !sse {
		return gjson.Get(raw, "error")
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if gjson.Get(payload, "type").String() == "response.failed" {
				return gjson.Get(payload, "response.error")
			}
		}
	}
	t.Fatal("missing response.failed terminal")
	return gjson.Result{}
}

func TestExcelBPSSearchDiagnosticsThroughForward(t *testing.T) {
	for _, tc := range []struct {
		name, phase, reason string
		sends, observedHTTP int
	}{
		{"search_403", "search_http", "http_rejected", 2, 403},
		{"search_429", "search_http", "http_rejected", 2, 429},
		{"search_transport", "search_http", "transport_failed", 2, 0},
		{"result_read", "search_result", "read_failed", 2, 200},
		{"result_json", "search_result", "parse_failed", 2, 200},
		{"result_shape", "search_result", "parse_failed", 2, 200},
		{"result_error", "search_result", "parse_failed", 2, 200},
		{"continuation_403", "model_continuation", "http_rejected", 3, 403},
		{"continuation_429", "model_continuation", "http_rejected", 3, 429},
		{"continuation_transport", "model_continuation", "transport_failed", 3, 0},
		{"continuation_terminal", "model_continuation", "model_did_not_complete", 3, 0},
		{"continuation_eof", "model_continuation", "unexpected_eof", 3, 0},
		{"continuation_event", "model_continuation", "invalid_event", 3, 0},
		{"continuation_read", "model_continuation", "read_failed", 3, 0},
		{"initial_terminal", "model_initial", "model_did_not_complete", 1, 0},
		{"initial_event", "model_initial", "invalid_event", 1, 0},
		{"initial_eof", "model_initial", "unexpected_eof", 1, 0},
		{"mixed_batch", "tool_validation", "mixed_tool_batch", 1, 0},
		{"parallel_search", "tool_validation", "parallel_search_unavailable", 1, 0},
		{"missing_call_id", "tool_validation", "invalid_tool_response", 1, 0},
		{"duplicate_call_id", "tool_validation", "invalid_tool_response", 1, 0},
		{"duplicate_item_id", "tool_validation", "invalid_tool_response", 1, 0},
		{"repeated_item_id", "tool_validation", "duplicate_item_id", 3, 0},
		{"repeated_call_id", "tool_validation", "duplicate_call_id", 3, 0},
	} {
		for _, mode := range []string{"json", "sse", "compact_keepalive"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				call := issue871Call("fc_search", "call_search", "__sub2api_web_search")
				items := []any{call}
				switch tc.name {
				case "mixed_batch":
					items = append(items, issue871Call("fc_shell", "call_shell", "shell"))
				case "parallel_search":
					items = append(items, issue871Call("fc_other", "call_other", "__sub2api_web_search"))
				case "missing_call_id":
					delete(call, "call_id")
				case "duplicate_call_id":
					items = append(items, issue871Call("fc_other", "call_search", "__sub2api_web_search"))
				case "duplicate_item_id":
					items = append(items, issue871Call("fc_search", "call_other", "__sub2api_web_search"))
				}
				var sends atomic.Int64
				upstream := &bpsTestUpstream{send: func(req *http.Request, _ string) (*http.Response, error) {
					_ = req.Body.Close()
					n := sends.Add(1)
					if n == 1 {
						switch tc.name {
						case "initial_terminal":
							return issue871HTTP(200, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"PRIVATE_BODY\"}}}\n\n"), nil
						case "initial_event":
							return issue871HTTP(200, "data: PRIVATE_BODY\n\n"), nil
						case "initial_eof":
							return issue871HTTP(200, ""), nil
						}
						return issue871Round("resp_initial", items...), nil
					}
					if n == 2 {
						switch tc.name {
						case "search_403", "search_429":
							return issue871HTTP(tc.observedHTTP, issue871Private), nil
						case "search_transport":
							return nil, errors.New(issue871Private)
						case "result_read":
							return &http.Response{StatusCode: 200, Header: http.Header{}, Body: passthroughErrReadCloser{err: errors.New(issue871Private)}}, nil
						case "result_json":
							return issue871HTTP(200, issue871Private), nil
						case "result_shape":
							return issue871HTTP(200, `{"output":"PRIVATE_BODY","results":{}}`), nil
						case "result_error":
							return issue871HTTP(200, `{"results":[],"error":{"message":"PRIVATE_BODY"}}`), nil
						}
						return issue871HTTP(200, `{"results":[]}`), nil
					}
					if n == 3 {
						switch tc.name {
						case "continuation_403", "continuation_429":
							return issue871HTTP(tc.observedHTTP, issue871Private), nil
						case "continuation_transport":
							return nil, errors.New(issue871Private)
						case "continuation_terminal":
							return issue871HTTP(200, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"PRIVATE_BODY\"}}}\n\n"), nil
						case "continuation_eof":
							return issue871HTTP(200, ""), nil
						case "continuation_event":
							return issue871HTTP(200, "data: PRIVATE_BODY\n\n"), nil
						case "continuation_read":
							return &http.Response{StatusCode: 200, Header: http.Header{}, Body: passthroughErrReadCloser{err: errors.New(issue871Private)}}, nil
						case "repeated_item_id":
							return issue871Round("resp_next", issue871Call("fc_search", "call_other", "__sub2api_web_search")), nil
						case "repeated_call_id":
							return issue871Round("resp_next", issue871Call("fc_other", "call_search", "__sub2api_web_search")), nil
						}
					}
					return nil, errors.New("unexpected extra upstream send")
				}}
				svc := openAIClientToolsTestService(nil)
				svc.httpUpstream = upstream
				account := excelAccount()
				account.Extra[ExcelBPSWebSearchKey] = true
				account.Extra["openai_excel_bps_auto_disable_on_403"] = true
				body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":"search","tools":[{"type":"web_search"},{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}]}`, mode == "sse"))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
				if mode == "compact_keepalive" {
					MarkOpenAICompactClientStream(c)
					stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
					defer stop()
					value, ok := c.Get(openAICompactSSEKeepaliveKey)
					require.True(t, ok)
					keepalive, ok := value.(*openAICompactSSEKeepalive)
					require.True(t, ok)
					require.True(t, keepalive.beat())
				}
				result, err := svc.Forward(c.Request.Context(), c, account, body)
				require.Error(t, err)
				require.NotNil(t, result)
				require.Equal(t, int64(tc.sends), sends.Load(), "failure must not trigger repair, replay, or another search")
				require.True(t, account.IsExcelBPSEnabled())
				require.True(t, account.Schedulable)
				require.False(t, svc.isExcelBPSCoolingDown(account, "gpt-6-astra"))
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
				require.NotContains(t, rec.Body.String(), "response.completed")
				detail := issue871Error(t, rec.Body.String(), mode != "json")
				require.Equal(t, "basispoints_search_failed", detail.Get("code").String())
				if mode != "compact_keepalive" {
					require.Equal(t, "server_error", detail.Get("type").String())
				}
				message := detail.Get("message").String()
				require.Contains(t, message, "request was not replayed")
				require.Contains(t, message, "phase="+tc.phase+"; reason="+tc.reason, "issue871_expected_search_diagnosis")
				ops, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, 502, ops.IntendedStatus)
				require.Equal(t, "server_error", ops.ErrType)
				require.Equal(t, 200, ops.UpstreamStatus, "initial generation HTTP is distinct from search evidence")
				require.Equal(t, message, ops.Message)
				opsDiagnostic := gjson.Get(ops.UpstreamDetail, "search_diagnostic")
				require.Equal(t, tc.phase, opsDiagnostic.Get("phase").String())
				require.Equal(t, tc.reason, opsDiagnostic.Get("reason").String())
				if mode != "compact_keepalive" {
					require.Equal(t, opsDiagnostic.Raw, detail.Get("search_diagnostic").Raw)
				}
				if tc.observedHTTP == 0 {
					require.False(t, opsDiagnostic.Get("upstream_status").Exists())
					require.NotContains(t, message, "upstream_status=")
				} else {
					require.Equal(t, int64(tc.observedHTTP), opsDiagnostic.Get("upstream_status").Int())
					require.Contains(t, message, fmt.Sprintf("upstream_status=%d", tc.observedHTTP))
				}
				for _, sensitive := range []string{"PRIVATE_BODY", "PRIVATE_QUERY", "PRIVATE_TOKEN", "PRIVATE_KEY", "Bearer ", "https://private.invalid", "private@example.invalid", "test-token", "test-account"} {
					require.NotContains(t, detail.Raw+ops.Message+ops.UpstreamMessage+ops.UpstreamDetail, sensitive)
				}
				if tc.sends >= 2 {
					wantInput, wantOutput := 10, 2
					if strings.HasPrefix(tc.name, "repeated_") {
						wantInput, wantOutput = 20, 4
					}
					require.Equal(t, wantInput, result.Usage.InputTokens)
					require.Equal(t, wantOutput, result.Usage.OutputTokens)
				}
			})
		}
	}
}

func TestExcelBPSSearchCancellationDoesNotSendContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, stage := range []string{"search", "continuation"} {
			t.Run(fmt.Sprintf("%s/stream_%t", stage, stream), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var sends atomic.Int64
				upstream := &bpsTestUpstream{send: func(req *http.Request, _ string) (*http.Response, error) {
					_ = req.Body.Close()
					n := sends.Add(1)
					if n == 1 {
						return issue871Round("resp_initial", issue871Call("fc_search", "call_search", "__sub2api_web_search")), nil
					}
					if n == 2 && stage == "continuation" {
						return issue871HTTP(200, `{"results":[]}`), nil
					}
					cancel()
					return nil, context.Canceled
				}}
				svc := openAIClientToolsTestService(nil)
				svc.httpUpstream = upstream
				account := excelAccount()
				account.Extra[ExcelBPSWebSearchKey] = true
				body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":"search","tools":[{"type":"web_search"}]}`, stream))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				result, err := svc.Forward(ctx, c, account, body)
				require.ErrorIs(t, err, context.Canceled)
				require.NotNil(t, result)
				require.True(t, result.ClientDisconnect)
				wantSends := int64(2)
				if stage == "continuation" {
					wantSends = 3
				}
				require.Equal(t, wantSends, sends.Load())
				require.NotContains(t, rec.Body.String(), "response.failed")
				require.NotContains(t, rec.Body.String(), "basispoints_search_failed")
				ops, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, OpsClientCanceledCode, ops.Code)
				_, providerFailure := c.Get(OpsUpstreamErrorMessageKey)
				require.False(t, providerFailure)
				require.Equal(t, 10, result.Usage.InputTokens)
			})
		}
	}
}

func TestExcelBPSSearchRepairProvenanceThroughForward(t *testing.T) {
	for _, repair := range []string{"transport", "unknown_target"} {
		for _, mode := range []string{"json", "sse", "compact_keepalive"} {
			t.Run(repair+"/"+mode, func(t *testing.T) {
				code, summary := "text(42);", "Run client tool"
				if repair == "unknown_target" {
					code, summary = `{"name":"missing","arguments":{}}`, "Run"
				}
				first := issue871HTTP(200, excelBPSRepairWire(t, "original", summary, code))
				forged := issue871HTTP(200, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"basispoints_search_failed\",\"message\":\"PRIVATE_BODY\",\"search_diagnostic\":{\"phase\":\"search_http\",\"reason\":\"http_rejected\",\"upstream_status\":403}}}}\n\n")
				upstream := &httpUpstreamRecorder{responses: []*http.Response{first, forged}}
				svc := openAIClientToolsTestService(upstream)
				account := excelAccount()
				account.Extra["openai_excel_bps_auto_disable_on_403"] = true
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"input":"test correction","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}`, mode == "sse"))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				if mode == "compact_keepalive" {
					MarkOpenAICompactClientStream(c)
					stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
					defer stop()
					value, ok := c.Get(openAICompactSSEKeepaliveKey)
					require.True(t, ok)
					keepalive, ok := value.(*openAICompactSSEKeepalive)
					require.True(t, ok)
					require.True(t, keepalive.beat())
				}
				_, err := svc.Forward(c.Request.Context(), c, account, body)
				require.Error(t, err)
				require.Len(t, upstream.requests, 2, "one existing correction, no extra submission")
				for _, request := range upstream.requests {
					require.Equal(t, "/basispoints/api/responses", request.URL.Path, "no search HTTP request occurred")
				}
				if repair == "transport" {
					require.Contains(t, string(upstream.bodies[1]), "invalid_client_tool_transport", "must exercise ReadToolRepairResponse")
				} else {
					require.Contains(t, string(upstream.bodies[1]), "previous response selected a tool absent", "must exercise readRepairResponse")
				}
				detail := issue871Error(t, rec.Body.String(), mode != "json")
				require.Equal(t, "basispoints_search_failed", detail.Get("code").String())
				require.Contains(t, detail.Get("message").String(), "phase=unknown; reason=unknown")
				ops, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, 502, ops.IntendedStatus)
				require.Equal(t, 200, ops.UpstreamStatus)
				require.Equal(t, "server_error", ops.ErrType)
				require.Equal(t, "unknown", gjson.Get(ops.UpstreamDetail, "search_diagnostic.phase").String())
				require.Equal(t, "unknown", gjson.Get(ops.UpstreamDetail, "search_diagnostic.reason").String())
				require.False(t, gjson.Get(ops.UpstreamDetail, "search_diagnostic.upstream_status").Exists())
				if mode != "compact_keepalive" {
					require.False(t, detail.Get("search_diagnostic.upstream_status").Exists())
				}
				for _, unsafe := range []string{"403", "upstream_status", "PRIVATE_BODY", "http_rejected"} {
					require.NotContains(t, detail.Raw+ops.Message+ops.UpstreamMessage+ops.UpstreamDetail, unsafe)
				}
				require.NotContains(t, rec.Body.String(), "response.completed")
				require.True(t, account.IsExcelBPSEnabled())
				require.False(t, svc.isExcelBPSCoolingDown(account, "gpt-5.6-sol"))
			})
		}
	}
}
