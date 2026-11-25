package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsWSContextRejectionRetainsRequestOwnership(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	repo := &ingressRejectOpsRepo{}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Set(opsAccountIDKey, int64(300))
		service.SetOpsUpstreamError(c, http.StatusOK, "", "")
		service.MarkOpsStreamErrorValue(c, service.OpsStreamError{
			ErrType: "invalid_request_error", Code: "context_length_exceeded", Message: "Input exceeds the context window",
			IntendedStatus: 400, RequestScoped: true, UpstreamWSFailure: &service.OpsUpstreamWSFailure{Phase: "upstream_event"},
		})
		c.Header("Content-Type", "text/event-stream")
		_, err := c.Writer.WriteString("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"Input exceeds the context window\"}}\n\n")
		require.NoError(t, err)
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	for len(opsErrorLogQueue) > 0 {
		job := <-opsErrorLogQueue
		flushOpsErrorLogBatch([]opsErrorLogJob{job})
	}
	require.Len(t, repo.entries, 1)
	entry := repo.entries[0]
	require.Equal(t, 400, entry.StatusCode)
	require.Equal(t, "client", entry.ErrorOwner)
	require.Equal(t, "request", entry.ErrorPhase)
	require.True(t, entry.IsBusinessLimited)
	require.Nil(t, entry.AccountID)
}
