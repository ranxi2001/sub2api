//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type bindingMailStub struct {
	db         *sql.DB
	mu         sync.Mutex
	bodies     []string
	recipients []string
	fail       bool
	beforeSend func()
}

func (m *bindingMailStub) FrontendURL(context.Context) string { return "https://site.example" }
func (m *bindingMailStub) LookupAccount(ctx context.Context, email string) (int64, string, error) {
	var uid int64
	var address string
	err := m.db.QueryRowContext(ctx, `SELECT id,email FROM users WHERE lower(email)=lower($1) AND status='active' AND deleted_at IS NULL`, email).Scan(&uid, &address)
	return uid, address, err
}
func (m *bindingMailStub) SendEmail(_ context.Context, to, subject, body string) error {
	if m.beforeSend != nil {
		m.beforeSend()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bodies = append(m.bodies, body)
	m.recipients = append(m.recipients, to)
	if m.fail {
		return errors.New("SMTP unavailable")
	}
	return nil
}
func (m *bindingMailStub) token(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.bodies)
	match := regexp.MustCompile(`#token=([a-f0-9]{48})`).FindStringSubmatch(m.bodies[len(m.bodies)-1])
	require.Len(t, match, 2)
	return match[1]
}
func emailFixture(t *testing.T) (*Service, *bindingMailStub, Config, Event, int64) {
	db := testDB(t)
	s := savedBot(t, db)
	m := &bindingMailStub{db: db}
	s.SetBindingEmailDelivery(m)
	c, err := s.Config(context.Background())
	require.NoError(t, err)
	e := Event{GroupID: "group"}
	e.Author.MemberID = "member"
	e.Author.Username = "Alice"
	uid := user(t, db)
	_, err = db.Exec(`UPDATE users SET email='Alice@example.com' WHERE id=$1`, uid)
	require.NoError(t, err)
	return s, m, c, e, uid
}

func requestBindingEmail(s *Service, ctx context.Context, c Config, e Event, email string) string {
	reply, pending := s.prepareEmailBinding(ctx, c, e, email)
	if pending != nil {
		s.deliverBindingEmail(ctx, e, pending)
	}
	return reply
}

func TestQQBotEmailBindingWorkerAndExplicitConfirmation(t *testing.T) {
	for _, eventType := range []string{groupAtMessageCreate, groupMessageCreate} {
		t.Run(eventType, func(t *testing.T) { testEmailBindingWorkerAndConfirmation(t, eventType) })
	}
}

func testEmailBindingWorkerAndConfirmation(t *testing.T, eventType string) {
	s, m, c, e, uid := emailFixture(t)
	ctx := context.Background()
	var reply string
	var acknowledged atomic.Bool
	m.beforeSend = func() { require.True(t, acknowledged.Load(), "QQ must acknowledge before SMTP starts") }
	qq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":7200}`))
			return
		}
		require.Equal(t, "/v2/groups/group/messages", r.URL.Path)
		data := readQQTestReply(t, r)
		require.Equal(t, 2, data.MsgType)
		require.Equal(t, "email-bind-message", data.MsgID)
		require.Equal(t, 1, data.Sequence)
		reply = data.text()
		acknowledged.Store(true)
		_, _ = w.Write([]byte(`{"id":"sent"}`))
	}))
	defer qq.Close()
	s.qqURL = qq.URL
	s.tokenURL = qq.URL + "/token"
	e.ID = "email-bind-message"
	e.Content = "/绑定 alice@example.com"
	if eventType == groupAtMessageCreate {
		e.Content = "<@bot> " + e.Content
	}
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	payload := Payload{Type: eventType, Data: raw}
	require.NoError(t, s.Enqueue(ctx, c, payload))
	s.runOne(ctx)
	expectedReply := emailBindingAccepted
	if eventType == groupMessageCreate {
		expectedReply = "<qqbot-at-user id=\"member\" />\n" + expectedReply
	}
	require.Equal(t, expectedReply, reply)
	require.NoError(t, s.Enqueue(ctx, c, payload))
	s.runOne(ctx)
	require.Len(t, m.bodies, 1)
	require.Equal(t, []string{"Alice@example.com"}, m.recipients)
	token := m.token(t)
	require.NotContains(t, reply, token)
	var hash string
	require.NoError(t, s.db.QueryRow(`SELECT token_hash FROM qq_bot_email_bind_requests`).Scan(&hash))
	require.Equal(t, codeHash(token), hash)
	b, _, err := s.Binding(ctx, c.AppID, uid)
	require.NoError(t, err)
	require.Nil(t, b)
	for i := 0; i < 2; i++ {
		preview, err := s.PreviewEmailBinding(ctx, token)
		require.NoError(t, err)
		require.Equal(t, "member", preview.MemberID)
	}
	_, err = s.ConfirmEmailBinding(ctx, token, "another-member")
	require.ErrorIs(t, err, ErrEmailBindingInvalid)
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ConfirmEmailBinding(ctx, token, "member"); err == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, success.Load())
	b, _, err = s.Binding(ctx, c.AppID, uid)
	require.NoError(t, err)
	require.Equal(t, "member", b.MemberID)
	_, err = s.PreviewEmailBinding(ctx, token)
	require.ErrorIs(t, err, ErrEmailBindingInvalid)
}

func TestQQBotBindingCommandAlreadyBound(t *testing.T) {
	for _, scenario := range []string{"same email", "different email", "unknown email", "binding code", "without mention", "disabled account", "other group"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, c, e, uid := emailFixture(t)
			ctx := context.Background()
			bind(t, s.db, uid, e.Author.MemberID)
			other := user(t, s.db)
			_, err := s.db.Exec(`UPDATE users SET email='other@example.com' WHERE id=$1`, other)
			require.NoError(t, err)
			code, err := s.CreateBindingCode(ctx, other)
			require.NoError(t, err)
			e.ID = "already-bound-message"
			e.Content = "<@bot> /绑定 alice@example.com"
			eventType := groupAtMessageCreate
			switch scenario {
			case "different email":
				e.Content = "<@bot> /绑定 other@example.com"
			case "unknown email":
				e.Content = "<@bot> /绑定 unknown@example.com"
			case "binding code":
				e.Content = "<@bot> /绑定 " + code.Code
			case "without mention":
				e.Content = "/绑定 alice@example.com"
				eventType = groupMessageCreate
			case "disabled account":
				_, err = s.db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, uid)
				require.NoError(t, err)
			case "other group":
				e.GroupID = "other-group"
			}
			var replies []string
			qq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":7200}`))
					return
				}
				require.Equal(t, "/v2/groups/"+e.GroupID+"/messages", r.URL.Path)
				data := readQQTestReply(t, r)
				replies = append(replies, data.text())
				_, _ = w.Write([]byte(`{"id":"sent"}`))
			}))
			defer qq.Close()
			s.qqURL, s.tokenURL = qq.URL, qq.URL+"/token"
			raw, err := json.Marshal(e)
			require.NoError(t, err)
			require.NoError(t, s.Enqueue(ctx, c, Payload{Type: eventType, Data: raw}))
			s.runOne(ctx)
			expectedReply := "当前 QQ 已绑定账号。"
			if eventType == groupMessageCreate {
				expectedReply = "<qqbot-at-user id=\"member\" />\n" + expectedReply
			}
			require.Equal(t, []string{expectedReply}, replies)
			require.Empty(t, m.bodies)
			var requests, limits int
			require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_email_bind_requests`).Scan(&requests))
			require.Zero(t, requests)
			require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_email_limits`).Scan(&limits))
			require.Zero(t, limits)
			var member string
			require.NoError(t, s.db.QueryRow(`SELECT member_id FROM qq_bot_bind_requests WHERE user_id=$1`, other).Scan(&member))
			require.Empty(t, member, "already-bound QQ must not claim another account's binding code")
		})
	}
}

func TestQQBotBindingCommandEmailAlreadyBound(t *testing.T) {
	for _, scenario := range []string{"with mention", "without mention", "email case", "other group", "rate limited"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, c, e, uid := emailFixture(t)
			ctx := context.Background()
			bind(t, s.db, uid, "original-member")
			e.Content = "<@bot> /绑定 alice@example.com"
			eventType := groupAtMessageCreate
			switch scenario {
			case "without mention":
				e.Content = "/绑定 alice@example.com"
				eventType = groupMessageCreate
			case "email case":
				e.Content = "<@bot> /绑定 ALICE@EXAMPLE.COM"
			case "other group":
				e.GroupID = "other-group"
			case "rate limited":
				require.NoError(t, s.reserveBindingEmails(ctx, c.AppID, []string{"member:" + e.Author.MemberID, "all"}, []int{5, 200}, []int{60, 0}))
			}
			var replies []string
			qq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":7200}`))
					return
				}
				require.Equal(t, "/v2/groups/"+e.GroupID+"/messages", r.URL.Path)
				data := readQQTestReply(t, r)
				replies = append(replies, data.text())
				_, _ = w.Write([]byte(`{"id":"sent"}`))
			}))
			defer qq.Close()
			s.qqURL, s.tokenURL = qq.URL, qq.URL+"/token"
			// Repeated attempts must keep the conflict response even during cooldown.
			for _, id := range []string{"bound-email-first", "bound-email-repeated"} {
				e.ID = id
				raw, err := json.Marshal(e)
				require.NoError(t, err)
				require.NoError(t, s.Enqueue(ctx, c, Payload{Type: eventType, Data: raw}))
				s.runOne(ctx)
			}
			expectedReply := emailBindingAlreadyBound
			if eventType == groupMessageCreate {
				expectedReply = "<qqbot-at-user id=\"member\" />\n" + expectedReply
			}
			require.Equal(t, []string{expectedReply, expectedReply}, replies)
			require.Empty(t, m.bodies)
			var requests int
			require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_email_bind_requests`).Scan(&requests))
			require.Zero(t, requests)
			binding, _, err := s.Binding(ctx, c.AppID, uid)
			require.NoError(t, err)
			require.NotNil(t, binding)
			require.Equal(t, "original-member", binding.MemberID)
		})
	}
}

func TestQQBotEmailBindingDetectsConcurrentBinding(t *testing.T) {
	s, m, c, e, uid := emailFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	confirmation, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = confirmation.Rollback() }()
	var pid int
	require.NoError(t, confirmation.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	_, err = confirmation.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,273))`, c.AppID)
	require.NoError(t, err)
	_, err = confirmation.ExecContext(ctx, `INSERT INTO qq_bot_bindings(app_id,member_id,user_id,group_id) VALUES($1,'original-member',$2,'group')`, c.AppID, uid)
	require.NoError(t, err)
	done := make(chan string, 1)
	go func() { done <- requestBindingEmail(s, ctx, c, e, "alice@example.com") }()
	waitForDatabaseBlocker(t, s.db, pid)
	require.NoError(t, confirmation.Commit())
	require.Equal(t, emailBindingAlreadyBound, <-done)
	require.Empty(t, m.bodies)
	var requests int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_email_bind_requests`).Scan(&requests))
	require.Zero(t, requests)
}

func TestQQBotEmailBindingInvalidation(t *testing.T) {
	for _, scenario := range []string{"expired", "email changed", "disabled account", "deleted account", "group removed", "bot disabled", "smtp failed", "already bound"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, c, e, uid := emailFixture(t)
			ctx := context.Background()
			m.fail = scenario == "smtp failed"
			require.Equal(t, emailBindingAccepted, requestBindingEmail(s, ctx, c, e, "alice@example.com"))
			token := m.token(t)
			var err error
			switch scenario {
			case "expired":
				_, err = s.db.Exec(`UPDATE qq_bot_email_bind_requests SET expires_at=NOW()-INTERVAL '1 second'`)
			case "email changed":
				_, err = s.db.Exec(`UPDATE users SET email='changed@example.com' WHERE id=$1`, uid)
			case "disabled account":
				_, err = s.db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, uid)
			case "deleted account":
				_, err = s.db.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, uid)
			case "group removed":
				c.AllowedGroups = []string{"another"}
				_, err = s.SaveConfig(ctx, c)
			case "bot disabled":
				c.Enabled = false
				_, err = s.SaveConfig(ctx, c)
			case "already bound":
				bind(t, s.db, uid, "other-member")
			}
			require.NoError(t, err)
			_, err = s.PreviewEmailBinding(ctx, token)
			require.Error(t, err)
			_, err = s.ConfirmEmailBinding(ctx, token, "member")
			require.Error(t, err)
		})
	}
}

func TestQQBotEmailBindingLimitsAndWebsiteRace(t *testing.T) {
	s, m, c, e, uid := emailFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); requestBindingEmail(s, ctx, c, e, "alice@example.com") }()
	}
	wg.Wait()
	require.Len(t, m.bodies, 1)
	old := m.token(t)
	_, err := s.db.Exec(`UPDATE qq_bot_email_limits SET last_sent_at=NOW()-INTERVAL '61 seconds'`)
	require.NoError(t, err)
	requestBindingEmail(s, ctx, c, e, "alice@example.com")
	require.Len(t, m.bodies, 2)
	_, err = s.PreviewEmailBinding(ctx, old)
	require.Error(t, err)
	token := m.token(t)
	r, err := s.CreateBindingCode(ctx, uid)
	require.NoError(t, err)
	e.Author.MemberID = "website-member"
	require.Contains(t, s.claimBinding(ctx, c, e, r.Code), "确认")
	var successes atomic.Int32
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := s.ConfirmEmailBinding(ctx, token, "member"); err == nil {
			successes.Add(1)
		}
	}()
	go func() {
		defer wg.Done()
		if err := s.ConfirmBinding(ctx, uid, "website-member"); err == nil {
			successes.Add(1)
		}
	}()
	wg.Wait()
	require.EqualValues(t, 1, successes.Load())
	var pending int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_email_bind_requests`).Scan(&pending))
	require.Zero(t, pending)
}

func TestQQBotEmailBindingRecipientLimitsAndUnknownAccount(t *testing.T) {
	s, m, c, e, _ := emailFixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		e.Author.MemberID = strings.Repeat("x", i+1)
		_, err := s.db.Exec(`UPDATE qq_bot_email_limits SET last_sent_at=NOW()-INTERVAL '61 seconds'`)
		require.NoError(t, err)
		require.Equal(t, emailBindingAccepted, requestBindingEmail(s, ctx, c, e, "alice@example.com"))
	}
	require.Len(t, m.bodies, 3)
	e.Author.MemberID = "unknown"
	require.Equal(t, emailBindingAccepted, requestBindingEmail(s, ctx, c, e, "unregistered@example.com"))
	require.Len(t, m.bodies, 3)
	require.NotEqual(t, emailBindingAccepted, requestBindingEmail(s, ctx, c, e, "Name <alice@example.com>"))
}

func TestQQBotEmailBindingInvitationSettlement(t *testing.T) {
	s, m, c, e, _ := emailFixture(t)
	ctx := context.Background()
	inviter := user(t, s.db)
	bind(t, s.db, inviter, "inviter")
	_, err := s.db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at) VALUES('app','group','member','inviter',NOW()-INTERVAL '1 minute',NOW())`)
	require.NoError(t, err)
	requestBindingEmail(s, ctx, c, e, "alice@example.com")
	member, err := s.ConfirmEmailBinding(ctx, m.token(t), "member")
	require.NoError(t, err)
	require.NoError(t, s.SettleInvites(ctx, member))
	require.NoError(t, s.SettleInvites(ctx, member))
	var balance float64
	require.NoError(t, s.db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, c.InviteReward, balance)
}
