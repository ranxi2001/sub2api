package repository

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.AccountExcelBPSGroupRepository = (*accountRepository)(nil)

func (r *accountRepository) MoveExcelBPSOn403(ctx context.Context, account *service.Account) (bool, error) {
	target, enabled := account.ExcelBPS403GroupTarget()
	if !enabled {
		return false, nil
	}
	if dbent.TxFromContext(ctx) != nil {
		return r.moveExcelBPSOn403InTx(ctx, account, target)
	}
	tx, err := r.client.Tx(ctx)
	if errors.Is(err, dbent.ErrTxStarted) {
		return r.moveExcelBPSOn403InTx(ctx, account, target)
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := r.moveExcelBPSOn403InTx(dbent.NewTxContext(ctx, tx), account, target)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if changed {
		r.syncSchedulerAccountSnapshot(ctx, account.ID)
	}
	return changed, nil
}

func (r *accountRepository) moveExcelBPSOn403InTx(ctx context.Context, account *service.Account, target int64) (bool, error) {
	client := clientFromContext(ctx, r.client)
	if target > 0 {
		rows, err := client.QueryContext(ctx, `SELECT id FROM groups
WHERE id = $1 AND deleted_at IS NULL AND platform IN ('openai', 'composite') FOR SHARE`, target)
		if err != nil {
			return false, err
		}
		found := rows.Next()
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return false, err
		}
		if !found {
			return false, service.ErrGroupNotFound
		}
	}
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return false, err
	}
	rows, err := client.QueryContext(ctx, `SELECT id FROM accounts
WHERE id = $1 AND deleted_at IS NULL AND parent_account_id IS NULL
  AND platform = 'openai' AND type = 'oauth' AND credentials = $2::jsonb
  AND extra -> 'openai_excel_bps' = 'true'::jsonb
  AND extra -> 'openai_excel_bps_auto_move_on_403' = 'true'::jsonb
  AND extra -> 'openai_excel_bps_403_target_group_id' = $3::jsonb
FOR UPDATE`, account.ID, string(credentials), strconv.FormatInt(target, 10))
	if err != nil {
		return false, err
	}
	found := rows.Next()
	err = rows.Err()
	_ = rows.Close()
	if err != nil || !found {
		return false, err
	}
	rows, err = client.QueryContext(ctx, `SELECT group_id FROM account_groups WHERE account_id = $1 ORDER BY group_id FOR UPDATE`, account.ID)
	if err != nil {
		return false, err
	}
	var current []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return false, err
		}
		current = append(current, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return false, err
	}
	expected := slices.Clone(account.GroupIDs)
	slices.Sort(expected)
	// A concurrent manual edit or a preceding 403 has already changed routing.
	if !slices.Equal(current, expected) || (target == 0 && len(current) == 0) || (len(current) == 1 && current[0] == target) {
		return false, nil
	}
	if _, err := client.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id = $1 AND ($2 = 0 OR group_id <> $2)`, account.ID, target); err != nil {
		return false, err
	}
	// Keep an existing destination membership's priority and model restrictions.
	if target > 0 && !slices.Contains(current, target) {
		if _, err := client.ExecContext(ctx, `INSERT INTO account_groups (account_id, group_id, priority) VALUES ($1, $2, 1)`, account.ID, target); err != nil {
			return false, err
		}
	}
	// Record the action for the account list, which flags the suspected ban
	// while the account's groups still match it.
	if _, err := client.ExecContext(ctx, `UPDATE accounts SET extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object('openai_excel_bps_403_moved_at', $2::text, 'openai_excel_bps_403_moved_group_id', $3::bigint), updated_at = NOW() WHERE id = $1`,
		account.ID, time.Now().UTC().Format(time.RFC3339), target); err != nil {
		return false, err
	}
	payload := buildSchedulerGroupPayload(mergeGroupIDs(current, []int64{target}))
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountGroupsChanged, &account.ID, nil, payload); err != nil {
		return false, err
	}
	return true, nil
}
