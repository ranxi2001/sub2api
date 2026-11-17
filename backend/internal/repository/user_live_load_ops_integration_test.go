//go:build integration

package repository_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type userLiveOpsUsers struct {
	service.UserRepository
	users []service.User
}

func (r *userLiveOpsUsers) ListWithFilters(context.Context, pagination.PaginationParams, service.UserListFilters) ([]service.User, *pagination.PaginationResult, error) {
	return r.users, &pagination.PaginationResult{Total: int64(len(r.users))}, nil
}

type userLiveOpsPartialCache struct {
	service.ConcurrencyCache
	err error
}

func (c *userLiveOpsPartialCache) GetUsersLoadBatch(ctx context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	loads, err := c.ConcurrencyCache.GetUsersLoadBatch(ctx, users)
	if err != nil {
		return nil, err
	}
	delete(loads, users[0].ID)
	return loads, c.err
}

func TestUserLiveLoadRegionalOpsHandlerNeverPublishesUnknownAsIdle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, local := range []bool{false, true} {
		for _, mode := range []string{"expired", "missing", "changed", "partial", "partial-error", "zero", "active"} {
			t.Run(fmt.Sprintf("local=%t/%s", local, mode), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				c := repository.NewUserLiveLoadTestCache(t, local)
				r, slot := repository.NewUserLiveLoadTestRequest(t, c)
				defer slot.ReleaseFunc()
				other, err := service.NewConcurrencyService(c).AcquireUserSlot(ctx, r.UserID+1, 0)
				require.NoError(t, err)
				defer other.ReleaseFunc()
				if mode == "expired" || mode == "missing" || mode == "changed" {
					ok, err := c.AcquireLiveLeaseTransferring(ctx, r)
					require.NoError(t, err)
					require.True(t, ok)
					slot.ReleaseFunc()
					if mode == "expired" {
						_, err = c.Database().Exec(`UPDATE regional_live_handoffs SET identity=jsonb_set(identity,
'{_user_stats_expires_at}',to_jsonb(extract(epoch FROM clock_timestamp())-1)) WHERE region_id=$1 AND lease_id=$2`, c.Region(), r.LeaseID)
					} else if mode == "missing" {
						_, err = c.Database().Exec(`UPDATE regional_live_handoffs SET identity=identity-'_user_stats_expires_at'
WHERE region_id=$1 AND lease_id=$2`, c.Region(), r.LeaseID)
					}
					require.NoError(t, err)
				}
				var cache service.ConcurrencyCache = c
				if mode == "partial" || mode == "partial-error" {
					partial := &userLiveOpsPartialCache{ConcurrencyCache: c}
					if mode == "partial-error" {
						partial.err = errors.New("partial statistics unavailable")
					}
					cache = partial
				}
				if mode == "zero" {
					slot.ReleaseFunc()
					other.ReleaseFunc()
				}
				users := &userLiveOpsUsers{users: []service.User{{ID: r.UserID, Status: service.StatusActive}, {ID: r.UserID + 1, Status: service.StatusActive}}}
				ops := service.NewOpsService(nil, nil, nil, nil, users, service.NewConcurrencyService(cache), nil, nil, nil, nil, nil)
				h := adminhandler.NewOpsHandler(ops)
				router := gin.New()
				router.GET("/user-concurrency", h.GetUserConcurrencyStats)
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, "/user-concurrency", nil).WithContext(ctx)
				if mode == "changed" {
					started, resume := c.PauseNextPipeline(t)
					done := make(chan struct{})
					go func() { router.ServeHTTP(recorder, request); close(done) }()
					select {
					case <-started:
					case <-ctx.Done():
						t.Fatal("Ops handler did not reach the real Redis pipeline")
					}
					next := r
					next.AccountID++
					next.ReplacedAccountID = r.AccountID
					ok, err := c.MigrateLiveLeaseAccount(ctx, next)
					require.NoError(t, err)
					require.True(t, ok)
					close(resume)
					<-done
				} else {
					router.ServeHTTP(recorder, request)
				}
				if mode == "zero" || mode == "active" {
					require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
					require.Contains(t, recorder.Body.String(), `"enabled":true`)
					require.Contains(t, recorder.Body.String(), `"timestamp"`)
					if mode == "zero" {
						require.Contains(t, recorder.Body.String(), `"user":{}`)
					} else {
						require.Contains(t, recorder.Body.String(), `"current_in_use":1`)
					}
				} else {
					require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
					require.Contains(t, recorder.Body.String(), "OPS_USER_CONCURRENCY_UNAVAILABLE")
					require.NotContains(t, recorder.Body.String(), `"timestamp"`)
					require.NotContains(t, recorder.Body.String(), `"enabled":true`)
				}
			})
		}
	}
}
