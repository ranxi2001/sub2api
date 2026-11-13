//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestTempUnschedTriggerCounterWindowAndReset(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetTime(time.Unix(1_700_000_000, 0))
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	counter := NewTempUnschedTriggerCounter(client)
	require.NotNil(t, counter)
	ctx := context.Background()

	key := "temp_unsched_trigger:7:0:account"
	first, epoch, err := counter.Increment(ctx, key, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), first)
	require.NotEmpty(t, epoch)
	second, sameEpoch, err := counter.Increment(ctx, key, 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), second)
	require.Equal(t, epoch, sameEpoch)

	require.NoError(t, counter.Reset(ctx, key, epoch))
	restarted, restartedEpoch, err := counter.Increment(ctx, key, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), restarted)
	require.NotEqual(t, epoch, restartedEpoch)

	server.FastForward(3 * time.Minute)
	expired, _, err := counter.Increment(ctx, key, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), expired, "the fixed window expires and the next match starts a new count")
}

func TestTempUnschedTriggerCounterDelayedResetKeepsNewRound(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	counter := NewTempUnschedTriggerCounter(client)
	ctx := context.Background()
	key := "temp_unsched_trigger:9:1:model:gpt-5.4"

	_, oldEpoch, err := counter.Increment(ctx, key, 5)
	require.NoError(t, err)
	count, sameEpoch, err := counter.Increment(ctx, key, 5)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	require.Equal(t, oldEpoch, sameEpoch)

	require.NoError(t, counter.Reset(ctx, key, oldEpoch))
	next, newEpoch, err := counter.Increment(ctx, key, 5)
	require.NoError(t, err)
	require.Equal(t, int64(1), next)
	require.NotEqual(t, oldEpoch, newEpoch)

	require.NoError(t, counter.Reset(ctx, key, oldEpoch))
	afterStaleReset, _, err := counter.Increment(ctx, key, 5)
	require.NoError(t, err)
	require.Equal(t, int64(2), afterStaleReset, "a delayed reset from the previous round must not delete the new count")
}

func TestNewTempUnschedTriggerCounterNilRedis(t *testing.T) {
	require.Nil(t, NewTempUnschedTriggerCounter(nil))
}
