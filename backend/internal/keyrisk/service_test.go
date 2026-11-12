package keyrisk

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

type fakeMail struct {
	body, to []string
	fail     bool
}

func (m *fakeMail) SendEmail(_ context.Context, to, subject, body string) error {
	m.body = append(m.body, body)
	m.to = append(m.to, to)
	if m.fail {
		return fmt.Errorf("ambiguous SMTP")
	}
	return nil
}
func fixture(t *testing.T) (*Service, *fakeMail) {
	t.Helper()
	dsn := os.Getenv("KEYRISK_TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("KEYRISK_TEST_REQUIRED") == "true" {
			t.Fatal("KEYRISK_TEST_POSTGRES_DSN is required for key risk acceptance")
		}
		t.Skip("requires isolated local KEYRISK_TEST_POSTGRES_DSN")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Path != "/keyrisk_task" || u.Hostname() != "127.0.0.1" {
		t.Fatal("only isolated local keyrisk_task allowed")
	}
	root, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("keyrisk_%d", time.Now().UnixNano())
	if _, e = root.Exec("CREATE SCHEMA " + schema); e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := sql.Open("postgres", u.String())
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(2)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if _, err := root.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	m := &fakeMail{}
	s := &Service{DB: db, Mail: m}
	exec(t, s, `CREATE TABLE users(id bigint PRIMARY KEY,email text,username text DEFAULT '',status text DEFAULT 'active',deleted_at timestamptz);CREATE TABLE api_keys(id bigint PRIMARY KEY,user_id bigint,key text DEFAULT 'sk-keyrisk-fixture',name text,status text DEFAULT 'active',created_at timestamptz DEFAULT now(),updated_at timestamptz DEFAULT now(),deleted_at timestamptz,expires_at timestamptz,ip_whitelist jsonb,ip_blacklist jsonb);CREATE TABLE usage_logs(id bigserial PRIMARY KEY,ip_address text,user_id bigint,api_key_id bigint,created_at timestamptz DEFAULT now());`)
	raw, e := os.ReadFile("../../migrations/333_key_risk.sql")
	if e != nil {
		t.Fatal(e)
	}
	exec(t, s, string(raw))
	upgrade, e := os.ReadFile("../../migrations/334_key_risk_delivery_evidence.sql")
	if e != nil {
		t.Fatal(e)
	}
	exec(t, s, string(upgrade))
	ack, e := os.ReadFile("../../migrations/336_key_risk_acknowledgements.sql")
	if e != nil {
		t.Fatal(e)
	}
	exec(t, s, string(ack))
	exec(t, s, `INSERT INTO users(id,email) VALUES(1,'one@example.invalid'),(2,'two@example.invalid'),(3,'three@example.invalid');INSERT INTO api_keys(id,user_id,name,created_at) VALUES(11,1,'<owner one>',now()-interval '100 days'),(22,2,'OWNER TWO',now()-interval '100 days'),(33,3,'OWNER THREE',now()-interval '100 days');INSERT INTO usage_logs(ip_address,user_id,api_key_id) VALUES('192.0.2.1',1,11),('192.0.2.2',1,11),('192.0.2.1',2,22),('192.0.2.1',3,33)`)
	return s, m
}
func exec(t *testing.T, s *Service, q string, args ...any) {
	t.Helper()
	if _, e := s.DB.Exec(q, args...); e != nil {
		t.Fatal(e)
	}
}
func run(t *testing.T, s *Service) {
	t.Helper()
	if e := s.RunOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresObserveManualMailAndRestart(t *testing.T) {
	s, m := fixture(t)
	run(t, s)
	v, e := s.Events(context.Background(), 0)
	if e != nil || len(v) != 1 {
		t.Fatalf("%+v %v", v, e)
	}
	if len(m.body) != 0 {
		t.Fatal("observe sent mail")
	}
	if e = s.Notify(context.Background(), v[0].ID, 99); e != nil {
		t.Fatal(e)
	}
	run(t, &Service{DB: s.DB, Mail: m})
	if len(m.body) != 1 {
		t.Fatalf("mail count %d", len(m.body))
	}
	for i, b := range m.body {
		if m.to[i] == "one@example.invalid" && (!strings.Contains(b, "&lt;owner one&gt;") || strings.Contains(b, "OWNER TWO")) {
			t.Fatal("privacy/escaping failure")
		}
	}
	if e = s.Notify(context.Background(), v[0].ID, 99); e != nil {
		t.Fatal(e)
	}
	run(t, s)
	if len(m.body) != 1 {
		t.Fatal("duplicate mail")
	}
	if e = s.SetState(context.Background(), v[0].ID, 99, "ignored"); e != nil {
		t.Fatal(e)
	}
	run(t, s)
	v, _ = s.Events(context.Background(), 0)
	if v[0].State != "ignored" {
		t.Fatal("administrator decision lost")
	}
}
func TestPostgresAgeAndSMTPUncertainty(t *testing.T) {
	s, m := fixture(t)
	c := DefaultConfig()
	c.AgeEmail = true
	if e := s.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	m.fail = true
	run(t, s)
	run(t, s)
	if len(m.body) != 3 {
		t.Fatalf("mail count %d", len(m.body))
	}
	var n int
	if e := s.DB.QueryRow("SELECT count(*) FROM key_risk_outbox WHERE state='uncertain'").Scan(&n); e != nil || n != 3 {
		t.Fatalf("uncertain %d %v", n, e)
	}
}
func TestPostgresAutoGateAndCrash(t *testing.T) {
	s, m := fixture(t)
	exec(t, s, `INSERT INTO usage_logs(ip_address,user_id,api_key_id) VALUES('192.0.2.2',1,11),('192.0.2.3',1,11),('192.0.2.4',1,11),('192.0.2.5',1,11)`)
	c := DefaultConfig()
	c.AutoEmail = true
	if e := s.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	exec(t, s, "UPDATE usage_logs SET created_at=now()-(user_id*120)*interval '1 second'")
	run(t, s)
	if len(m.body) != 1 || m.to[0] != "one@example.invalid" {
		t.Fatal("auto gate failed")
	}
	exec(t, s, `INSERT INTO key_risk_outbox(user_id,kind,bucket,state,updated_at) VALUES(2,'risk',1,'sending',now()-interval '11 minutes')`)
	run(t, s)
	var state string
	if e := s.DB.QueryRow("SELECT state FROM key_risk_outbox WHERE bucket=1").Scan(&state); e != nil || state != "uncertain" {
		t.Fatalf("crash: %s %v", state, e)
	}
}
func TestPostgresDisabledAndTrusted(t *testing.T) {
	s, m := fixture(t)
	c := DefaultConfig()
	c.Enabled = false
	if e := s.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	run(t, s)
	v, _ := s.Events(context.Background(), 0)
	if len(v) != 0 || len(m.body) != 0 {
		t.Fatal("disabled ran")
	}
	c.Enabled = true
	c.TrustedNetworks = []string{"192.0.2.0/24"}
	if e := s.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	run(t, s)
	v, _ = s.Events(context.Background(), 0)
	if len(v) != 0 {
		t.Fatal("trusted flagged")
	}
}

func TestPostgresCoverageAndRevokedKeys(t *testing.T) {
	s, m := fixture(t)
	c := DefaultConfig()
	c.AutoEmail = true
	if e := s.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	exec(t, s, `INSERT INTO usage_logs(ip_address,user_id,api_key_id) SELECT '192.0.2.2',1,11 FROM generate_series(1,10001)`)
	run(t, s)
	var issue string
	if e := s.DB.QueryRow("SELECT last_error FROM key_risk_control").Scan(&issue); e != nil || !strings.HasPrefix(issue, "coverage_incomplete") {
		t.Fatalf("coverage: %s %v", issue, e)
	}
	if len(m.body) != 0 {
		t.Fatal("incomplete coverage sent automatic email")
	}
	c.AutoEmail = false
	if e := s.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	exec(t, s, "DELETE FROM usage_logs WHERE id>4")
	run(t, s)
	events, e := s.Events(context.Background(), 0)
	if e != nil || len(events) == 0 {
		t.Fatal("missing event", e)
	}
	if e = s.Notify(context.Background(), events[0].ID, 99); e != nil {
		t.Fatal(e)
	}
	exec(t, s, "UPDATE api_keys SET status='inactive'")
	run(t, s)
	if len(m.body) != 0 {
		t.Fatal("revoked keys included in notices")
	}
}

func TestPostgresSharedExitDoesNotEmail(t *testing.T) {
	s, m := fixture(t)
	c := DefaultConfig()
	c.AutoEmail = true
	if e := s.Save(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	exec(t, s, "DELETE FROM usage_logs WHERE ip_address='192.0.2.2'")
	run(t, s)
	events, err := s.Events(context.Background(), 0)
	if err != nil || len(events) != 0 || len(m.body) != 0 {
		t.Fatal("shared team exit generated risk", events, err)
	}
}
