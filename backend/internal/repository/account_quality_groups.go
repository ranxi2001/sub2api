package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Shared by standalone group quarantine and composed model isolation. Callers
// hold the plan/account locks and commit ownership plus outbox in one transaction.
func removeQualityGroups(ctx context.Context, tx *sql.Tx, accountID int64, targets []int64, state *qualityState) error {
	raw, err := qualityGroups(ctx, tx, accountID)
	if err != nil {
		return err
	}
	var groups []qualityGroup
	if err = json.Unmarshal(raw, &groups); err != nil {
		return err
	}
	selected := map[int64]bool{}
	for _, id := range targets {
		selected[id] = true
	}
	for _, group := range groups {
		if !selected[group.GroupID] {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, accountID, group.GroupID); err != nil {
			return err
		}
		state.Removed = append(state.Removed, group)
	}
	if len(state.Removed) > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET updated_at=clock_timestamp() WHERE id=$1`, accountID)
	}
	return err
}

func restoreQualityGroups(ctx context.Context, tx *sql.Tx, accountID int64, state *qualityState) (bool, error) {
	// Validate the entire restore before inserting anything. A manual membership
	// replacement must never be silently claimed as a successful rule restore.
	diagnostic := qualityRestoreConflict("membership_already_present")
	proposed := make([]int64, 0, len(state.Removed))
	for _, group := range state.Removed {
		proposed = append(proposed, group.GroupID)
	}
	allowed, err := lockOAuthDegradationGroupBindings(ctx, tx, accountID, proposed)
	if err != nil {
		return false, err
	}
	if len(allowed) != len(proposed) {
		diagnostic.Reason = "oauth_degradation_group_limited"
		state.RestoreDiagnostic = diagnostic
		return true, nil
	}
	conflict, allPresent := false, len(state.Removed) > 0
	for _, group := range state.Removed {
		var exists, replaced bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM groups WHERE id=$1 AND deleted_at IS NULL),EXISTS(SELECT 1 FROM account_groups WHERE account_id=$2 AND group_id=$1)`, group.GroupID, accountID).Scan(&exists, &replaced); err != nil {
			return false, err
		}
		diagnostic.Groups = append(diagnostic.Groups, service.QualityRestoreGroupEvidence{GroupID: group.GroupID, GroupExists: exists, MembershipPresent: replaced})
		allPresent = allPresent && exists && replaced
		if !exists {
			diagnostic.Reason = "group_missing"
		}
		conflict = conflict || !exists || replaced
	}
	if conflict {
		// Presence only: priority, model limits and the creator belong to the
		// external writer. Do not claim its membership or clear our snapshot.
		if allPresent {
			diagnostic.Disposition = "external_membership_present"
		}
		state.RestoreDiagnostic = diagnostic
		return true, nil
	}
	for _, group := range state.Removed {
		var allowed any
		if len(group.AllowedModels) > 0 && string(group.AllowedModels) != "null" {
			allowed = string(group.AllowedModels)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at,allowed_models) VALUES($1,$2,$3,$4,$5)`, accountID, group.GroupID, group.Priority, group.CreatedAt, allowed); err != nil {
			return false, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET updated_at=clock_timestamp() WHERE id=$1`, accountID)
	return false, err
}

func qualityGroupsChanged(ctx context.Context, tx *sql.Tx, accountID int64, before []byte, removed []qualityGroup) error {
	var groups []qualityGroup
	if err := json.Unmarshal(before, &groups); err != nil {
		return err
	}
	ids := make([]int64, 0, len(groups)+len(removed))
	for _, group := range groups {
		ids = append(ids, group.GroupID)
	}
	for _, group := range removed {
		ids = append(ids, group.GroupID)
	}
	payload, err := json.Marshal(map[string]any{"group_ids": ids})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduler_outbox(event_type,account_id,payload) VALUES('account_groups_changed',$1,$2)`, accountID, string(payload))
	return err
}
