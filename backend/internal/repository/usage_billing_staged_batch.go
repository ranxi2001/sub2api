package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type stagedBillingRow struct {
	key       service.UsageBillingEventKey
	recovered *service.UsageBillingRecovery
	state     string
}

// All durable state transitions and the aggregated monetary effects commit in
// one transaction. Existing upstream batch helpers retain decimal arithmetic,
// sorted row locks, and each request's post-charge balance/quota result.
func (r *usageBillingRepository) applyStagedUsageBillingBatch(ctx context.Context, owner string, keys []service.UsageBillingEventKey) (_ []*service.UsageBillingRecovery, err error) {
	if len(keys) == 0 || len(keys) > stagedBillingBatchSize {
		return nil, errors.New("invalid staged billing batch size")
	}
	release, err := r.admission.acquire(ctx, owner, r.db.Stats().MaxOpenConnections)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() {
		if err == nil {
			return
		}
		// Back off a failed batch in one bounded write. The state predicate
		// protects a commit whose response was lost and whose receipt is durable.
		retry, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		ids, apiKeys := stagedKeyArrays(keys)
		_, _ = r.db.ExecContext(retry, `UPDATE usage_billing_events e SET
			attempts=e.attempts+1,next_attempt_at=NOW()+INTERVAL '30 seconds',
			failure_code='apply_failed'
			FROM unnest($1::text[],$2::bigint[]) k(request_id,api_key_id)
			WHERE e.request_id=k.request_id AND e.api_key_id=k.api_key_id AND e.state='pending'`,
			pq.Array(ids), pq.Array(apiKeys))
	}()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := readStagedBillingRows(ctx, tx, owner, keys)
	if err != nil {
		return nil, err
	}
	var pending []*stagedBillingRow
	var cmds []*service.UsageBillingCommand
	seen := make(map[service.UsageBillingEventKey]bool)
	for _, key := range keys {
		row := rows[key]
		if row.state == "pending" && !seen[key] {
			pending = append(pending, row)
			cmds = append(cmds, &row.recovered.Event.Command)
		}
		seen[key] = true
	}
	if len(cmds) > 0 {
		user, err := validateUsageBillingBatch(cmds)
		if err != nil {
			return nil, err
		}
		outcomes := make([]service.UsageBillingBatchOutcome, len(cmds))
		applied, err := claimUsageBillingBatch(ctx, tx, cmds, outcomes)
		if err != nil {
			return nil, err
		}
		var fresh []service.UsageBillingEventKey
		for i, row := range pending {
			if outcomes[i].Err != nil {
				return nil, outcomes[i].Err
			}
			if applied[i] {
				outcomes[i].Result = &service.UsageBillingApplyResult{Applied: true}
				fresh = append(fresh, row.key)
			}
		}
		if len(fresh) > 0 {
			ids, apiKeys := stagedKeyArrays(fresh)
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_logs u
				JOIN unnest($1::text[],$2::bigint[]) k(request_id,api_key_id)
				ON u.request_id=k.request_id AND u.api_key_id=k.api_key_id)`, pq.Array(ids), pq.Array(apiKeys)).Scan(&exists); err != nil {
				return nil, err
			}
			if exists {
				return nil, service.ErrUsageBillingRequestConflict
			}
			if err := r.applyStagedBatchEffects(ctx, tx, user, cmds, applied, outcomes); err != nil {
				return nil, err
			}
		}
		updates := make([]stagedBillingUpdate, 0, len(pending))
		for i, row := range pending {
			if !applied[i] {
				row.state = "legacy"
				updates = append(updates, stagedBillingUpdate{RequestID: row.key.RequestID, APIKeyID: row.key.APIKeyID, State: "legacy", Result: json.RawMessage(`{}`)})
				continue
			}
			cmd := cmds[i]
			if err := insertUsageBillingQuote(ctx, tx, cmd); err != nil {
				return nil, err
			}
			if err := execUsageLogInsertNoResult(ctx, tx, prepareUsageLogInsert(&row.recovered.Event.Usage)); err != nil {
				return nil, err
			}
			row.recovered.Result = *outcomes[i].Result
			row.recovered.Settled, row.recovered.NeedsCache = true, true
			row.state = "applied"
			result, err := json.Marshal(row.recovered.Result)
			if err != nil {
				return nil, err
			}
			updates = append(updates, stagedBillingUpdate{RequestID: row.key.RequestID, APIKeyID: row.key.APIKeyID, State: "applied", Result: result})
		}
		raw, err := json.Marshal(updates)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE usage_billing_events e SET state=v.state,result=v.result,
			applied_at=CASE WHEN v.state='applied' THEN NOW() ELSE e.applied_at END,
			completed_at=CASE WHEN v.state='legacy' THEN NOW() ELSE e.completed_at END,
			failure_code=NULL,next_attempt_at=NOW()
			FROM jsonb_to_recordset($1::jsonb) v(request_id text,api_key_id bigint,state text,result jsonb)
			WHERE e.request_id=v.request_id AND e.api_key_id=v.api_key_id AND e.state='pending'`, string(raw))
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		// No per-request replay after an ambiguous COMMIT. A fresh read must
		// prove every receipt settled; otherwise leave recovery to the journal.
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		ids, apiKeys := stagedKeyArrays(keys)
		stored, checkErr := r.db.QueryContext(check, `SELECT e.request_id,e.api_key_id,e.state,e.result
            FROM usage_billing_events e WHERE (e.request_id,e.api_key_id) IN
            (SELECT * FROM unnest($1::text[],$2::bigint[]))`, pq.Array(ids), pq.Array(apiKeys))
		if checkErr != nil {
			return nil, errors.Join(service.ErrUsageBillingOutcomeUnknown, err, checkErr)
		}
		confirmed := make(map[service.UsageBillingEventKey]bool)
		for stored.Next() {
			var key service.UsageBillingEventKey
			var state string
			var result []byte
			if checkErr = stored.Scan(&key.RequestID, &key.APIKeyID, &state, &result); checkErr != nil {
				break
			}
			if state != "applied" && state != "complete" && state != "legacy" {
				checkErr = service.ErrUsageBillingOutcomeUnknown
				break
			}
			recovery := rows[key].recovered
			if checkErr = json.Unmarshal(result, &recovery.Result); checkErr != nil {
				break
			}
			recovery.Settled = state != "legacy" && recovery.Result.Applied
			recovery.NeedsCache = state == "applied"
			// A concurrent settler may own this receipt. Only a confirmed
			// local commit can emit first-application notifications.
			recovery.Result.Applied = false
			confirmed[key] = true
		}
		checkErr = errors.Join(checkErr, stored.Err(), stored.Close())
		if checkErr != nil || len(confirmed) != len(rows) {
			return nil, errors.Join(service.ErrUsageBillingOutcomeUnknown, err, checkErr)
		}
	}
	out := make([]*service.UsageBillingRecovery, len(keys))
	clear(seen)
	for i, key := range keys {
		copy := *rows[key].recovered
		if seen[key] {
			copy.Result.Applied = false
		}
		seen[key] = true
		out[i] = &copy
	}
	return out, nil
}

type stagedBillingUpdate struct {
	RequestID string          `json:"request_id"`
	APIKeyID  int64           `json:"api_key_id"`
	State     string          `json:"state"`
	Result    json.RawMessage `json:"result"`
}

func stagedKeyArrays(keys []service.UsageBillingEventKey) ([]string, []int64) {
	ids, apiKeys := make([]string, len(keys)), make([]int64, len(keys))
	for i, key := range keys {
		ids[i], apiKeys[i] = key.RequestID, key.APIKeyID
	}
	return ids, apiKeys
}

func readStagedBillingRows(ctx context.Context, tx *sql.Tx, owner string, keys []service.UsageBillingEventKey) (map[service.UsageBillingEventKey]*stagedBillingRow, error) {
	ids, apiKeys := stagedKeyArrays(keys)
	result, err := tx.QueryContext(ctx, `SELECT e.request_id,e.api_key_id,e.schema_version,e.payload,e.state,e.result,e.request_fingerprint
		FROM usage_billing_events e WHERE (e.request_id,e.api_key_id) IN (SELECT * FROM unnest($1::text[],$2::bigint[]))
		ORDER BY e.request_id,e.api_key_id FOR UPDATE OF e`, pq.Array(ids), pq.Array(apiKeys))
	if err != nil {
		return nil, err
	}
	defer func() { _ = result.Close() }()
	rows := make(map[service.UsageBillingEventKey]*stagedBillingRow, len(keys))
	for result.Next() {
		row := &stagedBillingRow{recovered: &service.UsageBillingRecovery{}}
		var version int
		var raw, stored []byte
		var fingerprint string
		if err := result.Scan(&row.key.RequestID, &row.key.APIKeyID, &version, &raw, &row.state, &stored, &fingerprint); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &row.recovered.Event); err != nil {
			return nil, err
		}
		if version != row.recovered.Event.Version {
			return nil, service.ErrUsageBillingEventVersion
		}
		if err := validateUsageBillingEvent(&row.recovered.Event, row.key, fingerprint); err != nil {
			return nil, err
		}
		if strconv.FormatInt(row.recovered.Event.Command.UserID, 10) != owner {
			return nil, service.ErrUsageBillingRequestConflict
		}
		switch row.state {
		case "pending":
		case "applied", "complete", "legacy":
			if len(stored) > 0 {
				if err := json.Unmarshal(stored, &row.recovered.Result); err != nil {
					return nil, err
				}
			}
			row.recovered.Settled = row.state != "legacy" && row.recovered.Result.Applied
			row.recovered.Result.Applied = false
			row.recovered.NeedsCache = row.state == "applied"
		default:
			return nil, service.ErrUsageBillingRequestConflict
		}
		rows[row.key] = row
	}
	if err := result.Err(); err != nil {
		return nil, err
	}
	for _, key := range keys {
		if rows[key] == nil {
			return nil, sql.ErrNoRows
		}
	}
	return rows, nil
}

func (r *usageBillingRepository) applyStagedBatchEffects(ctx context.Context, tx *sql.Tx, user int64, cmds []*service.UsageBillingCommand, applied []bool, outcomes []service.UsageBillingBatchOutcome) error {
	if err := applyUsageBillingBatchSubscriptions(ctx, tx, cmds, applied); err != nil {
		return err
	}
	if err := applyUsageBillingBatchBalance(ctx, tx, user, cmds, applied, outcomes); err != nil {
		return err
	}
	if err := applyUsageBillingBatchAPIKeys(ctx, tx, cmds, applied, outcomes); err != nil {
		return err
	}
	if err := applyUsageBillingBatchAccountQuotas(ctx, tx, cmds, applied, outcomes); err != nil {
		return err
	}
	for i, c := range cmds {
		if applied[i] && c.PlatformQuotaCost > 0 && c.PlatformQuotaPlatform != "" {
			if _, err := tx.ExecContext(ctx, lockActivePlatformQuotasSQL, user); err != nil {
				return err
			}
			break
		}
	}
	for i, c := range cmds {
		if applied[i] && c.PlatformQuotaCost > 0 && c.PlatformQuotaPlatform != "" {
			if err := incrementUsageBillingPlatformQuota(ctx, tx, c); err != nil {
				return err
			}
			outcomes[i].Result.PlatformQuotaApplied = true
		}
	}
	// Auto credit is an automatic recharge, evaluated once against the batch's
	// closing balance. Its wallet lock, cap and ledger identity remain atomic
	// with the debit; do not manufacture intermediate per-request recharges.
	for i := len(cmds) - 1; i >= 0; i-- {
		if applied[i] && cmds[i].BalanceCost > 0 {
			if err := advanceAutoCreditAfterDebit(ctx, tx, cmds[i], outcomes[i].Result); err != nil {
				return err
			}
			break
		}
	}
	return nil
}
