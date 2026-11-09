package repository

import (
	"context"
	"encoding/json"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func insertAutoConfigEvent(ctx context.Context, db sqlExecutor, event service.AutoConfigEvent) error {
	raw, err := json.Marshal(event.Details)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO account_auto_config_events(account_id,account_name,platform,kind,details) VALUES($1,$2,$3,$4,$5::jsonb)`, event.AccountID, event.AccountName, event.Platform, event.Kind, string(raw))
	return err
}

func (r *accountOpsRepository) ListAutoConfigEvents(ctx context.Context, before int64, kind string, limit int) ([]service.AutoConfigEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,account_id,account_name,platform,kind,details,created_at
 FROM account_auto_config_events WHERE ($1::bigint=0 OR id<$1) AND ($2='' OR kind=$2)
 ORDER BY id DESC LIMIT $3`, before, kind, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.AutoConfigEvent, 0)
	for rows.Next() {
		var event service.AutoConfigEvent
		var raw []byte
		if err := rows.Scan(&event.ID, &event.AccountID, &event.AccountName, &event.Platform, &event.Kind, &raw, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &event.Details); err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	return items, rows.Err()
}

func (r *accountRepository) RecordAutoConfigInitial(ctx context.Context, account *service.Account, groups []int64) error {
	mapping := make(map[string]string)
	if raw, ok := account.Credentials["model_mapping"].(map[string]any); ok {
		for from, value := range raw {
			if to, ok := value.(string); ok {
				mapping[from] = to
			}
		}
	}
	load := 1
	if account.LoadFactor != nil {
		load = *account.LoadFactor
	}
	return insertAutoConfigEvent(ctx, r.sql, service.AutoConfigEvent{
		AccountID: account.ID, AccountName: account.Name, Platform: account.Platform, Kind: service.AutoConfigEventInitial,
		Details: service.AutoConfigEventDetails{Priority: account.Priority, LoadFactor: load, Concurrency: account.Concurrency, GroupIDs: groups, ModelMapping: mapping},
	})
}

// Persist the save and its snapshot in the same transaction; failed saves have no history.
func (r *settingRepository) saveAutoConfigWithEvent(ctx context.Context, value string) error {
	return r.saveOAuthAutoConfig(ctx, value, nil)
}
func (r *settingRepository) CompareAndSetOAuthAutoConfig(ctx context.Context, value, expected string) error {
	return r.saveOAuthAutoConfig(ctx, value, &expected)
}
func (r *settingRepository) saveOAuthAutoConfig(ctx context.Context, value string, expected *string) error {
	var config service.OAuthAutoConfig
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		return err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize the absent-row case too; existing readers use the row lock.
	if _, err = tx.Client().ExecContext(ctx, "SELECT pg_advisory_xact_lock(681705431)"); err != nil {
		return err
	}
	rows, err := tx.Client().QueryContext(ctx, "SELECT value FROM settings WHERE key=$1 FOR UPDATE", service.SettingKeyOAuthAutoConfig)
	if err != nil {
		return err
	}
	var prior service.OAuthAutoConfig
	if rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &prior)
		}
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	if expected != nil && prior.Revision != *expected {
		return infraerrors.Conflict("AUTO_CONFIG_CONFLICT", "automatic configuration changed; reload before saving")
	}
	_, err = tx.Client().ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW())
 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at`, service.SettingKeyOAuthAutoConfig, value)
	if err != nil {
		return err
	}
	if err = insertAutoConfigEvent(ctx, tx.Client(), service.AutoConfigEvent{Kind: service.AutoConfigEventSaved, Platform: config.Platform, Details: service.AutoConfigEventDetails{Config: &config}}); err != nil {
		return err
	}
	return tx.Commit()
}
