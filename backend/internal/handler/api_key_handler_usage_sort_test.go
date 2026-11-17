package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usageSortKeyRepo struct {
	service.APIKeyRepository
	called  bool
	userID  int64
	params  pagination.PaginationParams
	filters service.APIKeyListFilters
}

func (r *usageSortKeyRepo) ListByUserID(_ context.Context, userID int64, params pagination.PaginationParams, filters service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	r.called, r.userID, r.params, r.filters = true, userID, params, filters
	return nil, &pagination.PaginationResult{}, nil
}

func usageSortListRequest(query string) (*httptest.ResponseRecorder, *usageSortKeyRepo) {
	repo := &usageSortKeyRepo{}
	h := NewAPIKeyHandler(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
	})
	router.GET("/keys", h.List)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/keys?"+query, nil))
	return w, repo
}

func TestAPIKeyListUsageSortRange(t *testing.T) {
	for _, tc := range []struct {
		name, date, zone, start, end string
	}{
		{"local day", "2026-10-01", "Asia/Shanghai", "2026-09-30T16:00:00Z", "2026-10-01T16:00:00Z"},
		{"DST spring", "2026-03-08", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"DST fall", "2026-11-01", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, repo := usageSortListRequest("sort_by=usage&page=2&page_size=5&search=example&status=active&group_id=7&start_date=" + tc.date + "&end_date=" + tc.date + "&timezone=" + tc.zone)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.True(t, repo.called)
			require.Equal(t, int64(42), repo.userID)
			require.Equal(t, pagination.PaginationParams{Page: 2, PageSize: 5, SortBy: "usage", SortOrder: "desc"}, repo.params)
			require.Equal(t, "example", repo.filters.Search)
			require.Equal(t, "active", repo.filters.Status)
			require.Equal(t, int64(7), *repo.filters.GroupID)
			require.Equal(t, tc.start, repo.filters.UsageStartTime.UTC().Format(time.RFC3339))
			require.Equal(t, tc.end, repo.filters.UsageEndTime.UTC().Format(time.RFC3339))
		})
	}
}

func TestAPIKeyListUsageSortRejectsInvalidRange(t *testing.T) {
	for _, query := range []string{
		"start_date=2026-10-01",
		"end_date=2026-10-01",
		"start_date=invalid&end_date=2026-10-01",
		"start_date=2026-10-01&end_date=2026-02-30",
		"start_date=2026-10-02&end_date=2026-10-01",
	} {
		w, repo := usageSortListRequest("sort_by=usage&" + query)
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		require.False(t, repo.called)
	}
}

func TestAPIKeyListPreservesLegacyDefaults(t *testing.T) {
	w, repo := usageSortListRequest("")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "created_at", repo.params.SortBy)
	require.Equal(t, "desc", repo.params.SortOrder)
	require.True(t, repo.filters.UsageStartTime.IsZero())
	require.True(t, repo.filters.UsageEndTime.IsZero())
	w, repo = usageSortListRequest("sort_by=usage")
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, repo.filters.UsageStartTime.IsZero())
	require.True(t, repo.filters.UsageEndTime.IsZero())
}
