//go:build unit

package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Use the real Go HTTP/2 reader, including its unexported StreamError type.
// Only routing/TLS trust is injected; the service, handler, capture middleware,
// persistence preparation and user DTO mapper are production implementations.
type peerResetHTTPUpstream struct {
	service.HTTPUpstream
	client *http.Client
	base   *url.URL
}

func (u *peerResetHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = u.base.Scheme, u.base.Host
	return u.client.Do(req)
}

func TestHTTP2PeerResetBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupOpsErrorLogTestQueue(t, 8)
	var submissions atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Error("fixture must negotiate real HTTP/2")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		submissions.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		w.(http.Flusher).Flush()
		// net/http sends RST_STREAM(INTERNAL_ERROR), without a successful terminal.
		panic(http.ErrAbortHandler)
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	base, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	h := newOpenAIResponsesFailoverTestHandler(t, &peerResetHTTPUpstream{client: upstream.Client(), base: base})
	repo := &ingressRejectOpsRepo{}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	var outcomes []service.AccountConcurrencyResult
	ops.SetAutoConfigObserver(func(result service.AccountConcurrencyResult) { outcomes = append(outcomes, result) })
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
		for key, value := range seed.Keys {
			c.Set(key, value)
		}
		h.Responses(c)
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","stream":true,"input":"synthetic"}`)))
	for len(opsErrorLogQueue) > 0 {
		flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
	}
	require.Equal(t, int32(1), submissions.Load(), "submitted request must never be replayed")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "partial")
	require.Len(t, repo.entries, 1)
	entry := repo.entries[0]
	require.Equal(t, "transport_error", entry.ErrorType, "peer reset must persist as transport_error, not synthetic HTTP 502")
	require.Equal(t, http.StatusOK, entry.StatusCode, "persist committed wire status")
	require.Nil(t, entry.UpstreamStatusCode, "a reset has no upstream HTTP error status")
	require.False(t, entry.IsBusinessLimited)
	require.Len(t, outcomes, 1)
	require.False(t, outcomes[0].Success)
	body := rec.Body.String()
	require.Equal(t, 1, strings.Count(body, "event: response.failed\n"))
	require.NotContains(t, body, "event: error\n")
	require.NotContains(t, body, "response.completed")
	require.NotContains(t, body, "[DONE]")
	for _, safe := range []string{"upstream_http2_stream_error", "reset by peer", "INTERNAL_ERROR", "incomplete", "Do not automatically retry"} {
		require.Contains(t, body, safe)
	}
	detail := service.ToUserErrorRequestDetail(&service.OpsErrorLogDetail{
		OpsErrorLog: service.OpsErrorLog{Type: entry.ErrorType, Phase: entry.ErrorPhase, StatusCode: entry.StatusCode, Message: entry.ErrorMessage},
		ErrorBody:   entry.ErrorBody, UpstreamStatusCode: entry.UpstreamStatusCode,
	})
	payload, err := json.Marshal(detail)
	require.NoError(t, err)
	require.Equal(t, "transport_error", detail.Category)
	for _, unsafe := range []string{"stream ID", "127.0.0.1", "synthetic-", "received from peer"} {
		require.NotContains(t, body, unsafe)
		require.NotContains(t, string(payload), unsafe)
	}
}
