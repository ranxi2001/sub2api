package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestUpstreamOperationsHTTPValidation(t *testing.T) {
	h := NewChannelHandler(nil, nil, nil)
	r := gin.New()
	r.GET("/profit", h.UpstreamOperations)
	for _, q := range []string{"", "?from=invalid&until=invalid", "?from=2026-01-01T00:00:00Z&until=2026-04-01T00:00:00Z"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/profit"+q, nil))
		require.Equal(t, 400, w.Code, "reject invalid input before repository")
	}
}
