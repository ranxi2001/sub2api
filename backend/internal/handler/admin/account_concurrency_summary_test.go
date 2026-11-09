//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type concurrencySummaryAdminService struct {
	*stubAdminService
	err error
	ctx context.Context
}

func (s *concurrencySummaryAdminService) ListAccounts(ctx context.Context, page, pageSize int, platform, accountType, status, search string, groupID int64, privacyMode, sortBy, sortOrder string) ([]service.Account, int64, error) {
	s.ctx = ctx
	if s.err != nil {
		return nil, 0, s.err
	}
	return s.stubAdminService.ListAccounts(ctx, page, pageSize, platform, accountType, status, search, groupID, privacyMode, sortBy, sortOrder)
}

type concurrencySummaryCache struct {
	testutil.StubConcurrencyCache
	counts map[int64]int
	err    error
	calls  [][]int64
}

func (s *concurrencySummaryCache) GetAccountConcurrencyBatch(ctx context.Context, ids []int64) (map[int64]int, error) {
	s.calls = append(s.calls, append([]int64(nil), ids...))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.counts, s.err
}

func concurrencySummaryRouter(accounts []service.Account, concurrency *service.ConcurrencyService) (*gin.Engine, *concurrencySummaryAdminService) {
	gin.SetMode(gin.TestMode)
	svc := &concurrencySummaryAdminService{stubAdminService: newStubAdminService()}
	svc.accounts = accounts
	h := &AccountHandler{adminService: svc, concurrencyService: concurrency}
	router := gin.New()
	router.GET("/accounts/concurrency-summary", h.GetConcurrencySummary)
	return router, svc
}

func requestConcurrencySummary(router *gin.Engine) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts/concurrency-summary", nil))
	return rec
}

func TestAccountConcurrencySummaryTotalsEveryAccountOnce(t *testing.T) {
	// Cross the account-list page boundary, then repeat an account to ensure
	// neither pagination nor membership in multiple groups duplicates slots.
	accounts := make([]service.Account, 1003)
	cache := &concurrencySummaryCache{counts: make(map[int64]int)}
	for i := range accounts {
		id := int64(i + 1)
		accounts[i] = service.Account{ID: id, Concurrency: 100, GroupIDs: []int64{1, 2}}
		cache.counts[id] = 2
	}
	accounts = append(accounts, accounts[0])
	router, svc := concurrencySummaryRouter(accounts, service.NewConcurrencyService(cache))
	rec := requestConcurrencySummary(router)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload struct {
		Data struct {
			CurrentConcurrency int64 `json:"current_concurrency"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.EqualValues(t, 2006, payload.Data.CurrentConcurrency)
	require.Equal(t, 2, svc.lastListAccounts.calls)
	require.Empty(t, svc.lastListAccounts.platform)
	require.Empty(t, svc.lastListAccounts.accountType)
	require.Empty(t, svc.lastListAccounts.status)
	require.Empty(t, svc.lastListAccounts.search)
	require.Zero(t, svc.lastListAccounts.groupID)
	require.Empty(t, svc.lastListAccounts.privacyMode)
	require.Equal(t, "id", svc.lastListAccounts.sortBy)
	require.Equal(t, "asc", svc.lastListAccounts.sortOrder)
	var queriedIDs []int64
	for _, batch := range cache.calls {
		require.LessOrEqual(t, len(batch), 250)
		queriedIDs = append(queriedIDs, batch...)
	}
	require.Len(t, queriedIDs, 1003)
	deadline, ok := svc.ctx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now(), deadline, 10*time.Second)
}

func TestAccountConcurrencySummaryEmptyScopeReturnsZero(t *testing.T) {
	// An empty visible scope has a known zero even without a concurrency cache.
	router, _ := concurrencySummaryRouter(nil, nil)
	rec := requestConcurrencySummary(router)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"code":0,"message":"success","data":{"current_concurrency":0}}`, rec.Body.String())
}

func TestAccountConcurrencySummaryUnavailableNeverReturnsZero(t *testing.T) {
	for _, tc := range []struct {
		name        string
		concurrency *service.ConcurrencyService
	}{
		{"missing service", nil},
		{"missing cache", service.NewConcurrencyService(nil)},
		{"redis error", service.NewConcurrencyService(&concurrencySummaryCache{err: errors.New("redis offline")})},
		{"incomplete counts", service.NewConcurrencyService(&concurrencySummaryCache{counts: map[int64]int{1: 0}})},
		{"invalid counts", service.NewConcurrencyService(&concurrencySummaryCache{counts: map[int64]int{1: 0, 2: -1}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, _ := concurrencySummaryRouter([]service.Account{{ID: 1}, {ID: 2}}, tc.concurrency)
			rec := requestConcurrencySummary(router)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "ACCOUNT_CONCURRENCY_UNAVAILABLE")
			require.NotContains(t, rec.Body.String(), "current_concurrency")
		})
	}
}

func TestAccountConcurrencySummaryListErrorReturnsUnavailable(t *testing.T) {
	cache := &concurrencySummaryCache{}
	router, svc := concurrencySummaryRouter(nil, service.NewConcurrencyService(cache))
	svc.err = errors.New("database offline")
	rec := requestConcurrencySummary(router)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "ACCOUNT_CONCURRENCY_UNAVAILABLE")
	require.NotContains(t, rec.Body.String(), "current_concurrency")
	require.Empty(t, cache.calls)
}

func TestAccountConcurrencySummaryRespectsRequestCancellation(t *testing.T) {
	cache := &concurrencySummaryCache{counts: map[int64]int{1: 9}}
	router, svc := concurrencySummaryRouter([]service.Account{{ID: 1}}, service.NewConcurrencyService(cache))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/accounts/concurrency-summary", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.ErrorIs(t, svc.ctx.Err(), context.Canceled)
	require.Empty(t, cache.calls)
}
