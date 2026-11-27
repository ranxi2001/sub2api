//go:build unit && issue1085

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIssue1085RequestSizeRecoveredHistory(t *testing.T) {
	for _, history := range []string{"none", "visible", "hidden"} {
		t.Run(history, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 4)
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/messages", func(c *gin.Context) {
				if history != "none" {
					c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
						AccountID: 42, UpstreamStatusCode: http.StatusServiceUnavailable,
						Message: "fixture earlier failure", SkipMonitoring: history == "hidden",
					}})
				}
				service.SetOpsUpstreamError(c, http.StatusRequestEntityTooLarge, "fixture current request too large", "")
				service.MarkOpsRequestScopedError(c, "request_too_large")
				service.MarkOpsStreamErrorValue(c, service.OpsStreamError{
					ErrType: "request_too_large", Message: "fixture current request too large",
					IntendedStatus: http.StatusRequestEntityTooLarge, RequestScoped: true,
				})
				c.Data(http.StatusOK, "text/event-stream", []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"request_too_large\",\"message\":\"fixture current request too large\"}}\n\n"))
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			issue1085FlushRecords()
			require.Equal(t, http.StatusOK, rec.Code)
			rows := 1
			if history == "visible" {
				rows = 2
			}
			require.Len(t, repo.entries, rows, "current 413 is not a recovered earlier attempt")
			current := repo.entries[0]
			require.Equal(t, http.StatusRequestEntityTooLarge, current.StatusCode)
			require.Equal(t, "request_too_large", current.ErrorType)
			require.Equal(t, "client", current.ErrorOwner)
			require.Equal(t, "client_request", current.ErrorSource)
			require.Equal(t, "request", current.ErrorPhase)
			require.Nil(t, current.UpstreamStatusCode)
			require.Nil(t, current.UpstreamErrorsJSON)
			if history == "visible" {
				recovered := repo.entries[1]
				require.Equal(t, http.StatusOK, recovered.StatusCode)
				require.Equal(t, "provider", recovered.ErrorOwner)
				require.Equal(t, "upstream_http", recovered.ErrorSource)
				require.NotNil(t, recovered.UpstreamStatusCode)
				require.Equal(t, http.StatusServiceUnavailable, *recovered.UpstreamStatusCode)
				require.Equal(t, "Recovered upstream error 503: fixture earlier failure", recovered.ErrorMessage)
				require.NotNil(t, recovered.UpstreamErrorsJSON)
				events, err := service.ParseOpsUpstreamErrors(*recovered.UpstreamErrorsJSON)
				require.NoError(t, err)
				require.Len(t, events, 1)
				require.Equal(t, int64(42), events[0].AccountID)
			}
		})
	}
}
