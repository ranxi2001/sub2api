package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type excelBPSRepairBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *excelBPSRepairBody) Close() error { b.closed.Store(true); return nil }

type excelBPSRepairUpstream struct {
	*httpUpstreamRecorder
	bodies []*excelBPSRepairBody
}

func (u *excelBPSRepairUpstream) Do(req *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	if accountID != 300 || concurrency != 1 {
		return nil, fmt.Errorf("correction changed account scheduling")
	}
	if n := len(u.requests); n > 0 && !u.bodies[n-1].closed.Load() {
		return nil, fmt.Errorf("previous response retained its concurrency lease")
	}
	return u.httpUpstreamRecorder.Do(req, proxy, accountID, concurrency)
}

func excelBPSRepairWire(t *testing.T, id, summary, code string) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{"summary": summary, "code": code, "extended_summary": "{}", "references": []any{}, "destructive": false})
	require.NoError(t, err)
	event, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
		"id": "resp_" + id, "status": "completed", "model": "gpt-5.6-sol", "usage": map[string]int{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12},
		"output": []any{map[string]any{"type": "function_call", "name": "run_officejs", "id": "fc_" + id, "call_id": "call_" + id, "arguments": string(args), "status": "completed"}},
	}})
	require.NoError(t, err)
	return "event: response.completed\ndata: " + string(event) + "\n\n"
}

func TestExcelBPSToolCorrectionPreservesRouteAndUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, corrected := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/corrected=%t", stream, corrected), func(t *testing.T) {
				upstream := &httpUpstreamRecorder{}
				checked := &excelBPSRepairUpstream{httpUpstreamRecorder: upstream}
				attempts := 3
				if corrected {
					attempts = 2
				}
				for i := 0; i < attempts; i++ {
					summary := "Run client tool"
					code := "text(42);"
					if corrected && i == attempts-1 {
						summary = "codex2api.custom/functions.exec"
						code = "text(24);"
					}
					body := &excelBPSRepairBody{Reader: strings.NewReader(excelBPSRepairWire(t, fmt.Sprint(i), summary, code))}
					checked.bodies = append(checked.bodies, body)
					upstream.responses = append(upstream.responses, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body})
				}
				svc := openAIClientToolsTestService(upstream)
				svc.httpUpstream = checked
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"reasoning":{"effort":"max"},"input":"test correction","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}`, stream))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				account := excelAccount()
				account.Concurrency = 1
				result, err := svc.Forward(context.Background(), c, account, body)
				if corrected {
					require.NoError(t, err)
					require.NotContains(t, rec.Body.String(), "response.failed")
					require.Contains(t, rec.Body.String(), `"input":"text(42);"`)
					require.NotContains(t, rec.Body.String(), "text(24);")
					require.Contains(t, rec.Body.String(), `"name":"exec"`)
					if stream {
						require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.output_item.added"))
						require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.completed"))
					}
				} else {
					require.Error(t, err)
					require.Contains(t, rec.Body.String(), "basispoints_protocol_error")
					require.NotContains(t, rec.Body.String(), "custom_tool_call")
				}
				require.Len(t, upstream.requests, attempts)
				require.NotNil(t, result)
				require.Equal(t, attempts*10, result.Usage.InputTokens)
				require.Equal(t, attempts*2, result.Usage.OutputTokens)
				require.Equal(t, "resp_0", result.ResponseID)
				for i, req := range upstream.requests {
					require.Equal(t, basispoints.ResponsesURL, req.URL.String())
					require.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
					require.Equal(t, "test-account", req.Header.Get("Chatgpt-Account-Id"))
					require.Equal(t, HTTPUpstreamProfileExcelBPS, HTTPUpstreamProfileFromContext(req.Context()))
					require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
					require.True(t, checked.bodies[i].closed.Load())
					require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.bodies[i], "model").String())
					require.Equal(t, "xhigh", gjson.GetBytes(upstream.bodies[i], "reasoning_effort").String())
					require.False(t, gjson.GetBytes(upstream.bodies[i], "tools").Exists())
					require.Equal(t, fmt.Sprint(i+1), gjson.GetBytes(upstream.bodies[i], "metadata.agent_iteration").String())
					require.Equal(t, gjson.GetBytes(upstream.bodies[0], "metadata.task_id").String(), gjson.GetBytes(upstream.bodies[i], "metadata.task_id").String())
					if i > 0 {
						require.Contains(t, string(upstream.bodies[i]), "invalid_client_tool_transport")
					}
				}
			})
		}
	}
}

func TestExcelBPSToolCorrectionStopsOnHTTPRejection(t *testing.T) {
	for _, status := range []int{403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			first := &excelBPSRepairBody{Reader: strings.NewReader(excelBPSRepairWire(t, "http_rejection", "Run", "text(42);"))}
			rejected := &excelBPSRepairBody{Reader: strings.NewReader(`{"error":{"message":"private echoed request text"}}`)}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: 200, Header: http.Header{}, Body: first},
				{StatusCode: status, Header: http.Header{}, Body: rejected},
			}}
			svc := openAIClientToolsTestService(upstream)
			body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":"test","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			result, err := svc.Forward(context.Background(), c, excelAccount(), body)
			require.Error(t, err)
			require.Len(t, upstream.requests, 2)
			require.True(t, first.closed.Load())
			require.True(t, rejected.closed.Load())
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Contains(t, rec.Body.String(), fmt.Sprintf("HTTP %d", status))
			require.NotContains(t, rec.Body.String(), "private echoed")
			require.NotContains(t, rec.Body.String(), "response.output_item.added")
		})
	}
}

func TestExcelBPS429CorrectionDoesNotChangeCodexState(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			first := &excelBPSRepairBody{Reader: strings.NewReader(excelBPSRepairWire(t, "rate_limit", "Run", "text(42);"))}
			rejected := &excelBPSRepairBody{Reader: strings.NewReader(`{"error":{"type":"usage_limit_reached","resets_in_seconds":7200,"message":"PRIVATE_UPSTREAM"}}`)}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: http.StatusOK, Header: http.Header{}, Body: first},
				{StatusCode: http.StatusTooManyRequests, Header: excelBPSQuotaHeaders("100", "100"), Body: rejected},
			}}
			svc := openAIClientToolsTestService(upstream)
			repo := &excelBPSQuotaRepo{writes: make(chan excelBPSQuotaWrite, 4)}
			svc.accountRepo = repo
			svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
			svc.rateLimitService.SetAccountRuntimeBlocker(svc)
			account := excelAccount()
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"input":"test","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

			result, err := svc.Forward(context.Background(), c, account, body)

			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Len(t, upstream.requests, 2, "stop after the throttled correction")
			require.True(t, first.closed.Load())
			require.True(t, rejected.closed.Load())
			require.Equal(t, 10, result.Usage.InputTokens)
			require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
			require.NotContains(t, rec.Body.String(), "response.output_item.added")
			requireNoExcelBPSQuotaWrite(t, repo)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
			require.True(t, account.IsSchedulable())
		})
	}
}

func TestExcelBPSMixedToolCorrectionKeepsOriginalOperation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			wire := func(corrected bool) string {
				summary, code, id := "Run client tool", "text(42);", "initial"
				command := "pwd"
				if corrected {
					summary, id, command = "codex2api.custom/functions.exec", "corrected", "changed command"
				}
				raw := excelBPSRepairWire(t, id, summary, code)
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.SplitN(raw, "data: ", 2)[1])), &event))
				response, ok := event["response"].(map[string]any)
				require.True(t, ok)
				output, ok := response["output"].([]any)
				require.True(t, ok)
				envelope, err := json.Marshal(map[string]any{"name": "shell", "arguments": map[string]any{"cmd": command}})
				require.NoError(t, err)
				args, err := json.Marshal(map[string]any{"code": string(envelope), "summary": "Inspect directory", "extended_summary": "Inspect current directory", "references": []any{}, "destructive": false})
				require.NoError(t, err)
				response["output"] = append(output, map[string]any{"type": "function_call", "name": "run_officejs", "id": "fc_shell_" + id, "call_id": "shell_" + id, "arguments": string(args)})
				encoded, err := json.Marshal(event)
				require.NoError(t, err)
				return "event: response.completed\ndata: " + string(encoded) + "\n\n"
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire(false)))},
				{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire(true)))},
			}}
			svc := openAIClientToolsTestService(upstream)
			body := []byte(fmt.Sprintf("{\"model\":\"gpt-5.5\",\"stream\":%t,\"input\":\"inspect\",\"tools\":[{\"type\":\"function\",\"name\":\"shell\",\"parameters\":{\"type\":\"object\",\"properties\":{\"cmd\":{\"type\":\"string\"}}}},{\"type\":\"namespace\",\"name\":\"functions\",\"tools\":[{\"type\":\"custom\",\"name\":\"exec\"}]}]}", stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			account := excelAccount()
			account.Credentials["model_mapping"] = map[string]any{"gpt-5.5": "gpt-5.6-sol"}
			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, 20, result.Usage.InputTokens)
			require.Contains(t, rec.Body.String(), "pwd")
			require.Contains(t, rec.Body.String(), "text(42);")
			require.NotContains(t, rec.Body.String(), "changed command")
			require.NotContains(t, rec.Body.String(), "response.failed")
			for _, sent := range upstream.bodies {
				require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(sent, "model").String())
			}
			if stream {
				require.Equal(t, 2, strings.Count(rec.Body.String(), "event: response.output_item.added"))
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.completed"))
			}
		})
	}
}
