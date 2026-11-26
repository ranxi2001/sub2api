package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type observerUsageRepo struct {
	userUsageRepoCapture
	record        *service.UsageLog
	timingCalls   int
	optionUserID  int64
	includeErrors bool
	modelFilters  usagestats.UsageLogFilters
	modelSource   string
}

func (r *observerUsageRepo) GetModelStatsWithUsageFiltersBySource(_ context.Context, _, _ time.Time, filters usagestats.UsageLogFilters, source string) ([]usagestats.ModelStat, error) {
	r.modelFilters, r.modelSource = filters, source
	return []usagestats.ModelStat{}, nil
}

func (r *observerUsageRepo) GetByID(context.Context, int64) (*service.UsageLog, error) {
	return r.record, nil
}
func (r *observerUsageRepo) RequestTimings(context.Context, int64) ([]json.RawMessage, error) {
	r.timingCalls++
	return []json.RawMessage{}, nil
}
func (r *observerUsageRepo) OwnUsageFilterOptions(_ context.Context, id int64, _, _ string, errors bool) ([]service.UsageFilterOption, error) {
	r.optionUserID, r.includeErrors = id, errors
	return []service.UsageFilterOption{{ID: 7, Name: "own"}}, nil
}
func observerUsageRouter(r *observerUsageRepo, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewUsageHandler(service.NewUsageService(r, nil, nil, nil), nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		c.Set(string(middleware.ContextKeyUserRole), role)
	})
	router.GET("/usage", h.List)
	router.GET("/usage/stats", h.Stats)
	router.GET("/usage/dashboard/models", h.DashboardModels)
	router.GET("/usage/dashboard/snapshot-v2", h.DashboardSnapshotV2)
	router.GET("/usage/filter-options", h.ObserverFilterOptions)
	router.GET("/usage/:id", h.GetByID)
	return router
}
func usageGET(router *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestObserverUsageScopeAndProjection(t *testing.T) {
	upstream := "mapped-model"
	accountRate := 2.0
	accountCost := 9.0
	for _, role := range []string{service.RoleObserver, service.RoleUser} {
		t.Run(role, func(t *testing.T) {
			r := &observerUsageRepo{userUsageRepoCapture: userUsageRepoCapture{
				listRows: []service.UsageLog{{ID: 1, UserID: 42, UpstreamModel: &upstream, AccountRateMultiplier: &accountRate, Account: &service.Account{ID: 7, Name: "account", Credentials: map[string]any{"secret": "must-not-leak"}}}},
				stats:    &usagestats.UsageStats{TotalAccountCost: &accountCost},
			}}
			router := observerUsageRouter(r, role)
			query := "?user_id=999&account_id=7&group_id=8&request_id=req&exact_total=true&upstream_model_mismatch=true&start_date=2026-09-01&end_date=2026-09-02"
			w := usageGET(router, "/usage"+query)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, int64(42), r.listFilters.UserID)
			require.NotContains(t, w.Body.String(), "must-not-leak")
			if role == service.RoleObserver {
				require.Contains(t, w.Body.String(), "mapped-model")
				require.Equal(t, int64(7), r.listFilters.AccountID)
				require.Equal(t, "req", r.listFilters.RequestID)
				require.True(t, r.listFilters.ExactTotal)
			} else {
				require.NotContains(t, w.Body.String(), "mapped-model")
				require.Zero(t, r.listFilters.AccountID)
			}
			w = usageGET(router, "/usage/stats"+query)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, int64(42), r.statsFilters.UserID)
			if role == service.RoleObserver {
				require.Contains(t, w.Body.String(), `"total_account_cost":9`)
			} else {
				require.NotContains(t, w.Body.String(), `"total_account_cost":9`)
			}
			w = usageGET(router, "/usage/dashboard/snapshot-v2"+query+"&include_group_stats=true")
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, int64(42), r.trendFilters.UserID)
			require.Equal(t, int64(42), r.groupFilters.UserID)
		})
	}
}

func TestObserverProcurementSnapshotFollowsExistingAccountCostScope(t *testing.T) {
	accountRate, accountCost := 2.0, 9.0
	for _, role := range []string{service.RoleObserver, service.RoleUser} {
		t.Run(role, func(t *testing.T) {
			row := service.UsageLog{ID: 1, UserID: 42, AccountRateMultiplier: &accountRate,
				Account: &service.Account{ID: 7, Credentials: map[string]any{"api_key": "synthetic-must-not-leak"}}}
			// Historical WS regressions compile this test package against older UsageLog types.
			// The observer response below must expose the decoded snapshot on the candidate.
			require.NoError(t, json.Unmarshal([]byte(`{"ProcurementCostSnapshot":{
				"version":1,"account_type":"gateway","provider":"new_api","target_platform":"openai",
				"basis":"total_cost","cost_multiplier":0.7}}`), &row))
			r := &observerUsageRepo{record: &row, userUsageRepoCapture: userUsageRepoCapture{
				listRows: []service.UsageLog{row}, stats: &usagestats.UsageStats{TotalAccountCost: &accountCost}}}
			router := observerUsageRouter(r, role)
			for _, path := range []string{"/usage?user_id=999", "/usage/1"} {
				w := usageGET(router, path)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.NotContains(t, w.Body.String(), "synthetic-must-not-leak")
				if role == service.RoleObserver {
					require.Contains(t, w.Body.String(), `"account_rate_multiplier":2`)
					require.Contains(t, w.Body.String(), `"procurement_cost_snapshot"`)
					require.Contains(t, w.Body.String(), `"cost_multiplier":0.7`)
				} else {
					require.NotContains(t, w.Body.String(), "account_rate_multiplier")
					require.NotContains(t, w.Body.String(), "procurement_cost_snapshot")
				}
			}
			require.Equal(t, int64(42), r.listFilters.UserID, "query user_id cannot expand existing ownership scope")
			w := usageGET(router, "/usage/stats?user_id=999")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			if role == service.RoleObserver {
				require.Contains(t, w.Body.String(), `"total_account_cost":9`)
			} else {
				require.NotContains(t, w.Body.String(), `"total_account_cost":9`)
			}
			r.record.UserID = 999
			w = usageGET(router, "/usage/1")
			require.Equal(t, http.StatusForbidden, w.Code)
			require.NotContains(t, w.Body.String(), "procurement_cost_snapshot", "a foreign record remains forbidden")
		})
	}
}

func TestObserverOwnUsageFiltersAndTiming(t *testing.T) {
	r := &observerUsageRepo{record: &service.UsageLog{ID: 1, UserID: 999}}
	router := observerUsageRouter(r, service.RoleObserver)
	require.Equal(t, 404, usageGET(router, "/usage/1/timing").Code)
	require.Equal(t, 200, usageGET(router, "/usage/filter-options?kind=account&user_id=999&include_errors=true").Code)
	require.Equal(t, int64(42), r.optionUserID)
	require.False(t, r.includeErrors)
	require.Equal(t, 400, usageGET(router, "/usage/filter-options?kind=user").Code)
	for _, q := range []string{"account_id=-1", "account_id=bad", "api_key_id=-1", "upstream_model_mismatch=bad", "exact_total=bad"} {
		require.Equal(t, 400, usageGET(router, "/usage?"+q).Code, q)
	}
	require.Equal(t, 200, usageGET(router, "/usage?api_key_id=999&user_id=999").Code)
	require.Equal(t, int64(42), r.listFilters.UserID)
	require.Equal(t, int64(999), r.listFilters.APIKeyID)
	for _, source := range []string{"requested", "upstream", "mapping"} {
		w := usageGET(router, "/usage/dashboard/models?model_source="+source+"&user_id=999&account_id=7")
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Equal(t, int64(42), r.modelFilters.UserID)
		require.Equal(t, int64(7), r.modelFilters.AccountID)
		require.Equal(t, source, r.modelSource)
	}
	for _, role := range []string{service.RoleUser, service.RoleAdmin, ""} {
		denied := observerUsageRouter(r, role)
		require.Equal(t, 403, usageGET(denied, "/usage/filter-options?kind=group").Code)
	}
}
