package repository

import (
	"context"
	"database/sql"
	"errors"
)

// Before is the membership before this mode took ownership; After is the last
// membership written/observed by this mode. Nil means absent. Keep both across
// route expiry and recovery so leaving the mode can undo only owned changes.
type astraGroupMembership struct {
	GroupID int64         `json:"group_id"`
	Before  *qualityGroup `json:"before"`
	After   *qualityGroup `json:"after"`
}

func astraSchedulingGroups(ctx context.Context, tx *sql.Tx, id int64) ([]qualityGroup, error) {
	rows, err := tx.QueryContext(ctx, `SELECT account_id,group_id,priority,created_at,allowed_models
 FROM account_groups WHERE account_id=$1 ORDER BY group_id FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var groups []qualityGroup
	for rows.Next() {
		var g qualityGroup
		var allowed []byte
		if err = rows.Scan(&g.AccountID, &g.GroupID, &g.Priority, &g.CreatedAt, &allowed); err != nil {
			return nil, err
		}
		g.AllowedModels = allowed
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func astraMembershipEqual(a, b *qualityGroup) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.AccountID == b.AccountID && a.GroupID == b.GroupID && a.Priority == b.Priority &&
		a.CreatedAt.Equal(b.CreatedAt) && astraJSONEqual(a.AllowedModels, b.AllowedModels)
}

// Runs inside the settings/account transaction. A conflict rolls back the whole
// action, including earlier group writes, ownership and cache invalidation.
func reconcileAstraGroups(ctx context.Context, tx *sql.Tx, id int64, state *astraActionState, groups []qualityGroup, ready, restore bool) (changed, dirty bool, err error) {
	current := make(map[int64]*qualityGroup, len(groups))
	for i := range groups {
		current[groups[i].GroupID] = &groups[i]
	}
	if len(state.Memberships) == 0 {
		// Upgrade legacy removal-only ownership without losing original priority,
		// timestamps or per-membership model restrictions.
		removed := make(map[int64]*qualityGroup, len(state.Removed))
		for i := range state.Removed {
			removed[state.Removed[i].GroupID] = &state.Removed[i]
		}
		for _, gid := range state.GroupIDs {
			m := astraGroupMembership{GroupID: gid, Before: current[gid], After: current[gid]}
			if original, ok := removed[gid]; ok {
				m.Before, m.After = original, nil
			}
			state.Memberships = append(state.Memberships, m)
		}
		state.Removed = nil
		dirty = true
	}
	for _, m := range state.Memberships {
		if !astraMembershipEqual(current[m.GroupID], m.After) {
			return false, false, errors.New("astra_restore_conflict")
		}
	}
	for i := range state.Memberships {
		m := &state.Memberships[i]
		var desired *qualityGroup
		if restore || ready {
			desired = m.Before
		}
		if !restore && ready && desired == nil {
			if m.After != nil {
				desired = m.After
			} else {
				// New memberships follow the normal group defaults. Existing
				// memberships retain their priority and model restrictions.
				desired = &qualityGroup{AccountID: id, GroupID: m.GroupID, Priority: 50}
			}
		}
		if astraMembershipEqual(m.After, desired) {
			continue
		}
		if desired == nil {
			if _, err = tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, id, m.GroupID); err != nil {
				return false, false, err
			}
		} else {
			allowedGroups, groupErr := lockOAuthDegradationGroupBindings(ctx, tx, id, []int64{m.GroupID})
			if groupErr != nil {
				return false, false, groupErr
			}
			if len(allowedGroups) == 0 {
				return false, false, errors.New("astra_restore_conflict")
			}
			if err = lockLiveGroups(ctx, tx, []int64{m.GroupID}); err != nil {
				return false, false, errors.New("astra_restore_conflict")
			}
			var created, allowed any
			if !desired.CreatedAt.IsZero() {
				created = desired.CreatedAt
			}
			if len(desired.AllowedModels) > 0 {
				allowed = string(desired.AllowedModels)
			}
			inserted := *desired
			err = tx.QueryRowContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at,allowed_models)
 VALUES($1,$2,$3,COALESCE($4::timestamptz,clock_timestamp()),$5)
 ON CONFLICT (account_id,group_id) DO NOTHING RETURNING created_at`, id, m.GroupID, desired.Priority, created, allowed).Scan(&inserted.CreatedAt)
			if errors.Is(err, sql.ErrNoRows) {
				return false, false, errors.New("astra_restore_conflict")
			}
			if err != nil {
				return false, false, err
			}
			desired = &inserted
		}
		m.After = desired
		changed, dirty = true, true
	}
	return changed, dirty, nil
}
