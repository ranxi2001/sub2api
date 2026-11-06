package accounttimer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func testStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Store{DB: db, Now: func() time.Time { return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC) }, Policy: DefaultPolicy()}
	t.Cleanup(func() {
		if err := m.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return s, m
}
func expectAccount(m sqlmock.Sqlmock, status string, deleted bool) {
	var d any
	if deleted {
		d = time.Now()
	}
	m.ExpectQuery("SELECT status, expires_at, deleted_at FROM accounts").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "deleted_at"}).AddRow(status, nil, d))
}
func TestSaveRejectsConflictingOppositeAction(t *testing.T) {
	s, m := testStore(t)
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectQuery("SELECT version FROM account_schedulable_timers").WithArgs(int64(7), "open").WillReturnRows(sqlmock.NewRows([]string{"version"}))
	m.ExpectQuery("SELECT EXISTS").WithArgs(int64(7), "open", s.now().Add(8*time.Hour), "once", "Asia/Shanghai", "", s.now()).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	m.ExpectRollback()
	_, err := s.Save(context.Background(), 7, 9, SaveRequest{Input: Input{Mode: "relative", Hours: 8, Timezone: "Asia/Shanghai"}, Action: "open"})
	if ErrorCode(err) != "conflict" {
		t.Fatalf("want conflict, got %v", err)
	}
}
func TestSaveRejectsStaleEdit(t *testing.T) {
	s, m := testStore(t)
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectQuery("SELECT version FROM account_schedulable_timers").WithArgs(int64(7), "open").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(4))
	m.ExpectRollback()
	_, err := s.Save(context.Background(), 7, 9, SaveRequest{Input: Input{Mode: "relative", Hours: 8, Timezone: "UTC"}, Action: "open", ExpectedVersion: 3})
	if ErrorCode(err) != "version" {
		t.Fatalf("want version, got %v", err)
	}
}
func TestSaveRequiresFixedPreviewConfirmation(t *testing.T) {
	s, m := testStore(t)
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectRollback()
	_, err := s.Save(context.Background(), 7, 9, SaveRequest{Input: Input{Mode: "absolute", Time: "08:00", Timezone: "Asia/Shanghai"}, Action: "open"})
	if ErrorCode(err) != "confirmation" {
		t.Fatalf("want confirmation, got %v", err)
	}
}
func TestCancelVersionChecked(t *testing.T) {
	s, m := testStore(t)
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectQuery("SELECT "+regexp.QuoteMeta(taskColumns)).WithArgs(int64(7), "open").WillReturnRows(taskRows().AddRow(7, "open", 4, s.now().Add(time.Hour), "UTC", 9, "pending", 0, nil, "", "once", ""))
	m.ExpectRollback()
	if err := s.Cancel(context.Background(), 7, 9, "open", 3); ErrorCode(err) != "version" {
		t.Fatalf("want version, got %v", err)
	}
}
func taskRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"account_id", "action", "version", "execute_at", "timezone", "requested_by", "status", "attempts", "actual_at", "reason", "recurrence", "daily_time"})
}

// The outbox is a transactional dependency; a failure must roll back schedulable
// and the terminal result, leaving a bounded retry instead of a lost event.
func TestExecuteOutboxFailureRollsBackAndRecordsRetry(t *testing.T) {
	s, m := testStore(t)
	s.Enqueue = func(context.Context, *sql.Tx, int64) error { return errors.New("fixture outbox unavailable") }
	now := s.now()
	task := Task{AccountID: 7, Action: "open", Version: 1, ExecuteAt: now, Timezone: "UTC", RequestedBy: 9, Status: "pending"}
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectQuery("SELECT "+regexp.QuoteMeta(taskColumns)).WithArgs(int64(7), "open", now).WillReturnRows(taskRows().AddRow(7, "open", 1, now, "UTC", 9, "pending", 0, nil, "", "once", ""))
	m.ExpectExec("UPDATE accounts SET schedulable").WithArgs(true, now, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectRollback()
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectExec("UPDATE account_schedulable_timers").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO account_schedulable_timer_audit").WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectCommit()
	if err := s.execute(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

func TestSavePersistsUTCAndOriginalZoneAndReplacesSameAction(t *testing.T) {
	for _, version := range []int64{0, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, m := testStore(t)
			now := s.now()
			due := now.Add(8 * time.Hour)
			m.ExpectBegin()
			expectAccount(m, "active", false)
			rows := sqlmock.NewRows([]string{"version"})
			if version > 0 {
				rows.AddRow(version)
			}
			m.ExpectQuery("SELECT version FROM account_schedulable_timers").WithArgs(int64(7), "open").WillReturnRows(rows)
			m.ExpectQuery("SELECT EXISTS").WithArgs(int64(7), "open", due, "once", "Asia/Shanghai", "", now).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			m.ExpectExec("INSERT INTO account_schedulable_timers").WithArgs(int64(7), "open", version+1, due, "Asia/Shanghai", int64(9), "once", "").WillReturnResult(sqlmock.NewResult(0, 1))
			result := "scheduled"
			if version > 0 {
				result = "replaced"
			}
			m.ExpectExec("INSERT INTO account_schedulable_timer_audit").WithArgs(int64(7), "open", version+1, int64(9), due, "Asia/Shanghai", now, result, "", "once", "").WillReturnResult(sqlmock.NewResult(1, 1))
			m.ExpectCommit()
			task, err := s.Save(context.Background(), 7, 9, SaveRequest{Input: Input{Mode: "relative", Hours: 8, Timezone: "Asia/Shanghai"}, Action: "open", ExpectedVersion: version})
			if err != nil || !task.ExecuteAt.Equal(due) || task.Version != version+1 || task.Timezone != "Asia/Shanghai" {
				t.Fatalf("%+v %v", task, err)
			}
		})
	}
}
func TestManualOverrideCancelsBothActionsAndPublishesOnlyAfterCommit(t *testing.T) {
	s, m := testStore(t)
	now := s.now()
	notified := 0
	s.Enqueue = func(ctx context.Context, tx *sql.Tx, id int64) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO fixture_outbox VALUES ($1)", id)
		return err
	}
	s.AfterCommit = func(context.Context, int64) { notified++ }
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectExec("WITH cancelled AS").WithArgs(int64(7), int64(9), now).WillReturnResult(sqlmock.NewResult(0, 2))
	m.ExpectExec("UPDATE accounts SET schedulable").WithArgs(false, now, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO fixture_outbox").WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit()
	if err := s.Manual(context.Background(), 7, 9, false); err != nil {
		t.Fatal(err)
	}
	if notified != 1 {
		t.Fatal("cache update missing")
	}
}
func TestManualOutboxFailureRollsBackCancellationAndSwitch(t *testing.T) {
	s, m := testStore(t)
	s.Enqueue = func(context.Context, *sql.Tx, int64) error { return errors.New("fixture failure") }
	s.AfterCommit = func(context.Context, int64) { t.Error("cache must not change before commit") }
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectExec("WITH cancelled AS").WillReturnResult(sqlmock.NewResult(0, 2))
	m.ExpectExec("UPDATE accounts SET schedulable").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectRollback()
	if err := s.Manual(context.Background(), 7, 9, false); err == nil {
		t.Fatal("must reject partial update")
	}
}
func TestAutomaticPoliciesDoNotCancelPlans(t *testing.T) {
	s, _ := testStore(t)
	if err := CancelPending(context.Background(), s.DB, 7, s.now()); err != nil {
		t.Fatal(err)
	}
	ctx := WithManualAccount(WithManualIntent(context.Background(), 9), 8)
	if err := CancelPending(ctx, s.DB, 7, s.now()); err != nil {
		t.Fatal(err)
	}
}
func TestExecutionRechecksVersionStatusAndNotifiesAfterCommit(t *testing.T) {
	cases := []struct {
		name, action, status, accountStatus, reason string
		version                                     int64
		deleted                                     bool
	}{
		{"restart opens once", "open", "pending", "active", "", 1, false},
		{"stale worker cannot overwrite edit", "open", "pending", "active", "", 2, false},
		{"second instance sees executed", "open", "executed", "active", "", 1, false},
		{"deleted account skipped", "open", "pending", "active", "account_deleted", 1, true},
		{"disabled remains disabled", "open", "pending", "disabled", "account_inactive", 1, false},
		{"offline close catchup", "close", "pending", "active", "", 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, m := testStore(t)
			now := s.now()
			due := now
			if c.action == "close" {
				due = now.Add(-24 * time.Hour)
			}
			candidate := Task{AccountID: 7, Action: c.action, Version: 1, ExecuteAt: due, Timezone: "Asia/Shanghai", RequestedBy: 9, Status: "pending"}
			notified := 0
			s.AfterCommit = func(context.Context, int64) { notified++ }
			m.ExpectBegin()
			expectAccount(m, c.accountStatus, c.deleted)
			m.ExpectQuery("SELECT "+regexp.QuoteMeta(taskColumns)).WithArgs(int64(7), c.action, now).WillReturnRows(taskRows().AddRow(7, c.action, c.version, due, "Asia/Shanghai", 9, c.status, 0, nil, "", "once", ""))
			active := c.version == 1 && c.status == "pending"
			result := "executed"
			if c.reason != "" {
				result = "skipped"
			}
			if active {
				if c.reason == "" {
					m.ExpectExec("UPDATE accounts SET schedulable").WithArgs(c.action == "open", now, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
				}
				m.ExpectExec("UPDATE account_schedulable_timers").WithArgs(int64(7), c.action, result, now, c.reason, int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectExec("INSERT INTO account_schedulable_timer_audit").WithArgs(int64(7), c.action, int64(1), int64(9), due, "Asia/Shanghai", now, result, c.reason, "once", "").WillReturnResult(sqlmock.NewResult(1, 1))
				m.ExpectCommit()
			} else {
				m.ExpectRollback()
			}
			if err := s.execute(context.Background(), candidate); err != nil {
				t.Fatal(err)
			}
			want := 0
			if active && c.reason == "" {
				want = 1
			}
			if notified != want {
				t.Fatalf("cache calls %d want %d", notified, want)
			}
		})
	}
}
func TestThirdFailureBecomesTerminalAndStaleRetryCannotChangeNewVersion(t *testing.T) {
	for _, affected := range []int64{1, 0} {
		t.Run(fmt.Sprint(affected), func(t *testing.T) {
			s, m := testStore(t)
			now := s.now()
			task := Task{AccountID: 7, Action: "open", Version: 1, ExecuteAt: now, Timezone: "UTC", RequestedBy: 9, Attempts: 2}
			m.ExpectBegin()
			expectAccount(m, "active", false)
			m.ExpectExec("UPDATE account_schedulable_timers").WithArgs(int64(7), "open", int64(1), 3, "failed", now.Add(120*time.Second), now, 2).WillReturnResult(sqlmock.NewResult(0, affected))
			if affected == 1 {
				m.ExpectExec("INSERT INTO account_schedulable_timer_audit").WillReturnResult(sqlmock.NewResult(1, 1))
				m.ExpectCommit()
			} else {
				m.ExpectRollback()
			}
			if err := s.retry(context.Background(), task); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestBusyAccountIsSkippedWithoutMisreportingDeletion(t *testing.T) {
	s, m := testStore(t)
	m.ExpectBegin()
	m.ExpectQuery("SELECT status, expires_at, deleted_at FROM accounts").WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "deleted_at"}))
	m.ExpectQuery("SELECT EXISTS").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	m.ExpectRollback()
	if err := s.execute(context.Background(), Task{AccountID: 7, Action: "open", Version: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveDailyPersistsRecurrenceWithoutAbsoluteConfirmation(t *testing.T) {
	s, m := testStore(t)
	now := s.now()
	due := now.Add(24 * time.Hour) // The fixture clock is exactly 08:00 Beijing.
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectQuery("SELECT version FROM account_schedulable_timers").WithArgs(int64(7), "open").WillReturnRows(sqlmock.NewRows([]string{"version"}))
	m.ExpectQuery("SELECT EXISTS").WithArgs(int64(7), "open", due, "daily", "Asia/Shanghai", "08:00", now).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	m.ExpectExec("INSERT INTO account_schedulable_timers").WithArgs(int64(7), "open", int64(1), due, "Asia/Shanghai", int64(9), "daily", "08:00").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO account_schedulable_timer_audit").WithArgs(int64(7), "open", int64(1), int64(9), due, "Asia/Shanghai", now, "scheduled", "", "daily", "08:00").WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectCommit()
	task, err := s.Save(context.Background(), 7, 9, SaveRequest{Input: Input{Mode: "daily", Time: "08:00", Timezone: "Asia/Shanghai"}, Action: "open"})
	if err != nil || task.Recurrence != "daily" || task.DailyTime != "08:00" || !task.ExecuteAt.Equal(due) {
		t.Fatalf("got %+v %v", task, err)
	}
}

func TestDailyWindowRejectsEqualTimesBeforeOpeningTransaction(t *testing.T) {
	s, _ := testStore(t)
	_, err := s.SaveDailyWindow(context.Background(), 7, 9, DailyWindowRequest{OpenTime: "08:00", CloseTime: "08:00", Timezone: "Asia/Shanghai"})
	if ErrorCode(err) != "conflict" {
		t.Fatalf("equal clocks accepted: %v", err)
	}
}

func TestDailyWindowChecksBothVersionsBeforeEitherWrite(t *testing.T) {
	s, m := testStore(t)
	m.ExpectBegin()
	expectAccount(m, "active", false)
	m.ExpectQuery("SELECT version FROM account_schedulable_timers").WithArgs(int64(7), "open").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(3))
	m.ExpectQuery("SELECT version FROM account_schedulable_timers").WithArgs(int64(7), "close").WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(5))
	m.ExpectRollback()
	_, err := s.SaveDailyWindow(context.Background(), 7, 9, DailyWindowRequest{OpenTime: "08:00", CloseTime: "01:00", Timezone: "Asia/Shanghai", ExpectedOpenVersion: 3, ExpectedCloseVersion: 4})
	if ErrorCode(err) != "version" {
		t.Fatalf("stale second version accepted: %v", err)
	}
}

func TestDailyExecutionAuditsOccurrenceAndFencesOldWorkers(t *testing.T) {
	cases := []struct {
		name, status, zone, result, reason string
		late, deleted, stale, advance      bool
	}{
		{name: "successful execution", status: "active", zone: "Asia/Shanghai", result: "executed", advance: true},
		{name: "late opening skips and continues", status: "active", zone: "Asia/Shanghai", result: "skipped", reason: "open_expired", late: true, advance: true},
		{name: "inactive opening skips and continues", status: "disabled", zone: "Asia/Shanghai", result: "skipped", reason: "account_inactive", advance: true},
		{name: "deleted account terminates recurrence", status: "active", zone: "Asia/Shanghai", result: "skipped", reason: "account_deleted", deleted: true},
		{name: "invalid persisted zone fails closed", status: "active", zone: "Mars/City", result: "failed", reason: "recurrence_invalid"},
		{name: "old instance cannot repeat occurrence", status: "active", zone: "Asia/Shanghai", stale: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, m := testStore(t)
			now := s.now()
			due := now
			if c.late {
				due = now.Add(-16 * time.Minute)
			}
			candidate := Task{AccountID: 7, Action: "open", Version: 1, ExecuteAt: due, Timezone: c.zone, RequestedBy: 9, Status: "pending", Recurrence: "daily", DailyTime: "08:00"}
			version := 1
			if c.stale {
				version = 2
			}
			m.ExpectBegin()
			expectAccount(m, c.status, c.deleted)
			m.ExpectQuery("SELECT "+regexp.QuoteMeta(taskColumns)).WithArgs(int64(7), "open", now).WillReturnRows(taskRows().AddRow(7, "open", version, due, c.zone, 9, "pending", 0, nil, "", "daily", "08:00"))
			if c.stale {
				m.ExpectRollback()
			} else {
				if c.reason == "" {
					m.ExpectExec("UPDATE accounts SET schedulable").WithArgs(true, now, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
				}
				if c.advance {
					m.ExpectExec("UPDATE account_schedulable_timers").WithArgs(int64(7), "open", now.Add(24*time.Hour), now, c.reason, int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
				} else {
					m.ExpectExec("UPDATE account_schedulable_timers").WithArgs(int64(7), "open", c.result, now, c.reason, int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
				}
				m.ExpectExec("INSERT INTO account_schedulable_timer_audit").WithArgs(int64(7), "open", int64(1), int64(9), due, c.zone, now, c.result, c.reason, "daily", "08:00").WillReturnResult(sqlmock.NewResult(1, 1))
				m.ExpectCommit()
			}
			if err := s.execute(context.Background(), candidate); err != nil {
				t.Fatal(err)
			}
		})
	}
}
