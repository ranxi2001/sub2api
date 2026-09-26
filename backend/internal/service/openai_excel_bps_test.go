package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func testExcelBPSAccessToken(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID},
	})
	require.NoError(t, err)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestExcelBPSAccountID(t *testing.T) {
	account := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "stored-account"},
	}
	require.Equal(t, "stored-account", excelBPSAccountID(account, testExcelBPSAccessToken(t, "jwt-account")))

	delete(account.Credentials, "chatgpt_account_id")
	require.Equal(t, "jwt-account", excelBPSAccountID(account, testExcelBPSAccessToken(t, "jwt-account")))
	require.Empty(t, excelBPSAccountID(account, "not-a-jwt"))
	require.Empty(t, excelBPSAccountID(account, testExcelBPSAccessToken(t, "")))
}

func TestNewExcelBPSRequestHeaders(t *testing.T) {
	token := testExcelBPSAccessToken(t, "jwt-account")
	req, err := newExcelBPSRequest(context.Background(), []byte(`{"model":"gpt-5.6-sol"}`), token, "jwt-account")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, basispoints.ResponsesURL, req.URL.String())
	require.Equal(t, "Bearer "+token, req.Header.Get("authorization"))
	require.Equal(t, "jwt-account", req.Header.Get("chatgpt-account-id"))
	require.Equal(t, "jwt-account", req.Header.Get("x-openai-account-id"))
	require.Equal(t, "chatgpt", req.Header.Get("x-basispoints-auth-mode"))
}

func excelAccount() *Account {
	return &Account{ID: 300, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}, Extra: map[string]any{"openai_excel_bps": true, "openai_passthrough": true}}
}
func TestExcelBPSForwardContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_excel\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"21\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%v,"reasoning":{"effort":"max"},"input":"test","tools":[{"type":"custom","name":"apply_patch"}]}`, stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
			c.Request.Header.Set("x-codex-turn-state", "must-not-leak")
			account := excelAccount()
			account.Proxy = &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 7890}
			account.Extra["openai_excel_bps_mihomo"] = false
			result, err := svc.Forward(context.Background(), c, account, body)
			require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
			require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
			require.Equal(t, "Bearer test-token", upstream.lastReq.Header.Get("Authorization"))
			require.Empty(t, upstream.lastReq.Header.Get("x-codex-turn-state"))
			require.Equal(t, HTTPUpstreamProfileExcelBPS, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
			require.True(t, HTTPUpstreamRedirectsDisabled(upstream.lastReq.Context()))
			require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
			require.Equal(t, "xhigh", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
			require.Equal(t, "xhigh", *result.ReasoningEffort)
			require.NotNil(t, result.RequestedReasoningEffort)
			require.Equal(t, "max", *result.RequestedReasoningEffort)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Contains(t, rec.Body.String(), "21")
		})
	}
}
func TestExcelBPSUsagePreservesRequestedEffortBeforeGroupMapping(t *testing.T) {
	for _, tc := range []struct {
		name, requested, ceiling, forwarded string
	}{
		{"bps caps max", "max", "", "xhigh"},
		{"group maps max to xhigh", "max", "xhigh", "xhigh"},
		{"group maps max to high", "max", "high", "high"},
		{"unchanged high", "high", "", "high"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bps_effort\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
				svc := openAIClientToolsTestService(upstream)
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.6-sol","stream":%t,"reasoning":{"effort":%q},"input":"test"}`, stream, tc.requested))
				ctx := WithRequestedReasoningEffort(context.Background(), tc.requested)
				if tc.ceiling != "" {
					var changed bool
					var err error
					body, changed, err = ApplyOpenAIReasoningEffortPolicy(body, tc.ceiling, nil, "downgrade")
					require.NoError(t, err)
					require.True(t, changed)
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				account := excelAccount()
				result, err := svc.Forward(ctx, c, account, body)
				require.NoError(t, err)
				require.Equal(t, tc.forwarded, gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
				require.Equal(t, tc.requested, optionalStringValue(result.RequestedReasoningEffort))
				require.Equal(t, tc.forwarded, optionalStringValue(result.ReasoningEffort))

				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				usageService := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				require.NoError(t, usageService.RecordUsage(ctx, &OpenAIRecordUsageInput{
					Result: result, APIKey: &APIKey{ID: 10}, User: &User{ID: 20}, Account: account,
				}))
				require.NotNil(t, usageRepo.lastLog)
				require.Equal(t, tc.requested, optionalStringValue(usageRepo.lastLog.RequestedReasoningEffort))
				require.Equal(t, tc.forwarded, optionalStringValue(usageRepo.lastLog.ReasoningEffort))
			})
		}
	}
}

func TestExcelBPSModelDeniedDoesNotFailover(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"basispoints_model_access_changed","message":"SECRET_UPSTREAM"}}`))}}
	svc := openAIClientToolsTestService(upstream)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-5.6-sol","input":"x"}`))
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
	require.Equal(t, 403, rec.Code)
	require.Contains(t, rec.Body.String(), "basispoints_model_access_changed")
	require.NotContains(t, rec.Body.String(), "SECRET_UPSTREAM")
	require.True(t, IsResponseCommitted(c))
}

func TestExcelBPSHTTPErrorRecordsUpstreamRejection(t *testing.T) {
	for _, logBody := range []bool{false, true} {
		t.Run(fmt.Sprint(logBody), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"X-Request-Id": {"bps-upstream-request"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_value","param":"input[2].id","message":"Expected an ID that begins with fc. token=test-token; refresh-secret Bearer other-secret https://user:pass@example.com/?api_key=query-secret; echoed prompt: private-user-input"},"access_token":"other-secret"}`)),
			}}
			svc := openAIClientToolsTestService(upstream)
			svc.cfg.Gateway.LogUpstreamErrorBody = logBody
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			account := excelAccount()
			account.Credentials["refresh_token"] = "refresh-secret"
			_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-6-astra","stream":true,"input":"continue"}`))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Len(t, upstream.requests, 1)
			require.True(t, account.Schedulable)
			require.True(t, IsResponseCommitted(c))
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.True(t, json.Valid(rec.Body.Bytes()))
			require.NotContains(t, rec.Body.String(), "Expected an ID")
			require.Equal(t, http.StatusBadRequest, c.GetInt(OpsUpstreamStatusCodeKey))
			if logBody {
				require.Contains(t, c.GetString(OpsUpstreamErrorMessageKey), "Expected an ID")
			} else {
				require.Equal(t, "Excel BPS returned HTTP 400", c.GetString(OpsUpstreamErrorMessageKey))
			}
			events, exists := c.Get(OpsUpstreamErrorsKey)
			require.True(t, exists)
			attempts, ok := events.([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.Len(t, attempts, 1)
			require.Equal(t, "bps-upstream-request", attempts[0].UpstreamRequestID)
			require.Equal(t, basispoints.ResponsesURL, attempts[0].UpstreamURL)
			require.Equal(t, account.ID, attempts[0].AccountID)
			require.Equal(t, "direct/no_proxy", attempts[0].ProxyName)
			if logBody {
				require.Equal(t, "invalid_value", gjson.Get(attempts[0].Detail, "error.code").String())
				require.Equal(t, "input[2].id", gjson.Get(attempts[0].Detail, "error.param").String())
				require.Equal(t, c.GetString(OpsUpstreamErrorDetailKey), attempts[0].Detail)
			} else {
				require.Empty(t, attempts[0].Detail)
				require.Empty(t, attempts[0].UpstreamResponseBody)
				require.Empty(t, c.GetString(OpsUpstreamErrorDetailKey))
				require.NotContains(t, attempts[0].Message, "private-user-input")
			}
			encoded, err := json.Marshal(attempts)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "test-token")
			require.NotContains(t, string(encoded), "other-secret")
			require.NotContains(t, string(encoded), "refresh-secret")
			require.NotContains(t, string(encoded), "user:pass")
			require.NotContains(t, string(encoded), "query-secret")
		})
	}
}

func TestExcelBPSHTTPErrorAfterCompactKeepalive(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"invalid input"}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	defer stop()
	value, exists := c.Get(openAICompactSSEKeepaliveKey)
	require.True(t, exists)
	keepalive, ok := value.(*openAICompactSSEKeepalive)
	require.True(t, ok)
	require.True(t, keepalive.beat())
	_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(`{"model":"gpt-6-astra","input":"continue"}`))
	require.Error(t, err)
	require.True(t, IsResponseCommitted(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"))
	require.NotContains(t, rec.Body.String(), `{"error":`)
	streamError, exists := GetOpsStreamError(c)
	require.True(t, exists)
	require.Equal(t, "basispoints_upstream_error", streamError.ErrType)
}
func TestExcelBPSThreadScopeSeparatesParallelChildren(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("session_id", "shared-root")
	first, _ := resolveOpenAIWSExecutionScope(c, []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"child-A\"}"}}`), 1)
	second, _ := resolveOpenAIWSExecutionScope(c, []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"child-B\"}"}}`), 1)
	require.NotEmpty(t, first)
	require.NotEqual(t, first, second)
}

func TestExcelBPSMihomoFailsClosed(t *testing.T) {
	for _, identity := range []bool{false, true} {
		t.Run(fmt.Sprint(identity), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc := openAIClientToolsTestService(upstream)
			account := excelAccount()
			account.Extra["openai_excel_bps_mihomo"] = true
			body, _ := nativeGatewayBody(t)
			enableNativeAttachments(svc)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
			if identity {
				c.Request.Header.Set("session_id", "sticky-session")
			}
			_, err := svc.Forward(context.Background(), c, account, body)
			require.ErrorContains(t, err, "basispoints_proxy_unavailable")
			require.Equal(t, 503, rec.Code)
			require.Nil(t, upstream.lastReq)
			account.Extra["openai_excel_bps"] = false
			require.False(t, account.IsExcelBPSMihomoEnabled())
		})
	}
}

func TestExcelBPSAnonymousIdentityIsRequestLocal(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":"identical prompt"}`)
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		return c
	}
	first := newContext()
	id, transient := resolveExcelBPSIdentity(first, body, 1, true)
	require.True(t, transient)
	require.NotEmpty(t, id)
	again, transient := resolveExcelBPSIdentity(first, body, 1, true)
	require.True(t, transient)
	require.Equal(t, id, again, "internal retries reuse a request identity")
	other, transient := resolveExcelBPSIdentity(newContext(), body, 1, true)
	require.True(t, transient)
	require.NotEqual(t, id, other, "identical anonymous prompts must not share a session")
	empty, transient := resolveExcelBPSIdentity(newContext(), body, 1, false)
	require.False(t, transient)
	require.Empty(t, empty, "static proxies keep their old identity behavior")
	first.Request.Header.Set("session_id", "declared-session")
	explicit, transient := resolveExcelBPSIdentity(first, body, 1, true)
	require.False(t, transient)
	second := newContext()
	second.Request.Header.Set("session_id", "declared-session")
	same, transient := resolveExcelBPSIdentity(second, body, 1, true)
	require.False(t, transient)
	require.Equal(t, explicit, same)
	otherKey, _ := resolveExcelBPSIdentity(second, body, 2, true)
	require.NotEqual(t, explicit, otherKey)
}
