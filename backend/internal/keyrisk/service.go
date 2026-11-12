package keyrisk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

type Mailer interface {
	SendEmail(context.Context, string, string, string) error
}
type Service struct {
	DB     *sql.DB
	Mail   Mailer
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run, stop := context.WithTimeout(ctx, 40*time.Second)
				if e := s.RunOnce(run); e != nil && ctx.Err() == nil {
					slog.Warn("key risk sweep failed", "error", e)
				}
				stop()
			}
		}
	}()
}
func (s *Service) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
func (s *Service) Config(ctx context.Context) (Config, error) {
	c := DefaultConfig()
	var raw []byte
	e := s.DB.QueryRowContext(ctx, "SELECT config FROM key_risk_control WHERE singleton").Scan(&raw)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(raw, &c)
	if c.TrustedNetworks == nil {
		c.TrustedNetworks = []string{}
	}
	if e == nil {
		e = c.Validate()
	}
	return c, e
}
func (s *Service) Save(ctx context.Context, c Config) error {
	if c.TrustedNetworks == nil {
		c.TrustedNetworks = []string{}
	}
	if e := c.Validate(); e != nil {
		return e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	_, e = s.DB.ExecContext(ctx, "UPDATE key_risk_control SET config=$1 WHERE singleton", string(b))
	return e
}

type NamedIdentity struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Event struct {
	Users      []NamedIdentity `json:"users"`
	Keys       []NamedIdentity `json:"keys"`
	ID         int64           `json:"id"`
	Generation int64           `json:"generation"`
	Recipients []Recipient     `json:"recipients"`
	Rule       string          `json:"rule"`
	Subject    string          `json:"subject"`
	Evidence   Evidence        `json:"evidence"`
	State      string          `json:"state"`
	First      time.Time       `json:"first_seen"`
	Last       time.Time       `json:"last_seen"`
}

func (s *Service) Events(ctx context.Context, before int64) ([]Event, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id,generation,rule,subject,evidence,state,first_seen,last_seen FROM key_risk_events WHERE ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT 50`, before)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := []Event{}
	for rows.Next() {
		var v Event
		var raw []byte
		if e = rows.Scan(&v.ID, &v.Generation, &v.Rule, &v.Subject, &raw, &v.State, &v.First, &v.Last); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v.Evidence); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	_ = rows.Close()
	if e = s.attachRecipients(ctx, out); e != nil {
		return nil, e
	}
	return out, s.attachNames(ctx, out)
}
func (s *Service) SetState(ctx context.Context, id, actor int64, state string) error {
	if state != "ignored" && state != "resolved" && state != "pending" {
		return errors.New("invalid state")
	}
	res, e := s.DB.ExecContext(ctx, `UPDATE key_risk_events SET state=$1,actor_id=$2,updated_at=now() WHERE id=$3 AND ($1!='pending' OR rule='key_ip_spread')`, state, actor, id)
	if e == nil {
		n, _ := res.RowsAffected()
		if n == 0 {
			e = sql.ErrNoRows
		}
	}
	return e
}

// Coalesce pending notifications while retaining the original per-key evidence.
// Callers hold the patrol advisory lock, serializing manual and automatic queueing.
func queue(ctx context.Context, tx *sql.Tx, user, eventID, generation int64, ev Evidence, now time.Time, manual bool, maxReminders int) (bool, error) {
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT acknowledged_at IS NULL AND NOT EXISTS(SELECT 1 FROM key_risk_deliveries recent WHERE recent.user_id=r.user_id AND recent.attempted_at>now()-interval '24 hours') AND (SELECT count(*) FROM key_risk_deliveries d WHERE d.event_id=r.event_id AND d.generation=r.generation AND d.user_id=r.user_id)<$4 FROM key_risk_recipients r WHERE event_id=$1 AND generation=$2 AND user_id=$3`, eventID, generation, user, maxReminders).Scan(&eligible)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !eligible) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO key_risk_outbox(user_id,kind,bucket,manual)
 SELECT $1,'risk',$2,$3 WHERE NOT EXISTS(SELECT 1 FROM key_risk_outbox WHERE user_id=$1 AND kind='risk' AND created_at>$4)
 ON CONFLICT DO NOTHING RETURNING id`, user, now.UTC().Unix()/86400, manual, now.Add(-24*time.Hour)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM key_risk_outbox WHERE user_id=$1 AND kind='risk' AND created_at>$2 AND state='pending' ORDER BY id DESC LIMIT 1 FOR UPDATE`, user, now.Add(-24*time.Hour)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE key_risk_outbox SET manual=manual OR $2 WHERE id=$1`, id, manual); err != nil {
		return false, err
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO key_risk_outbox_items(outbox_id,event_id,generation,key_id,evidence)
 SELECT $1,$2,$3,id,$4 FROM api_keys WHERE user_id=$5 AND id=ANY($6)
 ON CONFLICT DO NOTHING`, id, eventID, generation, string(raw), user, pq.Array(ev.KeyIDs))
	return err == nil, err
}
func (s *Service) Notify(ctx context.Context, id, actor int64) error {
	_, err := s.NotifyUsers(ctx, id, actor)
	return err
}

// NotifyUsers reports actual queued/coalesced recipients, including suppression.
func (s *Service) NotifyUsers(ctx context.Context, id, actor int64) (int, error) {
	cfg, e := s.Config(ctx)
	if e != nil {
		return 0, e
	}
	queued := 0
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(3330501)"); e != nil {
		return 0, e
	}
	var raw []byte
	var generation int64
	e = tx.QueryRowContext(ctx, `SELECT evidence,generation FROM key_risk_events WHERE id=$1 AND state='pending' AND rule='key_ip_spread' FOR UPDATE`, id).Scan(&raw, &generation)
	if e != nil {
		return 0, e
	}
	var ev Evidence
	if e = json.Unmarshal(raw, &ev); e != nil {
		return 0, e
	}
	if e = ensureRecipients(ctx, tx, id, generation, ev); e != nil {
		return 0, e
	}
	for _, u := range ev.UserIDs {
		ok, err := queue(ctx, tx, u, id, generation, ev, time.Now(), true, cfg.MaxReminders)
		if err != nil {
			return 0, err
		}
		if ok {
			queued++
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE key_risk_events SET actor_id=$1,updated_at=now() WHERE id=$2`, actor, id)
	if e != nil {
		return 0, e
	}
	return queued, tx.Commit()
}
func (s *Service) RunOnce(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			report, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _ = s.DB.ExecContext(report, "UPDATE key_risk_control SET last_error='sweep_failed' WHERE singleton")
		}
	}()
	c, e := s.Config(ctx)
	if e != nil {
		return e
	}
	if !c.Enabled {
		return nil
	}
	// Transaction-scoped advisory lock survives neither disconnect nor process crash.
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if e = tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock(3330501)").Scan(&locked); e != nil || !locked {
		return e
	}
	if _, e = tx.ExecContext(ctx, "SET LOCAL statement_timeout='5s'"); e != nil {
		return e
	}
	now := time.Now().UTC()
	start := now.Add(-time.Duration(c.WindowMinutes) * time.Minute)
	// Time-indexed, bounded overlap scans include delayed billing writes. No request hooks.
	rows, e := tx.QueryContext(ctx, `SELECT ip_address,user_id,api_key_id,created_at FROM usage_logs WHERE created_at >= $1 AND created_at <= $2 ORDER BY created_at DESC LIMIT 10001`, start, now)
	if e != nil {
		return e
	}
	rs := make([]Request, 0)
	for rows.Next() {
		var r Request
		var ip sql.NullString
		if e = rows.Scan(&ip, &r.UserID, &r.KeyID, &r.At); e != nil {
			_ = rows.Close()
			return e
		}
		r.IP = ip.String
		rs = append(rs, r)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return e
	}
	note := ""
	if len(rs) > 10000 {
		note = "coverage_incomplete: more than 10000 usage rows in window"
		rs = rs[:10000]
	}
	findings, e := allowedSourceFindings(ctx, tx, Evaluate(rs, c, now))
	if e != nil {
		return e
	}
	for _, f := range findings {
		raw, _ := json.Marshal(f.Evidence)
		var state string
		var eventID, generation int64
		e = tx.QueryRowContext(ctx, `INSERT INTO key_risk_events(rule,subject,evidence,first_seen,last_seen) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(rule,subject) DO UPDATE SET
 generation=key_risk_events.generation + CASE WHEN key_risk_events.state='resolved' AND (
 EXCLUDED.last_seen>key_risk_events.updated_at OR
 NOT ((key_risk_events.evidence->'key_ids') @> (EXCLUDED.evidence->'key_ids')) OR
 NOT ((key_risk_events.evidence->'user_ids') @> (EXCLUDED.evidence->'user_ids')) OR
 NOT (COALESCE(key_risk_events.evidence->'ips','[]'::jsonb) @> (EXCLUDED.evidence->'ips'))) THEN 1 ELSE 0 END,
 state=CASE WHEN key_risk_events.state='resolved' AND (
 EXCLUDED.last_seen>key_risk_events.updated_at OR
 NOT ((key_risk_events.evidence->'key_ids') @> (EXCLUDED.evidence->'key_ids')) OR
 NOT ((key_risk_events.evidence->'user_ids') @> (EXCLUDED.evidence->'user_ids')) OR
 NOT (COALESCE(key_risk_events.evidence->'ips','[]'::jsonb) @> (EXCLUDED.evidence->'ips'))) THEN 'pending' ELSE key_risk_events.state END,
 evidence=CASE WHEN key_risk_events.state='resolved' AND EXCLUDED.last_seen<=key_risk_events.updated_at
 AND (key_risk_events.evidence->'key_ids') @> (EXCLUDED.evidence->'key_ids')
 AND (key_risk_events.evidence->'user_ids') @> (EXCLUDED.evidence->'user_ids')
 AND COALESCE(key_risk_events.evidence->'ips','[]'::jsonb) @> (EXCLUDED.evidence->'ips') THEN key_risk_events.evidence ELSE EXCLUDED.evidence END,
 last_seen=GREATEST(key_risk_events.last_seen,EXCLUDED.last_seen)
 RETURNING id,generation,state`, f.Rule, f.Subject, string(raw), f.Evidence.First, f.Evidence.Last).Scan(&eventID, &generation, &state)
		if e != nil {
			return e
		}
		if e = ensureRecipients(ctx, tx, eventID, generation, f.Evidence); e != nil {
			return e
		}
		if c.AutoEmail && note == "" && state == "pending" && f.Rule == "key_ip_spread" {
			for _, u := range f.Evidence.UserIDs {
				if _, e = queue(ctx, tx, u, eventID, generation, f.Evidence, now, false, c.MaxReminders); e != nil {
					return e
				}
			}
		}
	}
	if c.AgeEmail {
		_, e = tx.ExecContext(ctx, `INSERT INTO key_risk_outbox(user_id,kind,bucket) SELECT DISTINCT k.user_id,'age',$1::bigint FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.deleted_at IS NULL AND k.status='active' AND u.deleted_at IS NULL AND u.status='active' AND (k.expires_at IS NULL OR k.expires_at>now()) AND k.created_at<$2 AND NOT EXISTS(SELECT 1 FROM key_risk_outbox o WHERE o.user_id=k.user_id AND o.kind='age' AND o.created_at>now()-interval '30 days') LIMIT 100 ON CONFLICT DO NOTHING`, now.Unix()/86400/30, now.Add(-time.Duration(c.AgeDays)*24*time.Hour))
		if e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE key_risk_control SET last_scan_at=$1,last_error=$2 WHERE singleton`, now, note); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM key_risk_events WHERE id IN (SELECT e.id FROM key_risk_events e WHERE e.last_seen<now()-interval '365 days' AND e.updated_at<now()-interval '365 days' AND e.state!='pending'
 AND NOT EXISTS(SELECT 1 FROM key_risk_recipients r WHERE r.event_id=e.id AND r.acknowledged_at>=now()-interval '365 days')
 AND NOT EXISTS(SELECT 1 FROM key_risk_deliveries d WHERE d.event_id=e.id AND d.attempted_at>=now()-interval '365 days') LIMIT 100)`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM key_risk_outbox WHERE id IN (SELECT id FROM key_risk_outbox WHERE updated_at<now()-interval '90 days' AND state NOT IN ('pending','sending') LIMIT 100)`); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return s.deliver(ctx)
}
func (s *Service) deliver(ctx context.Context) error {
	if s.Mail == nil {
		return nil
	}
	// SMTP acceptance can be ambiguous. Never automatically resend a crashed/in-flight message.
	_, e := s.DB.ExecContext(ctx, `WITH expired AS (UPDATE key_risk_outbox SET state='uncertain' WHERE state='sending' AND updated_at<now()-interval '10 minutes' RETURNING id) UPDATE key_risk_deliveries SET state='uncertain' WHERE outbox_id IN (SELECT id FROM expired)`)
	if e != nil {
		return e
	}
	for i := 0; i < 10; i++ {
		c, e := s.Config(ctx)
		if e != nil || !c.Enabled {
			return e
		}
		more, err := s.deliverOne(ctx, c)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return nil
}

// Claim and prepare inside one transaction. Any failure before SMTP rolls back
// to pending; a process crash during preparation releases the row lock as well.
func (s *Service) deliverOne(ctx context.Context, c Config) (bool, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer func() { _ = tx.Rollback() }()
	// Use the queue lock before outbox/recipient locks to prevent lock-order inversions.
	if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(3330501)"); e != nil {
		return false, e
	}
	var id, user int64
	var kind string
	var manual bool
	e = tx.QueryRowContext(ctx, `SELECT id,user_id,kind,manual FROM key_risk_outbox
 WHERE state='pending' AND available_at<=now() ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &user, &kind, &manual)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	finish := func(state string) error {
		if _, err := tx.ExecContext(ctx, `UPDATE key_risk_outbox SET state=$1,updated_at=now() WHERE id=$2`, state, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	if (kind == "risk" && !manual && !c.AutoEmail) || (kind == "age" && !c.AgeEmail) {
		return true, finish("skipped")
	}
	var email string
	e = tx.QueryRowContext(ctx, `SELECT email FROM users WHERE id=$1 AND status='active' AND deleted_at IS NULL`, user).Scan(&email)
	if errors.Is(e, sql.ErrNoRows) {
		return true, finish("skipped")
	}
	if e != nil {
		return false, e
	}
	if kind == "risk" {
		// Space actual attempts, not just enqueue timestamps. A delayed message
		// must not be followed immediately by another day's queued message.
		var next sql.NullTime
		if e = tx.QueryRowContext(ctx, `SELECT max(attempted_at)+interval '24 hours' FROM key_risk_deliveries WHERE user_id=$1 AND attempted_at>now()-interval '24 hours'`, user).Scan(&next); e != nil {
			return false, e
		}
		if next.Valid {
			if _, e = tx.ExecContext(ctx, `UPDATE key_risk_outbox SET available_at=$2 WHERE id=$1`, id, next.Time); e != nil {
				return false, e
			}
			return true, tx.Commit()
		}
		// Serialize confirmation against preparation, and keep the cap valid for concurrent consumers.
		locked, err := tx.QueryContext(ctx, `SELECT r.event_id FROM key_risk_recipients r WHERE r.user_id=$1 AND EXISTS(SELECT 1 FROM key_risk_outbox_items i WHERE i.outbox_id=$2 AND i.event_id=r.event_id AND i.generation=r.generation) ORDER BY r.event_id,r.generation FOR UPDATE`, user, id)
		if err != nil {
			return false, err
		}
		for locked.Next() {
		}
		err = locked.Err()
		_ = locked.Close()
		if err != nil {
			return false, err
		}
	}
	// The frozen item supplies attribution; current rows supply ownership and validity.
	rows, e := tx.QueryContext(ctx, `SELECT k.id,k.name,k.created_at FROM api_keys k
 WHERE k.user_id=$1 AND k.deleted_at IS NULL AND k.status='active' AND (k.expires_at IS NULL OR k.expires_at>now())
 AND (($2='age' AND k.created_at<$3) OR ($2='risk' AND EXISTS(
 SELECT 1 FROM key_risk_outbox_items i JOIN key_risk_events v ON v.id=i.event_id AND v.generation=i.generation
 JOIN key_risk_recipients r ON r.event_id=i.event_id AND r.generation=i.generation AND r.user_id=k.user_id
 WHERE i.outbox_id=$4 AND i.key_id=k.id AND v.state='pending' AND v.rule='key_ip_spread' AND r.acknowledged_at IS NULL
 AND (SELECT count(*) FROM key_risk_deliveries d WHERE d.event_id=r.event_id AND d.generation=r.generation AND d.user_id=r.user_id)<$5)))
	 ORDER BY k.id LIMIT CASE WHEN $2='age' THEN 50 ELSE NULL END`, user, kind, time.Now().Add(-time.Duration(c.AgeDays)*24*time.Hour), id, c.MaxReminders)
	if e != nil {
		return false, e
	}
	var body strings.Builder
	n := 0
	selectedKeys := []int64{}
	for rows.Next() {
		var key int64
		var name string
		var at time.Time
		if e = rows.Scan(&key, &name, &at); e != nil {
			_ = rows.Close()
			return false, e
		}
		fmt.Fprintf(&body, `<li>%s <span style="color:#6b7280">#%d</span> (%s)</li>`, html.EscapeString(name), key, at.UTC().Format("2006-01-02"))
		n++
		selectedKeys = append(selectedKeys, key)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return false, e
	}
	if email == "" || n == 0 {
		return true, finish("skipped")
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	if kind == "risk" {
		_, e = tx.ExecContext(ctx, `INSERT INTO key_risk_deliveries(outbox_id,event_id,generation,user_id,key_ids)
 SELECT $1,i.event_id,i.generation,$2,array_agg(DISTINCT i.key_id) FROM key_risk_outbox_items i
 JOIN key_risk_events v ON v.id=i.event_id AND v.generation=i.generation AND v.state='pending' AND v.rule='key_ip_spread'
 JOIN key_risk_recipients r ON r.event_id=i.event_id AND r.generation=i.generation AND r.user_id=$2
 WHERE i.outbox_id=$1 AND i.key_id=ANY($3) AND r.acknowledged_at IS NULL
 AND (SELECT count(*) FROM key_risk_deliveries d WHERE d.event_id=r.event_id AND d.generation=r.generation AND d.user_id=r.user_id)<$4
 GROUP BY i.event_id,i.generation`, id, user, pq.Array(selectedKeys), c.MaxReminders)
		if e != nil {
			return false, e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE key_risk_outbox SET state='sending',attempts=attempts+1,updated_at=now() WHERE id=$1`, id); e != nil {
		return false, e
	}
	// Only the actual send boundary is ambiguous across crashes.
	if e = tx.Commit(); e != nil {
		return false, e
	}
	subject := "API Key 安全提醒：建议轮换"
	intro := "以下密钥已达到建议轮换周期。请登录站点创建新密钥，更新客户端后停用旧密钥。"
	if kind == "risk" {
		subject = "API Key 跨 IP 使用风险提醒"
		intro = "以下密钥在检测窗口内来自多个 IP。请核查是否为已授权的客户端或出口；如有异常来源，请配置此 Key 的 IP 白名单或替换密钥。可在站点的 API 密钥页面查看风险提醒，并选择“我已知晓，暂停本次提醒”。确认仅暂停本次事件的后续提醒，不表示密钥已安全；已开始发送的邮件可能仍会到达。"
	}
	send, stop := context.WithTimeout(ctx, 10*time.Second)
	e = s.Mail.SendEmail(send, email, subject, "<p>"+intro+"</p><ul>"+body.String()+"</ul>")
	stop()
	state := "sent"
	if e != nil {
		state = "uncertain"
	}
	// Persist the result even if the sweep budget expired during SMTP.
	record, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, e = s.DB.ExecContext(record, `WITH finished AS (UPDATE key_risk_outbox SET state=$1,updated_at=now() WHERE id=$2 AND state='sending' RETURNING id) UPDATE key_risk_deliveries SET state=$1 WHERE outbox_id IN (SELECT id FROM finished)`, state, id)
	return true, e
}

// RestrictionTarget binds an administrator action to one current key episode.
// Ownership is checked again by APIKeyService.Update before writing its ACL.
func (s *Service) RestrictionTarget(ctx context.Context, event, generation int64) (int64, int64, error) {
	var keyID, owner int64
	err := s.DB.QueryRowContext(ctx, `SELECT k.id,k.user_id FROM key_risk_events e
 JOIN api_keys k ON e.subject=k.id::text
 WHERE e.id=$1 AND e.generation=$2 AND e.rule='key_ip_spread' AND e.state='pending'
 AND k.deleted_at IS NULL AND k.status='active' AND (k.expires_at IS NULL OR k.expires_at>now())
 AND e.evidence->'key_ids' @> jsonb_build_array(k.id)
 AND e.evidence->'user_ids' @> jsonb_build_array(k.user_id)`, event, generation).Scan(&keyID, &owner)
	return keyID, owner, err
}
