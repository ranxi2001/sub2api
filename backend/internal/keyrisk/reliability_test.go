package keyrisk

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPendingNotificationKeepsOriginalEvidence(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	if e := s.Notify(context.Background(), events[0].ID, 99); e != nil {
		t.Fatal(e)
	}
	exec(t, s, `INSERT INTO users(id,email) VALUES(4,'four@example.invalid');INSERT INTO api_keys(id,user_id,name) VALUES(44,4,'four');UPDATE usage_logs SET user_id=4,api_key_id=44 WHERE user_id=1`)
	run(t, s)
	if len(m.to) != 1 {
		t.Fatalf("original recipients lost: %v", m.to)
	}
	found := false
	for _, to := range m.to {
		found = found || to == "one@example.invalid"
	}
	if !found {
		t.Fatal("original victim missing")
	}
	var n int
	if e := s.DB.QueryRow(`SELECT count(*) FROM key_risk_outbox_items i JOIN key_risk_outbox o ON o.id=i.outbox_id WHERE o.user_id=1 AND i.key_id=11 AND i.evidence->'user_ids' @> '1'::jsonb`).Scan(&n); e != nil || n != 1 {
		t.Fatalf("immutable attribution missing %d %v", n, e)
	}
}

func TestPreparationFailureRetriesWithoutSMTPUncertainty(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	if e := s.Notify(context.Background(), events[0].ID, 99); e != nil {
		t.Fatal(e)
	}
	exec(t, s, "ALTER TABLE api_keys RENAME TO temporarily_unavailable")
	if e := s.deliver(context.Background()); e == nil {
		t.Fatal("expected preparation error")
	}
	var state string
	var attempts int
	if e := s.DB.QueryRow("SELECT state,attempts FROM key_risk_outbox WHERE user_id=1").Scan(&state, &attempts); e != nil {
		t.Fatal(e)
	}
	if state != "pending" || attempts != 0 || len(m.to) != 0 {
		t.Fatalf("preparation consumed delivery: %s %d %v", state, attempts, m.to)
	}
	exec(t, s, "ALTER TABLE temporarily_unavailable RENAME TO api_keys")
	if e := s.deliver(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(m.to) != 1 {
		t.Fatalf("retry lost recipient: %v", m.to)
	}
}

func TestResolvedEpisodeReopensOnlyForFreshEvidence(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	id := events[0].ID
	if e := s.SetState(context.Background(), id, 99, "resolved"); e != nil {
		t.Fatal(e)
	}
	c := DefaultConfig()
	c.AutoEmail = true
	if e := s.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	run(t, s)
	var state string
	var generation int64
	if e := s.DB.QueryRow("SELECT state,generation FROM key_risk_events WHERE id=$1", id).Scan(&state, &generation); e != nil {
		t.Fatal(e)
	}
	if state != "resolved" || generation != 1 || len(m.to) != 0 {
		t.Fatal("old window reopened resolved episode")
	}
	exec(t, s, `INSERT INTO usage_logs(ip_address,user_id,api_key_id,created_at) VALUES('192.0.2.3',1,11,now()-interval '1 minute')`)
	run(t, s)
	if e := s.DB.QueryRow("SELECT state,generation FROM key_risk_events WHERE id=$1", id).Scan(&state, &generation); e != nil {
		t.Fatal(e)
	}
	if state != "pending" || generation != 2 || len(m.to) != 1 {
		t.Fatalf("new source suppressed: %s %d %v", state, generation, m.to)
	}
	if e := s.SetState(context.Background(), id, 99, "resolved"); e != nil {
		t.Fatal(e)
	}
	exec(t, s, `UPDATE usage_logs SET created_at=clock_timestamp()`)
	run(t, s)
	if e := s.DB.QueryRow("SELECT state,generation FROM key_risk_events WHERE id=$1", id).Scan(&state, &generation); e != nil {
		t.Fatal(e)
	}
	if state != "pending" || generation != 3 {
		t.Fatal("new activity with same participants suppressed")
	}
}

func TestNoticeUsesOnlySelectedEvent(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	exec(t, s, `INSERT INTO api_keys(id,user_id,name) VALUES(12,1,'unselected-key');INSERT INTO key_risk_events(rule,subject,evidence,first_seen,last_seen) VALUES('key_ip_spread','12','{"user_ids":[1],"key_ids":[12]}',now(),now())`)
	if e := s.Notify(context.Background(), events[0].ID, 99); e != nil {
		t.Fatal(e)
	}
	if e := s.deliver(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, body := range m.body {
		if strings.Contains(body, "unselected-key") {
			t.Fatal("unselected evidence leaked into notice")
		}
	}
}

func TestUpgradeBackfillsPendingEvidence(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	exec(t, s, `DROP TABLE key_risk_outbox_items;ALTER TABLE key_risk_events DROP COLUMN generation;INSERT INTO key_risk_outbox(user_id,kind,bucket,manual) VALUES(1,'risk',1,true)`)
	raw, e := os.ReadFile("../../migrations/334_key_risk_delivery_evidence.sql")
	if e != nil {
		t.Fatal(e)
	}
	exec(t, s, string(raw))
	if e = s.deliver(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(m.to) != 1 || m.to[0] != "one@example.invalid" {
		t.Fatal("legacy pending notice lost", m.to)
	}
}

type blockingMail struct {
	started    chan struct{}
	release    chan struct{}
	mu         sync.Mutex
	recipients []string
}

func (m *blockingMail) SendEmail(ctx context.Context, to, subject, body string) error {
	m.mu.Lock()
	m.recipients = append(m.recipients, to)
	m.mu.Unlock()
	if to == "one@example.invalid" {
		close(m.started)
		select {
		case <-m.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func TestConcurrentDeliveryDoesNotDuplicateClaims(t *testing.T) {
	s, _ := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	if e := s.Notify(context.Background(), events[0].ID, 99); e != nil {
		t.Fatal(e)
	}
	m := &blockingMail{started: make(chan struct{}), release: make(chan struct{})}
	s.Mail = m
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.deliver(ctx) }()
	select {
	case <-m.started:
	case <-ctx.Done():
		t.Fatal("first sender did not start")
	}
	if e := s.deliver(ctx); e != nil {
		t.Error(e)
	}
	close(m.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if len(m.recipients) != 1 {
		t.Fatalf("duplicate delivery: %v", m.recipients)
	}
}

func TestPendingNoticeMergesAdditionalSelectedKeys(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	if e := s.Notify(context.Background(), events[0].ID, 99); e != nil {
		t.Fatal(e)
	}
	exec(t, s, `INSERT INTO api_keys(id,user_id,name) VALUES(12,1,'additional-key');INSERT INTO key_risk_events(rule,subject,evidence,first_seen,last_seen) VALUES('key_ip_spread','12','{"user_ids":[1],"key_ids":[12]}',now(),now())`)
	var eventID int64
	if e := s.DB.QueryRow("SELECT id FROM key_risk_events WHERE subject='12'").Scan(&eventID); e != nil {
		t.Fatal(e)
	}
	if e := s.Notify(context.Background(), eventID, 99); e != nil {
		t.Fatal(e)
	}
	if e := s.deliver(context.Background()); e != nil {
		t.Fatal(e)
	}
	for i, to := range m.to {
		if to == "one@example.invalid" {
			if !strings.Contains(m.body[i], "additional-key") || !strings.Contains(m.body[i], "&lt;owner one&gt;") {
				t.Fatal("coalescing lost selected keys")
			}
			return
		}
	}
	t.Fatal("recipient missing")
}

func TestRiskNoticeIncludesAllKeysBeyondFifty(t *testing.T) {
	s, m := fixture(t)
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.MaxReminders = 1
	if err := s.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	exec(t, s, `INSERT INTO api_keys(id,user_id,name) SELECT id,1,'overflow-'||id FROM generate_series(1001,1050) id;
 INSERT INTO usage_logs(ip_address,user_id,api_key_id) SELECT ip,1,id FROM generate_series(1001,1050) id CROSS JOIN (VALUES('192.0.2.1'),('192.0.2.2')) ips(ip)`)
	run(t, s)
	rows, err := s.DB.Query("SELECT id FROM key_risk_events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(ids) != 51 {
		t.Fatalf("expected independent key events, got %d", len(ids))
	}
	for _, id := range ids {
		if err = s.Notify(ctx, id, 99); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.to) != 1 || m.to[0] != "one@example.invalid" || strings.Count(m.body[0], "<li>") != 51 || !strings.Contains(m.body[0], "overflow-1050") {
		t.Fatal("coalesced mail omitted selected keys", m.to)
	}
	var recorded int
	if err = s.DB.QueryRow("SELECT count(*) FROM key_risk_deliveries WHERE user_id=1").Scan(&recorded); err != nil || recorded != 51 {
		t.Fatalf("lost per-key delivery proof: %d %v", recorded, err)
	}
	exec(t, s, "DELETE FROM key_risk_outbox")
	exec(t, s, "UPDATE key_risk_deliveries SET attempted_at=attempted_at-interval '25 hours'")
	for _, id := range ids {
		queued, err := s.NotifyUsers(ctx, id, 99)
		if err != nil || queued != 0 {
			t.Fatalf("reminder cap bypassed: %d %v", queued, err)
		}
	}
}
