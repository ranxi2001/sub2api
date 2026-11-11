//go:build vipcreditintegration

package repository

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func emailCredit(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	uid := creditUser(t, db, "0")
	configureCredit(t, db, uid, "50", "300", "500")
	_, err := NewAutoCreditRepository(db).Save(context.Background(), uid, uid, service.AutoCreditPatch{Version: 1, NotifyEmail: boolPtr(true)})
	require.NoError(t, err)
	return uid
}

func emailReceiptCount(t *testing.T, db *sql.DB, uid int64) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auto_credit_email_outbox o JOIN auto_credit_ledger l ON l.id=o.ledger_id WHERE l.user_id=$1`, uid).Scan(&n))
	return n
}

func TestVipCreditEmailNoIssueAndRollback(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := emailCredit(t, db)
	repo := NewAutoCreditRepository(db)
	require.Zero(t, emailReceiptCount(t, db, uid), "saving opt-in must not send")
	var key int64
	require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name,status) VALUES($1,$2,'denied fixture','inactive') RETURNING id`, uid, fmt.Sprintf("sk-denied-email-%d", uid)).Scan(&key))
	r, err := repo.EvaluateAdmission(ctx, uid, key)
	require.NoError(t, err)
	require.False(t, r.Issued)
	require.Zero(t, emailReceiptCount(t, db, uid))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	balance, status, err := lockAutoCreditUser(ctx, tx, uid)
	require.NoError(t, err)
	r, err = evaluateAutoCredit(ctx, tx, uid, balance, status)
	require.NoError(t, err)
	require.True(t, r.Issued)
	require.Zero(t, emailReceiptCount(t, db, uid), "uncommitted outbox must be invisible")
	require.NoError(t, tx.Rollback())
	require.Zero(t, emailReceiptCount(t, db, uid))
	state, err := repo.Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Empty(t, state.Entries)
	_, err = repo.Save(ctx, uid, uid, service.AutoCreditPatch{Version: state.Config.Version, Enabled: boolPtr(false), Threshold: creditJSON("null"), Amount: creditJSON("null"), Cap: creditJSON("null")})
	require.NoError(t, err)
	r, err = repo.Evaluate(ctx, uid)
	require.NoError(t, err)
	require.False(t, r.Issued)
	require.Zero(t, emailReceiptCount(t, db, uid))
	legacy := creditUser(t, db, "0")
	configureCredit(t, db, legacy, "50", "300", "500")
	r, err = repo.Evaluate(ctx, legacy)
	require.NoError(t, err)
	require.True(t, r.Issued)
	require.Zero(t, emailReceiptCount(t, db, legacy), "legacy opt-out must not enqueue")
}

func TestVipCreditEmailEnqueueFailureRollsBackMoney(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := emailCredit(t, db)
	name := fmt.Sprintf("email_enqueue_failure_%d", uid)
	_, err := db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS(SELECT 1 FROM auto_credit_ledger WHERE id=NEW.ledger_id AND user_id=%d) THEN RAISE EXCEPTION 'fixture enqueue failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON auto_credit_email_outbox FOR EACH ROW EXECUTE FUNCTION %s()`, name, uid, name, name))
	require.NoError(t, err)
	t.Cleanup(func() { _, e := db.Exec(fmt.Sprintf(`DROP TRIGGER %s ON auto_credit_email_outbox; DROP FUNCTION %s()`, name, name)); require.NoError(t, e) })
	_, err = NewAutoCreditRepository(db).Evaluate(ctx, uid)
	require.ErrorContains(t, err, "fixture enqueue failure")
	_, err = NewUsageBillingRepository(nil, db).Apply(ctx, &service.UsageBillingCommand{RequestID: fmt.Sprintf("email-fail-%d", uid), APIKeyID: uid, UserID: uid, BalanceCost: 1})
	require.ErrorContains(t, err, "fixture enqueue failure")
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, "0.00000000", balance)
	s, err := NewAutoCreditRepository(db).Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Empty(t, s.Entries)
	require.Zero(t, emailReceiptCount(t, db, uid))
}

type emailFixtureSender func(context.Context, service.NotificationEmailSendInput) error
func (f emailFixtureSender) Send(ctx context.Context, in service.NotificationEmailSendInput) error { return f(ctx, in) }

func TestVipCreditEmailIndependentNodesAndLifecycle(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := emailCredit(t, db)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := NewAutoCreditRepository(db).Evaluate(ctx, uid); errs <- e }()
	}
	wg.Wait(); close(errs)
	for e := range errs { require.NoError(t, e) }
	require.Equal(t, 1, emailReceiptCount(t, db, uid))
	// Earlier test fixtures may have pending receipts: defer them, never delete
	// history. This fixture owns the entire disposable database during the test.
	_, err := db.Exec(`UPDATE auto_credit_email_outbox SET available_at=NOW()+INTERVAL '1 hour' WHERE ledger_id NOT IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid)
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	var sends atomic.Int64
	sender := emailFixtureSender(func(_ context.Context, in service.NotificationEmailSendInput) error {
		sends.Add(1)
		if in.UserID != uid { return errors.New("unexpected fixture user") }
		close(entered); <-release
		return nil
	})
	first := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), sender)
	finished := make(chan error, 1)
	go func() { _, e := first.ProcessNext(ctx); finished <- e }()
	select { case <-entered: case <-time.After(5*time.Second): t.Fatal("sender did not enter") }
	second := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), sender)
	found, err := second.ProcessNext(ctx)
	require.NoError(t, err)
	require.False(t, found, "independent node must skip the in-flight ledger")
	close(release)
	require.NoError(t, <-finished)
	found, err = second.ProcessNext(ctx)
	require.NoError(t, err)
	require.False(t, found)
	require.EqualValues(t, 1, sends.Load())

	// Cancellation does not abandon a sender/DB lock; Stop waits for it, then
	// a new worker can retry the persisted failure after restart.
	_, err = db.Exec(`UPDATE auto_credit_email_outbox SET sent_at=NULL,available_at=NOW() WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid)
	require.NoError(t, err)
	started := make(chan struct{})
	w := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), emailFixtureSender(func(c context.Context, _ service.NotificationEmailSendInput) error { close(started); <-c.Done(); return c.Err() }))
	w.Start(); w.Start()
	select { case <-started: case <-time.After(5*time.Second): t.Fatal("worker did not start") }
	stopped := make(chan struct{})
	go func() { w.Stop(); w.Stop(); close(stopped) }()
	select { case <-stopped: case <-time.After(5*time.Second): t.Fatal("worker did not stop") }
	w.Start() // stopped instance cannot restart
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT attempts FROM auto_credit_email_outbox WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid).Scan(&attempts))
	require.Equal(t, 1, attempts)
	_, err = db.Exec(`UPDATE auto_credit_email_outbox SET available_at=NOW() WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid)
	require.NoError(t, err)
	restarted := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), emailFixtureSender(func(context.Context, service.NotificationEmailSendInput) error { return nil }))
	found, err = restarted.ProcessNext(ctx)
	require.NoError(t, err); require.True(t, found)
	restarted.Stop() // Stop before Start is safe
}

// A real local SMTP protocol capture inside GitHub Actions only. No external
// network is used; the first DATA is rejected to prove durable retry.
type creditSMTPFixture struct {
	listener net.Listener
	wg sync.WaitGroup
	fail atomic.Bool
	mu sync.Mutex
	messages []string
	recipients []string
}
func (s *creditSMTPFixture) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept(); if err != nil { return }
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.handle(conn) }()
	}
}
func (s *creditSMTPFixture) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5*time.Second))
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	line := func(v string) { _, _ = rw.WriteString(v+"\r\n"); _ = rw.Flush() }
	line("220 localhost ESMTP")
	var recipient string
	for {
		v, err := rw.ReadString('\n'); if err != nil { return }
		cmd := strings.ToUpper(strings.TrimSpace(v))
		switch {
		case strings.HasPrefix(cmd,"EHLO"), strings.HasPrefix(cmd,"HELO"): line("250-localhost\r\n250 AUTH PLAIN")
		case strings.HasPrefix(cmd,"AUTH"): line("235 authenticated")
		case strings.HasPrefix(cmd,"RCPT TO:"): recipient = strings.TrimSpace(v[len("RCPT TO:"):]); line("250 recipient")
		case strings.HasPrefix(cmd,"DATA"):
			if s.fail.Swap(false) { line("451 fixture transient failure"); continue }
			line("354 data")
			var body strings.Builder
			for { v, err = rw.ReadString('\n'); if err != nil { return }; if strings.TrimSpace(v)=="." { break }; body.WriteString(v) }
			s.mu.Lock(); s.messages=append(s.messages,body.String()); s.recipients=append(s.recipients,recipient); s.mu.Unlock()
			line("250 accepted")
		case strings.HasPrefix(cmd,"QUIT"): line("221 bye"); return
		default: line("250 ok")
		}
	}
}

func TestVipCreditEmailSMTPTemplateAndRetry(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := emailCredit(t, db)
	_, err := db.Exec(`UPDATE users SET email='vip-recipient@example.invalid' WHERE id=$1`, uid)
	require.NoError(t, err)
	_, err = NewAutoCreditRepository(db).Evaluate(ctx, uid)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE auto_credit_email_outbox SET available_at=NOW()+INTERVAL '1 hour' WHERE ledger_id NOT IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	smtp := &creditSMTPFixture{listener:listener}; smtp.fail.Store(true); smtp.wg.Add(1); go smtp.serve()
	t.Cleanup(func() { _=listener.Close(); smtp.wg.Wait() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	settings := NewSettingRepository(client)
	host, port, err := net.SplitHostPort(listener.Addr().String()); require.NoError(t, err)
	require.NoError(t, settings.SetMultiple(ctx, map[string]string{
		service.SettingKeySMTPHost:host, service.SettingKeySMTPPort:port, service.SettingKeySMTPUsername:"fixture", service.SettingKeySMTPPassword:"fixture-only", service.SettingKeySMTPFrom:"sender@example.invalid", service.SettingKeySMTPFromName:"Fixture", service.SettingKeySMTPUseTLS:"false",
	}))
	mailService := service.NewNotificationEmailService(settings, service.NewEmailService(settings,nil))
	mailService.RememberRecipientLocale(ctx,uid,"vip-recipient@example.invalid","zh")
	w := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db),mailService)
	found, err := w.ProcessNext(ctx); require.True(t,found); require.ErrorContains(t,err,"delivery failed")
	var balance string
	require.NoError(t,db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`,uid).Scan(&balance)); require.Equal(t,"300.00000000",balance,"SMTP failure must not undo committed credit")
	var sent *time.Time
	var attempts int
	require.NoError(t,db.QueryRow(`SELECT attempts,sent_at FROM auto_credit_email_outbox WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`,uid).Scan(&attempts,&sent)); require.Equal(t,1,attempts); require.Nil(t,sent)
	found, err = w.ProcessNext(ctx); require.NoError(t,err); require.False(t,found,"backoff must defer retry")
	_, err = db.Exec(`UPDATE auto_credit_email_outbox SET available_at=NOW() WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`,uid); require.NoError(t,err)
	// Disabling after issuance changes future issues, not this committed event.
	_, err = NewAutoCreditRepository(db).Save(ctx,uid,uid,service.AutoCreditPatch{Version:2,NotifyEmail:boolPtr(false)}); require.NoError(t,err)
	found, err = w.ProcessNext(ctx); require.NoError(t,err); require.True(t,found)
	found, err = w.ProcessNext(ctx); require.NoError(t,err); require.False(t,found)
	smtp.mu.Lock(); messages:=append([]string{},smtp.messages...); recipients:=append([]string{},smtp.recipients...); smtp.mu.Unlock()
	require.Len(t,messages,1); require.Equal(t,[]string{"<vip-recipient@example.invalid>"},recipients)
	message, err := mail.ReadMessage(strings.NewReader(messages[0])); require.NoError(t,err)
	body, err := io.ReadAll(quotedprintable.NewReader(message.Body)); require.NoError(t,err)
	require.Contains(t,string(body),"300.00000000"); require.Contains(t,string(body),"垫付"); require.Contains(t,string(body),"尚未支付")
	// Template infrastructure supports custom English as well as official zh.
	_, err = mailService.UpdateTemplate(ctx,service.NotificationEmailEventAutoCreditSuccess,"en","Advance {{ledger_id}}","<p>{{credit_amount}} / {{current_balance}}</p>"); require.NoError(t,err)
	preview, err := mailService.PreviewTemplate(ctx,service.NotificationEmailPreviewInput{Event:service.NotificationEmailEventAutoCreditSuccess,Locale:"en",Variables:map[string]string{"ledger_id":"42","credit_amount":"300.00000000","current_balance":"309.00000000"}}); require.NoError(t,err); require.Equal(t,"Advance 42",preview.Subject)
	require.Contains(t,preview.HTML,"309.00000000")
}
