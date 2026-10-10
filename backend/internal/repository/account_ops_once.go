package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"strings"
	"time"
)

// Serialize each account/metric observation in PostgreSQL; replica-local caches
// and process lifetime never decide whether a transition already happened.
func (r *accountOpsRepository) ObserveThreshold(ctx context.Context, e service.AccountOpsEvent, observation string, now time.Time, alert, recovery bool) error {
	now = now.Truncate(time.Microsecond)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	groupID := accountOpsEventGroupID(&e)
	_, err = tx.ExecContext(ctx, `INSERT INTO account_ops_threshold_monitors(account_id,group_id,kind,criteria,identity) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, e.AccountID, groupID, e.Kind, e.Criteria, e.Identity)
	if err != nil {
		return err
	}

	var criteria, identity, episode string
	var adverse, eligible bool
	var healthy, checked sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT criteria,identity,episode_id,adverse,eligible,healthy_since,checked_at FROM account_ops_threshold_monitors WHERE group_id=$1 AND kind=$2 FOR UPDATE`, groupID, e.Kind).Scan(&criteria, &identity, &episode, &adverse, &eligible, &healthy, &checked)
	if err != nil {
		return err
	}
	if checked.Valid && !now.After(checked.Time) {
		return tx.Commit()
	}
	phase := ""
	notify := false
	criteriaChanged := criteria != "" && criteria != "invalid" && criteria != e.Criteria
	identityChanged := e.Identity != "" && identity != "" && identity != e.Identity
	baselineInvalid := criteria == "invalid"
	changed := criteriaChanged || identityChanged || baselineInvalid
	previousAdverse := adverse
	previousEpisode := episode
	invalid := observation == "invalid"
	if changed || invalid {
		adverse = false
		eligible = false
		healthy = sql.NullTime{}
		episode = ""
		if _, err = tx.ExecContext(ctx, `UPDATE account_ops_threshold_events SET state='suppressed',lease='',lease_until=NULL WHERE group_id=$1 AND kind=$2 AND state IN ('pending','failed','sending')`, groupID, e.Kind); err != nil {
			return err
		}
	}
	if e.Identity != "" {
		identity = e.Identity
	}
	// Migrated work keeps its original snapshot, outcomes and attempt budget.
	// Bind only private eligibility metadata after a valid current observation;
	// unknown samples must neither enable delivery nor consume retries.
	if strings.HasPrefix(episode, "legacy:") && !changed && !invalid && (observation == "active" || observation == "resolved") && e.Criteria != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE account_ops_threshold_events SET criteria=$2,identity=$3 WHERE episode_id=$1 AND phase='alert' AND criteria=''`, episode, e.Criteria, identity); err != nil {
			return err
		}
	}
	criteria = e.Criteria
	switch observation {
	case "active":
		eligible = true
		healthy = sql.NullTime{}
		if !adverse {
			adverse = true
			episode = uuid.NewString()
			if !changed && !invalid {
				phase = "alert"
				notify = alert
			} else if criteriaChanged && !identityChanged && !invalid && !previousAdverse {
				phase = "alert"
				notify = alert
			}
		}
	case "resolved":
		eligible = true
		if criteriaChanged && !identityChanged && !invalid && previousAdverse {
			adverse = false
			healthy = sql.NullTime{}
			if previousEpisode == "" {
				previousEpisode = uuid.NewString()
			}
			episode = previousEpisode
			phase = "recovery"
			notify = recovery
		} else if adverse {
			if !healthy.Valid {
				healthy = sql.NullTime{Time: now, Valid: true}
			} else if now.Sub(healthy.Time) >= 15*time.Second {
				adverse = false
				healthy = sql.NullTime{}
				phase = "recovery"
				notify = recovery
			}
		}
	default:
		eligible = false
		healthy = sql.NullTime{}
	}
	// A first observation for a new rule can warn. Criteria changes notify only
	// when the adjustment itself crosses the current state boundary, while
	// migrated blank criteria adopts the old episode.
	if _, err = tx.ExecContext(ctx, `UPDATE account_ops_threshold_monitors SET account_id=$3,criteria=$4,identity=$5,episode_id=$6,adverse=$7,eligible=$8,healthy_since=$9,checked_at=$10 WHERE group_id=$1 AND kind=$2`, groupID, e.Kind, e.AccountID, criteria, identity, episode, adverse, eligible, healthy, now); err != nil {
		return err
	}
	if phase != "" {
		raw, err := json.Marshal(e.Details)
		if err != nil {
			return err
		}
		state := "pending"
		if !notify {
			state = "suppressed"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO account_ops_threshold_events(id,episode_id,phase,notification_enabled,account_id,group_id,group_name,kind,account_name,signal,http_status,first_seen,last_seen,occurrences,state,details,identity,criteria) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8,0,$10,$10,1,$11,$12::jsonb,$13,$14) ON CONFLICT(episode_id,phase) DO NOTHING`, uuid.NewString(), episode, phase, notify, e.AccountID, groupID, e.GroupName, e.Kind, e.AccountName, now, state, string(raw), identity, criteria)
		if err != nil {
			return err
		}
		if phase == "recovery" {
			if _, err = tx.ExecContext(ctx, `UPDATE account_ops_threshold_events SET state='suppressed',lease='',lease_until=NULL WHERE episode_id=$1 AND phase='alert' AND state IN ('pending','failed','sending')`, episode); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func (r *accountOpsRepository) ThresholdEventEligible(ctx context.Context, e *service.AccountOpsEvent) (bool, error) {
	var valid bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_ops_threshold_monitors WHERE group_id=$1 AND kind=$2 AND criteria<>'' AND criteria=$3 AND identity=$4 AND ($5='recovery' OR (adverse AND episode_id=$6)))`, accountOpsEventGroupID(e), e.Kind, e.Criteria, e.Identity, e.Phase, e.EpisodeID).Scan(&valid)
	return valid, err
}

const opsTransitionColumns = `id,episode_id,phase,notification_enabled,` + accountOpsColumns + `,criteria`

func scanOpsTransition(row scannable) (*service.AccountOpsEvent, error) {
	var e service.AccountOpsEvent
	var details, deliveries []byte
	err := row.Scan(&e.ID, &e.EpisodeID, &e.Phase, &e.NotificationEnabled, &e.AccountID, &e.GroupID, &e.GroupName, &e.Kind, &e.AccountName, &e.Signal, &e.HTTPStatus, &e.FirstSeen, &e.LastSeen, &e.Occurrences, &e.State, &e.LastSentAt, &e.NextSendAt, &e.Attempts, &e.Lease, &details, &deliveries, &e.Identity, &e.Criteria)
	if err == nil {
		if err = json.Unmarshal(details, &e.Details); err == nil {
			err = json.Unmarshal(deliveries, &e.Deliveries)
		}
	}
	return &e, err
}
func (r *accountOpsRepository) claimTransition(ctx context.Context) (*service.AccountOpsEvent, error) {
	if _, err := r.db.ExecContext(ctx, `UPDATE account_ops_threshold_events SET state='failed',lease='',lease_until=NULL WHERE state='sending' AND lease_until<NOW() AND attempts>=3`); err != nil {
		return nil, err
	}
	e, err := scanOpsTransition(r.db.QueryRowContext(ctx, `UPDATE account_ops_threshold_events SET state='sending',attempts=attempts+1,lease=$1,lease_until=NOW()+INTERVAL '2 minutes' WHERE id=(SELECT id FROM account_ops_threshold_events WHERE criteria<>'' AND attempts<3 AND ((state IN ('pending','failed') AND next_send_at<=NOW()) OR (state='sending' AND lease_until<NOW())) ORDER BY next_send_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING `+opsTransitionColumns, uuid.NewString()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}
func (r *accountOpsRepository) completeTransition(ctx context.Context, e *service.AccountOpsEvent, state string, delay time.Duration) error {
	if state == "deferred" {
		_, err := r.db.ExecContext(ctx, `UPDATE account_ops_threshold_events SET state='pending',attempts=GREATEST(attempts-1,0),lease='',lease_until=NULL,next_send_at=NOW()+INTERVAL '15 seconds' WHERE id=$1 AND lease=$2 AND state='sending' AND lease_until>NOW()`, e.ID, e.Lease)
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE account_ops_threshold_events SET state=$3,lease='',lease_until=NULL,next_send_at=NOW()+($4*INTERVAL '1 second'),last_sent_at=CASE WHEN $3='sent' THEN NOW() ELSE last_sent_at END WHERE id=$1 AND lease=$2 AND state='sending' AND lease_until>NOW()`, e.ID, e.Lease, state, int64(delay/time.Second))
	return err
}
func (r *accountOpsRepository) saveTransitionDelivery(ctx context.Context, e *service.AccountOpsEvent, key string, out service.AccountOpsDelivery) (bool, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE account_ops_threshold_events SET deliveries=jsonb_set(deliveries,ARRAY[$3]::text[],COALESCE(deliveries->$3,'{}'::jsonb)||$4::jsonb,true) WHERE id=$1 AND lease=$2 AND state='sending' AND lease_until>NOW()`, e.ID, e.Lease, key, string(raw))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// A transaction advisory lock coordinates full and scoped config updates across
// replicas, including inserts into a previously absent settings row.
func (r *accountOpsRepository) WithAccountOpsConfigLock(ctx context.Context, fn func(context.Context) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('account_ops_notifications_v1',0))`); err != nil {
		return err
	}
	if err = fn(context.WithValue(ctx, accountOpsTransactionKey{}, tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *accountOpsRepository) suppressTransitions(ctx context.Context, exec opsConfigExecutor, c service.AccountOpsConfig) error {
	type rule struct {
		AccountID int64  `json:"account_id"`
		Kind      string `json:"kind"`
		Criteria  string `json:"criteria"`
		Alert     bool   `json:"alert"`
		Recovery  bool   `json:"recovery"`
	}
	rules := []rule{}
	for _, v := range c.BalanceThresholds {
		criteria, enabled, a, b := c.ThresholdCriteria(v.AccountID, "balance_threshold")
		if enabled {
			rules = append(rules, rule{v.AccountID, "balance_threshold", criteria, a, b})
		}
	}
	for _, v := range c.QuotaThresholds {
		criteria, enabled, a, b := c.ThresholdCriteria(v.AccountID, "quota_threshold")
		if enabled {
			rules = append(rules, rule{v.AccountID, "quota_threshold", criteria, a, b})
		}
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	// Disabled/removed rules invalidate the baseline. Merely pausing the master or
	// notification switches retains the episode and cannot invent a recovery.
	_, err = exec.ExecContext(ctx, `UPDATE account_ops_threshold_monitors m SET adverse=false,eligible=false,healthy_since=NULL,criteria='invalid' WHERE criteria<>'invalid' AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements($1::jsonb) x WHERE (x->>'account_id')::bigint=m.account_id AND x->>'kind'=m.kind)`, string(raw))
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `UPDATE account_ops_threshold_events e SET state='suppressed',lease='',lease_until=NULL WHERE state IN ('pending','failed','sending') AND (NOT $1 OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements($2::jsonb) x WHERE (x->>'account_id')::bigint=e.account_id AND x->>'kind'=e.kind AND (e.criteria='' OR x->>'criteria'=e.criteria) AND ((e.phase='alert' AND (x->>'alert')::boolean) OR (e.phase='recovery' AND (x->>'recovery')::boolean))))`, c.Enabled, string(raw))
	if err != nil {
		return err
	}

	return nil
}

type accountOpsTransactionKey struct{}

func (r *accountOpsRepository) ReadAccountOpsConfig(ctx context.Context) (string, error) {
	var raw string
	var err error
	if tx, ok := ctx.Value(accountOpsTransactionKey{}).(*sql.Tx); ok {
		err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='account_ops_notifications_v1'`).Scan(&raw)
	} else {
		err = r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='account_ops_notifications_v1'`).Scan(&raw)
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = service.ErrSettingNotFound
	}
	return raw, err
}
func (r *accountOpsRepository) PersistAccountOpsConfig(ctx context.Context, raw string, c service.AccountOpsConfig) error {
	persist := func(tx *sql.Tx) error {
		var oldRaw string
		readErr := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='account_ops_notifications_v1'`).Scan(&oldRaw)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		var old service.AccountOpsConfig
		if oldRaw != "" {
			if err := json.Unmarshal([]byte(oldRaw), &old); err != nil {
				return err
			}
		}
		if err := r.invalidateUnboundLegacyCriteria(ctx, tx, old, c); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('account_ops_notifications_v1',$1,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at`, raw); err != nil {
			return err
		}
		return r.suppressDisabled(ctx, tx, c)
	}
	if tx, ok := ctx.Value(accountOpsTransactionKey{}).(*sql.Tx); ok {
		return persist(tx)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = persist(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// A transfer has no criteria fingerprint until its first valid scan. Comparing
// old/new private settings inside the save transaction prevents an intervening
// rule edit from adopting an obsolete warning under the replacement criteria.
func (r *accountOpsRepository) invalidateUnboundLegacyCriteria(ctx context.Context, tx *sql.Tx, old, next service.AccountOpsConfig) error {
	type changedRule struct {
		AccountID int64  `json:"account_id"`
		Kind      string `json:"kind"`
	}
	changes := []changedRule{}
	for _, rule := range old.BalanceThresholds {
		before, _, _, _ := old.ThresholdCriteria(rule.AccountID, "balance_threshold")
		after, _, _, _ := next.ThresholdCriteria(rule.AccountID, "balance_threshold")
		if before != after {
			changes = append(changes, changedRule{rule.AccountID, "balance_threshold"})
		}
	}
	for _, rule := range old.QuotaThresholds {
		before, _, _, _ := old.ThresholdCriteria(rule.AccountID, "quota_threshold")
		after, _, _, _ := next.ThresholdCriteria(rule.AccountID, "quota_threshold")
		if before != after {
			changes = append(changes, changedRule{rule.AccountID, "quota_threshold"})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	raw, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	// Consistent monitor-before-event lock order, with both writes covered by the
	// same configuration transaction and its cross-replica advisory lock.
	_, err = tx.ExecContext(ctx, `UPDATE account_ops_threshold_monitors m SET criteria='invalid',episode_id='',adverse=false,eligible=false,healthy_since=NULL,checked_at=NOW() WHERE EXISTS(SELECT 1 FROM jsonb_array_elements($1::jsonb) x WHERE (x->>'account_id')::bigint=m.account_id AND x->>'kind'=m.kind) AND EXISTS(SELECT 1 FROM account_ops_threshold_events e WHERE e.account_id=m.account_id AND e.kind=m.kind AND e.criteria='' AND e.phase='alert' AND e.state IN ('pending','failed','sending'))`, string(raw))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE account_ops_threshold_events e SET state='suppressed',lease='',lease_until=NULL WHERE criteria='' AND phase='alert' AND state IN ('pending','failed','sending') AND EXISTS(SELECT 1 FROM jsonb_array_elements($1::jsonb) x WHERE (x->>'account_id')::bigint=e.account_id AND x->>'kind'=e.kind)`, string(raw))
	return err
}
