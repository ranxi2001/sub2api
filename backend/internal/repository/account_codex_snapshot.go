package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func hasCodexWindowUpdates(updates map[string]any) bool {
	for key := range updates {
		if service.IsCodexUsageFieldKey(key) {
			return true
		}
	}
	return false
}

// Full account edits already hold FOR UPDATE in lockAndMergeAccountProbeExtra.
// Preserve the committed usage generation when an unrelated edit carries a
// stale full Extra object. Identity-changing edits retain their invalidation.
func preserveCommittedCodexUsage(incoming, current map[string]any) map[string]any {
	currentVersion, _ := strconv.ParseInt(fmt.Sprint(current[service.CodexUsageObservationVersionKey]), 10, 64)
	incomingVersion, _ := strconv.ParseInt(fmt.Sprint(incoming[service.CodexUsageObservationVersionKey]), 10, 64)
	if currentVersion <= 0 || incomingVersion > currentVersion {
		return incoming
	}
	if incoming == nil {
		incoming = make(map[string]any)
	}
	for key := range incoming {
		if service.IsCodexUsageExtraKey(key) {
			delete(incoming, key)
		}
	}
	for key, value := range current {
		if service.IsCodexUsageExtraKey(key) {
			incoming[key] = value
		}
	}
	return incoming
}

// The observation CAS is in the UPDATE, never in a preceding read. Independent
// gateway processes and independent repository instances share the same fence.
// Display caches are independent: an old usage observation can still refresh
// credits without rewriting its old windows.
func (r *accountRepository) UpdateCodexUsageSnapshot(ctx context.Context, id int64, updates map[string]any) (bool, error) {
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.UpdateCodexUsageSnapshot(ctx, id, updates)
	}
	window, display := make(map[string]any), make(map[string]any)
	for k, v := range stripCodexFingerprintSeedFromExtraUpdate(updates) {
		if service.IsCodexUsageExtraKey(k) {
			window[k] = v
		} else {
			display[k] = v
		}
	}
	if !hasCodexWindowUpdates(window) {
		return true, r.UpdateExtra(ctx, id, display)
	}
	stamp, _ := window["codex_usage_updated_at"].(string)
	observed, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		observed = time.Now()
		window["codex_usage_updated_at"] = observed.UTC().Format(time.RFC3339Nano)
	}
	version := strconv.FormatInt(observed.UnixNano(), 10)
	versions := service.CodexUsageFieldVersions(window, observed)
	delete(window, "codex_usage_updated_at")
	delete(window, service.CodexUsageObservationVersionKey)
	delete(window, service.CodexUsageFieldVersionsKey)
	versionsJSON, err := json.Marshal(versions)
	if err != nil {
		return false, err
	}
	metadataJSON, err := json.Marshal(map[string]any{
		"codex_usage_updated_at":                observed.UTC().Format(time.RFC3339Nano),
		service.CodexUsageObservationVersionKey: version,
	})
	if err != nil {
		return false, err
	}
	windowJSON, err := json.Marshal(window)
	if err != nil {
		return false, err
	}
	displayJSON, err := json.Marshal(display)
	if err != nil {
		return false, err
	}
	client := r.client
	var tx *dbent.Tx
	if dbent.TxFromContext(ctx) == nil {
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return false, err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			client = tx.Client()
		}
	}
	// Every field is compared in this atomic UPDATE against its own committed
	// version. _legacy freezes the pre-field-version fence for existing rows;
	// advancing another window must not advance this fallback. No pre-read CAS.
	rows, err := client.QueryContext(ctx, `UPDATE accounts AS a
 SET extra = (COALESCE(a.extra,'{}'::jsonb) || $1::jsonb) || (
 SELECT COALESCE(jsonb_object_agg(incoming.key, incoming.value),'{}'::jsonb) ||
 jsonb_build_object('codex_usage_field_versions',
   jsonb_build_object('_legacy', COALESCE(a.extra->'codex_usage_field_versions'->'_legacy',
     a.extra->'codex_usage_observed_at_ns', '"0"'::jsonb)) ||
   COALESCE(a.extra->'codex_usage_field_versions','{}'::jsonb) ||
   COALESCE(jsonb_object_agg(incoming.key, $3::jsonb->incoming.key),'{}'::jsonb))
 FROM jsonb_each($2::jsonb) AS incoming
 WHERE (CASE WHEN COALESCE(a.extra->'codex_usage_field_versions'->>incoming.key,
   a.extra->'codex_usage_field_versions'->>'_legacy', a.extra->>'codex_usage_observed_at_ns','0') ~ '^[0-9]{1,19}$'
 THEN COALESCE(a.extra->'codex_usage_field_versions'->>incoming.key,
   a.extra->'codex_usage_field_versions'->>'_legacy', a.extra->>'codex_usage_observed_at_ns','0')::numeric
 ELSE 0 END) <= ($3::jsonb->>incoming.key)::numeric
 ) || CASE WHEN (CASE WHEN a.extra->>'codex_usage_observed_at_ns' ~ '^[0-9]{1,19}$'
 THEN (a.extra->>'codex_usage_observed_at_ns')::numeric ELSE 0 END) <= $4::numeric
 THEN $5::jsonb ELSE '{}'::jsonb END,
 updated_at=NOW()
 WHERE id=$6 AND deleted_at IS NULL
 RETURNING extra->'codex_usage_field_versions' @> $3::jsonb`,
		string(displayJSON), string(windowJSON), string(versionsJSON), version, string(metadataJSON), id)
	if err != nil {
		return false, err
	}
	applied := false
	if !rows.Next() {
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return false, err
		}
		return false, service.ErrAccountNotFound
	}
	if err := rows.Scan(&applied); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if shouldEnqueueSchedulerOutboxForExtraUpdates(display) {
		if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return false, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	if dbent.TxFromContext(ctx) == nil {
		if err := r.publishSchedulerAccountSnapshot(ctx, id); err != nil {
			return applied, fmt.Errorf("codex snapshot cache publication failed: %w", err)
		}
	}
	return applied, nil
}

// Serializes all usage-bearing cache publications across instances, including
// delayed generic updates/rebuilds. The fresh read and Redis write are inside
// the lock; database CAS writes can proceed, but their publication waits and
// republishes the newest committed row before reporting success.
func (r *accountRepository) WithLatestSchedulerAccounts(ctx context.Context, ids []int64, publish func([]service.Account) error) (retErr error) {
	if len(ids) == 0 {
		return publish(nil)
	}
	started := time.Now()
	phase := "begin"
	var lockSQLDuration, readDuration, publishDuration time.Duration
	var lockCompletedAt time.Time
	var lockSQLAttempted, readAttempted, publishAttempted, ownsTransaction bool
	lockCount := 0
	defer func() {
		elapsed := time.Since(started)
		var postLockDuration time.Duration
		if !lockCompletedAt.IsZero() {
			postLockDuration = time.Since(lockCompletedAt)
		}
		level := slog.LevelDebug
		if retErr != nil || elapsed >= time.Second {
			level = slog.LevelWarn
		}
		failureKind := "none"
		if retErr != nil {
			failureKind = "error"
			if errors.Is(retErr, context.DeadlineExceeded) {
				failureKind = "deadline"
			} else if errors.Is(retErr, context.Canceled) {
				failureKind = "canceled"
			}
		}
		// Durations include driver/network work; lock SQL time is not a direct
		// measurement of PostgreSQL lock wait. Never log IDs, rows or raw errors.
		slog.Log(ctx, level, "[Scheduler] account publication timing",
			"phase", phase, "failure_kind", failureKind, "requested_count", len(ids), "lock_count", lockCount,
			"owns_transaction", ownsTransaction, "elapsed_ms", elapsed.Milliseconds(),
			"lock_sql_attempted", lockSQLAttempted, "lock_sql_ms", lockSQLDuration.Milliseconds(),
			"lock_acquired", !lockCompletedAt.IsZero(), "post_lock_ms", postLockDuration.Milliseconds(),
			"read_attempted", readAttempted, "read_ms", readDuration.Milliseconds(),
			"publish_attempted", publishAttempted, "publish_ms", publishDuration.Milliseconds())
	}()
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}
	client := r.client
	if tx != nil {
		ownsTransaction = true
		defer func() { _ = tx.Rollback() }()
		client = tx.Client()
	}
	keys := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			keys = append(keys, int64(uint64(id)^uint64(0x5780000000000000)))
		}
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	lockCount = len(keys)
	phase = "lock"
	lockStarted := time.Now()
	if len(keys) == 1 {
		lockSQLAttempted = true
		if _, err := client.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", keys[0]); err != nil {
			lockSQLDuration = time.Since(lockStarted)
			return err
		}
	} else if len(keys) > 1 {
		lockSQLAttempted = true
		// Keep the account-level publication fence shared with single-account
		// feedback, but acquire it in one database round trip. The ordered
		// subquery feeds the volatile lock function in the same global order as
		// before, including when two buckets overlap on only some accounts.
		if _, err := client.ExecContext(ctx, `SELECT pg_advisory_xact_lock(lock_key)
FROM (SELECT unnest($1::bigint[]) AS lock_key ORDER BY lock_key) AS ordered_locks`, pq.Array(keys)); err != nil {
			lockSQLDuration = time.Since(lockStarted)
			return err
		}
	}
	if lockSQLAttempted {
		lockSQLDuration = time.Since(lockStarted)
		lockCompletedAt = time.Now()
	}
	scoped := *r
	scoped.client = client
	scoped.sql = client
	scoped.schedulerCache = nil
	phase = "read"
	readAttempted = true
	readStarted := time.Now()
	accounts, err := scoped.GetByIDs(ctx, ids)
	readDuration = time.Since(readStarted)
	if err != nil {
		return err
	}
	values := make([]service.Account, 0, len(accounts))
	for _, a := range accounts {
		values = append(values, *a)
	}
	phase = "publish"
	publishAttempted = true
	publishStarted := time.Now()
	err = publish(values)
	publishDuration = time.Since(publishStarted)
	if err != nil {
		return err
	}
	if tx != nil {
		phase = "commit"
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	phase = "complete"
	return nil
}

func (r *accountRepository) publishSchedulerAccountSnapshot(ctx context.Context, id int64) error {
	if r.schedulerCache == nil || id <= 0 {
		return nil
	}
	return r.WithLatestSchedulerAccounts(ctx, []int64{id}, func(accounts []service.Account) error {
		if len(accounts) == 0 {
			return r.schedulerCache.DeleteAccount(ctx, id)
		}
		return r.schedulerCache.SetAccount(ctx, &accounts[0])
	})
}
