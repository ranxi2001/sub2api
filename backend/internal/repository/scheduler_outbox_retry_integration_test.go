//go:build integration

package repository

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func newOutboxRetryPostgres(t *testing.T) (*sql.DB, *schedulerOutboxRepository) {
	t.Helper()
	ctx := context.Background()
	dsn, err := url.Parse(integrationPostgresDSN)
	require.NoError(t, err)
	name := "outbox_retry_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pq.QuoteIdentifier(name)
	_, err = integrationDB.ExecContext(ctx, "CREATE DATABASE "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), "DROP DATABASE "+quoted)
		require.NoError(t, err)
	})
	dsn.Path = "/" + name
	db, err := sql.Open("postgres", dsn.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	for _, filename := range []string{"036_scheduler_outbox.sql", "152_scheduler_outbox_dedup_key.sql", "334_scheduler_outbox_retries.sql"} {
		ddl, err := migrations.FS.ReadFile(filename)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, string(ddl))
		require.NoError(t, err)
	}
	return db, &schedulerOutboxRepository{db: db}
}

func seedOutboxRetry(t *testing.T, db *sql.DB, repo *schedulerOutboxRepository) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`INSERT INTO scheduler_outbox(event_type,account_id,group_id,payload,created_at)
		VALUES('account_changed',101,202,'{"group_ids":[202],"fixture":"original"}',NOW()-INTERVAL '1 hour') RETURNING id`).Scan(&id))
	require.NoError(t, repo.DeferOutboxEvent(context.Background(), id))
	_, err := db.Exec(`UPDATE scheduler_outbox_retries SET next_retry_at=NOW()-INTERVAL '1 second' WHERE event_id=$1`, id)
	require.NoError(t, err)
	return id
}

func TestSchedulerOutboxRetryPostgresPersistsOriginalBeforeCleanupAndRestart(t *testing.T) {
	db, repo := newOutboxRetryPostgres(t)
	id := seedOutboxRetry(t, db, repo)
	// Repeat a deferral after an ambiguous main checkpoint. It must preserve the
	// first event snapshot and retry state, even if the source is later changed.
	_, err := db.Exec(`UPDATE scheduler_outbox SET payload='{"fixture":"changed"}' WHERE id=$1`, id)
	require.NoError(t, err)
	require.NoError(t, repo.DeferOutboxEvent(context.Background(), id))
	deleted, err := repo.DeleteConsumedUpTo(context.Background(), id, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)

	restarted := &schedulerOutboxRepository{db: db}
	retry, err := restarted.ClaimOutboxRetry(context.Background(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.Equal(t, id, retry.Event.ID)
	require.Equal(t, service.SchedulerOutboxEventAccountChanged, retry.Event.EventType)
	require.EqualValues(t, 101, *retry.Event.AccountID)
	require.EqualValues(t, 202, *retry.Event.GroupID)
	require.Equal(t, "original", retry.Event.Payload["fixture"])
	require.EqualValues(t, 1, retry.Attempts)
	require.NoError(t, restarted.CompleteOutboxRetry(context.Background(), id, retry.Generation))
	next, err := restarted.ClaimOutboxRetry(context.Background(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, next)

	// A missing original must never be reported as durably copied.
	require.ErrorIs(t, repo.DeferOutboxEvent(context.Background(), id+100), sql.ErrNoRows)
}

func TestSchedulerOutboxRetryPostgresFencesExpiredOwnersAndHonorsBackoff(t *testing.T) {
	db, repo := newOutboxRetryPostgres(t)
	id := seedOutboxRetry(t, db, repo)
	first, err := repo.ClaimOutboxRetry(context.Background(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, first)
	_, err = db.Exec(`UPDATE scheduler_outbox_retries SET lease_until=NOW()-INTERVAL '1 second' WHERE event_id=$1`, id)
	require.NoError(t, err)
	require.Error(t, repo.CompleteOutboxRetry(context.Background(), id, first.Generation), "expiry alone invalidates ownership")
	require.Error(t, repo.RescheduleOutboxRetry(context.Background(), id, first.Generation, time.Second))

	second, err := (&schedulerOutboxRepository{db: db}).ClaimOutboxRetry(context.Background(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Greater(t, second.Generation, first.Generation)
	require.EqualValues(t, 2, second.Attempts)
	require.Error(t, repo.CompleteOutboxRetry(context.Background(), id, first.Generation))
	require.Error(t, repo.RescheduleOutboxRetry(context.Background(), id, first.Generation, time.Hour))
	require.NoError(t, repo.RescheduleOutboxRetry(context.Background(), id, second.Generation, time.Hour))
	next, err := repo.ClaimOutboxRetry(context.Background(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, next, "backoff must be enforced by the durable database schedule")

	var attempts int
	var generation int64
	require.NoError(t, db.QueryRow(`SELECT attempts,lease_generation FROM scheduler_outbox_retries WHERE event_id=$1`, id).Scan(&attempts, &generation))
	require.Equal(t, 2, attempts)
	require.Equal(t, second.Generation, generation)
}

func TestSchedulerOutboxRetryPostgresConcurrentClaimsHaveOneOwner(t *testing.T) {
	db, repo := newOutboxRetryPostgres(t)
	seedOutboxRetry(t, db, repo)
	const workers = 8
	var wg sync.WaitGroup
	results := make(chan *service.SchedulerOutboxRetry, workers)
	errors := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			retry, err := (&schedulerOutboxRepository{db: db}).ClaimOutboxRetry(context.Background(), time.Minute)
			results <- retry
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	claimed := 0
	for err := range errors {
		require.NoError(t, err)
	}
	for retry := range results {
		if retry != nil {
			claimed++
		}
	}
	require.Equal(t, 1, claimed)
}

func TestSchedulerOutboxRetryPostgresGenerationDoesNotRepeatAfterDeleteAndReinsert(t *testing.T) {
	db, repo := newOutboxRetryPostgres(t)
	id := seedOutboxRetry(t, db, repo)
	old, err := repo.ClaimOutboxRetry(context.Background(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, old)
	_, err = db.Exec(`UPDATE scheduler_outbox_retries SET lease_until=NOW()-INTERVAL '1 second' WHERE event_id=$1`, id)
	require.NoError(t, err)
	successor, err := repo.ClaimOutboxRetry(context.Background(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, successor)
	require.NoError(t, repo.CompleteOutboxRetry(context.Background(), id, successor.Generation))

	// Main watermark persistence may have failed after the first durable copy.
	// A replay may insert the same original event ID again after completion.
	require.NoError(t, repo.DeferOutboxEvent(context.Background(), id))
	_, err = db.Exec(`UPDATE scheduler_outbox_retries SET next_retry_at=NOW()-INTERVAL '1 second' WHERE event_id=$1`, id)
	require.NoError(t, err)
	current, err := repo.ClaimOutboxRetry(context.Background(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Greater(t, current.Generation, successor.Generation)
	require.Error(t, repo.CompleteOutboxRetry(context.Background(), id, old.Generation))
	require.Error(t, repo.RescheduleOutboxRetry(context.Background(), id, old.Generation, time.Hour))
	require.NoError(t, repo.CompleteOutboxRetry(context.Background(), id, current.Generation))
}
