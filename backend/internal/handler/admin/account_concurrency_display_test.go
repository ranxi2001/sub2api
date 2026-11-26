//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountConcurrencyDisplayPreservesUnknownAndZero(t *testing.T) {
	for _, lite := range []string{"0", "1"} {
		t.Run("lite="+lite, func(t *testing.T) {
			cache := &accountSortConcurrencyCache{counts: map[int64]int{1: 1, 2: 2, 3: 0}}
			accounts := []service.Account{
				{ID: 1, Platform: "openai", Type: "oauth", Concurrency: 3},
				{ID: 2, Platform: "openai", Type: "apikey", Concurrency: 7},
				{ID: 3, Platform: "anthropic", Type: "setup-token", Concurrency: 0},
			}
			router, _ := accountConcurrencySortRouter(accounts, cache)
			read := func(etag string) (map[int64]*int, *httptest.ResponseRecorder) {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, "/accounts?lite="+lite, nil)
				req.Header.Set("If-None-Match", etag)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				out := map[int64]*int{}
				if rec.Code == http.StatusOK {
					var payload struct {
						Data struct {
							Items []struct {
								ID      int64 `json:"id"`
								Current *int  `json:"current_concurrency"`
							} `json:"items"`
						} `json:"data"`
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
					for _, item := range payload.Data.Items {
						out[item.ID] = item.Current
					}
				}
				return out, rec
			}
			counts, rec := read("")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, 1, *counts[1])
			require.Equal(t, 2, *counts[2])
			require.Equal(t, 0, *counts[3])
			etag := rec.Header().Get("ETag")
			_, rec = read(etag)
			require.Equal(t, http.StatusNotModified, rec.Code)

			cache.err = errors.New("fictional Redis failure")
			counts, rec = read(etag)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, counts, 3)
			for _, count := range counts {
				require.Nil(t, count)
			}
			require.Contains(t, rec.Body.String(), `"current_concurrency":null`)
			require.NotEqual(t, etag, rec.Header().Get("ETag"))

			// An incomplete or invalid observation is also unknown, not idle.
			cache.err = nil
			delete(cache.counts, 2)
			counts, rec = read("")
			require.Equal(t, http.StatusOK, rec.Code)
			require.Nil(t, counts[2])
			cache.counts[2] = -1
			counts, _ = read("")
			require.Nil(t, counts[2])
			cache.counts[2] = 0
			counts, rec = read(rec.Header().Get("ETag"))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 0, *counts[2])
		})
	}
}

func TestAccountConcurrencyDisplayDetailAndMissingCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, concurrency := range []*service.ConcurrencyService{nil, service.NewConcurrencyService(nil)} {
		svc := newStubAdminService()
		svc.accounts = []service.Account{{ID: 1}}
		h := &AccountHandler{adminService: svc, concurrencyService: concurrency}
		item := h.buildAccountResponseWithRuntime(context.Background(), &service.Account{ID: 1})
		require.Nil(t, item.CurrentConcurrency)
		router := gin.New()
		router.GET("/accounts", h.List)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts?lite=1", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"current_concurrency":null`)
	}
	cache := &accountSortConcurrencyCache{counts: map[int64]int{1: 0}}
	h := &AccountHandler{concurrencyService: service.NewConcurrencyService(cache)}
	item := h.buildAccountResponseWithRuntime(context.Background(), &service.Account{ID: 1})
	// Zero remains a successful observation even in single-account edit responses.
	require.NotNil(t, item.CurrentConcurrency)
	require.Zero(t, *item.CurrentConcurrency)
	cache.err = errors.New("fictional Redis failure")
	item = h.buildAccountResponseWithRuntime(context.Background(), &service.Account{ID: 1})
	// Keep the detail response's serialization observable as well.
	data, err := json.Marshal(item)
	require.NoError(t, err)
	require.Contains(t, string(data), `"current_concurrency":null`)
}
