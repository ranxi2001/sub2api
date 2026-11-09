package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func (r *accountRepository) OAuthDegradationIDs(ctx context.Context, after int64, limit int) ([]int64, error) {
	// Do not query quality tables at all while the independent switch is off.
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return nil, errors.New("OAuth degradation requires transactional database")
	}
	var raw string
	err := db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", service.SettingKeyOAuthAutoConfig).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c := service.DefaultOAuthAutoConfig()
	if err = json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, err
	}
	if err = c.DegradationAction.Validate(); err != nil {
		return nil, err
	}
	if !c.DegradationAction.Enabled {
		return nil, nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT id FROM accounts WHERE id>$1 AND deleted_at IS NULL AND parent_account_id IS NULL AND type='oauth' AND NOT(COALESCE(extra,'{}'::jsonb) ? 'oauth_degradation_action') ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *accountRepository) EvaluateOAuthDegradation(ctx context.Context, id int64, now time.Time) error {
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return errors.New("OAuth degradation requires transactional database")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Same lock order as native concurrency upgrades: settings, then account.
	// A save/disable waits for this action, or this action sees the new policy.
	var raw string
	err = tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1 FOR SHARE", service.SettingKeyOAuthAutoConfig).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	c := service.DefaultOAuthAutoConfig()
	if err = json.Unmarshal([]byte(raw), &c); err != nil {
		return err
	}
	if err = c.DegradationAction.Validate(); err != nil {
		return err
	}
	if !c.DegradationAction.Enabled {
		return nil
	}
	a := service.Account{ID: id}
	var credentials, extra []byte
	err = tx.QueryRowContext(ctx, `SELECT name,platform,type,concurrency,credentials,COALESCE(extra,'{}'::jsonb) FROM accounts WHERE id=$1 AND deleted_at IS NULL AND parent_account_id IS NULL AND type='oauth' FOR UPDATE`, id).Scan(&a.Name, &a.Platform, &a.Type, &a.Concurrency, &credentials, &extra)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(extra, &a.Extra); err != nil {
		return err
	}
	if a.HasOAuthDegradationAction() {
		return nil
	}
	if err = json.Unmarshal(credentials, &a.Credentials); err != nil {
		return err
	}
	// Reuse the exact UI definition and its live global rolling window. This
	// does not require (or modify) an account_performance_monitors row.
	metrics, err := readAccountPerformance(ctx, tx, []int64{id}, now)
	if err != nil {
		return err
	}
	m, found := metrics[id]
	if !found || !c.DegradationAction.Triggered(m) {
		return nil
	}
	models := make([]string, 0, len(c.DegradationAction.Models))
	for _, model := range c.DegradationAction.Models {
		if c.DegradationAction.RestrictsModels() && a.IsModelSupported(model) {
			models = append(models, model)
		}
	}
	removedGroups := make([]int64, 0)
	if c.DegradationAction.LimitGroups {
		rows, err := tx.QueryContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=ANY($2) RETURNING group_id`, id, pq.Array(c.DegradationAction.GroupIDs))
		if err != nil {
			return err
		}
		for rows.Next() {
			var groupID int64
			if err := rows.Scan(&groupID); err != nil {
				_ = rows.Close()
				return err
			}
			removedGroups = append(removedGroups, groupID)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	next := a.Concurrency
	if c.DegradationAction.RestrictsConcurrency() {
		next = min(next, c.DegradationAction.TargetConcurrency)
	}
	limitConcurrency, limitModels := c.DegradationAction.RestrictsConcurrency(), c.DegradationAction.RestrictsModels()
	state := service.OAuthDegradationState{LimitConcurrency: &limitConcurrency, LimitModels: &limitModels, LimitGroups: c.DegradationAction.LimitGroups, GroupIDs: append([]int64{}, c.DegradationAction.GroupIDs...), RemovedGroupIDs: removedGroups, Revision: c.Revision, Models: models, ConcurrencyCap: next, ThresholdPercent: *c.DegradationAction.ThresholdPercent, DegradationPercent: *m.DegradationPercent, QualitySamples: m.QualitySamples, WindowStart: m.WindowStart, WindowEnd: m.WindowEnd, AppliedAt: now}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET concurrency=$2,extra=jsonb_set(CASE WHEN $4 THEN jsonb_set(COALESCE(extra,'{}'::jsonb),'{auto_config_concurrency}',COALESCE(extra->'auto_config_concurrency','{}'::jsonb)||'{"degradation_hold":true}'::jsonb) ELSE COALESCE(extra,'{}'::jsonb) END,'{oauth_degradation_action}',$3::jsonb),updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1`, id, next, string(encoded), limitConcurrency)
	if err != nil {
		return err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return err
	}
	if len(removedGroups) > 0 {
		if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountGroupsChanged, &id, nil, buildSchedulerGroupPayload(removedGroups)); err != nil {
			return err
		}
	}
	if err = insertAutoConfigEvent(ctx, tx, service.AutoConfigEvent{AccountID: id, AccountName: a.Name, Platform: a.Platform, Kind: service.AutoConfigEventDegradation, Details: service.AutoConfigEventDetails{PreviousConcurrency: a.Concurrency, Concurrency: next, Degradation: &state}}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return nil
}

// A bulk update evaluates this expression against each locked live row.
func oauthDegradationConcurrencySQL(proposed string) string {
	cap := "(extra->'oauth_degradation_action'->>'concurrency_cap')::int"
	ceiling := "(CASE WHEN " + cap + ">0 THEN CASE WHEN concurrency>0 THEN LEAST(concurrency," + cap + ") ELSE " + cap + " END ELSE concurrency END)"
	return "CASE WHEN extra ? 'oauth_degradation_action' AND COALESCE((extra->'oauth_degradation_action'->>'limit_concurrency')::boolean,true) THEN CASE WHEN " + ceiling + "<=0 THEN " + ceiling + " WHEN " + proposed + "<=0 THEN " + ceiling + " ELSE LEAST(" + proposed + "," + ceiling + ") END ELSE " + proposed + " END"
}
