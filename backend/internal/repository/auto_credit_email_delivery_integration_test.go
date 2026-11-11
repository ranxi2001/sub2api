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
	"net/http/httptest"
	"net/mail"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
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
	t.Cleanup(func() {
		_, e := db.Exec(fmt.Sprintf(`DROP TRIGGER %s ON auto_credit_email_outbox; DROP FUNCTION %s()`, name, name))
		require.NoError(t, e)
	})
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

func (f emailFixtureSender) Send(ctx context.Context, in service.NotificationEmailSendInput) error {
	return f(ctx, in)
}

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
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
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
		if in.UserID != uid {
			return errors.New("unexpected fixture user")
		}
		close(entered)
		<-release
		return nil
	})
	first := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), sender)
	finished := make(chan error, 1)
	go func() { _, e := first.ProcessNext(ctx); finished <- e }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not enter")
	}
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
	w := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), emailFixtureSender(func(c context.Context, _ service.NotificationEmailSendInput) error {
		close(started)
		<-c.Done()
		return c.Err()
	}))
	w.Start()
	w.Start()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	stopped := make(chan struct{})
	go func() { w.Stop(); w.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	w.Start() // stopped instance cannot restart
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT attempts FROM auto_credit_email_outbox WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid).Scan(&attempts))
	require.Equal(t, 1, attempts)
	_, err = db.Exec(`UPDATE auto_credit_email_outbox SET available_at=NOW() WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid)
	require.NoError(t, err)
	restarted := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), emailFixtureSender(func(context.Context, service.NotificationEmailSendInput) error { return nil }))
	found, err = restarted.ProcessNext(ctx)
	require.NoError(t, err)
	require.True(t, found)
	restarted.Stop() // Stop before Start is safe
}

func TestVipCreditEmailStopWhilePoolSaturated(t *testing.T) {
	db := creditDB(t)
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	w := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), emailFixtureSender(func(context.Context, service.NotificationEmailSendInput) error { return errors.New("must not send") }))
	w.Start()
	require.Eventually(t, func() bool { return db.Stats().WaitCount > 0 }, 5*time.Second, 10*time.Millisecond)
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop hung behind saturated DB pool")
	}
}

// A real local SMTP protocol capture inside GitHub Actions only. No external
// network is used; the first DATA is rejected to prove durable retry.
type creditSMTPFixture struct {
	listener   net.Listener
	wg         sync.WaitGroup
	fail       atomic.Bool
	mu         sync.Mutex
	messages   []string
	recipients []string
	accepted   chan struct{}
	release    chan struct{}
}

func (s *creditSMTPFixture) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.handle(conn) }()
	}
}
func (s *creditSMTPFixture) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	line := func(v string) { _, _ = rw.WriteString(v + "\r\n"); _ = rw.Flush() }
	line("220 localhost ESMTP")
	var recipient string
	for {
		v, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(v))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			line("250-localhost\r\n250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH"):
			line("235 authenticated")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			recipient = strings.TrimSpace(v[len("RCPT TO:"):])
			line("250 recipient")
		case strings.HasPrefix(cmd, "DATA"):
			if s.fail.Swap(false) {
				line("451 fixture transient failure")
				continue
			}
			line("354 data")
			var body strings.Builder
			for {
				v, err = rw.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimSpace(v) == "." {
					break
				}
				body.WriteString(v)
			}
			s.mu.Lock()
			s.messages = append(s.messages, body.String())
			s.recipients = append(s.recipients, recipient)
			accepted, release := s.accepted, s.release
			s.accepted, s.release = nil, nil
			s.mu.Unlock()
			if accepted != nil {
				close(accepted)
				<-release
			}
			line("250 accepted")
		case strings.HasPrefix(cmd, "QUIT"):
			line("221 bye")
			return
		default:
			line("250 ok")
		}
	}
}

func TestVipCreditEmailSMTPTemplateAndRetry(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := emailCredit(t, db)
	_, err := db.Exec(`UPDATE users SET email='vip-recipient@example.invalid' WHERE id=$1`, uid)
	require.NoError(t, err)
	var key int64
	credential := fmt.Sprintf("sk-email-admission-fixture-%d", uid)
	require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'smtp admission fixture') RETURNING id`, uid, credential).Scan(&key))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	cfg := &config.Config{}
	keys := service.NewAPIKeyService(NewAPIKeyRepository(client, db), NewUserRepository(client, db), NewGroupRepository(client, db), nil, NewUserGroupRateRepository(db), nil, cfg)
	keys.SetAutoCredit(NewAutoCreditRepository(db), nil)
	engine := gin.New()
	engine.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)))
	engine.POST("/v1/messages", func(c *gin.Context) { c.Status(200) })
	request := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.Header.Set("Authorization", "Bearer "+credential)
		engine.ServeHTTP(w, r)
		return w.Code
	}
	require.Equal(t, 200, request(), "actual middleware admission must issue the credit before mail")
	_, err = db.Exec(`UPDATE auto_credit_email_outbox SET available_at=NOW()+INTERVAL '1 hour' WHERE ledger_id NOT IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	smtp := &creditSMTPFixture{listener: listener}
	smtp.fail.Store(true)
	smtp.wg.Add(1)
	go smtp.serve()
	t.Cleanup(func() { _ = listener.Close(); smtp.wg.Wait() })
	settings := NewSettingRepository(client)
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, settings.SetMultiple(ctx, map[string]string{
		service.SettingKeySMTPHost: host, service.SettingKeySMTPPort: port, service.SettingKeySMTPUsername: "fixture", service.SettingKeySMTPPassword: "fixture-only", service.SettingKeySMTPFrom: "sender@example.invalid", service.SettingKeySMTPFromName: "Fixture", service.SettingKeySMTPUseTLS: "false",
	}))
	mailService := service.NewNotificationEmailService(settings, service.NewEmailService(settings, nil))
	mailService.RememberRecipientLocale(ctx, uid, "vip-recipient@example.invalid", "zh")
	// Match production's dedicated one-connection outbox pool and prove the
	// main template/settings pool works even when configured with one slot.
	claimDB, err := sql.Open("postgres", os.Getenv("VIP_CREDIT_TEST_DSN"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = claimDB.Close() })
	claimDB.SetMaxOpenConns(1)
	db.SetMaxOpenConns(1)
	w := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(claimDB), mailService)
	found, err := w.ProcessNext(ctx)
	require.True(t, found)
	require.ErrorContains(t, err, "delivery failed")
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, "300.00000000", balance, "SMTP failure must not undo committed credit")
	var sent *time.Time
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT attempts,sent_at FROM auto_credit_email_outbox WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid).Scan(&attempts, &sent))
	require.Equal(t, 1, attempts)
	require.Nil(t, sent)
	found, err = w.ProcessNext(ctx)
	require.NoError(t, err)
	require.False(t, found, "backoff must defer retry")
	_, err = db.Exec(`UPDATE auto_credit_email_outbox SET available_at=NOW() WHERE ledger_id IN (SELECT id FROM auto_credit_ledger WHERE user_id=$1)`, uid)
	require.NoError(t, err)
	// Disabling after issuance changes future issues, not this committed event.
	_, err = NewAutoCreditRepository(db).Save(ctx, uid, uid, service.AutoCreditPatch{Version: 2, NotifyEmail: boolPtr(false)})
	require.NoError(t, err)
	// Collection may precede a delayed notification. Confirm the historical
	// issue without adding another receipt or claiming it is still unpaid.
	issuedState, err := NewAutoCreditRepository(db).Get(ctx, uid, "all")
	require.NoError(t, err)
	_, err = NewAutoCreditRepository(db).Settle(ctx, uid, issuedState.Entries[0].ID, uid)
	require.NoError(t, err)
	require.Equal(t, 1, emailReceiptCount(t, db, uid))
	found, err = w.ProcessNext(ctx)
	require.NoError(t, err)
	require.True(t, found)
	found, err = w.ProcessNext(ctx)
	require.NoError(t, err)
	require.False(t, found)
	smtp.mu.Lock()
	messages := append([]string{}, smtp.messages...)
	recipients := append([]string{}, smtp.recipients...)
	smtp.mu.Unlock()
	require.Len(t, messages, 1)
	require.Equal(t, []string{"<vip-recipient@example.invalid>"}, recipients)
	message, err := mail.ReadMessage(strings.NewReader(messages[0]))
	require.NoError(t, err)
	body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	require.NoError(t, err)
	require.Contains(t, string(body), "300.00000000")
	require.Contains(t, string(body), "垫付")
	require.NotContains(t, string(body), "尚未支付")
	require.Contains(t, string(body), "vip-recipient@example.invalid")
	require.NotContains(t, string(body), "张三")
	require.NotContains(t, string(body), "Alex")
	// End-to-end real debit -> ledger -> outbox -> existing templates -> SMTP.
	repo := NewAutoCreditRepository(db)
	state, err := repo.Get(ctx, uid, "all")
	require.NoError(t, err)
	_, err = repo.Settle(ctx, uid, state.Entries[0].ID, uid)
	require.NoError(t, err)
	_, err = repo.Save(ctx, uid, uid, service.AutoCreditPatch{Version: 3, NotifyEmail: boolPtr(true)})
	require.NoError(t, err)
	cmd := &service.UsageBillingCommand{RequestID: fmt.Sprintf("smtp-real-debit-%d", uid), APIKeyID: key, UserID: uid, BalanceCost: 290}
	applied, err := NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, applied.AutoCreditIssued)
	found, err = w.ProcessNext(ctx)
	require.NoError(t, err)
	require.True(t, found)
	applied, err = NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, applied.Applied)
	found, err = w.ProcessNext(ctx)
	require.NoError(t, err)
	require.False(t, found)
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, uid)
	require.NoError(t, err)
	require.Equal(t, 401, request())
	found, err = w.ProcessNext(ctx)
	require.NoError(t, err)
	require.False(t, found)
	smtp.mu.Lock()
	messages = append([]string{}, smtp.messages...)
	recipients = append([]string{}, smtp.recipients...)
	smtp.mu.Unlock()
	require.Len(t, messages, 2)
	require.Equal(t, []string{"<vip-recipient@example.invalid>", "<vip-recipient@example.invalid>"}, recipients)
	message, err = mail.ReadMessage(strings.NewReader(messages[1]))
	require.NoError(t, err)
	body, err = io.ReadAll(quotedprintable.NewReader(message.Body))
	require.NoError(t, err)
	require.Contains(t, string(body), "310.00000000")
	state, err = repo.Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Len(t, state.Entries, 2)
	for i, raw := range messages {
		parsed, e := mail.ReadMessage(strings.NewReader(raw))
		require.NoError(t, e)
		decoded, e := io.ReadAll(quotedprintable.NewReader(parsed.Body))
		require.NoError(t, e)
		require.Contains(t, string(decoded), fmt.Sprintf("账本编号：%d</p>", state.Entries[1-i].ID), "each mail must identify exactly its own committed ledger")
	}
	for _, locale := range []string{"zh", "en"} {
		preview, e := mailService.PreviewTemplate(ctx, service.NotificationEmailPreviewInput{Event: service.NotificationEmailEventAutoCreditSuccess, Locale: locale})
		require.NoError(t, e)
		require.Contains(t, preview.HTML, "300.00000000")
		require.Contains(t, preview.HTML, "42")
	}
	// Cancel the worker while real SMTP is waiting to acknowledge DATA. Its
	// accepted result must still be acknowledged, and another node must remain
	// excluded even after the five-second claim preparation budget has elapsed.
	_, err = db.Exec(`UPDATE users SET status='active' WHERE id=$1`, uid)
	require.NoError(t, err)
	_, err = repo.Settle(ctx, uid, state.Entries[0].ID, uid)
	require.NoError(t, err)
	cmd.RequestID += "-shutdown"
	applied, err = NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, applied.AutoCreditIssued)
	accepted, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseSMTP := func() { releaseOnce.Do(func() { close(release) }) }
	smtp.mu.Lock()
	smtp.accepted, smtp.release = accepted, release
	smtp.mu.Unlock()
	activeContext := make(chan context.Context, 1)
	stopping := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(claimDB), emailFixtureSender(func(c context.Context, in service.NotificationEmailSendInput) error {
		activeContext <- c
		return mailService.Send(c, in)
	}))
	t.Cleanup(func() { releaseSMTP(); stopping.Stop() })
	stopping.Start()
	var sendingContext context.Context
	select {
	case sendingContext = <-activeContext:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown SMTP sender did not start")
	}
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown SMTP fixture did not receive DATA")
	}
	stopDone := make(chan struct{})
	go func() { stopping.Stop(); close(stopDone) }()
	select {
	case <-sendingContext.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel delivery context")
	}
	<-time.After(6 * time.Second)
	otherNode := service.NewAutoCreditEmailWorker(NewAutoCreditEmailOutboxRepository(db), mailService)
	found, err = otherNode.ProcessNext(ctx)
	require.NoError(t, err)
	require.False(t, found, "a canceled in-flight SMTP sender must retain exclusive ownership")
	releaseSMTP()
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not join accepted SMTP delivery")
	}
	smtp.mu.Lock()
	smtp.accepted, smtp.release = nil, nil
	messageCount := len(smtp.messages)
	smtp.mu.Unlock()
	require.Equal(t, 3, messageCount)
	state, err = repo.Get(ctx, uid, "all")
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT attempts,sent_at FROM auto_credit_email_outbox WHERE ledger_id=$1`, state.Entries[0].ID).Scan(&attempts, &sent))
	require.NotNil(t, sent, "accepted SMTP during Stop must acknowledge the outbox")
	require.Zero(t, attempts, "normal shutdown must not schedule an accepted message for retry")
	found, err = otherNode.ProcessNext(ctx)
	require.NoError(t, err)
	require.False(t, found, "accepted shutdown delivery must never be resent")
	// Template infrastructure supports custom English as well as official zh.
	_, err = mailService.UpdateTemplate(ctx, service.NotificationEmailEventAutoCreditSuccess, "en", "Advance {{ledger_id}}", "<p>{{credit_amount}} / {{current_balance}}</p>")
	require.NoError(t, err)
	preview, err := mailService.PreviewTemplate(ctx, service.NotificationEmailPreviewInput{Event: service.NotificationEmailEventAutoCreditSuccess, Locale: "en", Variables: map[string]string{"ledger_id": "42", "credit_amount": "300.00000000", "current_balance": "309.00000000"}})
	require.NoError(t, err)
	require.Equal(t, "Advance 42", preview.Subject)
	require.Contains(t, preview.HTML, "309.00000000")
}
