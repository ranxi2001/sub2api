package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// Register the actual process selector once during AccountOps construction.
// Replicas with the same selector share a revision. A change and change-back
// receive different revisions; in-flight work can never revive by value alone.
func (r *accountRepository) ConfigureOAuth429Routing(ctx context.Context, model string) (service.OAuth429RoutingPolicy, error) {
	model = strings.TrimSpace(model)
	if r.oauth429RoutingModel == nil {
		r.oauth429RoutingModel = new(atomic.Pointer[string])
	}
	r.oauth429RoutingModel.Store(&model)
	var policy service.OAuth429RoutingPolicy
	db, err := r.oauth429DB()
	if err != nil {
		return policy, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return policy, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize initialization as well as updates; ordinary task transactions
	// subsequently hold the settings row FOR SHARE through their final write.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4299282)`); err != nil {
		return policy, err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR UPDATE`, service.SettingKeyOAuth429Routing).Scan(&raw)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return policy, err
	}
	if !missing {
		if err = json.Unmarshal(raw, &policy); err != nil {
			return policy, err
		}
	}
	if missing || policy.CompactModel != model {
		policy.CompactModel, policy.Revision = model, uuid.NewString()
		if err = tx.QueryRowContext(ctx, `SELECT GREATEST(clock_timestamp(),$1::timestamptz+interval '1 microsecond')`, policy.ValidSince).Scan(&policy.ValidSince); err != nil {
			return policy, err
		}
		raw, err = json.Marshal(policy)
		if err != nil {
			return policy, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, service.SettingKeyOAuth429Routing, string(raw)); err != nil {
			return policy, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE oauth_429_tasks SET state=CASE WHEN state IN ('pending','running') THEN 'cancelled' ELSE state END,data=jsonb_set(data,'{samples}','[]'),updated_at=clock_timestamp()`); err != nil {
			return policy, err
		}
	}
	return policy, tx.Commit()
}

func (r *accountRepository) oauth429RoutingPolicy(ctx context.Context, tx *sql.Tx) (service.OAuth429RoutingPolicy, error) {
	var policy service.OAuth429RoutingPolicy
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, service.SettingKeyOAuth429Routing).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) && r.oauth429ExpectedRoutingModel() == nil {
		return policy, nil // Legacy direct repository fixtures have no selector.
	}
	if err != nil {
		return policy, err
	}
	if err = json.Unmarshal(raw, &policy); err != nil {
		return policy, err
	}
	if expected := r.oauth429ExpectedRoutingModel(); expected != nil && *expected != policy.CompactModel {
		return policy, errors.New("oauth429 process routing differs from current routing revision")
	}
	return policy, nil
}

func (r *accountRepository) oauth429ExpectedRoutingModel() *string {
	if r.oauth429RoutingModel == nil {
		return nil
	}
	return r.oauth429RoutingModel.Load()
}
