package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsCancellationKeepsClientAttributionAndRespectsFiltering(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		name := "recorded"
		if ignored {
			name = "filtered"
		}
		t.Run(name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 4)
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, &ingressRejectSettingRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			settings, err := ops.GetOpsAdvancedSettings(context.Background())
			require.NoError(t, err)
			settings.IgnoreContextCanceled = ignored
			_, err = ops.UpdateOpsAdvancedSettings(context.Background(), settings)
			require.NoError(t, err)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				c.Set(opsAccountIDKey, int64(300))
				// A real earlier failed attempt must remain separate telemetry.
				service.SetOpsUpstreamError(c, 503, "earlier upstream failure", "")
				service.MarkOpsClientCancellation(c, true)
			})
			recorder := httptest.NewRecorder()
			start := time.Now().Add(-200 * time.Millisecond)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request = request.WithContext(context.WithValue(request.Context(), ctxkey.RequestStartTime, start))
			router.ServeHTTP(recorder, request)
			require.Empty(t, recorder.Body.String(), "logging cancellation must not emit an error response")
			var rows []*service.OpsInsertErrorLogInput
			for len(opsErrorLogQueue) > 0 {
				job := <-opsErrorLogQueue
				flushOpsErrorLogBatch([]opsErrorLogJob{job})
			}
			rows = repo.entries
			if ignored {
				require.Len(t, rows, 1)
			} else {
				require.Len(t, rows, 2)
				canceled := rows[0]
				require.Equal(t, 499, canceled.StatusCode)
				require.Equal(t, service.OpsClientCanceledCode, canceled.ErrorType)
				require.Equal(t, "request", canceled.ErrorPhase)
				require.Equal(t, "client", canceled.ErrorOwner)
				require.Equal(t, "client_request", canceled.ErrorSource)
				require.False(t, canceled.IsBusinessLimited)
				require.Nil(t, canceled.UpstreamStatusCode)
				require.Nil(t, canceled.UpstreamErrorMessage)
				require.NotNil(t, canceled.DurationMs)
				require.GreaterOrEqual(t, *canceled.DurationMs, int64(200))
			}
			previous := rows[len(rows)-1]
			require.Equal(t, http.StatusOK, previous.StatusCode)
			require.Equal(t, "provider", previous.ErrorOwner)
			require.Equal(t, 503, *previous.UpstreamStatusCode)
		})
	}
}

func TestOpsErrorDurationUsesIngressClockAndFreezesBeforeQueue(t *testing.T) {
	for _, mode := range []string{"json", "stream", "recovered", "dedicated"} {
		t.Run(mode, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 3)
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			var start time.Time
			router.POST("/v1/responses", func(c *gin.Context) {
				var ok bool
				start, ok = c.Request.Context().Value(ctxkey.RequestStartTime).(time.Time)
				require.True(t, ok)
				switch mode {
				case "json":
					c.JSON(400, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "invalid input"}})
				case "stream":
					_, err := c.Writer.WriteString(": keepalive\n\n")
					require.NoError(t, err)
					c.Writer.Flush()
					service.MarkOpsStreamFailure(c, "upstream_error", "stream_incomplete", "stream failed", 502)
				case "recovered":
					service.SetOpsUpstreamError(c, 503, "earlier failure", "")
					c.Status(200)
				case "dedicated":
					entry := &service.OpsInsertErrorLogInput{ErrorType: "api_error", ErrorPhase: "internal", StatusCode: 500}
					applyOpsLatencyFieldsFromContext(c, entry)
					enqueueOpsErrorLog(ops, entry)
					c.Set(opsDedicatedErrorRecordedKey, true)
				}
			})
			knownStart := time.Now().Add(-250 * time.Millisecond)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request = request.WithContext(context.WithValue(request.Context(), ctxkey.RequestStartTime, knownStart))
			router.ServeHTTP(httptest.NewRecorder(), request)
			require.Equal(t, knownStart, start)
			require.Equal(t, int64(1), OpsErrorLogQueueLength())
			job := <-opsErrorLogQueue
			require.NotNil(t, job.entry.DurationMs)
			measured := *job.entry.DurationMs
			require.GreaterOrEqual(t, measured, int64(250))
			flushOpsErrorLogBatch([]opsErrorLogJob{job})
			require.Len(t, repo.entries, 1)
			require.Equal(t, measured, *repo.entries[0].DurationMs)
		})
	}
}

func TestOpsErrorDurationWithoutAccessLoggerStillRecordsZero(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	service.SetOpsLatencyMs(c, service.OpsRequestDurationMsKey, 0)
	entry := new(service.OpsInsertErrorLogInput)
	applyOpsLatencyFieldsFromContext(c, entry)
	require.NotNil(t, entry.DurationMs)
	require.Zero(t, *entry.DurationMs)
}

func TestAccessLoggerPublishesIngressClock(t *testing.T) {
	router := gin.New()
	router.Use(middleware2.Logger())
	router.GET("/health", func(c *gin.Context) {
		start, ok := c.Request.Context().Value(ctxkey.RequestStartTime).(time.Time)
		require.True(t, ok)
		require.False(t, start.IsZero())
		require.False(t, start.After(time.Now()))
		c.Status(200)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
}

func TestOpsUpstreamCannotClaimClientCancellation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST("/v1/responses", func(c *gin.Context) {
				service.SetOpsUpstreamError(c, 502, "upstream rejected request", "")
				if stream {
					c.Header("Content-Type", "text/event-stream")
					_, err := c.Writer.WriteString("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"client_canceled\",\"code\":\"client_canceled\",\"message\":\"upstream rejected request\"}}}\n\n")
					require.NoError(t, err)
					c.Writer.Flush()
				} else {
					c.JSON(502, gin.H{"error": gin.H{"type": "client_canceled", "code": "client_canceled", "message": "upstream rejected request"}})
				}
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			require.Equal(t, int64(1), OpsErrorLogQueueLength())
			job := <-opsErrorLogQueue
			require.NotEqual(t, service.OpsClientCanceledCode, job.entry.ErrorType)
			require.Equal(t, "provider", job.entry.ErrorOwner)
			require.False(t, job.entry.IsBusinessLimited)
		})
	}
}

func TestOpsServerlessCancellationIsClientOutcome(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 4)
	repo := &ingressRejectOpsRepo{}
	ops := service.NewOpsService(repo, &ingressRejectSettingRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	settings, err := ops.GetOpsAdvancedSettings(context.Background())
	require.NoError(t, err)
	settings.IgnoreContextCanceled = false
	_, err = ops.UpdateOpsAdvancedSettings(context.Background(), settings)
	require.NoError(t, err)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		// Internal marker produced by the actual transfer cancellation boundary.
		c.Set("serverless.forward_client_canceled", true)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Empty(t, w.Body.String())
	require.Len(t, opsErrorLogQueue, 1)
	flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
	require.Len(t, repo.entries, 1)
	row := repo.entries[0]
	require.Equal(t, 499, row.StatusCode)
	require.Equal(t, service.OpsClientCanceledCode, row.ErrorType)
	require.Equal(t, "client", row.ErrorOwner)
	require.Equal(t, "client_request", row.ErrorSource)
	require.Nil(t, row.UpstreamStatusCode)
}
