package keyrisk

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

// Recipient records are keyed by episode and owner. Acknowledgement never resolves
// a shared event or mutes another owner or a later episode.
type Recipient struct {
	UserName       string     `json:"user_name"`
	UserID         int64      `json:"user_id"`
	Generation     int64      `json:"generation"`
	Attempts       int        `json:"attempts"`
	Sent           int        `json:"sent"`
	Uncertain      int        `json:"uncertain"`
	LastAttempt    *time.Time `json:"last_attempt"`
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
}
type UserNotice struct {
	Keys           []NamedIdentity `json:"keys"`
	EventID        int64           `json:"event_id"`
	Generation     int64           `json:"generation"`
	Rule           string          `json:"rule"`
	State          string          `json:"state"`
	KeyIDs         []int64         `json:"key_ids"`
	First          time.Time       `json:"first_seen"`
	Last           time.Time       `json:"last_seen"`
	Attempts       int             `json:"attempts"`
	MaxReminders   int             `json:"max_reminders"`
	AcknowledgedAt *time.Time      `json:"acknowledged_at"`
}

func ensureRecipients(ctx context.Context, tx *sql.Tx, event, generation int64, ev Evidence) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO key_risk_recipients(event_id,generation,user_id,key_ids)
 SELECT $1,$2,user_id,array_agg(id ORDER BY id) FROM api_keys WHERE id=ANY($3) AND user_id=ANY($4) GROUP BY user_id
 ON CONFLICT(event_id,generation,user_id) DO UPDATE SET key_ids=ARRAY(SELECT DISTINCT unnest(key_risk_recipients.key_ids || EXCLUDED.key_ids) ORDER BY 1)`, event, generation, pq.Array(ev.KeyIDs), pq.Array(ev.UserIDs))
	return err
}

func (s *Service) UserNotices(ctx context.Context, user, before int64) ([]UserNotice, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT e.id,r.generation,e.rule,e.state,
 ARRAY(SELECT k.id FROM api_keys k WHERE k.user_id=$1 AND k.id=ANY(r.key_ids) ORDER BY k.id),
 ARRAY(SELECT k.name FROM api_keys k WHERE k.user_id=$1 AND k.id=ANY(r.key_ids) ORDER BY k.id),
 e.first_seen,e.last_seen,r.acknowledged_at,
 (SELECT count(*) FROM key_risk_deliveries d WHERE d.event_id=r.event_id AND d.generation=r.generation AND d.user_id=r.user_id)
 FROM key_risk_recipients r JOIN key_risk_events e ON e.id=r.event_id AND e.generation=r.generation
 WHERE r.user_id=$1 AND ($2::bigint=0 OR e.id<$2) ORDER BY e.id DESC LIMIT 50`, user, before)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []UserNotice{}
	for rows.Next() {
		var n UserNotice
		var names []string
		if err = rows.Scan(&n.EventID, &n.Generation, &n.Rule, &n.State, pq.Array(&n.KeyIDs), pq.Array(&names), &n.First, &n.Last, &n.AcknowledgedAt, &n.Attempts); err != nil {
			return nil, err
		}
		n.Keys = []NamedIdentity{}
		for i, id := range n.KeyIDs {
			n.Keys = append(n.Keys, NamedIdentity{ID: id, Name: names[i]})
		}
		n.MaxReminders = cfg.MaxReminders
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) Acknowledge(ctx context.Context, user, event, generation int64) error {
	// The UPDATE locks the same recipient row as delivery preparation. A send that
	// already crossed the SMTP boundary cannot be recalled; later sends are muted.
	result, err := s.DB.ExecContext(ctx, `UPDATE key_risk_recipients r SET acknowledged_at=COALESCE(r.acknowledged_at,now()),
 acknowledgement=COALESCE(r.acknowledgement,jsonb_build_object('notice_version',1,'rule',e.rule,'last_seen',e.last_seen,'key_ids',to_jsonb(r.key_ids)))
 FROM key_risk_events e WHERE r.event_id=e.id AND e.generation=r.generation AND e.state='pending'
 AND r.user_id=$1 AND r.event_id=$2 AND r.generation=$3`, user, event, generation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Service) attachRecipients(ctx context.Context, events []Event) error {
	ids := make([]int64, 0, len(events))
	byID := map[int64]int{}
	for i := range events {
		ids = append(ids, events[i].ID)
		byID[events[i].ID] = i
		events[i].Recipients = []Recipient{}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.event_id,r.user_id,r.generation,r.acknowledged_at,
 count(d.outbox_id),count(d.outbox_id) FILTER(WHERE d.state='sent'),count(d.outbox_id) FILTER(WHERE d.state IN ('uncertain','sending')),max(d.attempted_at)
 FROM key_risk_recipients r LEFT JOIN key_risk_deliveries d USING(event_id,generation,user_id)
 WHERE r.event_id=ANY($1) GROUP BY r.event_id,r.generation,r.user_id ORDER BY r.event_id,r.generation DESC,r.user_id`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var event int64
		var r Recipient
		if err = rows.Scan(&event, &r.UserID, &r.Generation, &r.AcknowledgedAt, &r.Attempts, &r.Sent, &r.Uncertain, &r.LastAttempt); err != nil {
			return err
		}
		i := byID[event]
		events[i].Recipients = append(events[i].Recipients, r)
	}
	return rows.Err()
}

// Resolve display metadata in two bounded page queries. Never select credential values.
func (s *Service) attachNames(ctx context.Context, events []Event) error {
	userIDs, keyIDs := map[int64]bool{}, map[int64]bool{}
	for _, e := range events {
		for _, id := range e.Evidence.UserIDs {
			userIDs[id] = true
		}
		for _, r := range e.Recipients {
			userIDs[r.UserID] = true
		}
		for _, id := range e.Evidence.KeyIDs {
			keyIDs[id] = true
		}
	}
	users, keys := map[int64]string{}, map[int64]string{}
	if len(userIDs) > 0 {
		rows, err := s.DB.QueryContext(ctx, `SELECT id,COALESCE(NULLIF(btrim(username),''),email,'') FROM users WHERE id=ANY($1)`, pq.Array(ids(userIDs)))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var name string
			if err = rows.Scan(&id, &name); err != nil {
				_ = rows.Close()
				return err
			}
			users[id] = name
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
	}
	if len(keyIDs) > 0 {
		rows, err := s.DB.QueryContext(ctx, `SELECT id,name FROM api_keys WHERE id=ANY($1)`, pq.Array(ids(keyIDs)))
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var name string
			if err = rows.Scan(&id, &name); err != nil {
				_ = rows.Close()
				return err
			}
			keys[id] = name
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
	}
	for i := range events {
		e := &events[i]
		e.Users = []NamedIdentity{}
		e.Keys = []NamedIdentity{}
		for _, id := range e.Evidence.UserIDs {
			e.Users = append(e.Users, NamedIdentity{ID: id, Name: users[id]})
		}
		for _, id := range e.Evidence.KeyIDs {
			e.Keys = append(e.Keys, NamedIdentity{ID: id, Name: keys[id]})
		}
		for j := range e.Recipients {
			e.Recipients[j].UserName = users[e.Recipients[j].UserID]
		}
	}
	return nil
}
