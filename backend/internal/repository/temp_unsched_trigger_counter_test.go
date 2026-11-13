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

	first, err := counter.Increment(ctx, "temp_unsched_trigger:7:0:account", 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), first)
	second, err := counter.Increment(ctx, "temp_unsched_trigger:7:0:account", 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), second)

	require.NoError(t, counter.Reset(ctx, "temp_unsched_trigger:7:0:account"))
	restarted, err := counter.Increment(ctx, "temp_unsched_trigger:7:0:account", 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), restarted)

	server.FastForward(3 * time.Minute)
	expired, err := counter.Increment(ctx, "temp_unsched_trigger:7:0:account", 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), expired, "the fixed window expires and the next match starts a new count")
}

func TestNewTempUnschedTriggerCounterNilRedis(t *testing.T) {
	require.Nil(t, NewTempUnschedTriggerCounter(nil))
}
