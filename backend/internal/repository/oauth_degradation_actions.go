package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
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
	err = tx.QueryRowContext(ctx, `SELECT name,platform,type,concurrency,schedulable,credentials,COALESCE(extra,'{}'::jsonb) FROM accounts WHERE id=$1 AND deleted_at IS NULL AND parent_account_id IS NULL AND type='oauth' FOR UPDATE`, id).Scan(&a.Name, &a.Platform, &a.Type, &a.Concurrency, &a.Schedulable, &credentials, &extra)
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
	reason := c.DegradationAction.TriggerReason(m)
	if !found || reason == "" {
		return nil
	}
	models := make([]string, 0, len(c.DegradationAction.Models))
	mapping := make(map[string]any)
	nativeSchedulingPaused := false
	if c.DegradationAction.RestrictsModels() {
		for _, model := range c.DegradationAction.Models {
			target := a.GetMappedModel(model)
			// Preserve existing aliases and exact/wildcard targets, and keep the
			// intersection on both sides of the mapping. Never grant a model
			// that the account or the selected post-action scope did not allow.
			if a.IsModelSupported(model) && slices.Contains(c.DegradationAction.Models, target) {
				models = append(models, model)
				mapping[model] = target
			}
		}
		if len(mapping) == 0 {
			// Native empty mappings mean unrestricted. Keep the original map
			// and visibly pause scheduling instead of inventing a hidden scope.
			a.Schedulable = false
			nativeSchedulingPaused = true
		} else {
			if a.Credentials == nil {
				a.Credentials = make(map[string]any)
			}
			a.Credentials["model_mapping"] = mapping
			a.Credentials["model_mapping_mode"] = "whitelist"
			delete(a.Credentials, "compact_model_mapping")
			if a.Extra == nil {
				a.Extra = make(map[string]any)
			}
			if a.Platform == service.PlatformOpenAI {
				a.Extra["openai_passthrough"] = false
				a.Extra["openai_oauth_passthrough"] = false
			}
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
	state := service.OAuthDegradationState{NativeControls: true, NativeSchedulingPaused: nativeSchedulingPaused, LimitConcurrency: &limitConcurrency, LimitModels: &limitModels, LimitGroups: c.DegradationAction.LimitGroups, GroupIDs: append([]int64{}, c.DegradationAction.GroupIDs...), RemovedGroupIDs: removedGroups, Revision: c.Revision, Models: models, ConcurrencyCap: next, QualitySamples: m.QualitySamples, WindowStart: m.WindowStart, WindowEnd: m.WindowEnd, AppliedAt: now}
	state.TriggerReason = reason
	if c.DegradationAction.ThresholdPercent != nil {
		state.ThresholdPercent = *c.DegradationAction.ThresholdPercent
	}
	if m.DegradationPercent != nil {
		state.DegradationPercent = *m.DegradationPercent
	}
	if reason == "upstream_429" {
		state.UpstreamError = &m.UpstreamError
		state.UpstreamErrorThresholdPercent = c.DegradationAction.HTTPErrorThreshold()
		state.Upstream429ShareThresholdPercent = c.DegradationAction.HTTP429ShareThreshold()
	}

	if a.Extra == nil {
		a.Extra = make(map[string]any)
	}
	a.Extra[service.OAuthDegradationActionExtraKey] = state
	if limitConcurrency {
		// A native manual concurrency edit clears this automatic-upgrade hold.
		// It does not cap later administrator changes at the historical value.
		a.Extra[service.AutoConfigConcurrencyExtraKey] = service.AutoConfigConcurrencyState{Concurrency: next, DegradationHold: true}
	}
	credentialsJSON, err := json.Marshal(a.Credentials)
	if err != nil {
		return err
	}
	extraJSON, err := json.Marshal(a.Extra)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET concurrency=$2,credentials=$3::jsonb,extra=$4::jsonb,schedulable=$5,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1`, id, next, string(credentialsJSON), string(extraJSON), a.Schedulable)
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
