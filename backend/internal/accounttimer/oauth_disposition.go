package accounttimer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// OAuthDispositionConfig is a create-only policy snapshot, not an ongoing sweep.
type OAuthDispositionConfig struct {
	Enabled      bool    `json:"enabled"`
	Scope        string  `json:"scope"`
	AccountIDs   []int64 `json:"account_ids"`
	GroupIDs     []int64 `json:"group_ids"`
	Action       string  `json:"action"`
	DelayMinutes int     `json:"delay_minutes"`
	PauseMinutes int     `json:"pause_minutes"`
	Timezone     string  `json:"timezone"`
}

func (p OAuthDispositionConfig) Validate() error {
	if !p.Enabled {
		return nil
	}
	if (p.Scope != "accounts" && p.Scope != "groups") || (p.Action != "pause" && p.Action != "disable") || p.DelayMinutes < 0 || p.DelayMinutes > 43200 || (p.Action == "pause" && (p.PauseMinutes < 1 || p.PauseMinutes > 43200)) {
		return invalid("oauth_disposition")
	}
	if p.Timezone == "Local" || (p.Timezone != "UTC" && !strings.Contains(p.Timezone, "/")) {
		return invalid("timezone")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return invalid("timezone")
	}
	for _, ids := range [][]int64{p.AccountIDs, p.GroupIDs} {
		if len(ids) > 1000 {
			return invalid("oauth_disposition")
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			if id <= 0 || seen[id] {
				return invalid("oauth_disposition")
			}
			seen[id] = true
		}
	}
	if p.Scope == "accounts" && len(p.AccountIDs) == 0 || p.Scope == "groups" && len(p.GroupIDs) == 0 {
		return invalid("oauth_disposition")
	}
	return nil
}

type dispositionExecutor interface {
	Executor
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ScheduleOAuthDisposition runs inside the new OAuth account + groups transaction.
// Lock order is stable, so simultaneous imports cannot replace a pending pause.
// Existing manual/daily timers and already disabled accounts are preserved.
func ScheduleOAuthDisposition(ctx context.Context, q dispositionExecutor, sourceID int64, platform string, p OAuthDispositionConfig, now time.Time) error {
	if !p.Enabled {
		return nil
	}
	if err := p.Validate(); err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT a.id FROM accounts a
 WHERE a.deleted_at IS NULL AND a.type='apikey' AND a.platform=$1 AND a.parent_account_id IS NULL
 AND a.schedulable AND a.status='active' AND (a.expires_at IS NULL OR a.expires_at>$5)
 AND (($2='accounts' AND a.id=ANY($3::bigint[])) OR ($2='groups' AND EXISTS (
 SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id
 WHERE ag.account_id=a.id AND ag.group_id=ANY($4::bigint[]) AND g.deleted_at IS NULL AND g.status='active' AND g.platform=$1)))
 ORDER BY a.id FOR UPDATE OF a`, platform, p.Scope, pq.Array(p.AccountIDs), pq.Array(p.GroupIDs), now)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	reason := fmt.Sprintf("oauth_initial:%d:%s", sourceID, platform)
	for _, id := range ids {
		pending, e := q.QueryContext(ctx, `SELECT 1 FROM account_schedulable_timers WHERE account_id=$1 AND status='pending' LIMIT 1`, id)
		if e != nil {
			return e
		}
		exists := pending.Next()
		e = pending.Err()
		_ = pending.Close()
		if e != nil {
			return e
		}
		if exists {
			continue
		}
		actions := []string{"close"}
		if p.Action == "pause" {
			actions = append(actions, "open")
		}
		for _, action := range actions {
			due := now.Add(time.Duration(p.DelayMinutes) * time.Minute)
			if action == "open" {
				due = due.Add(time.Duration(p.PauseMinutes) * time.Minute)
			}
			_, err = q.ExecContext(ctx, `WITH scheduled AS (
 INSERT INTO account_schedulable_timers(account_id,action,version,execute_at,timezone,requested_by,status,next_attempt_at,reason,recurrence,daily_time)
 VALUES($1,$2,1,$3,$4,0,'pending',$3,$5,'once','')
 ON CONFLICT(account_id,action) DO UPDATE SET version=account_schedulable_timers.version+1,
 execute_at=EXCLUDED.execute_at,timezone=EXCLUDED.timezone,requested_by=0,status='pending',next_attempt_at=EXCLUDED.execute_at,
 attempts=0,actual_at=NULL,reason=EXCLUDED.reason,recurrence='once',daily_time='' RETURNING *)
 INSERT INTO account_schedulable_timer_audit(account_id,action,version,actor_id,execute_at,timezone,actual_at,result,reason,recurrence,daily_time)
 SELECT account_id,action,version,0,execute_at,timezone,$6,'scheduled',reason,recurrence,daily_time FROM scheduled`, id, action, due, p.Timezone, reason, now)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Restore only the switch changed by the paired close, provided no later write
// touched the account. Explicit manual edits also cancel both native timers.
func oauthDispositionDecision(ctx context.Context, tx *sql.Tx, t Task, now time.Time) (string, error) {
	parts := strings.Split(t.Reason, ":")
	if len(parts) != 3 {
		return "oauth_disposition_superseded", nil
	}
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT type='apikey' AND platform=$4 AND parent_account_id IS NULL AND status='active' AND (expires_at IS NULL OR expires_at>$5)
 AND CASE WHEN $2='close' THEN schedulable ELSE NOT schedulable AND EXISTS (
 SELECT 1 FROM account_schedulable_timers c WHERE c.account_id=a.id AND c.action='close'
 AND c.status='executed' AND c.reason=$3 AND c.actual_at=a.updated_at) END
 FROM accounts a WHERE id=$1`, t.AccountID, t.Action, t.Reason, parts[2], now).Scan(&eligible)
	if err != nil {
		return "", err
	}
	if !eligible {
		return "oauth_disposition_superseded", nil
	}
	return "", nil
}
