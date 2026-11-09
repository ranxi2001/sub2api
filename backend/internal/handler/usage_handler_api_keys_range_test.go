package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type keyRangeUsageRepo struct {
	service.UsageLogRepository
	called     bool
	ids        []int64
	start, end time.Time
}

func (r *keyRangeUsageRepo) GetBatchAPIKeyUsageStats(_ context.Context, ids []int64, start, end time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	r.called, r.ids, r.start, r.end = true, ids, start, end
	stats := make(map[int64]*usagestats.BatchAPIKeyUsageStats, len(ids))
	for _, id := range ids {
		stats[id] = &usagestats.BatchAPIKeyUsageStats{APIKeyID: id, TodayActualCost: 1.25, TotalActualCost: 12.5}
	}
	return stats, nil
}

type keyRangeOwnershipRepo struct {
	service.APIKeyRepository
	called bool
	userID int64
}

func (r *keyRangeOwnershipRepo) VerifyOwnership(_ context.Context, userID int64, ids []int64) ([]int64, error) {
	r.called, r.userID = true, userID
	var owned []int64
	for _, id := range ids {
		if id == 7 {
			owned = append(owned, id)
		}
	}
	return owned, nil
}

func keyRangeRequest(t *testing.T, body string) (*httptest.ResponseRecorder, *keyRangeUsageRepo, *keyRangeOwnershipRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo, keys := &keyRangeUsageRepo{}, &keyRangeOwnershipRepo{}
	h := NewUsageHandler(service.NewUsageService(repo, nil, nil, nil), service.NewAPIKeyService(keys, nil, nil, nil, nil, nil, nil), nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
	})
	router.POST("/usage/dashboard/api-keys-usage", h.DashboardAPIKeysUsage)
	req := httptest.NewRequest(http.MethodPost, "/usage/dashboard/api-keys-usage", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w, repo, keys
}

func TestDashboardAPIKeysUsageDateRange(t *testing.T) {
	for _, tc := range []struct {
		name, start, end, zone, wantStart, wantEnd string
	}{
		{"inclusive days", "2026-11-01", "2026-11-03", "Asia/Shanghai", "2026-10-31T16:00:00Z", "2026-11-03T16:00:00Z"},
		{"same day", "2026-11-01", "2026-11-01", "UTC", "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z"},
		{"spring DST", "2026-03-08", "2026-03-08", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"autumn DST", "2026-11-01", "2026-11-01", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(BatchAPIKeysUsageRequest{APIKeyIDs: []int64{7, 999}, StartDate: tc.start, EndDate: tc.end, Timezone: tc.zone})
			require.NoError(t, err)
			w, repo, keys := keyRangeRequest(t, string(body))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, int64(42), keys.userID)
			require.Equal(t, []int64{7}, repo.ids)
			require.Equal(t, tc.wantStart, repo.start.UTC().Format(time.RFC3339))
			require.Equal(t, tc.wantEnd, repo.end.UTC().Format(time.RFC3339))
			require.Contains(t, w.Body.String(), `"total_actual_cost":12.5`)
			require.NotContains(t, w.Body.String(), `"999"`)
		})
	}
}

func TestDashboardAPIKeysUsageWithoutDatesPreservesDefaults(t *testing.T) {
	w, repo, _ := keyRangeRequest(t, `{"api_key_ids":[7]}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, repo.called)
	require.True(t, repo.start.IsZero())
	require.True(t, repo.end.IsZero())
}

func TestDashboardAPIKeysUsageRejectsInvalidRange(t *testing.T) {
	for _, dates := range []string{
		`"start_date":"2026-11-01"`,
		`"end_date":"2026-11-01"`,
		`"start_date":"2026-02-30","end_date":"2026-11-01"`,
		`"start_date":"2026-11-01","end_date":"invalid"`,
		`"start_date":"2026-11-03","end_date":"2026-11-01"`,
	} {
		t.Run(dates, func(t *testing.T) {
			w, repo, keys := keyRangeRequest(t, `{"api_key_ids":[7],`+dates+`}`)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.False(t, repo.called)
			require.False(t, keys.called)
		})
	}
}

func TestDashboardAPIKeysUsageRangeWithoutOwnedKeys(t *testing.T) {
	w, repo, _ := keyRangeRequest(t, `{"api_key_ids":[999],"start_date":"2026-11-01","end_date":"2026-11-02"}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.False(t, repo.called)
	require.Contains(t, w.Body.String(), `"stats":{}`)
}
