//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountSortConcurrencyCache struct {
	testutil.StubConcurrencyCache
	counts map[int64]int
	calls  [][]int64
	err    error
}

func (s *accountSortConcurrencyCache) GetAccountConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	s.calls = append(s.calls, append([]int64(nil), ids...))
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		if count, ok := s.counts[id]; ok {
			out[id] = count
		}
	}
	return out, nil
}

func accountConcurrencySortRouter(accounts []service.Account, cache *accountSortConcurrencyCache) (*gin.Engine, *stubAdminService) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	svc.accounts = accounts
	handler := &AccountHandler{adminService: svc, concurrencyService: service.NewConcurrencyService(cache)}
	router := gin.New()
	router.GET("/accounts", handler.List)
	router.GET("/billing-rates", handler.GetUpstreamBillingRates)
	return router, svc
}

type accountSortPage struct {
	Data struct {
		Items []struct {
			ID                 int64 `json:"id"`
			CurrentConcurrency int   `json:"current_concurrency"`
		} `json:"items"`
		Total int64 `json:"total"`
		Pages int   `json:"pages"`
	} `json:"data"`
}

func readAccountSortPage(t *testing.T, router *gin.Engine, query, etag string) (accountSortPage, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/accounts?sort_by=current_concurrency&"+query, nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var page accountSortPage
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	}
	return page, rec
}

func TestAccountCurrentConcurrencySortAcrossPages(t *testing.T) {
	accounts := []service.Account{
		{ID: 1, Concurrency: 2}, // 100% occupied must still follow 9/100 in descending order.
		{ID: 2, Concurrency: 100},
		{ID: 3, Concurrency: 1000}, // A larger ceiling must not outrank an active account.
		{ID: 4, Concurrency: 1},
		{ID: 5, Concurrency: 50},
	}
	for _, lite := range []string{"0", "1"} {
		for _, test := range []struct {
			order string
			ids   []int64
		}{
			{"asc", []int64{3, 4, 1, 5, 2}},
			{"desc", []int64{2, 5, 1, 4, 3}},
		} {
			t.Run(lite+"/"+test.order, func(t *testing.T) {
				cache := &accountSortConcurrencyCache{counts: map[int64]int{1: 2, 2: 9, 3: 0, 4: 0, 5: 2}}
				router, _ := accountConcurrencySortRouter(accounts, cache)
				var ids []int64
				for p := 1; p <= 4; p++ {
					page, rec := readAccountSortPage(t, router, fmt.Sprintf("sort_order=%s&page=%d&page_size=2&lite=%s", test.order, p, lite), "")
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.EqualValues(t, 5, page.Data.Total)
					require.Equal(t, 3, page.Data.Pages)
					for _, item := range page.Data.Items {
						ids = append(ids, item.ID)
						require.Equal(t, cache.counts[item.ID], item.CurrentConcurrency)
					}
				}
				require.Equal(t, test.ids, ids)
				// One full snapshot per HTTP request; never reread the visible page for display.
				require.Len(t, cache.calls, 4)
			})
		}
	}
}

func TestAccountCurrentConcurrencySortRefreshUsesNewSnapshot(t *testing.T) {
	cache := &accountSortConcurrencyCache{counts: map[int64]int{1: 2, 2: 9}}
	router, _ := accountConcurrencySortRouter([]service.Account{{ID: 1}, {ID: 2}}, cache)
	page, rec := readAccountSortPage(t, router, "sort_order=desc&page_size=1&lite=1", "")
	require.Equal(t, int64(2), page.Data.Items[0].ID)
	etag := rec.Header().Get("ETag")
	_, same := readAccountSortPage(t, router, "sort_order=desc&page_size=1&lite=1", etag)
	require.Equal(t, http.StatusNotModified, same.Code)
	cache.counts[1] = 10
	page, refreshed := readAccountSortPage(t, router, "sort_order=desc&page_size=1&lite=1", etag)
	require.Equal(t, http.StatusOK, refreshed.Code)
	require.NotEqual(t, etag, refreshed.Header().Get("ETag"))
	require.Equal(t, int64(1), page.Data.Items[0].ID)
	require.Equal(t, 10, page.Data.Items[0].CurrentConcurrency)
}

func TestAccountCurrentConcurrencySortBillingRefreshPreservesPage(t *testing.T) {
	cache := &accountSortConcurrencyCache{counts: map[int64]int{1: 2, 2: 9}}
	router, _ := accountConcurrencySortRouter([]service.Account{{ID: 1}, {ID: 2}}, cache)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/billing-rates?sort_by=current_concurrency&sort_order=desc&page_size=1", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []struct {
				AccountID int64 `json:"account_id"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, int64(2), payload.Data.Items[0].AccountID)
}

func TestAccountCurrentConcurrencySortReadsAllFilteredPagesInBoundedBatches(t *testing.T) {
	accounts := make([]service.Account, 1003)
	cache := &accountSortConcurrencyCache{counts: map[int64]int{}}
	for i := range accounts {
		accounts[i] = service.Account{ID: int64(i + 1)}
		cache.counts[int64(i+1)] = i
	}
	router, svc := accountConcurrencySortRouter(accounts, cache)
	page, rec := readAccountSortPage(t, router, "sort_order=desc&page_size=2&platform=openai&type=oauth&status=active&search=fixture&group=12&privacy_mode=training_off", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, int64(1003), page.Data.Items[0].ID)
	require.Equal(t, int64(1002), page.Data.Items[1].ID)
	require.EqualValues(t, 1003, page.Data.Total)
	require.Equal(t, 2, svc.lastListAccounts.calls)
	require.Equal(t, "openai", svc.lastListAccounts.platform)
	require.Equal(t, "oauth", svc.lastListAccounts.accountType)
	require.Equal(t, "active", svc.lastListAccounts.status)
	require.Equal(t, "fixture", svc.lastListAccounts.search)
	require.Equal(t, int64(12), svc.lastListAccounts.groupID)
	require.Equal(t, "training_off", svc.lastListAccounts.privacyMode)
	require.Equal(t, "id", svc.lastListAccounts.sortBy)
	require.Equal(t, "asc", svc.lastListAccounts.sortOrder)
	require.Len(t, cache.calls, 5)
	for _, batch := range cache.calls {
		require.LessOrEqual(t, len(batch), 250)
	}
}

func TestAccountCurrentConcurrencySortUnknownSnapshotFailsExplicitly(t *testing.T) {
	for _, test := range []struct {
		name   string
		counts map[int64]int
		err    error
	}{
		{"cache error", nil, errors.New("offline")},
		{"missing count", map[int64]int{1: 0}, nil},
		{"negative count", map[int64]int{1: 0, 2: -1}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, _ := accountConcurrencySortRouter([]service.Account{{ID: 1}, {ID: 2}}, &accountSortConcurrencyCache{counts: test.counts, err: test.err})
			_, rec := readAccountSortPage(t, router, "sort_order=asc", "")
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			require.Contains(t, rec.Body.String(), "ACCOUNT_CONCURRENCY_UNAVAILABLE")
			require.NotContains(t, rec.Body.String(), "current_concurrency")
		})
	}
	for _, concurrency := range []*service.ConcurrencyService{nil, service.NewConcurrencyService(nil)} {
		svc := newStubAdminService()
		handler := &AccountHandler{adminService: svc, concurrencyService: concurrency}
		router := gin.New()
		router.GET("/accounts", handler.List)
		_, rec := readAccountSortPage(t, router, "sort_order=desc", "")
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	}
}

func TestAccountCurrentConcurrencySortEmptyResults(t *testing.T) {
	cache := &accountSortConcurrencyCache{}
	router, _ := accountConcurrencySortRouter([]service.Account{}, cache)
	page, rec := readAccountSortPage(t, router, "sort_order=desc&page=2", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, page.Data.Items)
	require.Zero(t, page.Data.Total)
	require.Empty(t, cache.calls)
}
