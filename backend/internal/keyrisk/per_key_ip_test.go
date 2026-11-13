package keyrisk

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
)

func TestCrossIPMailTargetsOnlyTheKeysOwner(t *testing.T) {
	s, m := fixture(t)
	cfg := DefaultConfig()
	cfg.AutoEmail = true
	if err := s.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	run(t, s)
	if len(m.to) != 1 || m.to[0] != "one@example.invalid" {
		t.Fatalf("mail attribution: %v", m.to)
	}
	events, err := s.Events(context.Background(), 0)
	if err != nil || len(events) != 1 || events[0].Rule != "key_ip_spread" || events[0].Subject != "11" {
		t.Fatal("wrong event", events, err)
	}
}

func TestExplicitlyAllowedMultiExitKeysDoNotAlert(t *testing.T) {
	for _, tt := range []struct {
		name, allow, deny string
		want              int
	}{
		{"two approved exits", `["192.0.2.1","192.0.2.2"]`, `[]`, 0},
		{"approved CIDR", `["192.0.2.0/24"]`, `[]`, 0},
		{"one unexpected exit", `["192.0.2.1"]`, `[]`, 1},
		{"blacklist takes precedence", `["192.0.2.0/24"]`, `["192.0.2.2"]`, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, m := fixture(t)
			exec(t, s, "UPDATE api_keys SET ip_whitelist=$1,ip_blacklist=$2 WHERE id=11", tt.allow, tt.deny)
			cfg := DefaultConfig()
			cfg.AutoEmail = true
			if err := s.Save(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			run(t, s)
			events, err := s.Events(context.Background(), 0)
			if err != nil || len(events) != tt.want || len(m.to) != tt.want {
				t.Fatalf("events=%v mail=%v err=%v", events, m.to, err)
			}
		})
	}
}

func TestRestrictionRequiresCurrentLiveOwnedKey(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	run(t, s)
	events, err := s.Events(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	ev := events[0]
	key, err := s.RestrictIPWhitelist(ctx, ev.ID, ev.Generation, []string{"192.0.2.0/24"})
	if err != nil || key != "sk-keyrisk-fixture" {
		t.Fatalf("bad restriction %q %v", key, err)
	}
	if _, err = s.RestrictIPWhitelist(ctx, ev.ID, ev.Generation+1, []string{"198.51.100.0/24"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("stale generation accepted", err)
	}
	for _, update := range []string{
		"UPDATE api_keys SET status='disabled' WHERE id=11",
		"UPDATE api_keys SET status='active',expires_at=now()-interval '1 second' WHERE id=11",
		"UPDATE api_keys SET expires_at=NULL,deleted_at=now() WHERE id=11",
		"UPDATE api_keys SET deleted_at=NULL,user_id=2 WHERE id=11",
	} {
		exec(t, s, update)
		if _, err = s.RestrictIPWhitelist(ctx, ev.ID, ev.Generation, []string{"198.51.100.0/24"}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("invalid target accepted", update, err)
		}
	}
	exec(t, s, "UPDATE api_keys SET user_id=1 WHERE id=11")
	if err = s.SetState(ctx, ev.ID, 99, "resolved"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestrictIPWhitelist(ctx, ev.ID, ev.Generation, []string{"198.51.100.0/24"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("resolved episode accepted", err)
	}
}

func TestRetiredRulesCannotNotifyOrRestrictBeforeMigration(t *testing.T) {
	s, m := fixture(t)
	ctx := context.Background()
	exec(t, s, `INSERT INTO key_risk_events(rule,subject,evidence,first_seen,last_seen) VALUES
 ('cross_user_ip','192.0.2.1','{"user_ids":[1],"key_ids":[11]}',now(),now()),
 ('key_ip_spread_linked','11','{"user_ids":[1],"key_ids":[11]}',now(),now());
 INSERT INTO key_risk_recipients(event_id,generation,user_id,key_ids) SELECT id,generation,1,ARRAY[11]::bigint[] FROM key_risk_events;
 INSERT INTO key_risk_outbox(user_id,kind,bucket,manual) VALUES(1,'risk',1,true);
 INSERT INTO key_risk_outbox_items(outbox_id,event_id,generation,key_id,evidence) SELECT 1,id,generation,11,evidence FROM key_risk_events`)
	events, err := s.Events(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if _, err = s.NotifyUsers(ctx, ev.ID, 99); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("retired notice queued", err)
		}
		if err = s.SetState(ctx, ev.ID, 99, "pending"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("retired rule reopened", err)
		}
		if _, err = s.RestrictIPWhitelist(ctx, ev.ID, ev.Generation, []string{"198.51.100.0/24"}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("retired target accepted", err)
		}
	}
	if err = s.deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.to) != 0 {
		t.Fatal("retired queued mail sent")
	}
	notices, err := s.UserNotices(ctx, 1, 0)
	if err != nil || len(notices) != 0 {
		t.Fatal("retired rule shown to customer", notices, err)
	}
}

func TestPerKeyIPMigrationPreservesAuditAndValidMixedQueue(t *testing.T) {
	s, m := fixture(t)
	ctx := context.Background()
	run(t, s)
	exec(t, s, `UPDATE key_risk_control SET config='{"enabled":true,"auto_email":true,"users_per_ip":3,"ips_per_key":5}';
 INSERT INTO key_risk_events(rule,subject,evidence,first_seen,last_seen) VALUES
 ('cross_user_ip','192.0.2.1','{"user_ids":[1,2],"key_ids":[11,22]}',now(),now()),
 ('key_ip_spread_linked','11','{"user_ids":[1],"key_ids":[11]}',now(),now());
 INSERT INTO key_risk_outbox(user_id,kind,bucket,manual) VALUES(1,'risk',1,true),(2,'risk',1,true),(3,'risk',1,true);
 INSERT INTO key_risk_outbox_items(outbox_id,event_id,generation,key_id,evidence)
 SELECT o.id,e.id,e.generation,CASE WHEN o.user_id=2 THEN 22 ELSE 11 END,e.evidence
 FROM key_risk_outbox o CROSS JOIN key_risk_events e WHERE o.user_id=1 OR e.rule='cross_user_ip';
 UPDATE key_risk_outbox SET state='sending' WHERE user_id=3`)
	// Ensure current recipients exist for the valid item; legacy recipients can remain historical.
	events, err := s.Events(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatal("missing legacy history")
	}
	migration, err := os.ReadFile("../../migrations/342_key_risk_per_key_ip.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, s, string(migration))
	cfg, err := s.Config(ctx)
	if err != nil || cfg.IPsPerKey != 2 {
		t.Fatal("default upgrade failed", cfg, err)
	}
	var pending, retired, sending, skipped int
	if err = s.DB.QueryRow(`SELECT count(*) FILTER(WHERE state='pending'),count(*) FILTER(WHERE state='resolved') FROM key_risk_events`).Scan(&pending, &retired); err != nil || pending != 1 || retired != 2 {
		t.Fatalf("history changed %d %d %v", pending, retired, err)
	}
	if err = s.DB.QueryRow(`SELECT count(*) FILTER(WHERE state='sending'),count(*) FILTER(WHERE state='skipped') FROM key_risk_outbox`).Scan(&sending, &skipped); err != nil || sending != 1 || skipped != 1 {
		t.Fatal("queue upgrade failed", sending, skipped, err)
	}
	if err = s.deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.to) != 1 || m.to[0] != "one@example.invalid" {
		t.Fatal("valid mixed queue lost or retired sent", m.to)
	}
	var attempts int
	if err = s.DB.QueryRow("SELECT count(*) FROM key_risk_deliveries").Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("retired delivery counted", attempts, err)
	}
	exec(t, s, `UPDATE key_risk_control SET config=jsonb_set(config,'{ips_per_key}','7')`)
	exec(t, s, string(migration))
	cfg, err = s.Config(ctx)
	if err != nil || cfg.IPsPerKey != 7 {
		t.Fatal("custom threshold overwritten", cfg, err)
	}
}
