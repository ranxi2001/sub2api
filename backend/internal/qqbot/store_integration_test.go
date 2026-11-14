//go:build integration

package qqbot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type testEncryptor struct{}

func (testEncryptor) Encrypt(s string) (string, error) { return "encrypted:" + s, nil }
func (testEncryptor) Decrypt(s string) (string, error) { return s[len("encrypted:"):], nil }

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("QQBOT_TEST_DSN")
	if dsn == "" {
		t.Skip("QQBOT_TEST_DSN is required")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := "qqbot_test_" + randomID()
	_, err = admin.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); _ = admin.Close() })
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
 CREATE TABLE users(id BIGSERIAL PRIMARY KEY,email TEXT NOT NULL DEFAULT '',balance NUMERIC(20,8) NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT 'active',deleted_at TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
 CREATE TABLE redeem_codes(id BIGSERIAL PRIMARY KEY,code VARCHAR(32) UNIQUE NOT NULL,type VARCHAR(20),value NUMERIC(20,8),status TEXT,used_by BIGINT REFERENCES users(id),used_at TIMESTAMPTZ,notes TEXT);`)
	require.NoError(t, err)
	raw, err := migrations.FS.ReadFile("350_qq_bot.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	querySQL, err := migrations.FS.ReadFile("372_qq_bot_site_queries.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(querySQL))
		require.NoError(t, err)
	}
	executionSQL, err := migrations.FS.ReadFile("373_qq_bot_chat_execution.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(executionSQL))
		require.NoError(t, err)
	}
	historySQL, err := migrations.FS.ReadFile("368_qq_bot_transfer_history.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(historySQL))
		require.NoError(t, err)
	}
	memberHintsSQL, err := migrations.FS.ReadFile("367_qq_bot_member_hints.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(memberHintsSQL))
		require.NoError(t, err)
	}
	moderationSQL, err := migrations.FS.ReadFile("366_qq_bot_moderation.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(moderationSQL))
		require.NoError(t, err)
	}
	spamReportSQL, err := migrations.FS.ReadFile("371_qq_bot_spam_reports.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(spamReportSQL))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("359_qq_bot_autonomous.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("374_qq_bot_autonomous_followups.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("375_qq_bot_autonomous_delivery.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	raw, err = migrations.FS.ReadFile("360_qq_bot_health_notifications.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("358_qq_bot_price_notifications.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("351_qq_bot_email_binding.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	raw, err = migrations.FS.ReadFile("352_qq_bot_activities.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	raw, err = migrations.FS.ReadFile("353_qq_bot_activity_record_indexes.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	raw, err = migrations.FS.ReadFile("354_qq_bot_message_references.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE payment_orders(id BIGSERIAL PRIMARY KEY,user_id BIGINT NOT NULL REFERENCES users(id),order_type TEXT NOT NULL DEFAULT 'balance',amount NUMERIC(20,2) NOT NULL,bonus_amount NUMERIC(20,2) NOT NULL DEFAULT 0,refund_amount NUMERIC(20,2) NOT NULL DEFAULT 0,recharge_code VARCHAR(64) NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'COMPLETED',paid_at TIMESTAMPTZ DEFAULT NOW(),completed_at TIMESTAMPTZ DEFAULT NOW());`)
	require.NoError(t, err)
	raw, err = migrations.FS.ReadFile("356_qq_bot_group_history.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("365_qq_bot_image_references.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("378_qq_bot_lottery_chance_grants.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("357_qq_bot_reward_redemption.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("361_qq_bot_growth_rewards.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("362_qq_bot_daily_rankings.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("363_qq_bot_ranking_claims.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("364_qq_bot_periodic_growth.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	raw, err = migrations.FS.ReadFile("377_qq_bot_growth_amount_days.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	intentSQL, err := migrations.FS.ReadFile("369_qq_bot_image_intent.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(intentSQL))
		require.NoError(t, err)
	}
	memberSQL, err := migrations.FS.ReadFile("370_qq_bot_member_context.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(memberSQL))
		require.NoError(t, err)
	}
	couponSQL, err := migrations.FS.ReadFile("376_recharge_coupons.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(couponSQL))
		require.NoError(t, err)
	}
	return db
}
func savedBot(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	s := newMatureFixtureService(db, testEncryptor{}, true, nil)
	c := DefaultConfig()
	c.Enabled = true
	c.AppID = "app"
	c.AppSecret = "test-secret"
	c.BaseURL = "https://example.invalid/v1"
	c.APIKey = "operator-key"
	c.ChatModel = "test-model"
	c.AllowedGroups = []string{"group", "other-group"}
	c.CheckinReward = .1
	c.InviteReward = 1
	c.InviteEnabled = true
	c.CooldownSeconds = 0
	_, err := s.SaveConfig(context.Background(), c)
	require.NoError(t, err)
	return s
}
func user(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&id))
	return id
}
func bind(t *testing.T, db *sql.DB, uid int64, member string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO qq_bot_bindings(app_id,member_id,user_id,group_id) VALUES('app',$1,$2,'group')`, member, uid)
	require.NoError(t, err)
}

func TestQQBotRewardsAreAtomicAndDeduplicated(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	c, err := s.Config(context.Background())
	require.NoError(t, err)
	var failures atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.reward(context.Background(), c, Job{GroupID: "group", MemberID: "member"}, "checkin")
			if err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, .1, balance)
	var records int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&records))
	require.Equal(t, 1, records)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes`).Scan(&records))
	require.Equal(t, 1, records)
	_, err = s.reward(context.Background(), c, Job{GroupID: "other-group", MemberID: "member"}, "checkin")
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, .1, balance)
	// A history write failure rolls back both the credit and its eligibility key.
	uid2 := user(t, db)
	bind(t, db, uid2, "member2")
	_, err = db.Exec(`ALTER TABLE redeem_codes ADD CONSTRAINT reject_second CHECK(used_by<>` + strconv.FormatInt(uid2, 10) + `)`)
	require.NoError(t, err)
	_, err = s.reward(context.Background(), c, Job{GroupID: "group", MemberID: "member2"}, "checkin")
	require.Error(t, err)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid2).Scan(&balance))
	require.Zero(t, balance)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1`, uid2).Scan(&records))
	require.Zero(t, records)
}

func TestQQBotBindingNeedsWebsiteConfirmation(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	uid := user(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	r, err := s.CreateBindingCode(ctx, uid)
	require.NoError(t, err)
	require.Len(t, r.Code, 24)
	e := Event{GroupID: "group"}
	e.Author.MemberID = "member"
	e.Author.Username = "Alice"
	require.Contains(t, s.claimBinding(ctx, c, e, r.Code), "确认")
	b, p, err := s.Binding(ctx, c.AppID, uid)
	require.NoError(t, err)
	require.Nil(t, b)
	require.Equal(t, "member", p.MemberID)
	require.Error(t, s.ConfirmBinding(ctx, uid, "attacker"))
	require.NoError(t, s.ConfirmBinding(ctx, uid, "member"))
	b, p, err = s.Binding(ctx, c.AppID, uid)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.Nil(t, p)
	require.Contains(t, s.claimBinding(ctx, c, e, r.Code), "过期")
	uid2 := user(t, db)
	r, err = s.CreateBindingCode(ctx, uid2)
	require.NoError(t, err)
	s.claimBinding(ctx, c, e, r.Code)
	require.Error(t, s.ConfirmBinding(ctx, uid2, "member"))
}

func TestQQBotInvitationRequiresJoinAndBothBindings(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	inviter, invitee := user(t, db), user(t, db)
	bind(t, db, inviter, "inviter")
	e := Event{GroupID: "group", MemberID: "invitee", InvitedBy: "inviter", ApplySource: "invited", ApplyAt: time.Now().Add(-time.Minute).Format(time.RFC3339)}
	j := Job{GroupID: "group", MemberID: "invitee", EventType: "GROUP_JOIN_REQUEST"}
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	bind(t, db, invitee, "invitee")
	result, err := s.reward(ctx, c, j, "invite")
	require.NoError(t, err)
	require.Contains(t, result, "暂无")
	e.Timestamp = json.RawMessage(strconv.FormatInt(time.Now().Unix(), 10))
	j.EventType = "GROUP_MEMBER_ADD"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 1.0, balance)
	j.EventType = "GROUP_MEMBER_REMOVE"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	j.EventType = "GROUP_MEMBER_ADD"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 1.0, balance)
	// Existing members cannot acquire an invitation retroactively.
	old := user(t, db)
	bind(t, db, old, "old")
	j.MemberID = "old"
	e.MemberID = "old"
	j.EventType = "GROUP_MEMBER_ADD"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	j.EventType = "GROUP_JOIN_REQUEST"
	e.ApplyAt = time.Now().Add(time.Minute).Format(time.RFC3339)
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	_, err = s.reward(ctx, c, j, "invite")
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 1.0, balance)
}

func TestQQBotDailyLimitsAndEventDedup(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.TotalDailyChatLimit = 5
	c.DailyChatLimit = 3
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	e := Event{ID: "message", GroupID: "group", Content: "hi"}
	e.Author.MemberID = "member"
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	p := Payload{ID: "delivery-a", Type: "GROUP_AT_MESSAGE_CREATE", Data: raw}
	require.NoError(t, s.Enqueue(ctx, c, p))
	p.ID = "delivery-b"
	require.NoError(t, s.Enqueue(ctx, c, p))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs`).Scan(&count))
	require.Equal(t, 1, count)
	j, err := s.claimJob(ctx)
	require.NoError(t, err)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.reserveUsage(ctx, c, j, "chat") == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 3, successes.Load())
	j.MemberID = "second"
	for i := 0; i < 5; i++ {
		if s.reserveUsage(ctx, c, j, "chat") == nil {
			successes.Add(1)
		}
	}
	require.EqualValues(t, 5, successes.Load())
	// No member balance is touched by free usage reservations.
	uid := user(t, db)
	bind(t, db, uid, "member")
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
}

func TestQQBotSecretsAndConversationIsolation(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	var raw string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key=$1`, configKey).Scan(&raw))
	require.Contains(t, raw, "encrypted:operator-key")
	changed := c.Public()
	changed.CheckinReward = .2
	_, err = s.SaveConfig(ctx, changed)
	require.NoError(t, err)
	got, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, "operator-key", got.APIKey)
	changed.BaseURL = "https://other.invalid/v1"
	_, err = s.SaveConfig(ctx, changed)
	require.Error(t, err)
	// Old personal conversations never become shared group context.
	_, err = db.Exec(`INSERT INTO qq_bot_conversations(app_id,group_id,member_id,messages) VALUES($1,'group','a','[{"role":"user","content":"old-private"}]')`, c.AppID)
	require.NoError(t, err)
	e := Event{ID: "shared", GroupID: "group", Content: "new-shared"}
	e.Author.MemberID = "a"
	require.NoError(t, s.recordGroupEvent(ctx, c, e))
	a := s.history(ctx, c, Job{GroupID: "group", MemberID: "a"})
	b := s.history(ctx, c, Job{GroupID: "group", MemberID: "b"})
	require.Len(t, b, 2)
	require.Len(t, a, 3)
	require.Contains(t, a[2].Content, "new-shared")
	require.NotContains(t, a[2].Content, "old-private")
	require.Len(t, s.history(ctx, c, Job{GroupID: "other-group", MemberID: "a"}), 2)
}

func TestQQBotMembershipEventsMayArriveOutOfOrder(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	inviter, invitee := user(t, db), user(t, db)
	bind(t, db, inviter, "inviter")
	bind(t, db, invitee, "invitee")
	now := time.Now().Unix()
	e := Event{GroupID: "group", MemberID: "invitee", Timestamp: json.RawMessage(strconv.FormatInt(now, 10)), ApplySource: "invited", InvitedBy: "inviter", ApplyAt: time.Unix(now-60, 0).Format(time.RFC3339)}
	j := Job{GroupID: "group", MemberID: "invitee", EventType: "GROUP_MEMBER_ADD"}
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	j.EventType = "GROUP_JOIN_REQUEST"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 1.0, balance)
	j.EventType = "GROUP_MEMBER_REMOVE"
	e.Timestamp = json.RawMessage(strconv.FormatInt(now+60, 10))
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	j.EventType = "GROUP_MEMBER_ADD"
	e.Timestamp = json.RawMessage(strconv.FormatInt(now, 10))
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	var left bool
	require.NoError(t, db.QueryRow(`SELECT left_at IS NOT NULL FROM qq_bot_members WHERE member_id='invitee'`).Scan(&left))
	require.True(t, left, "late join must not undo a newer departure")
}

func TestQQBotWorkerChatAndImageDelivery(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	uid := user(t, db)
	bind(t, db, uid, "member")
	var chatCalls, imageCalls, replies int
	var picture bytes.Buffer
	require.NoError(t, png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 16, 16))))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls++
			require.Zero(t, replies, "do not send a placeholder before generating the answer")
			require.Equal(t, "Bearer operator-key", r.Header.Get("Authorization"))
			_, _ = w.Write(textToolTestJSON(t, "chat_completions", "free response"))
		case "/v1/images/generations":
			imageCalls++
			require.Equal(t, 2, replies, "image generation starts after its processing notice")
			require.Equal(t, "Bearer operator-key", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(picture.Bytes())}}})
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"qq-test-token","expires_in":7200}`))
		case "/v2/groups/group/files":
			_, _ = w.Write([]byte(`{"file_info":"saved-image"}`))
		case "/v2/groups/group/messages":
			body := readQQTestReply(t, r)
			if body.MsgType == 7 {
				require.Equal(t, 3, body.Sequence)
			} else if body.text() == "图片已生成。" {
				require.Equal(t, 2, body.Sequence)
			} else {
				require.Equal(t, 1, body.Sequence)
				require.NotContains(t, body.text(), "正在思考")
			}
			replies++
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	c.BaseURL = upstream.URL + "/v1"
	c.ImageEnabled = true
	c.ImageModel = "image-model"
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	s.qqURL = upstream.URL
	s.tokenURL = upstream.URL + "/token"
	enqueue := func(id, content string) Payload {
		e := Event{ID: id, GroupID: "group", Content: content}
		e.Author.MemberID = "member"
		raw, _ := json.Marshal(e)
		p := Payload{ID: id, Type: "GROUP_AT_MESSAGE_CREATE", Data: raw}
		require.NoError(t, s.Enqueue(ctx, c, p))
		return p
	}
	p := enqueue("message1", "hello")
	s.runOne(ctx)
	var status, kind, result string
	require.NoError(t, db.QueryRow(`SELECT status,kind,result_text FROM qq_bot_jobs WHERE message_id='message1'`).Scan(&status, &kind, &result))
	require.Equal(t, "completed", status)
	require.Equal(t, "chat", kind)
	require.Equal(t, "free response", result)
	require.NoError(t, s.Enqueue(ctx, c, p))
	s.runOne(ctx)
	require.Equal(t, 1, chatCalls)
	p = enqueue("message2", "/生图 cat")
	s.runOne(ctx)
	var size int
	require.NoError(t, db.QueryRow(`SELECT status,octet_length(result_image) FROM qq_bot_jobs WHERE message_id='message2'`).Scan(&status, &size))
	require.Equal(t, "completed", status)
	require.Positive(t, size)
	require.NoError(t, s.Enqueue(ctx, c, p))
	s.runOne(ctx)
	require.Equal(t, 1, imageCalls)
	require.Equal(t, 4, replies)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
}
