package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProfitOperationsHTTPValidation(t *testing.T) {
	h := NewChannelHandler(nil, nil, nil)
	r := gin.New()
	r.GET("/upstream", h.ProfitOperations)
	for _, q := range []string{"", "?from=invalid&until=invalid", "?from=2026-01-01T00:00:00Z&until=2026-04-01T00:00:00Z"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/upstream"+q, nil))
		require.Equal(t, 400, w.Code, "reject invalid input before repository")
		require.Contains(t, w.Body.String(), "UPSTREAM_OPERATIONS_VALIDATION", "independent analytics must not report model-channel membership errors")
	}
}

type profitHTTPRepo struct {
	service.ChannelRepository
	filter service.UpstreamOperationsFilter
	err    error
}

func (r *profitHTTPRepo) ReadProfitOperations(_ context.Context, f service.UpstreamOperationsFilter) (*service.ProfitOperationsSnapshot, error) {
	r.filter = f
	return &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{Scope: f, Usage: json.RawMessage(`{"summary":{"requests":2},"rows":[],"total":0}`)}}, r.err
}
func TestProfitOperationsHTTPIndependentScopesAndSafeFailure(t *testing.T) {
	repo := &profitHTTPRepo{}
	h := NewChannelHandler(service.NewChannelService(repo, nil, nil, nil, nil), nil, nil)
	r := gin.New()
	r.GET("/upstream", h.ProfitOperations)
	path := "/upstream?from=2026-10-01T00:00:00Z&until=2026-10-02T00:00:00Z&account_id=42&dimension=model&model_scope=upstream&sort=estimated_profit&direction=asc"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, int64(42), repo.filter.AccountID)
	require.Equal(t, "model", repo.filter.Dimension)
	require.Contains(t, w.Body.String(), `"requests":2`)
	repo.err = errors.New("sensitive raw SQL credentials token")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	require.Equal(t, 500, w.Code)
	require.Contains(t, w.Body.String(), `"reason":"PROFIT_OPERATIONS_QUERY_FAILED"`)
	require.NotContains(t, w.Body.String(), "sensitive")
	require.NotContains(t, w.Body.String(), `"requests":0`)
	repo.err = fmt.Errorf("sensitive driver details: %w", context.DeadlineExceeded)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	require.Equal(t, 504, w.Code)
	require.Contains(t, w.Body.String(), `"reason":"PROFIT_OPERATIONS_TIMEOUT"`)
	require.NotContains(t, w.Body.String(), "sensitive")
	require.NotContains(t, w.Body.String(), `"requests":0`)
	require.NotContains(t, w.Body.String(), `"summary"`, "timeouts cannot return a partial financial snapshot")
}
