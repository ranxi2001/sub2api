//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestProfitOperationsCachePagesSortAndInvalidation(t *testing.T) {
	var cache profitReportCache
	calls := 0
	f := service.UpstreamOperationsFilter{From: time.Now().Add(-time.Hour), Until: time.Now(), Dimension: "upstream", Sort: "upstream_cost", Direction: "asc", Page: 1, PageSize: 1}
	load := func(_ context.Context, collect bool) (*service.ProfitOperationsSnapshot, error) {
		calls++
		require.True(t, collect)
		return &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{Scope: f, QueriedAt: f.Until, Usage: json.RawMessage(`{"summary":{"requests":1000000,"request_income":123},"rows":[{"key":"missing","upstream_cost":null},{"key":"two","upstream_cost":2},{"key":"one","upstream_cost":1}],"total":3,"members":[],"member_total":0,"catalog":[],"catalog_total":0}`)}}, nil
	}
	first, err := cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Contains(t, string(first.Usage), `"key":"one"`)
	f.Page = 2
	second, err := cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Contains(t, string(second.Usage), `"key":"two"`)
	require.Equal(t, first.QueriedAt, second.QueriedAt)
	f.Page = 1
	f.Direction = "desc"
	third, err := cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Contains(t, string(third.Usage), `"key":"two"`)
	require.Equal(t, 1, calls)
	f.Page = 3
	last, err := cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Contains(t, string(last.Usage), `"key":"missing"`)
	cache.clear()
	_, err = cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	f.AccountID = 4
	_, err = cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}
func TestProfitOperationsCacheDoesNotCacheOversizedReports(t *testing.T) {
	var cache profitReportCache
	calls := 0
	f := service.UpstreamOperationsFilter{Page: 1, PageSize: 20}
	load := func(_ context.Context, collect bool) (*service.ProfitOperationsSnapshot, error) {
		calls++
		raw := `{"total":10001}`
		if !collect {
			raw = `{"total":10001,"rows":[]}`
		}
		return &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{Usage: json.RawMessage(raw)}}, nil
	}
	for i := 0; i < 2; i++ {
		_, err := cache.read(context.Background(), f, load)
		require.NoError(t, err)
	}
	require.Equal(t, 4, calls)
}
