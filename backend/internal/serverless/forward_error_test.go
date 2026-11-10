package serverless

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestServerlessForwardFailureLogDoesNotExposeRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m, err := New(Runtime{}, nil, &memoryStore{})
	require.NoError(t, err)
	t.Cleanup(m.Stop)
	core, logs := observer.New(zap.WarnLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "safe-request-id")))
	ctx, cancel := context.WithCancel(ctx)
	cancel()

	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		m.forward(c, Pod{ID: "test-pod", Endpoint: "http://127.0.0.1:1"}, "203.0.113.1")
	})
	r := httptest.NewRequest("POST", "/v1/responses?token=fake-query-secret", strings.NewReader("fake-private-body"))
	r.Header.Set("Authorization", "Bearer fake-auth-secret")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r.WithContext(ctx))
	require.Equal(t, 503, w.Code)

	entries := logs.FilterMessage("serverless.forward_failed").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, "test-pod", fields["pod_id"])
	require.Equal(t, "canceled", fields["error_kind"])
	require.Equal(t, "safe-request-id", fields["request_id"])
	require.EqualValues(t, 0, fields["response_header_timeout_ms"])
	encoded, err := json.Marshal(fields)
	require.NoError(t, err)
	for _, secret := range []string{"fake-query-secret", "fake-private-body", "fake-auth-secret", "127.0.0.1:1"} {
		require.NotContains(t, string(encoded), secret)
	}
}
