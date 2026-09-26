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

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const excelBPSToolPolicyResponse = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_policy\",\"status\":\"completed\",\"model\":\"gpt-6-astra\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"

func TestExcelBPSToolFallbackPolicy(t *testing.T) {
	cases := []struct {
		name         string
		tool         map[string]any
		choice       any
		nativeReason string
	}{
		{"desktop search", map[string]any{"type": "web_search", "external_web_access": true}, "auto", "web_search"},
		{"implicit auto", map[string]any{"type": "web_search", "external_web_access": true}, nil, "web_search"},
		{"default search", map[string]any{"type": "web_search"}, "auto", ""},
		{"offline search", map[string]any{"type": "web_search", "external_web_access": false}, "auto", ""},
		{"high context", map[string]any{"type": "web_search", "external_web_access": false, "search_context_size": "high"}, "auto", "web_search"},
		{"medium context", map[string]any{"type": "web_search", "search_context_size": "medium"}, "auto", ""},
		{"low context", map[string]any{"type": "web_search", "search_context_size": "low"}, "auto", ""},
		{"preview", map[string]any{"type": "web_search_preview", "external_web_access": true}, "auto", "web_search"},
		{"dated preview", map[string]any{"type": "web_search_preview_2025_03_11", "external_web_access": true}, "auto", "web_search"},
		{"dated search", map[string]any{"type": "web_search_2025_08_26", "external_web_access": true}, "auto", "web_search"},
		{"image", map[string]any{"type": "image_generation"}, "auto", "image_generation"},
		{"disabled search", map[string]any{"type": "web_search", "external_web_access": true}, "none", ""},
		{"disabled image", map[string]any{"type": "image_generation"}, "none", ""},
		{"forced search", map[string]any{"type": "web_search"}, map[string]any{"type": "web_search"}, "tool_choice"},
		{"forced image", map[string]any{"type": "image_generation"}, map[string]any{"type": "image_generation"}, "tool_choice"},
		{"required search", map[string]any{"type": "web_search", "external_web_access": true}, "required", "web_search"},
	}
	for _, tc := range cases {
		for _, omit := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/omit=%t/stream=%t", tc.name, omit, stream), func(t *testing.T) {
					core, logs := observer.New(zap.InfoLevel)
					ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "req-policy")))
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(excelBPSToolPolicyResponse))}}
					svc := openAIClientToolsTestService(upstream)
					account := excelAccount()
					if omit {
						account.Extra[ExcelBPSOmitUnsupportedToolsKey] = true
					}
					source := map[string]any{"model": "gpt-6-astra", "reasoning": map[string]any{"effort": "high"}, "input": "private-user-prompt", "stream": stream, "tools": []any{tc.tool, map[string]any{"type": "function", "name": "lookup_client", "parameters": map[string]any{"type": "object"}}}}
					if tc.choice != nil {
						source["tool_choice"] = tc.choice
					}
					body, err := json.Marshal(source)
					require.NoError(t, err)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
					c.Header("X-Codex2API-Basispoints-Bypass", "stale-attempt")
					c.Header("X-Codex2API-Upstream", "codex")
					_, err = svc.Forward(ctx, c, account, body)
					native := !omit && tc.nativeReason != ""
					choice, _ := tc.choice.(string)
					forced := tc.choice != nil && choice != "auto" && choice != "none"
					if !native && forced {
						require.Error(t, err)
						require.Equal(t, http.StatusBadRequest, rec.Code)
						require.Contains(t, rec.Body.String(), "basispoints supports tool_choice auto or none only")
						require.True(t, IsResponseCommitted(c))
						require.Empty(t, upstream.requests)
					} else {
						require.NoError(t, err)
						require.Equal(t, http.StatusOK, rec.Code)
						require.Len(t, upstream.requests, 1)
						if native {
							require.Equal(t, "/backend-api/codex/responses", upstream.lastReq.URL.Path)
							require.Equal(t, "codex", rec.Header().Get("X-Codex2API-Upstream"))
							require.Equal(t, tc.nativeReason, rec.Header().Get("X-Codex2API-Basispoints-Bypass"))
						} else {
							require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
							require.Equal(t, "/basispoints/api/responses", GetActualOpenAIUpstreamEndpoint(c))
							if choice != "none" {
								require.Contains(t, string(upstream.lastBody), "Hosted tools unavailable through Basispoints: "+fmt.Sprint(tc.tool["type"]))
								require.Contains(t, string(upstream.lastBody), "Do not claim to have used them")
								require.Contains(t, string(upstream.lastBody), "lookup_client")
							} else {
								require.NotContains(t, string(upstream.lastBody), "Hosted tools unavailable through Basispoints")
								require.NotContains(t, string(upstream.lastBody), "lookup_client")
							}
						}
					}
					entries := logs.FilterMessage("excel_bps.native_fallback").All()
					if native {
						require.Len(t, entries, 1)
						require.Equal(t, tc.nativeReason, entries[0].ContextMap()["reason"])
						require.Equal(t, "req-policy", entries[0].ContextMap()["request_id"])
						require.Equal(t, account.ID, entries[0].ContextMap()["account_id"])
						fields := fmt.Sprint(entries[0].ContextMap())
						require.NotContains(t, fields, "private-user-prompt")
						require.NotContains(t, fields, "test-token")
					} else {
						require.Empty(t, entries)
						require.Empty(t, rec.Header().Get("X-Codex2API-Basispoints-Bypass"))
						require.Empty(t, rec.Header().Get("X-Codex2API-Upstream"))
					}
				})
			}
		}
	}
}

func TestExcelBPSOmitUnsupportedToolsRequiresExplicitOptIn(t *testing.T) {
	var missing *Account
	require.False(t, missing.IsExcelBPSOmitUnsupportedToolsEnabled())
	for _, value := range []any{nil, false, "true", 1, true} {
		account := excelAccount()
		account.Extra[ExcelBPSOmitUnsupportedToolsKey] = value
		require.Equal(t, value == true, account.IsExcelBPSOmitUnsupportedToolsEnabled())
		account.Extra["openai_excel_bps"] = false
		require.False(t, account.IsExcelBPSOmitUnsupportedToolsEnabled())
		account.Extra["openai_excel_bps"] = true
		account.Type = AccountTypeAPIKey
		require.False(t, account.IsExcelBPSOmitUnsupportedToolsEnabled())
	}
}

func TestExcelBPSToolPolicyRespectsModelScope(t *testing.T) {
	for _, models := range [][]string{{}, {"gpt-5.6-sol"}} {
		t.Run(fmt.Sprint(models), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(excelBPSToolPolicyResponse))}}
			account := excelAccount()
			account.Extra[ExcelBPSOmitUnsupportedToolsKey] = true
			account.Extra["openai_excel_bps_models"] = models
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			_, err := openAIClientToolsTestService(upstream).Forward(context.Background(), c, account, []byte("{\"model\":\"gpt-6-astra\",\"input\":\"hello\",\"tools\":[{\"type\":\"web_search\",\"external_web_access\":true}]}"))
			require.NoError(t, err)
			require.Equal(t, "/backend-api/codex/responses", upstream.lastReq.URL.Path)
			require.Empty(t, rec.Header().Get("X-Codex2API-Basispoints-Bypass"))
			require.NotContains(t, string(upstream.lastBody), "Hosted tools unavailable through Basispoints")
		})
	}
}
