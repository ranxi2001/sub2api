//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserLiveLoadRegionalPipelineLaterErrorNeverMeasuredZero(t *testing.T) {
	for _, excluded := range []bool{false, true} {
		for _, kind := range []string{"ordinary", "live", "wait", "invalid-wait-number"} {
			t.Run(fmt.Sprintf("excluded=%t/%s", excluded, kind), func(t *testing.T) {
				ctx := context.Background()
				c := userLiveLoadRegionalCache(t, false)
				base, ok := c.ConcurrencyCache.(*concurrencyCache)
				require.True(t, ok)
				users := []service.UserWithConcurrency{{ID: 4101}, {ID: 4102}}
				exclusions := map[int64][]string{}
				if excluded {
					exclusions[4102] = []string{"transferred"}
				}
				key := userSlotKey(4102)
				switch kind {
				case "live":
					key = liveUserSlotKey(4102)
				case "wait", "invalid-wait-number":
					key = waitQueueKeyPrefix + "4102"
				}
				if kind == "wait" {
					require.NoError(t, base.rdb.HSet(ctx, key, "field", "wrong-type").Err())
				} else {
					require.NoError(t, base.rdb.Set(ctx, key, "wrong-type-or-number", 0).Err())
				}
				// U1's absent waiting GET is the pipeline's first error (redis.Nil).
				// Later U2 pruning/count/EVAL/live/GET errors must still invalidate it.
				loads, err := base.getUsersLoadBatchExcluding(ctx, users, exclusions)
				require.Error(t, err)
				require.Nil(t, loads)
				loads, err = c.GetUsersLoadBatch(ctx, users)
				require.Error(t, err)
				require.Nil(t, loads)
				require.NoError(t, base.rdb.Del(ctx, key).Err())
				loads, err = base.getUsersLoadBatchExcluding(ctx, users, exclusions)
				require.NoError(t, err)
				require.Zero(t, loads[4101].CurrentConcurrency)
				require.Zero(t, loads[4102].CurrentConcurrency)
				// Corrupting reads does not add any unlimited-user admission check.
				slot, err := service.NewConcurrencyService(c).AcquireUserSlot(ctx, 4101, 0)
				require.NoError(t, err)
				require.True(t, slot.Acquired)
				slot.ReleaseFunc()
			})
		}
	}
}
