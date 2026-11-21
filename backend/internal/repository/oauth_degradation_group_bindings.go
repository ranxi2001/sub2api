package repository

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Membership writers share the account lock with the degradation evaluator.
// Degradation removes native bindings once; later administrator edits may restore
// them. The historical action must never filter an ordinary binding save.
func lockOAuthDegradationGroupBindings(ctx context.Context, exec sqlExecutor, accountID int64, proposed []int64) ([]int64, error) {
	rows, err := exec.QueryContext(ctx, `SELECT COALESCE(extra,'{}'::jsonb), platform, type FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR NO KEY UPDATE`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAccountNotFound
	}
	var raw []byte
	var platform, kind string
	if err := rows.Scan(&raw, &platform, &kind); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return proposed, validateGatewayGroupBindings(ctx, exec, &service.Account{Platform: platform, Type: kind}, proposed)
}

// Recheck live policy under a shared group lock, including indirect writers
// (scoped admin, quality and AI moves) that do not call the usual binding API.
func validateGatewayGroupBindings(ctx context.Context, exec sqlExecutor, a *service.Account, ids []int64) error {
	if a == nil || (!a.IsGateway() && a.Platform != service.PlatformGateway) || len(ids) == 0 {
		return nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	rows, err := exec.QueryContext(ctx, `SELECT id,platform,require_oauth_only FROM groups
		WHERE id IN (SELECT value::bigint FROM jsonb_array_elements_text($1::jsonb)) AND deleted_at IS NULL
		ORDER BY id FOR SHARE`, string(encoded))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	seen := make(map[int64]bool, len(ids))
	for rows.Next() {
		var group service.Group
		if err := rows.Scan(&group.ID, &group.Platform, &group.RequireOAuthOnly); err != nil {
			return err
		}
		if err := service.ValidateGatewayAccountGroup(a, &group); err != nil {
			return err
		}
		seen[group.ID] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if !seen[id] {
			return service.ErrGatewayAccountGroup
		}
	}
	return nil
}
