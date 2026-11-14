package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func excelSearchResponse(value map[string]any) *http.Response {
	raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": value})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: " + string(raw) + "\n\n"))}
}
func excelSearchCallResponse() *http.Response {
	args, _ := json.Marshal(map[string]any{"code": `{"name":"__sub2api_web_search","arguments":{"query":"synthetic query"}}`, "summary": "Search", "extended_summary": "{}", "destructive": false, "references": []any{}})
	return excelSearchResponse(map[string]any{"id": "resp_initial", "status": "completed", "model": "gpt-6-astra", "output": []any{map[string]any{"type": "function_call", "id": "fc_search", "call_id": "call_search", "name": "run_officejs", "arguments": string(args)}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}})
}

func TestExcelBPSAccountSearchUsesPinnedGrantAndAccumulatesUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{excelSearchCallResponse(), {StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"output":"Found result","results":[{"url":"https://example.com","title":"Example"}]}`))}, excelSearchResponse(map[string]any{"id": "resp_next", "status": "completed", "model": "gpt-6-astra", "output": []any{map[string]any{"type": "message", "id": "msg_answer", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "See https://example.com"}}}}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 3}})}}
			svc := openAIClientToolsTestService(upstream)
			account := excelAccount()
			account.Extra[ExcelBPSWebSearchKey] = true
			account.Extra["openai_excel_bps_mihomo"] = false
			account.Proxy = &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 7890}
			body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":"search","tools":[{"type":"web_search","external_web_access":true}],"tool_choice":{"type":"web_search"}}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
			c.Request.Header.Set("Cookie", "private-cookie")
			c.Request.Header.Set("x-codex-turn-state", "private-ticket")
			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 3)
			require.Equal(t, 30, result.Usage.InputTokens)
			require.Equal(t, 5, result.Usage.OutputTokens)
			require.Equal(t, "chatgpt.com", upstream.requests[1].URL.Host)
			require.Equal(t, "/backend-api/codex/alpha/search", upstream.requests[1].URL.Path)
			for _, request := range upstream.requests {
				require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
				require.Empty(t, request.Header.Get("Cookie"))
				require.Empty(t, request.Header.Get("x-codex-turn-state"))
				require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
			}
			for _, header := range []string{"X-Openai-Account-Id", "X-Openai-Account-User-Id", "X-Basispoints-Auth-Mode", "Origin"} {
				require.Empty(t, upstream.requests[1].Header.Get(header))
			}
			require.Equal(t, upstream.requests[0].Header.Get("Chatgpt-Account-Id"), upstream.requests[1].Header.Get("Chatgpt-Account-Id"))
			require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
			require.Equal(t, "synthetic query", gjson.GetBytes(upstream.bodies[1], "commands.search_query.0.q").String())
			require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.bodies[1], "model").String())
			require.Contains(t, string(upstream.bodies[2]), "untrusted_search_data")
			require.Contains(t, rec.Body.String(), "web_search_call")
			require.Contains(t, rec.Body.String(), "url_citation")
			require.NotContains(t, rec.Body.String(), "__sub2api_web_search")
		})
	}
}

func TestExcelBPSSearchFailureDoesNotReplayOrDisableAccount(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{excelSearchCallResponse(), {StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"PRIVATE_UPSTREAM_BODY"}}`))}}}
	svc := openAIClientToolsTestService(upstream)
	account := excelAccount()
	account.Extra[ExcelBPSWebSearchKey] = true
	account.Extra["openai_excel_bps_auto_disable_on_403"] = true
	body := []byte(`{"model":"gpt-6-astra","stream":true,"input":"search","tools":[{"type":"web_search"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	_, _ = svc.Forward(context.Background(), c, account, body)
	require.Len(t, upstream.requests, 2)
	require.True(t, account.IsExcelBPSEnabled())
	require.Contains(t, rec.Body.String(), "basispoints_search_failed")
	require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM_BODY")
	require.NotContains(t, rec.Body.String(), "response.completed")
}

func TestExcelBPSSearchOptionsRequireOptInAndPreserveImageFallback(t *testing.T) {
	require.False(t, (*Account)(nil).IsExcelBPSSearchEnabled())
	account := excelAccount()
	require.False(t, account.IsExcelBPSSearchEnabled())
	for _, value := range []any{false, "true", true} {
		account.Extra[ExcelBPSWebSearchKey] = value
		require.Equal(t, value == true, account.IsExcelBPSSearchEnabled())
	}
	account.Extra[ExcelBPSWebSearchAutoKey] = true
	require.True(t, account.IsExcelBPSSearchAutoEnabled())
	require.Empty(t, account.excelBPSNativeFallbackReason([]byte(`{"tools":[{"type":"web_search","external_web_access":true}]}`)))
	require.Equal(t, "image_generation", account.excelBPSNativeFallbackReason([]byte(`{"tools":[{"type":"web_search","external_web_access":true},{"type":"image_generation"}]}`)))
	account.Extra["openai_excel_bps"] = false
	require.False(t, account.IsExcelBPSSearchAutoEnabled())
}
