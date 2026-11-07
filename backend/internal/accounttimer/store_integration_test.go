//go:build integration

package accounttimer

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
)

// Minimal isolated fixtures, not the application migration runner. Deliberately
// include safety/financial sentinels to catch accidental writes outside schedulable.
const timerFixture = `
CREATE TABLE accounts (
 id bigint PRIMARY KEY, status text NOT NULL DEFAULT 'active', schedulable boolean NOT NULL DEFAULT false,
 expires_at timestamptz, deleted_at timestamptz, updated_at timestamptz NOT NULL DEFAULT now(),
 temp_unschedulable_until timestamptz, rate_limit_reset_at timestamptz,
 quota_used numeric NOT NULL DEFAULT 17, quality_reason text NOT NULL DEFAULT 'quality-isolation',
 manual_ip_acl text NOT NULL DEFAULT 'deny-fixture', balance numeric NOT NULL DEFAULT 23
);
CREATE TABLE account_schedulable_timers (
 account_id bigint NOT NULL, action text NOT NULL CHECK(action IN ('open','close')),
 version bigint NOT NULL CHECK(version>0), execute_at timestamptz NOT NULL, timezone text NOT NULL,
 requested_by bigint NOT NULL, status text NOT NULL CHECK(status IN ('pending','executed','skipped','cancelled','failed')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 3), next_attempt_at timestamptz NOT NULL,
 actual_at timestamptz, reason text NOT NULL DEFAULT '', PRIMARY KEY(account_id,action)
);
CREATE INDEX timer_fixture_due ON account_schedulable_timers(next_attempt_at,account_id) WHERE status='pending';
CREATE UNIQUE INDEX timer_fixture_conflict ON account_schedulable_timers(account_id,execute_at) WHERE status='pending';
CREATE TABLE account_schedulable_timer_audit (
 id bigserial PRIMARY KEY, account_id bigint NOT NULL, action text NOT NULL, version bigint NOT NULL,
 actor_id bigint NOT NULL, execute_at timestamptz NOT NULL, timezone text NOT NULL,
 actual_at timestamptz NOT NULL, result text NOT NULL, reason text NOT NULL DEFAULT ''
);
CREATE TABLE fixture_outbox(id bigserial PRIMARY KEY, account_id bigint NOT NULL);
INSERT INTO accounts(id,temp_unschedulable_until,rate_limit_reset_at) VALUES(7,'2030-01-01Z','2030-01-02Z');
`

func integrationStore(t *testing.T) (*Store, *time.Time, *atomic.Int64) {
	t.Helper()
	dsn := os.Getenv("ACCOUNT_TIMER_TEST_DSN")
	if dsn == "" {
		t.Skip("ACCOUNT_TIMER_TEST_DSN not set; disposable loopback fixture required")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Path != "/schedulable_timer_fixture" {
		t.Fatal("test DSN must target loopback /schedulable_timer_fixture")
	}
	root, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	schema := fmt.Sprintf("timer_fixture_%d", time.Now().UnixNano())
	if _, err = root.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, e := root.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
		if e != nil {
			t.Error(e)
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("TimeZone", "Asia/Shanghai")
	u.RawQuery = query.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(6)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(timerFixture); err != nil {
		t.Fatal(err)
	}
	dailyMigration, err := os.ReadFile("../../migrations/335_account_daily_schedulable_timers.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(dailyMigration)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	notified := &atomic.Int64{}
	s := &Store{DB: db, Now: func() time.Time { return now }, Policy: DefaultPolicy(), Enqueue: func(ctx context.Context, tx *sql.Tx, id int64) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO fixture_outbox(account_id) VALUES($1)", id)
		return err
	}, AfterCommit: func(context.Context, int64) { notified.Add(1) }}
	return s, &now, notified
}
func saveRelative(t *testing.T, s *Store, action string, hours int64) Task {
	t.Helper()
	task, err := s.Save(context.Background(), 7, 9, SaveRequest{Input: Input{Mode: "relative", Hours: hours, Timezone: "Asia/Shanghai"}, Action: action})
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func snapshotTask(t *testing.T, s *Store, action string) Task {
	t.Helper()
	snap, err := s.List(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range snap.Tasks {
		if task.Action == action {
			return task
		}
	}
	t.Fatal("missing task")
	return Task{}
}
func scalar(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestIntegrationPersistRestartMultiInstanceAndSafetyGates(t *testing.T) {
	s, now, notified := integrationStore(t)
	open := saveRelative(t, s, "open", 8)
	saveRelative(t, s, "close", 9)
	if open.ExecuteAt.Format(time.RFC3339) != "2026-10-22T08:00:00Z" || open.Timezone != "Asia/Shanghai" {
		t.Fatal(open)
	}
	listed := snapshotTask(t, s, "open")
	if listed.ExecuteAt.Location() != time.UTC || listed.LocalDate != "2026-10-22" || listed.LocalTime != "16:00" || listed.Offset != "+08:00" {
		t.Fatalf("server/database timezone leaked into UTC output: %+v", listed)
	}
	// A fresh Store is a simulated process restart; the DB owns pending work.
	restarted := *s
	*now = now.Add(8 * time.Hour)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- restarted.RunDue(context.Background()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 1 || scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox") != 1 || notified.Load() != 1 {
		t.Fatal("logical effect and cache publication must happen once")
	}
	if scalar(t, s.DB, "SELECT count(*) FROM account_schedulable_timer_audit WHERE result='executed' AND actor_id=9") != 1 {
		t.Fatal("execution audit missing/duplicated")
	}
	if scalar(t, s.DB, `SELECT count(*) FROM accounts WHERE quota_used=17 AND quality_reason='quality-isolation' AND manual_ip_acl='deny-fixture' AND balance=23 AND temp_unschedulable_until='2030-01-01Z' AND rate_limit_reset_at='2030-01-02Z'`) != 1 {
		t.Fatal("safety or financial sentinel changed")
	}
	*now = now.Add(24 * time.Hour)
	if err := s.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 0 || snapshotTask(t, s, "close").Status != "executed" {
		t.Fatal("offline close must catch up")
	}
}
func TestIntegrationConflictsReplacementCancellationAndManualOverride(t *testing.T) {
	s, now, _ := integrationStore(t)
	ctx := context.Background()
	open := saveRelative(t, s, "open", 8)
	_, err := s.Save(ctx, 7, 9, SaveRequest{Input: Input{Mode: "relative", Hours: 8, Timezone: "UTC"}, Action: "close"})
	if ErrorCode(err) != "conflict" {
		t.Fatalf("%v", err)
	}
	closeTask := saveRelative(t, s, "close", 9)
	edit := SaveRequest{Input: Input{Mode: "relative", Hours: 10, Timezone: "UTC"}, Action: "open", ExpectedVersion: open.Version}
	replacement, err := s.Save(ctx, 7, 10, edit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, 7, 9, edit); ErrorCode(err) != "version" {
		t.Fatalf("old editor accepted: %v", err)
	}
	if err = s.Cancel(ctx, 7, 9, "open", open.Version); ErrorCode(err) != "version" {
		t.Fatalf("old cancel accepted: %v", err)
	}
	*now = now.Add(8 * time.Hour)
	if err = s.execute(ctx, open); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox") != 0 {
		t.Fatal("stale claimed task changed account")
	}
	if err = s.Cancel(ctx, 7, 9, "close", closeTask.Version); err != nil {
		t.Fatal(err)
	}
	if snapshotTask(t, s, "open").Version != replacement.Version {
		t.Fatal("opposite cancellation affected open")
	}
	if err = s.Manual(ctx, 7, 9, false); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(24 * time.Hour)
	if err = s.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM account_schedulable_timers WHERE status='pending'") != 0 || scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 0 {
		t.Fatal("old timers reopened a manual close")
	}
}
func TestIntegrationSkipDisabledExpiredDeletedAndLateOpens(t *testing.T) {
	for _, kind := range []string{"disabled", "expired", "soft_deleted", "hard_deleted", "late"} {
		t.Run(kind, func(t *testing.T) {
			s, now, _ := integrationStore(t)
			saveRelative(t, s, "open", 1)
			*now = now.Add(time.Hour)
			query := ""
			reason := ""
			switch kind {
			case "disabled":
				query = "UPDATE accounts SET status='disabled'"
				reason = "account_inactive"
			case "expired":
				query = "UPDATE accounts SET expires_at='2026-10-21Z'"
				reason = "account_expired"
			case "soft_deleted":
				query = "UPDATE accounts SET deleted_at=now()"
				reason = "account_deleted"
			case "hard_deleted":
				query = "DELETE FROM accounts"
				reason = "account_deleted"
			case "late":
				*now = now.Add(16 * time.Minute)
				reason = "open_expired"
			}
			if query != "" {
				if _, err := s.DB.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RunDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			task := snapshotTask(t, s, "open")
			if task.Status != "skipped" || task.Reason != reason || task.ActualAt == nil {
				t.Fatal(task)
			}
			if scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox") != 0 {
				t.Fatal("skipped open published account mutation")
			}
		})
	}
}
func TestIntegrationBoundedRetryAndOutboxRollback(t *testing.T) {
	s, now, notified := integrationStore(t)
	saveRelative(t, s, "open", 1)
	*now = now.Add(time.Hour)
	s.Enqueue = func(ctx context.Context, tx *sql.Tx, id int64) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO missing_fixture_outbox VALUES($1)", id)
		return err
	}
	for i := 1; i <= 3; i++ {
		if err := s.RunDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		task := snapshotTask(t, s, "open")
		if task.Attempts != i || task.Reason != "execution_error" {
			t.Fatal(task)
		}
		if scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 0 {
			t.Fatal("account update survived failed outbox transaction")
		}
		if err := s.RunDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if snapshotTask(t, s, "open").Attempts != i {
			t.Fatal("retry ignored backoff")
		}
		*now = now.Add(2 * time.Minute)
	}
	if task := snapshotTask(t, s, "open"); task.Status != "failed" {
		t.Fatal(task)
	}
	if notified.Load() != 0 {
		t.Fatal("uncommitted effect notified cache")
	}
}
func TestIntegrationChronologicalOrderDuringCompetingClaims(t *testing.T) {
	s, now, _ := integrationStore(t)
	open := saveRelative(t, s, "open", 1)
	closeTask := saveRelative(t, s, "close", 2)
	*now = now.Add(2 * time.Hour)
	// Force a later claim first. It must wait for the earlier action's terminal
	// decision so a delayed opening cannot overwrite a completed close.
	if err := s.execute(context.Background(), closeTask); err != nil {
		t.Fatal(err)
	}
	if snapshotTask(t, s, "close").Status != "pending" {
		t.Fatal("later action ran out of order")
	}
	if err := s.execute(context.Background(), open); err != nil {
		t.Fatal(err)
	} // skips late open
	if err := s.execute(context.Background(), closeTask); err != nil {
		t.Fatal(err)
	}
	if snapshotTask(t, s, "close").Status != "executed" || scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 0 {
		t.Fatal("wrong final scheduling state")
	}
}
func TestIntegrationManualAndExecutionAreSerialized(t *testing.T) {
	s, now, _ := integrationStore(t)
	saveRelative(t, s, "open", 1)
	*now = now.Add(time.Hour)
	entered := make(chan struct{})
	release := make(chan struct{})
	original := s.Enqueue
	var once sync.Once
	s.Enqueue = func(ctx context.Context, tx *sql.Tx, id int64) error {
		once.Do(func() { close(entered); <-release })
		return original(ctx, tx, id)
	}
	done := make(chan error, 1)
	go func() { done <- s.RunDue(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not enter transaction")
	}
	manual := make(chan error, 1)
	go func() { manual <- s.Manual(context.Background(), 7, 9, false) }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-manual; err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 0 {
		t.Fatal("worker overwrote later manual close")
	}
}
func TestIntegrationAdditiveRollbackFixture(t *testing.T) {
	s, _, _ := integrationStore(t)
	// Old binaries can use accounts after additive timer tables are removed.
	// Real operator rollback must first cancel all plans and stop new workers.
	_, err := s.DB.Exec("DROP TABLE account_schedulable_timer_audit; DROP TABLE account_schedulable_timers")
	if err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE status='active'") != 1 {
		t.Fatal("existing account schema affected")
	}
}
func TestMigrationIsAdditiveAndHasIndexedPendingLookup(t *testing.T) {
	b, err := os.ReadFile("../../migrations/295_account_schedulable_timers.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(b))
	for _, required := range []string{"create table if not exists account_schedulable_timers", "primary key (account_id, action)", "on account_schedulable_timers (next_attempt_at, account_id) where status = 'pending'", "create unique index", "actor_id", "timezone", "actual_at"} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"alter table accounts", "drop table", "delete from", "update accounts"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unexpected %s", forbidden)
		}
	}
}

func TestIntegrationDailyCrossMidnightRepeatsOnceAcrossWorkersAndManualOverrideStopsBoth(t *testing.T) {
	s, now, notified := integrationStore(t)
	*now = now.Add(-time.Minute) // 07:59 Beijing; both daily events are future.
	ctx := context.Background()
	snapshot, err := s.SaveDailyWindow(ctx, 7, 9, DailyWindowRequest{OpenTime: "08:00", CloseTime: "01:00", Timezone: "Asia/Shanghai"})
	if err != nil || len(snapshot.Tasks) != 2 {
		t.Fatalf("window %+v: %v", snapshot, err)
	}
	open := snapshotTask(t, s, "open")
	closeTask := snapshotTask(t, s, "close")
	if open.Recurrence != "daily" || open.DailyTime != "08:00" || closeTask.DailyTime != "01:00" || closeTask.ExecuteAt.Sub(open.ExecuteAt) != 17*time.Hour {
		t.Fatalf("wrong cross-midnight events: %+v %+v", open, closeTask)
	}
	*now = open.ExecuteAt
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.RunDue(ctx) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	advanced := snapshotTask(t, s, "open")
	if advanced.Version != open.Version+1 || advanced.Status != "pending" || advanced.Attempts != 0 || advanced.ActualAt == nil || !advanced.ExecuteAt.Equal(open.ExecuteAt.Add(24*time.Hour)) {
		t.Fatalf("daily event did not advance: %+v", advanced)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox") != 1 || notified.Load() != 1 || scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 1 {
		t.Fatal("concurrent instances repeated/missed the opening")
	}
	if err = s.execute(ctx, open); err != nil || scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox") != 1 {
		t.Fatalf("stale claimed occurrence repeated: %v", err)
	}
	*now = closeTask.ExecuteAt
	if err = s.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 0 || snapshotTask(t, s, "close").Status != "pending" {
		t.Fatal("next-day 01:00 must close and retain tomorrow's plan")
	}
	*now = advanced.ExecuteAt
	if err = s.RunDue(ctx); err != nil || scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 1 {
		t.Fatalf("next daily opening did not run: %v", err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM account_schedulable_timer_audit WHERE result='executed' AND recurrence='daily'") != 3 {
		t.Fatal("each daily occurrence must retain its own audit")
	}
	if err = s.Manual(ctx, 7, 19, false); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(48 * time.Hour)
	if err = s.RunDue(ctx); err != nil || scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 0 || scalar(t, s.DB, "SELECT count(*) FROM account_schedulable_timers WHERE status='pending'") != 0 {
		t.Fatalf("manual override must cancel both daily actions: %v", err)
	}
}

func TestIntegrationDailyWindowReplacementIsAtomicAndAllowsSwappingTimes(t *testing.T) {
	s, _, _ := integrationStore(t)
	ctx := context.Background()
	_, err := s.SaveDailyWindow(ctx, 7, 9, DailyWindowRequest{OpenTime: "08:00", CloseTime: "01:00", Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	beforeOpen, beforeClose := snapshotTask(t, s, "open"), snapshotTask(t, s, "close")
	request := DailyWindowRequest{OpenTime: "01:00", CloseTime: "08:00", Timezone: "Asia/Shanghai", ExpectedOpenVersion: beforeOpen.Version, ExpectedCloseVersion: beforeClose.Version}
	// Fail only the second audit write, after the first action already changed.
	_, err = s.DB.Exec(`CREATE FUNCTION reject_daily_close_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='close' THEN RAISE EXCEPTION 'fixture second write'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_daily_close_audit BEFORE INSERT ON account_schedulable_timer_audit
 FOR EACH ROW EXECUTE FUNCTION reject_daily_close_audit();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveDailyWindow(ctx, 7, 19, request); err == nil {
		t.Fatal("partial pair must not commit")
	}
	if got := snapshotTask(t, s, "open"); got.Version != beforeOpen.Version || got.DailyTime != beforeOpen.DailyTime || got.Status != "pending" {
		t.Fatalf("first action escaped rollback: %+v", got)
	}
	if got := snapshotTask(t, s, "close"); got.Version != beforeClose.Version || got.DailyTime != beforeClose.DailyTime || got.Status != "pending" {
		t.Fatalf("second action escaped rollback: %+v", got)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM account_schedulable_timer_audit") != 2 {
		t.Fatal("rolled-back replacement left audit rows")
	}
	if _, err = s.DB.Exec("DROP TRIGGER reject_daily_close_audit ON account_schedulable_timer_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveDailyWindow(ctx, 7, 19, request); err != nil {
		t.Fatalf("swapping pair times should not hit old uniqueness slots: %v", err)
	}
	if got := snapshotTask(t, s, "open"); got.Version != beforeOpen.Version+1 || got.DailyTime != "01:00" {
		t.Fatal(got)
	}
	if got := snapshotTask(t, s, "close"); got.Version != beforeClose.Version+1 || got.DailyTime != "08:00" {
		t.Fatal(got)
	}
	if _, err = s.SaveDailyWindow(ctx, 7, 19, request); ErrorCode(err) != "version" {
		t.Fatalf("stale pair editor accepted: %v", err)
	}
	request.ExpectedOpenVersion++
	if _, err = s.SaveDailyWindow(ctx, 7, 19, request); ErrorCode(err) != "version" {
		t.Fatalf("stale close version must leave open untouched: %v", err)
	}
	if got := snapshotTask(t, s, "open"); got.Version != beforeOpen.Version+1 {
		t.Fatalf("first action changed before second version check: %+v", got)
	}
}

func TestIntegrationDailySkippedOpenContinuesButDeletedStops(t *testing.T) {
	s, now, _ := integrationStore(t)
	ctx := context.Background()
	*now = now.Add(-time.Minute)
	_, err := s.Save(ctx, 7, 9, SaveRequest{Input: Input{Mode: "daily", Time: "08:00", Timezone: "Asia/Shanghai"}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	open := snapshotTask(t, s, "open")
	// The service missed several days, including today's 08:00. Preserve the
	// conservative grace contract: do not substitute today's occurrence for the
	// persisted stale opening even though today is only five minutes late.
	*now = open.ExecuteAt.Add(72*time.Hour + 5*time.Minute)
	if err = s.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	open = snapshotTask(t, s, "open")
	if open.Status != "pending" || open.Reason != "open_expired" || !open.ExecuteAt.After(now.Add(23*time.Hour)) || scalar(t, s.DB, "SELECT count(*) FROM accounts WHERE schedulable") != 0 {
		t.Fatalf("late open lost the recurring plan or enabled account: %+v", open)
	}
	if _, err = s.DB.Exec("UPDATE accounts SET status='disabled'"); err != nil {
		t.Fatal(err)
	}
	*now = open.ExecuteAt
	if err = s.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	open = snapshotTask(t, s, "open")
	if open.Status != "pending" || open.Reason != "account_inactive" {
		t.Fatal(open)
	}
	if _, err = s.DB.Exec("UPDATE accounts SET deleted_at=now()"); err != nil {
		t.Fatal(err)
	}
	*now = open.ExecuteAt
	if err = s.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if got := snapshotTask(t, s, "open"); got.Status != "skipped" || got.Reason != "account_deleted" {
		t.Fatalf("deleted account should stop recurring: %+v", got)
	}
}

func TestIntegrationDailyConflictChecksCoverFutureOneShotsAndZones(t *testing.T) {
	cases := []struct {
		name          string
		first, second SaveRequest
	}{
		{"daily then same local one shot tomorrow", SaveRequest{Input: Input{Mode: "daily", Time: "08:00", Timezone: "Asia/Shanghai"}, Action: "open"}, SaveRequest{Input: Input{Mode: "relative", Hours: 48, Timezone: "Asia/Shanghai"}, Action: "close"}},
		{"one shot then matching daily", SaveRequest{Input: Input{Mode: "relative", Hours: 48, Timezone: "Asia/Shanghai"}, Action: "close"}, SaveRequest{Input: Input{Mode: "daily", Time: "08:00", Timezone: "Asia/Shanghai"}, Action: "open"}},
		{"daily pair must share timezone", SaveRequest{Input: Input{Mode: "daily", Time: "08:00", Timezone: "Asia/Shanghai"}, Action: "open"}, SaveRequest{Input: Input{Mode: "daily", Time: "01:00", Timezone: "America/New_York"}, Action: "close"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := integrationStore(t)
			if _, err := s.Save(context.Background(), 7, 9, c.first); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Save(context.Background(), 7, 9, c.second); ErrorCode(err) != "conflict" {
				t.Fatalf("missed recurring conflict: %v", err)
			}
		})
	}
}

func TestIntegrationDailyMigrationPreservesOldOneShotsAndCanRepeat(t *testing.T) {
	s, _, _ := integrationStore(t)
	saveRelative(t, s, "open", 1)
	data, err := os.ReadFile("../../migrations/335_account_daily_schedulable_timers.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(string(data)); err != nil {
		t.Fatal(err)
	}
	if task := snapshotTask(t, s, "open"); task.Recurrence != "once" || task.DailyTime != "" || task.Status != "pending" {
		t.Fatalf("old one-shot semantics changed: %+v", task)
	}
	if _, err = s.DB.Exec("UPDATE account_schedulable_timers SET recurrence='daily',daily_time='25:00'"); err == nil {
		t.Fatal("database must reject malformed daily clocks")
	}
}

func TestIntegrationCancelRacingExecutionHasOneSerializableOutcome(t *testing.T) {
	s, now, _ := integrationStore(t)
	task := saveRelative(t, s, "open", 1)
	*now = now.Add(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := make(chan struct{})
	cancelled := make(chan error, 1)
	executed := make(chan error, 1)
	go func() { <-start; cancelled <- s.Cancel(ctx, 7, 19, "open", task.Version) }()
	go func() { <-start; executed <- s.execute(ctx, task) }()
	close(start)
	cancelErr := <-cancelled
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	final := snapshotTask(t, s, "open")
	effects := scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox")
	switch final.Status {
	case "cancelled":
		if cancelErr != nil || effects != 0 {
			t.Fatalf("cancelled but effect survived: %v %d", cancelErr, effects)
		}
	case "executed":
		if ErrorCode(cancelErr) != "version" || effects != 1 {
			t.Fatalf("execution/cancel conflict not reported: %v %d", cancelErr, effects)
		}
	default:
		t.Fatal(final)
	}
	if err := s.execute(ctx, task); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox") != effects {
		t.Fatal("stale candidate replayed after cancellation race")
	}
}
func TestIntegrationEditRacingExecutionFencesTheOldCandidate(t *testing.T) {
	s, now, _ := integrationStore(t)
	task := saveRelative(t, s, "open", 1)
	*now = now.Add(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := make(chan struct{})
	saved := make(chan error, 1)
	executed := make(chan error, 1)
	go func() {
		<-start
		_, err := s.Save(ctx, 7, 19, SaveRequest{Input: Input{Mode: "relative", Hours: 2, Timezone: "Asia/Shanghai"}, Action: "open", ExpectedVersion: task.Version})
		saved <- err
	}()
	go func() { <-start; executed <- s.execute(ctx, task) }()
	close(start)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	final := snapshotTask(t, s, "open")
	if final.Version != 2 || final.Status != "pending" || !final.ExecuteAt.Equal(now.Add(2*time.Hour)) || final.RequestedBy != 19 {
		t.Fatal(final)
	}
	effects := scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox")
	if effects > 1 {
		t.Fatal("old version applied multiple times")
	}
	if err := s.execute(ctx, task); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.DB, "SELECT count(*) FROM fixture_outbox") != effects {
		t.Fatal("old candidate overwrote replacement")
	}
}
