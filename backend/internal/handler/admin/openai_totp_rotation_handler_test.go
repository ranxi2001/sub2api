package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTOTPRotationAdminEndpointsRejectObserversAndDisableCaching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIOAuthReauthHandler{}
	for _, endpoint := range []gin.HandlerFunc{h.RotationStatus, h.RotateTOTP, h.RetryTOTP, h.ExportTOTP, h.ExportTOTPRecovery, h.ImportTOTPRecovery} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/accounts/42", strings.NewReader("{}"))
		c.Request = c.Request.WithContext(service.WithObserverScope(c.Request.Context(), []int64{1}))
		endpoint(c)
		require.Equal(t, 403, recorder.Code)
		require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	}
}
func TestTOTPRotationWorkerEndpointsRequireWorkerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(openAIOAuthReauthWorkerTokenEnv, strings.Repeat("x", 32))
	h := &OpenAIOAuthReauthHandler{}
	for _, endpoint := range []gin.HandlerFunc{h.ClaimTOTP, h.TOTPPhase, h.TOTPFinish, h.TOTPRecover} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/internal", strings.NewReader("{}"))
		endpoint(c)
		require.Equal(t, 401, recorder.Code)
		require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	}
}
