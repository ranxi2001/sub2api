//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type userLiveLoadCache struct {
	testutil.StubConcurrencyCache
	loads map[int64]*service.UserLoadInfo
	err   error
}

func (s *userLiveLoadCache) GetUsersLoadBatch(ctx context.Context, _ []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	if err := ctx.Err(); err != nil {
		return s.loads, err
	}
	return s.loads, s.err
}

func TestUserLiveLoadHandlerSerialization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name        string
		concurrency *service.ConcurrencyService
		available   bool
		count       int
		expired     bool
	}{
		{name: "missing-service"},
		{name: "missing-cache", concurrency: service.NewConcurrencyService(nil)},
		{name: "read-error", concurrency: service.NewConcurrencyService(&userLiveLoadCache{err: errors.New("unavailable")})},
		{name: "partial-error", concurrency: service.NewConcurrencyService(&userLiveLoadCache{loads: map[int64]*service.UserLoadInfo{42: {CurrentConcurrency: 9}}, err: errors.New("partial")})},
		{name: "timeout", concurrency: service.NewConcurrencyService(&userLiveLoadCache{}), expired: true},
		{name: "missing-user", concurrency: service.NewConcurrencyService(&userLiveLoadCache{})},
		{name: "real-zero", concurrency: service.NewConcurrencyService(&userLiveLoadCache{loads: map[int64]*service.UserLoadInfo{42: {}}}), available: true},
		{name: "active", concurrency: service.NewConcurrencyService(&userLiveLoadCache{loads: map[int64]*service.UserLoadInfo{42: {CurrentConcurrency: 3}}}), available: true, count: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admin := newStubAdminService()
			rpm := 0
			admin.users = []service.User{{ID: 42, Email: "fixture@example.test", Role: service.RoleUser, Status: service.StatusActive, CurrentRPM: &rpm}}
			h := NewUserHandler(admin, tc.concurrency, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil)
			if tc.expired {
				ctx, cancel := context.WithDeadline(req.Context(), time.Now().Add(-time.Second))
				defer cancel()
				req = req.WithContext(ctx)
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req
			h.List(c)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var payload struct {
				Data struct {
					Items []struct {
						CurrentConcurrency   int  `json:"current_concurrency"`
						ConcurrencyAvailable bool `json:"concurrency_available"`
					} `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
			require.Len(t, payload.Data.Items, 1)
			require.Equal(t, tc.available, payload.Data.Items[0].ConcurrencyAvailable)
			require.Equal(t, tc.count, payload.Data.Items[0].CurrentConcurrency)
			// Remote CI feeds these actual handler responses to the real Vue view.
			if dir := os.Getenv("USER_LIVE_LOAD_CONTRACT_DIR"); dir != "" {
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, tc.name+".json"), rec.Body.Bytes(), 0644))
			}
		})
	}
}
