package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAuditConnectionAuthorizationOmitsSessionAndCallback(t *testing.T) {
	for _, action := range []string{"callback", "authorization-status"} {
		t.Run(action, func(t *testing.T) {
			repository := &auditCaptureRepository{}
			auditService := service.NewAuditLogService(repository, nil)
			auditService.Start()
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(ContextKeyUser), AuthSubject{UserID: 17})
				c.Set(string(ContextKeyUserRole), "admin")
				c.Next()
			})
			router.Use(gin.HandlerFunc(NewAuditLogMiddleware(auditService)))
			body := `{"session_id":"PRIVATE_SESSION","callback_url":"https://example.invalid/?code=PRIVATE_CODE&state=PRIVATE_STATE"}`
			router.POST("/api/v1/admin/accounts/:id/auth-connections/:connection_id/"+action, func(c *gin.Context) {
				got, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Equal(t, body, string(got))
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/auth-connections/fixture/"+action, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			auditService.Stop()
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, repository.logs, 1)
			require.Equal(t, "<credential-bearing body omitted>", repository.logs[0].RequestBody)
		})
	}
}
