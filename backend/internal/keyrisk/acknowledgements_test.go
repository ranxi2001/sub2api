package keyrisk

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReminderLimitSurvivesOutboxCleanupAndRestart(t *testing.T) {
	s, m := fixture(t)
	cfg := DefaultConfig()
	cfg.AutoEmail = true
	cfg.MaxReminders = 2
	if err := s.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		run(t, s)
		// Expired outbox history must not reset the persistent event limit.
		exec(t, s, "DELETE FROM key_risk_outbox")
		exec(t, s, "UPDATE key_risk_deliveries SET attempted_at=attempted_at-interval '25 hours'")
		s = &Service{DB: s.DB, Mail: m}
	}
	if len(m.to) != 6 {
		t.Fatalf("wanted two notices per user, got %d", len(m.to))
	}
	events, err := s.Events(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range events[0].Recipients {
		if r.Attempts != 2 || r.Sent != 2 {
			t.Fatalf("bad proof: %+v", r)
		}
	}
	n, err := s.NotifyUsers(context.Background(), events[0].ID, 99)
	if err != nil || n != 0 {
		t.Fatalf("manual bypassed limit: %d %v", n, err)
	}
}

func TestAcknowledgementIsOwnedIdempotentAndSuppressesQueuedMail(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	ctx := context.Background()
	events, _ := s.Events(ctx, 0)
	ev := events[0]
	if err := s.Acknowledge(ctx, 999, ev.ID, ev.Generation); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign acknowledgement: %v", err)
	}
	if err := s.Acknowledge(ctx, 1, ev.ID, ev.Generation+1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale generation: %v", err)
	}
	if err := s.Notify(ctx, ev.ID, 99); err != nil {
		t.Fatal(err)
	}
	if err := s.Acknowledge(ctx, 1, ev.ID, ev.Generation); err != nil {
		t.Fatal(err)
	}
	notices, err := s.UserNotices(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || len(notices[0].KeyIDs) != 1 || notices[0].KeyIDs[0] != 11 || notices[0].AcknowledgedAt == nil {
		t.Fatalf("owner view: %+v", notices)
	}
	first := *notices[0].AcknowledgedAt
	var proof string
	if err = s.DB.QueryRow("SELECT acknowledgement::text FROM key_risk_recipients WHERE user_id=1 AND event_id=$1", ev.ID).Scan(&proof); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(proof, `"key_ids": [11]`) || !strings.Contains(proof, `"notice_version": 1`) {
		t.Fatalf("missing private acknowledgement snapshot: %s", proof)
	}
	if err := s.Acknowledge(ctx, 1, ev.ID, ev.Generation); err != nil {
		t.Fatal(err)
	}
	notices, _ = s.UserNotices(ctx, 1, 0)
	if !first.Equal(*notices[0].AcknowledgedAt) {
		t.Fatal("duplicate changed confirmation timestamp")
	}
	if err := s.deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.to) != 2 {
		t.Fatalf("queued notice not suppressed: %v", m.to)
	}
	for _, to := range m.to {
		if to == "one@example.invalid" {
			t.Fatal("confirmed user received email")
		}
	}
	events, _ = s.Events(ctx, 0)
	if events[0].State != "pending" {
		t.Fatal("confirmation resolved shared risk")
	}
	foreign, err := s.UserNotices(ctx, 999, 0)
	if err != nil || len(foreign) != 0 {
		t.Fatal("foreign events exposed")
	}
}

func TestNewEpisodeRemindsAgainWithoutErasingAcknowledgement(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	ctx := context.Background()
	events, _ := s.Events(ctx, 0)
	ev := events[0]
	if err := s.Acknowledge(ctx, 1, ev.ID, ev.Generation); err != nil {
		t.Fatal(err)
	}
	if err := s.SetState(ctx, ev.ID, 99, "resolved"); err != nil {
		t.Fatal(err)
	}
	// Fresh request after resolution creates the next generation.
	exec(t, s, "INSERT INTO usage_logs(ip_address,user_id,api_key_id) VALUES('192.0.2.1',1,11)")
	cfg := DefaultConfig()
	cfg.AutoEmail = true
	if err := s.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	run(t, s)
	if len(m.to) != 3 {
		t.Fatalf("new event was muted: %v", m.to)
	}
	if err := s.Acknowledge(ctx, 1, ev.ID, ev.Generation); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("old confirmation applied to new generation")
	}
	events, _ = s.Events(ctx, 0)
	found := false
	for _, r := range events[0].Recipients {
		if r.UserID == 1 && r.Generation == ev.Generation && r.AcknowledgedAt != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("historical acknowledgement lost")
	}
}

func TestUncertainAttemptsCountButNeverClaimAcknowledgement(t *testing.T) {
	s, m := fixture(t)
	m.fail = true
	cfg := DefaultConfig()
	cfg.AutoEmail = true
	cfg.MaxReminders = 1
	if err := s.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	run(t, s)
	exec(t, s, "DELETE FROM key_risk_outbox")
	exec(t, s, "UPDATE key_risk_deliveries SET attempted_at=attempted_at-interval '25 hours'")
	run(t, s)
	if len(m.to) != 3 {
		t.Fatal("ambiguous attempts retried beyond limit")
	}
	ev, err := s.Events(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ev[0].Recipients {
		if r.Uncertain != 1 || r.Sent != 0 || r.AcknowledgedAt != nil {
			t.Fatalf("false proof: %+v", r)
		}
	}
}

func TestUpgradePreservesPriorDeliveryAttempts(t *testing.T) {
	s, _ := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	if err := s.Notify(context.Background(), events[0].ID, 99); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	exec(t, s, "DROP TABLE key_risk_deliveries;DROP TABLE key_risk_recipients")
	migration, err := os.ReadFile("../../migrations/336_key_risk_acknowledgements.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, s, string(migration))
	events, err = s.Events(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range events[0].Recipients {
		if r.Attempts != 1 || r.Sent != 1 {
			t.Fatalf("upgrade lost history: %+v", r)
		}
	}
}

func TestManualSuppressionReportsNoQueuedRecipients(t *testing.T) {
	s, _ := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	n, err := s.NotifyUsers(context.Background(), events[0].ID, 99)
	if err != nil || n != 3 {
		t.Fatalf("first queue: %d %v", n, err)
	}
	if err = s.deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err = s.NotifyUsers(context.Background(), events[0].ID, 99)
	if err != nil || n != 0 {
		t.Fatalf("false queued result: %d %v", n, err)
	}
}

func TestAcknowledgementCannotLeakOtherOwnersKeys(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	events, _ := s.Events(context.Background(), 0)
	if err := s.Notify(context.Background(), events[0].ID, 99); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, to := range m.to {
		if to == "one@example.invalid" && (strings.Contains(m.body[i], "OWNER TWO") || !strings.Contains(m.body[i], "暂停本次提醒")) {
			t.Fatal("unsafe or unactionable email")
		}
	}
}

func TestAcknowledgementDuringInFlightMailStopsLaterReminders(t *testing.T) {
	s, _ := fixture(t)
	run(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, _ := s.Events(ctx, 0)
	ev := events[0]
	if err := s.Notify(ctx, ev.ID, 99); err != nil {
		t.Fatal(err)
	}
	m := &blockingMail{started: make(chan struct{}), release: make(chan struct{})}
	s.Mail = m
	done := make(chan error, 1)
	go func() { done <- s.deliver(ctx) }()
	select {
	case <-m.started:
	case <-ctx.Done():
		t.Fatal("sender did not start")
	}
	if err := s.Acknowledge(ctx, 1, ev.ID, ev.Generation); err != nil {
		close(m.release)
		t.Fatal(err)
	}
	close(m.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	exec(t, s, "DELETE FROM key_risk_outbox")
	exec(t, s, "UPDATE key_risk_deliveries SET attempted_at=attempted_at-interval '25 hours'")
	n, err := s.NotifyUsers(ctx, ev.ID, 99)
	if err != nil || n != 2 {
		t.Fatalf("confirmed user queued again: %d %v", n, err)
	}
	notices, err := s.UserNotices(ctx, 1, 0)
	if err != nil || notices[0].AcknowledgedAt == nil || notices[0].Attempts != 1 {
		t.Fatalf("in-flight evidence lost: %+v %v", notices, err)
	}
}

func TestRecentAcknowledgementRetainedForOldResolvedEvent(t *testing.T) {
	s, _ := fixture(t)
	run(t, s)
	ctx := context.Background()
	events, _ := s.Events(ctx, 0)
	ev := events[0]
	if err := s.Acknowledge(ctx, 1, ev.ID, ev.Generation); err != nil {
		t.Fatal(err)
	}
	exec(t, s, "DELETE FROM usage_logs")
	exec(t, s, "UPDATE key_risk_events SET state='resolved',last_seen=now()-interval '400 days',updated_at=now()-interval '400 days'")
	run(t, s)
	events, err := s.Events(ctx, 0)
	if err != nil || len(events) != 1 {
		t.Fatal("recent confirmation purged", err)
	}
	exec(t, s, "UPDATE key_risk_recipients SET acknowledged_at=now()-interval '400 days' WHERE acknowledged_at IS NOT NULL")
	run(t, s)
	events, err = s.Events(ctx, 0)
	if err != nil || len(events) != 0 {
		t.Fatal("expired terminal proof not cleaned", err)
	}
}

func TestRiskCooldownUsesActualAttemptTime(t *testing.T) {
	s, _ := fixture(t)
	run(t, s)
	ctx := context.Background()
	events, _ := s.Events(ctx, 0)
	if err := s.Notify(ctx, events[0].ID, 99); err != nil {
		t.Fatal(err)
	}
	exec(t, s, "UPDATE key_risk_outbox SET created_at=now()-interval '25 hours',bucket=bucket-1")
	if err := s.deliver(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := s.NotifyUsers(ctx, events[0].ID, 99)
	if err != nil || n != 0 {
		t.Fatalf("enqueue age bypassed actual-send cooldown: %d %v", n, err)
	}
}

func TestRiskDisplayNamesAndOwnerIsolation(t *testing.T) {
	s, _ := fixture(t)
	exec(t, s, "UPDATE users SET username='Customer One' WHERE id=1")
	run(t, s)
	events, err := s.Events(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	event := events[0]
	if event.Users[0].Name != "Customer One" || event.Users[1].Name != "two@example.invalid" {
		t.Fatalf("username/email fallback: %+v", event.Users)
	}
	if event.Keys[0].Name != "<owner one>" || event.Recipients[0].UserName != "Customer One" {
		t.Fatal("missing key or recipient name")
	}
	notices, err := s.UserNotices(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices[0].Keys) != 1 || notices[0].Keys[0].ID != 11 || notices[0].Keys[0].Name != "<owner one>" {
		t.Fatalf("private names not scoped to owner: %+v", notices)
	}
	exec(t, s, "DELETE FROM api_keys WHERE id=22")
	events, err = s.Events(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Keys[1].ID != 22 || events[0].Keys[1].Name != "" {
		t.Fatal("deleted key lost its audit ID")
	}
}
