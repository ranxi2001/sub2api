package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsAnthropicClientRequestStatus(t *testing.T) {
	for _, code := range []int{
		http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusRequestTimeout,
		http.StatusConflict,
		http.StatusGone,
		http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity,
		http.StatusTooEarly,
	} {
		require.Truef(t, isAnthropicClientRequestStatus(code), "status %d should pass through", code)
	}

	for _, code := range []int{
		http.StatusOK,
		http.StatusMovedPermanently,
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		require.Falsef(t, isAnthropicClientRequestStatus(code), "status %d must not pass through", code)
	}
}

func TestWriteAnthropicUpstreamClientErrorPreservesStatusAndBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Antigravity tool result was already consumed"}}`)
	writeAnthropicUpstreamClientError(c, http.StatusConflict, body, "Antigravity tool result was already consumed")

	require.Equal(t, http.StatusConflict, w.Code)
	require.Equal(t, "error", gjson.Get(w.Body.String(), "type").String())
	require.Equal(t, "invalid_request_error", gjson.Get(w.Body.String(), "error.type").String())
	require.Equal(t, "Antigravity tool result was already consumed", gjson.Get(w.Body.String(), "error.message").String())
}

func TestMapUpstreamStatusCodePreservesGatewayTimeout(t *testing.T) {
	require.Equal(t, http.StatusGatewayTimeout, mapUpstreamStatusCode(http.StatusGatewayTimeout))
	require.Equal(t, http.StatusBadGateway, mapUpstreamStatusCode(http.StatusBadGateway))
	require.Equal(t, http.StatusBadGateway, mapUpstreamStatusCode(http.StatusServiceUnavailable))
	require.Equal(t, http.StatusBadGateway, mapUpstreamStatusCode(http.StatusInternalServerError))
	require.Equal(t, http.StatusConflict, mapUpstreamStatusCode(http.StatusConflict))
	require.Equal(t, http.StatusNotFound, mapUpstreamStatusCode(http.StatusNotFound))
}

// TestWriteMappedClaudeErrorPassesThroughUpstreamClient4xx 复现问题：上游 Meridian 对
// 反重力账号返回 409 invalid_request_error，此前会被 default 分支折叠成 502。
func TestWriteMappedClaudeErrorPassesThroughUpstreamClient4xx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &AntigravityGatewayService{}
	account := &Account{ID: 1, Name: "meridian", Platform: PlatformAntigravity}

	for _, tc := range []struct {
		status  int
		errType string
		message string
	}{
		{http.StatusConflict, "invalid_request_error", "Antigravity tool result was already consumed"},
		{http.StatusNotFound, "not_found_error", "model not found"},
		{http.StatusRequestEntityTooLarge, "invalid_request_error", "request too large"},
		{http.StatusUnprocessableEntity, "invalid_request_error", "unprocessable entity"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := []byte(`{"type":"error","error":{"type":"` + tc.errType + `","message":"` + tc.message + `"}}`)

		err := svc.writeMappedClaudeError(c, account, tc.status, "req-1", body)

		require.Error(t, err)
		require.Equalf(t, tc.status, w.Code, "upstream status %d must pass through", tc.status)
		require.Equal(t, tc.errType, gjson.Get(w.Body.String(), "error.type").String())
		require.Equal(t, tc.message, gjson.Get(w.Body.String(), "error.message").String())
	}
}

// TestWriteMappedClaudeErrorKeepsGatewayTimeout 与 5xx：504 保持 504，其它 5xx 仍归 502。
func TestWriteMappedClaudeErrorKeepsGatewayTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &AntigravityGatewayService{}
	account := &Account{ID: 1, Name: "meridian", Platform: PlatformAntigravity}

	for _, tc := range []struct {
		upstreamStatus int
		wantStatus     int
	}{
		{http.StatusGatewayTimeout, http.StatusGatewayTimeout},
		{http.StatusBadGateway, http.StatusBadGateway},
		{http.StatusServiceUnavailable, http.StatusBadGateway},
		{http.StatusInternalServerError, http.StatusBadGateway},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := []byte(`{"type":"error","error":{"type":"api_error","message":"upstream failed"}}`)

		err := svc.writeMappedClaudeError(c, account, tc.upstreamStatus, "req-1", body)

		require.Error(t, err)
		require.Equalf(t, tc.wantStatus, w.Code, "upstream %d -> client %d", tc.upstreamStatus, tc.wantStatus)
	}
}
