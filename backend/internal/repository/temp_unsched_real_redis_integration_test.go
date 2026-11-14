//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type tempUnschedPersistedRepo struct {
	service.AccountRepository
	writes atomic.Int64
}

func (r *tempUnschedPersistedRepo) SetModelRateLimit(ctx context.Context, id int64, model string, until time.Time, reason ...string) error {
	if err := r.AccountRepository.SetModelRateLimit(ctx, id, model, until, reason...); err != nil {
		return err
	}
	r.writes.Add(1)
	return nil
}

type tempUnschedResetCall struct{ key, epoch string }

type tempUnschedDelayedReset struct {
	service.TempUnschedTriggerCounter
	entered chan tempUnschedResetCall
	release <-chan struct{}
}

func (c *tempUnschedDelayedReset) Reset(ctx context.Context, key, epoch string) error {
	c.entered <- tempUnschedResetCall{key, epoch}
	select {
	case <-c.release:
		return c.TempUnschedTriggerCounter.Reset(ctx, key, epoch)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Independent service/counter/repository instances share real Redis and real
// PostgreSQL. The barrier is after A's durable write, immediately before its
// Reset; B completes the same round and starts another before A is released.
func TestTempUnschedRealRedisCrossInstancePersistAndDelayedReset(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{
		Name: "fictional-temp-unsched-interleave", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "fictional-key", "temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{
				"error_code": 502, "keywords": []any{"bad gateway"}, "scope": "model",
				"duration_minutes": 5, "trigger_count": 2, "trigger_window_minutes": 5,
			}},
		},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
	})
	optionsA, optionsB := *integrationRedis.Options(), *integrationRedis.Options()
	clientA, clientB := redis.NewClient(&optionsA), redis.NewClient(&optionsB)
	t.Cleanup(func() { _ = clientA.Close(); _ = clientB.Close() })
	require.NoError(t, clientA.Ping(ctx).Err())
	require.NoError(t, clientB.Ping(ctx).Err())
	repoA := &tempUnschedPersistedRepo{AccountRepository: NewAccountRepository(integrationEntClient, integrationDB, nil)}
	repoB := &tempUnschedPersistedRepo{AccountRepository: NewAccountRepository(integrationEntClient, integrationDB, nil)}
	account, err := repoA.GetByID(ctx, account.ID)
	require.NoError(t, err)
	ratesA := service.NewRateLimitService(repoA, nil, &config.Config{}, nil, nil)
	ratesB := service.NewRateLimitService(repoB, nil, &config.Config{}, nil, nil)
	release := make(chan struct{})
	defer close(release)
	barrier := &tempUnschedDelayedReset{TempUnschedTriggerCounter: NewTempUnschedTriggerCounter(clientA), entered: make(chan tempUnschedResetCall, 1), release: release}
	ratesA.SetTempUnschedTriggerCounter(barrier)
	ratesB.SetTempUnschedTriggerCounter(NewTempUnschedTriggerCounter(clientB))
	body := []byte(`{"error":{"message":"bad gateway"}}`)
	require.False(t, ratesA.HandleUpstreamError(ctx, account, http.StatusBadGateway, nil, body, "gpt-5.1"))
	finishedA := make(chan bool, 1)
	go func() {
		finishedA <- ratesA.HandleUpstreamError(ctx, account, http.StatusBadGateway, nil, body, "gpt-5.1")
	}()
	var old tempUnschedResetCall
	select {
	case old = <-barrier.entered:
	case <-ctx.Done():
		t.Fatal("A did not reach Reset after its threshold/persistence boundary")
	}
	t.Cleanup(func() { _ = clientB.Del(context.Background(), old.key, old.key+":epoch").Err() })
	require.EqualValues(t, 1, repoA.writes.Load(), "A must durably persist before the Reset barrier")
	var reason, status string
	var schedulable bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra->'model_rate_limits'->'gpt-5.1'->>'reason',status,schedulable FROM accounts WHERE id=$1`, account.ID).Scan(&reason, &status, &schedulable))
	var state map[string]any
	require.NoError(t, json.Unmarshal([]byte(reason), &state))
	require.Equal(t, float64(2), state["trigger_count"])
	require.Equal(t, service.StatusActive, status)
	require.True(t, schedulable)
	// Another already in-flight failure completes the old epoch and deletes it.
	require.True(t, ratesB.HandleUpstreamError(ctx, account, http.StatusBadGateway, nil, body, "gpt-5.1"))
	require.EqualValues(t, 1, repoB.writes.Load())
	// A later in-flight failure begins a new epoch while A is still paused.
	require.False(t, ratesB.HandleUpstreamError(ctx, account, http.StatusBadGateway, nil, body, "gpt-5.1"))
	newEpoch, err := clientB.Get(ctx, old.key+":epoch").Result()
	require.NoError(t, err)
	require.NotEqual(t, old.epoch, newEpoch)
	require.Equal(t, "1", clientB.Get(ctx, old.key).Val())
	// Release only A's old Reset; it must leave B's new round intact.
	release <- struct{}{}
	select {
	case triggered := <-finishedA:
		require.True(t, triggered)
	case <-ctx.Done():
		t.Fatal("A's delayed Reset did not finish")
	}
	require.Equal(t, "1", clientB.Get(ctx, old.key).Val(), "old Reset must not delete a new Redis round")
	require.Equal(t, newEpoch, clientB.Get(ctx, old.key+":epoch").Val())
	require.True(t, ratesB.HandleUpstreamError(ctx, account, http.StatusBadGateway, nil, body, "gpt-5.1"))
	require.EqualValues(t, 2, repoB.writes.Load(), "the retained new round must reach its real persistence threshold")
	require.EqualValues(t, 0, clientB.Exists(ctx, old.key, old.key+":epoch").Val())
	t.Log("real Redis: two independent clients/services; real PostgreSQL threshold writes; old Reset cannot erase the new epoch")
}
