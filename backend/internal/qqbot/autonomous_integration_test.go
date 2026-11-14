//go:build integration

package qqbot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"image"
	"image/color"
	"image/gif"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type autoHarness struct {
	s         *Service
	c         Config
	db        *sql.DB
	calls     atomic.Int32
	sends     atomic.Int32
	output    atomic.Value
	request   atomic.Value
	sent      atomic.Value
	failModel atomic.Bool
	failSend  atomic.Bool
	hook      func()
}

func newAutoHarness(t *testing.T) *autoHarness {
	t.Helper()
	db := testDB(t)
	s := savedBot(t, db)
	h := &autoHarness{s: s, db: db}
	h.output.Store(`{"action":"reply","text":"可以先看看错误日志。"}`)
	h.sent.Store("")
	h.request.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			fmt.Fprint(w, `{"access_token":"test-only","expires_in":7200}`)
		case r.URL.Path == "/v2/groups/group/bot_state":
			fmt.Fprint(w, `{"member_openid":"this-bot"}`)
		case r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/responses":
			h.calls.Add(1)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			h.request.Store(string(body))
			if h.hook != nil {
				h.hook()
			}
			if h.failModel.Load() {
				w.WriteHeader(500)
				return
			}
			content := h.output.Load().(string)
			if r.URL.Path == "/v1/responses" {
				_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": content}}}}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 10}})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10}})
			}
		case strings.HasSuffix(r.URL.Path, "/messages"):
			body := readQQTestReply(t, r)
			h.sent.Store(body.text())
			n := h.sends.Add(1)
			if h.failSend.Load() {
				w.WriteHeader(500)
				return
			}
			fmt.Fprintf(w, `{"id":"reply-%d","ext_info":{"ref_idx":"ref-%d"}}`, n, n)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	c, err := s.Config(context.Background())
	require.NoError(t, err)
	c.BaseURL = server.URL + "/v1"
	c.Autonomous.Enabled = true
	c.Autonomous.Groups = []string{"group", "other-group"}
	c.Autonomous.StartHour = 0
	c.Autonomous.EndHour = 0
	c.Autonomous.MinMessages = 0
	h.c = c
	h.save(t)
	s.qqURL = server.URL
	s.tokenURL = server.URL + "/token"
	return h
}
func (h *autoHarness) save(t *testing.T) {
	t.Helper()
	_, err := h.s.SaveConfig(context.Background(), h.c)
	require.NoError(t, err)
}
func (h *autoHarness) enqueue(t *testing.T, id, text, member string) Payload {
	t.Helper()
	e := Event{ID: id, GroupID: "group", Content: text}
	e.Author.MemberID = member
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	p := Payload{ID: id, Type: groupMessageCreate, Data: raw}
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
	return p
}
func (h *autoHarness) enqueueEvent(t *testing.T, e Event) Payload {
	t.Helper()
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	p := Payload{ID: e.ID, Type: groupMessageCreate, Data: raw}
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
	return p
}
func (h *autoHarness) run() { h.s.runOne(context.Background()) }
func (h *autoHarness) resetCooldown(t *testing.T) {
	t.Helper()
	_, err := h.db.Exec(`UPDATE qq_bot_autonomous_state SET last_candidate_at=NOW()-INTERVAL '1 hour',last_reply_at=NOW()-INTERVAL '1 hour',last_reply_counter=0`)
	require.NoError(t, err)
}
func (h *autoHarness) job(t *testing.T, id string) Job {
	t.Helper()
	j, err := scanJob(h.db.QueryRow(`SELECT `+jobColumns+` FROM qq_bot_jobs WHERE message_id=$1`, id))
	require.NoError(t, err)
	return j
}
func TestAutonomousReplyAndBoundedFollowups(t *testing.T) {
	h := newAutoHarness(t)
	p := h.enqueue(t, "first", "这个错误怎么解决？", "alice")
	for range 3 {
		require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
	}
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	require.EqualValues(t, 1, h.sends.Load())
	require.NotContains(t, h.sent.Load(), "qqbot-at-user")
	j := h.job(t, "first")
	require.Equal(t, "replied", j.DecisionReason)
	require.Equal(t, "completed", j.Status)
	require.Equal(t, "question", j.TriggerReason)
	var personal, total int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage WHERE scope='member:alice'`).Scan(&personal))
	require.Zero(t, personal)
	require.NoError(t, h.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE scope='all' AND kind='chat'`).Scan(&total))
	require.Equal(t, 1, total)
	var shared bool
	require.NoError(t, h.db.QueryRow(`SELECT shareable FROM qq_bot_message_references WHERE message_id='reply-1'`).Scan(&shared))
	require.True(t, shared)
	h.enqueue(t, "follow", "那具体应该怎么检查？", "alice")
	h.run()
	require.EqualValues(t, 2, h.calls.Load())
	require.Equal(t, "followup", h.job(t, "follow").TriggerReason)

	h.enqueue(t, "third", "没看懂", "alice")
	h.run()
	require.EqualValues(t, 3, h.calls.Load())
	h.enqueue(t, "fourth", "再说说", "alice")
	h.run()
	require.EqualValues(t, 4, h.calls.Load())
	h.enqueue(t, "fifth", "那还能怎么继续检查？", "alice")
	h.run()
	require.EqualValues(t, 4, h.calls.Load())
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE message_id='fifth'`).Scan(&count))
	require.Zero(t, count)
	runtime, err := h.s.Runtime(context.Background(), h.c)
	require.NoError(t, err)
	require.Contains(t, runtime.AutonomousStats, AutonomousStat{GroupID: "group", Reason: "model_calls", Count: 4})
	require.Contains(t, runtime.AutonomousStats, AutonomousStat{GroupID: "group", Reason: "sent", Count: 4})
}
func TestAutonomousSilenceErrorsAndCallLimit(t *testing.T) {
	for _, mode := range []string{"skip", "invalid", "model_error", "send_error"} {
		t.Run(mode, func(t *testing.T) {
			h := newAutoHarness(t)
			h.c.Autonomous.DailyCalls = 1
			h.save(t)
			switch mode {
			case "skip":
				h.output.Store(`{"action":"skip"}`)
			case "invalid":
				h.output.Store("bad JSON")
			case "model_error":
				h.failModel.Store(true)
			case "send_error":
				h.failSend.Store(true)
			}
			h.enqueue(t, "one", "这个报错怎么处理？", "alice")
			h.run()
			h.run()
			require.EqualValues(t, 1, h.calls.Load())
			sends := int32(0)
			if mode == "send_error" {
				sends = 1
			}
			require.Equal(t, sends, h.sends.Load())
			if mode == "skip" {
				require.Equal(t, "skipped", h.job(t, "one").Status)
			}
			h.resetCooldown(t)
			if mode == "send_error" {
				_, err := h.db.Exec(`UPDATE qq_bot_jobs SET reply_reserved_at=NOW()-INTERVAL '15 minutes' WHERE message_id='one'`)
				require.NoError(t, err)
			}
			h.enqueue(t, "two", "另外这个故障如何解决？", "bob")
			h.run()
			require.EqualValues(t, 1, h.calls.Load())
			require.Equal(t, "call_limit", h.job(t, "two").DecisionReason)
		})
	}
}
func TestAutonomousRechecksConversationAndRevocation(t *testing.T) {
	for _, mode := range []string{"new_message", "ended", "disable", "quiet_hours", "max_length"} {
		t.Run(mode, func(t *testing.T) {
			h := newAutoHarness(t)
			h.hook = func() {
				switch mode {
				case "new_message":
					h.enqueue(t, "new", "哈哈", "bob")
				case "ended":
					h.enqueue(t, "ended", "不用回复", "alice")
				case "disable":
					h.c.Autonomous.Enabled = false
					h.save(t)
				case "quiet_hours":
					hour := time.Now().In(time.FixedZone("CST", 28800)).Hour()
					h.c.Autonomous.StartHour = (hour + 1) % 24
					h.c.Autonomous.EndHour = (hour + 2) % 24
					h.save(t)
				case "max_length":
					h.c.Autonomous.MaxChars = 20
					h.save(t)
				}
			}
			if mode == "max_length" {
				h.output.Store(`{"action":"reply","text":"` + strings.Repeat("字", 21) + `"}`)
			}
			h.enqueue(t, "one", "这个报错怎么处理？", "alice")
			h.run()
			require.EqualValues(t, 1, h.calls.Load())
			if mode == "new_message" {
				require.EqualValues(t, 1, h.sends.Load())
			} else {
				require.Zero(t, h.sends.Load())
			}
		})
	}
}
func TestAutonomousDirectNameUsesNormalQuotaAndProtocol(t *testing.T) {
	h := newAutoHarness(t)
	h.c.Autonomous.DailyCalls = 0
	h.c.HistoryMessages = 0
	h.save(t)
	h.output.Store("普通回答")
	p := h.enqueue(t, "name", "CTAV，帮我看看这个问题", "alice")
	p.Type = groupAtMessageCreate
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	require.EqualValues(t, 1, h.sends.Load())
	require.Contains(t, h.sent.Load(), "qqbot-at-user")
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE scope='member:alice' AND kind='chat'`).Scan(&count))
	require.Equal(t, 1, count)
	require.Equal(t, "name", h.job(t, "name").TriggerReason)
	h.c.Autonomous.DailyCalls = 40
	h.c.ChatProtocol = "responses"
	h.c.Autonomous.MentionUser = true
	h.save(t)
	h.resetCooldown(t)
	h.output.Store(`{"action":"reply","text":"可以先看看错误日志。"}`)
	h.enqueue(t, "responses", "另一个问题怎么排查？", "bob")
	h.run()
	require.EqualValues(t, 2, h.calls.Load())
	require.Equal(t, "replied", h.job(t, "responses").DecisionReason)
	require.Contains(t, h.sent.Load(), "qqbot-at-user")
}

func TestAutonomousNameAnywhereUsesNormalChat(t *testing.T) {
	h := newAutoHarness(t)
	h.c.HistoryMessages = 0
	h.c.Autonomous.Names = []string{"CTAV", "小爱"}
	h.c.Autonomous.DailyCalls = 0
	h.c.Autonomous.DailyReplies = 0
	h.c.Autonomous.HourlyReplies = 0
	h.c.Autonomous.MinMessages = 100
	h.c.Autonomous.StartHour = (time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Hour() + 12) % 24
	h.c.Autonomous.EndHour = (h.c.Autonomous.StartHour + 1) % 24
	h.save(t)
	h.output.Store("普通回答")
	for i, text := range []string{"今天CTAV不错", "这个问题想让cTaV看看", "CTAV", "小爱", "小爱", "小爱 看看 https://example.com"} {
		id := fmt.Sprintf("name-%d", i)
		p := h.enqueue(t, id, text, "alice")
		require.Equal(t, "event", h.job(t, id).Kind)
		require.Equal(t, "name", h.job(t, id).TriggerReason)
		// Redelivery does not repeat the reply, while a new message containing
		// exactly the same nickname is still an explicit request.
		require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
		h.run()
		h.run()
		require.EqualValues(t, i+1, h.calls.Load())
		require.EqualValues(t, i+1, h.sends.Load())
		require.Contains(t, h.sent.Load(), "普通回答")
		require.Contains(t, h.sent.Load(), "qqbot-at-user")
	}
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE scope='member:alice' AND kind='chat'`).Scan(&count))
	require.Equal(t, 6, count)

	// Name calls still consume the ordinary personal chat allowance.
	h.c.DailyChatLimit = count
	h.save(t)
	h.enqueue(t, "limited", "小爱", "alice")
	h.run()
	require.EqualValues(t, count, h.calls.Load())
}

func TestAutonomousNameWithQuoteMentionOrAttachment(t *testing.T) {
	for _, mode := range []string{"quote", "mention", "attachment", "elements"} {
		t.Run(mode, func(t *testing.T) {
			h := newAutoHarness(t)
			h.c.HistoryMessages = 0
			h.save(t)
			h.output.Store("普通回答")
			e := Event{ID: "name", GroupID: "group", Content: "这个问题想让CTAV看看"}
			e.Author.MemberID = "alice"
			switch mode {
			case "quote":
				e.MessageReference.MessageID = "member-message"
			case "mention":
				e.Content = "<@other-member> 这个问题想让CTAV看看"
				e.Mentions = []Mention{{MemberID: "other-member"}}
			case "attachment":
				e.Attachments = []Attachment{{ContentType: "application/pdf"}}
			case "elements":
				e.Content = ""
				e.MsgElements = []MessageElement{{Content: "这个问题想让CTAV看看"}}
			}
			raw, err := json.Marshal(e)
			require.NoError(t, err)
			p := Payload{ID: e.ID, Type: groupMessageCreate, Data: raw}
			require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
			require.Equal(t, "name", h.job(t, e.ID).TriggerReason)
			h.run()
			require.EqualValues(t, 1, h.calls.Load())
			require.EqualValues(t, 1, h.sends.Load())
		})
	}
}

func TestAutonomousNameStillRequiresEnabledGroup(t *testing.T) {
	for _, mode := range []string{"disabled", "other_group"} {
		t.Run(mode, func(t *testing.T) {
			h := newAutoHarness(t)
			h.c.HistoryMessages = 0
			if mode == "disabled" {
				h.c.Autonomous.Enabled = false
			} else {
				h.c.Autonomous.Groups = []string{"other-group"}
			}
			h.save(t)
			h.enqueue(t, "name", "这个问题想让CTAV看看", "alice")
			h.run()
			var count int
			require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs`).Scan(&count))
			require.Zero(t, count)
			require.Zero(t, h.calls.Load())
		})
	}
}

func TestAutonomousAtomicReservationsAndReplay(t *testing.T) {
	h := newAutoHarness(t)
	h.enqueue(t, "one", "这个错误怎么处理？", "alice")
	j, err := h.s.claimJob(context.Background())
	require.NoError(t, err)
	var successes atomic.Int32
	var failures atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reason, err := h.s.reserveAutonomous(context.Background(), h.c, j, false)
			if err != nil {
				failures.Add(1)
			} else if reason == "" {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	require.EqualValues(t, 1, successes.Load())
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE scope='all' AND kind='chat'`).Scan(&count))
	require.Equal(t, 1, count)
	h.c.Autonomous.HourlyReplies = 1
	h.save(t)
	successes.Store(0)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reason, err := h.s.reserveAutonomous(context.Background(), h.c, j, true)
			if err != nil {
				failures.Add(1)
			} else if reason == "" {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	require.EqualValues(t, 1, successes.Load())
	h.s.finish(j, "interrupted", "")
	_, err = h.db.Exec(`UPDATE qq_bot_jobs SET reply_reserved_at=NOW()-INTERVAL '15 minutes' WHERE id=$1`, j.ID)
	require.NoError(t, err)
	h.resetCooldown(t)
	h.enqueue(t, "two", "另一个故障怎么处理？", "bob")
	h.run()
	require.Zero(t, h.calls.Load())
	require.Equal(t, "reply_limit", h.job(t, "two").DecisionReason)
}
func TestAutonomousLocalFiltersIsolationAndMessageGap(t *testing.T) {
	h := newAutoHarness(t)
	h.c.Autonomous.MinMessages = 3
	h.c.Autonomous.SamplePercent = 0
	h.save(t)
	h.enqueue(t, "first", "这是第一条普通聊天", "alice")
	h.run()
	require.Zero(t, h.calls.Load())
	h.enqueue(t, "repeat", "这是第一条普通聊天", "bob")
	h.enqueue(t, "second", "这是第二条正常聊天", "alice")
	h.enqueue(t, "third", "这个错误怎么解决？", "bob")
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	h.resetCooldown(t)
	h.c.Autonomous.Groups = []string{"other-group"}
	h.save(t)
	h.enqueue(t, "disabled-group", "这个故障如何排查？", "new-user")
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	h.c.Autonomous.Groups = []string{"group"}
	h.save(t)
	h.resetCooldown(t)
	var wg sync.WaitGroup
	var errorsCount atomic.Int32
	e := Event{ID: "parallel", GroupID: "group", Content: "这个服务如何排查？"}
	e.Author.MemberID = "charlie"
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	p := Payload{Type: groupMessageCreate, Data: raw}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h.s.Enqueue(context.Background(), h.c, p) != nil {
				errorsCount.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, errorsCount.Load())
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE message_id='parallel'`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestAutonomousBudgetRollbackAndPolicyChanges(t *testing.T) {
	for _, mode := range []string{"global", "disabled", "stale", "new_message", "member_cooldown", "failed_send_cooldown"} {
		t.Run(mode, func(t *testing.T) {
			h := newAutoHarness(t)
			if mode == "global" {
				h.c.TotalDailyChatLimit = 0
				h.save(t)
			}
			h.enqueue(t, "one", "这个故障怎么排查？", "alice")
			if mode == "global" {
				require.Zero(t, h.calls.Load())
				return
			}
			switch mode {
			case "disabled":
				h.c.Autonomous.Enabled = false
				h.save(t)
			case "stale":
				_, err := h.db.Exec(`UPDATE qq_bot_jobs SET created_at=NOW()-INTERVAL '3 minutes' WHERE message_id='one'`)
				require.NoError(t, err)
			case "new_message":
				h.enqueue(t, "new", "不用回复", "alice")
			case "member_cooldown":
				_, err := h.db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,payload,kind,status,reply_reserved_at) VALUES('old','app','old','GROUP_MESSAGE_CREATE','group','alice','{}','ambient_chat','delivery_failed',NOW()-INTERVAL '90 seconds')`)
				require.NoError(t, err)
			case "failed_send_cooldown":
				_, err := h.db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,payload,kind,status,reply_reserved_at) VALUES('old','app','old','GROUP_MESSAGE_CREATE','group','bob','{}','ambient_chat','delivery_failed',NOW())`)
				require.NoError(t, err)
			}
			h.run()
			require.Zero(t, h.calls.Load())
			require.Zero(t, h.sends.Load())
			var count int
			require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage WHERE kind='ambient_chat'`).Scan(&count))
			require.Zero(t, count)
			reasons := map[string]string{"disabled": "config_changed", "stale": "stale", "new_message": "conversation_ended", "member_cooldown": "member_cooldown", "failed_send_cooldown": "group_cooldown"}
			require.Equal(t, reasons[mode], h.job(t, "one").DecisionReason)
		})
	}
	h := newAutoHarness(t)
	h.c.TotalDailyChatLimit = 1
	h.save(t)
	_, err := h.db.Exec(`INSERT INTO qq_bot_daily_usage(app_id,scope,day,kind,count) VALUES('app','all',$1,'chat',1)`, day(time.Now()))
	require.NoError(t, err)
	h.enqueue(t, "budget", "这个报错怎么处理？", "alice")
	h.run()
	require.Zero(t, h.calls.Load())
	require.Equal(t, "global_limit", h.job(t, "budget").DecisionReason)
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage WHERE kind='ambient_chat'`).Scan(&count))
	require.Zero(t, count, "group reservation rolls back when global quota fails")
}
func TestAutonomousFollowupCannotBeRetriedAfterModelSkip(t *testing.T) {
	h := newAutoHarness(t)
	h.enqueue(t, "one", "这个报错怎么处理？", "alice")
	h.run()
	h.output.Store(`{"action":"skip"}`)
	h.enqueue(t, "two", "那如何进一步检查？", "alice")
	h.run()
	h.enqueue(t, "three", "那还能怎么继续检查？", "alice")
	h.run()
	require.EqualValues(t, 2, h.calls.Load())
	require.EqualValues(t, 1, h.sends.Load())
}

func TestAutonomousMentionDeliveryUpgradesPendingCandidate(t *testing.T) {
	h := newAutoHarness(t)
	p := h.enqueue(t, "both", "这个故障怎么排查？", "alice")
	p.Type = groupAtMessageCreate
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
	h.output.Store("正常回答")
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	require.Equal(t, "chat", h.job(t, "both").Kind)
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE scope='member:alice' AND kind='chat'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE message_id='both'`).Scan(&count))
	require.Equal(t, 1, count)
}
func TestAutonomousZeroHistoryStillRepliesWithoutFollowup(t *testing.T) {
	h := newAutoHarness(t)
	h.c.HistoryMessages = 0
	h.save(t)
	h.enqueue(t, "one", "这个故障怎么排查？", "alice")
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	h.enqueue(t, "two", "那应该怎么继续排查？", "alice")
	h.run()
	require.EqualValues(t, 1, h.calls.Load())
	var count int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_group_messages`).Scan(&count))
	require.Zero(t, count)
}

func TestAutonomousFollowupContextAndKnowledge(t *testing.T) {
	h := newAutoHarness(t)
	h.c.SiteKnowledge = SiteKnowledgeConfig{Enabled: true, Entries: []SiteKnowledgeEntry{
		{ID: "api", Title: "API 接入", Keywords: []string{"API"}, Content: "本站使用控制台中的专用接入地址", Enabled: true},
		{ID: "unrelated", Title: "发票", Content: "不应注入的发票资料", Enabled: true},
	}}
	h.save(t)
	h.output.Store(`{"action":"reply","text":"先到控制台看看接入地址。"}`)
	h.enqueue(t, "one", "API 接入怎么操作？", "alice")
	h.run()
	require.Contains(t, h.request.Load().(string), "本站使用控制台中的专用接入地址")
	require.NotContains(t, h.request.Load().(string), "不应注入的发票资料")
	h.enqueue(t, "bob", "这是 Bob 的其他话题", "bob")
	h.output.Store(`{"action":"reply","text":"就在控制台的接入说明里。"}`)
	h.enqueue(t, "two", "在哪", "alice")
	h.run()
	require.EqualValues(t, 2, h.calls.Load())
	var req struct {
		Messages []Message `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(h.request.Load().(string)), &req))
	content := sharedHistoryText(req.Messages)
	require.Contains(t, content, "API 接入怎么操作？")
	require.Contains(t, content, "先到控制台看看接入地址。")
	require.Contains(t, content, "本站使用控制台中的专用接入地址", "short followups retain the topic's selected knowledge")
	require.NotContains(t, content, "Bob 的其他话题")
	require.Less(t, strings.Index(content, "API 接入怎么操作？"), strings.Index(content, "先到控制台看看接入地址。"))
	require.Less(t, strings.Index(content, "先到控制台看看接入地址。"), strings.LastIndex(content, "在哪"))
	j := h.job(t, "two")
	// Ordinary directed chat does not inherit possibly mixed ambient answers.
	personal := sharedHistoryText(h.s.history(context.Background(), h.c, j))
	require.NotContains(t, personal, "先到控制台看看接入地址。")
	other := j
	other.MemberID = "bob"
	require.NotContains(t, sharedHistoryText(h.s.autonomousHistory(context.Background(), h.c, other, "在哪")), "先到控制台看看接入地址。")
}

func TestAutonomousFollowupStillRespectsFiltersAndBudgets(t *testing.T) {
	for _, mode := range []string{"mention", "secret", "ack", "other_member", "quota", "failure"} {
		t.Run(mode, func(t *testing.T) {
			h := newAutoHarness(t)
			if mode == "quota" {
				h.c.Autonomous.HourlyReplies = 1
				h.save(t)
			}
			h.enqueue(t, "one", "这个问题怎么处理？", "alice")
			h.run()
			text, member := "不太懂", "alice"
			switch mode {
			case "mention":
				text = "@Bob 真的？"
			case "secret":
				text = "我的密码是 12345"
			case "ack":
				text = "谢谢！"
			case "other_member":
				member = "bob"
			case "failure":
				h.failModel.Store(true)
			}
			h.enqueue(t, "two", text, member)
			h.run()
			if mode == "failure" {
				require.EqualValues(t, 2, h.calls.Load())
				h.failModel.Store(false)
				h.enqueue(t, "three", "那怎么办？", "alice")
				h.run()
				require.EqualValues(t, 2, h.calls.Load())
			} else {
				require.EqualValues(t, 1, h.calls.Load())
			}
			require.EqualValues(t, 1, h.sends.Load())
		})
	}
}

func TestAutonomousAndKnowledgeAtomicSave(t *testing.T) {
	h := newAutoHarness(t)
	ctx := context.Background()
	h.c.Autonomous.Enabled = false
	h.save(t)
	current, err := h.s.Config(ctx)
	require.NoError(t, err)
	draft := current.Public()
	draft.Autonomous.Enabled = true
	draft.Autonomous.Groups = nil
	draft.SiteKnowledge = SiteKnowledgeConfig{Enabled: true, Content: "本站公开资料"}
	versions := draft.SectionVersions
	// A stale knowledge version must not enable chat as a partial save.
	bad := map[string]string{"autonomous": versions["autonomous"], "knowledge": "stale"}
	_, err = h.s.SaveSetup(ctx, 1, SetupRequest{Config: draft, Sections: []string{"autonomous", "knowledge"}, Versions: bad})
	require.ErrorIs(t, err, ErrConfigConflict)
	unchanged, err := h.s.Config(ctx)
	require.NoError(t, err)
	require.False(t, unchanged.Autonomous.Enabled)
	_, err = h.s.SaveSetup(ctx, 1, SetupRequest{Config: draft, Sections: []string{"autonomous", "knowledge"}, Versions: versions})
	require.NoError(t, err)
	saved, err := h.s.Config(ctx)
	require.NoError(t, err)
	require.True(t, saved.Autonomous.Enabled)
	require.True(t, saved.Autonomous.InheritGroups)
	require.Equal(t, "本站公开资料", saved.SiteKnowledge.Content)
	require.Equal(t, current.APIKey, saved.APIKey)
}

func TestAutonomousImageUsesVisionWithoutImageGeneration(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, format := range []string{"png", "gif"} {
			t.Run(protocol+"/"+format, func(t *testing.T) {
				h := newAutoHarness(t)
				h.c.ChatProtocol = protocol
				h.c.ImageEnabled = false
				h.c.SystemPrompt = "你是喜欢接梗的群助手，保持自然口语。"
				h.c.Autonomous.SamplePercent = 100
				h.save(t)
				data := testInputPNG(t)
				if format == "gif" {
					palette := color.Palette{color.White, color.Black}
					first := image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
					second := image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
					first.SetColorIndex(0, 0, 1)
					second.SetColorIndex(1, 1, 1)
					var encoded bytes.Buffer
					require.NoError(t, gif.EncodeAll(&encoded, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{5, 5}}))
					data = encoded.Bytes()
				}
				mime := "image/" + format
				sourceURL := "https://image.example/meme." + format
				var downloads int
				h.s.imageDownload = func(_ context.Context, source string) ([]byte, error) {
					downloads++
					require.Equal(t, sourceURL, source)
					return data, nil
				}
				e := Event{ID: "meme", GroupID: "group", Attachments: []Attachment{{URL: sourceURL, ContentType: mime}}}
				e.Author.MemberID = "alice"
				p := h.enqueueEvent(t, e)
				require.Equal(t, "ambient_chat", h.job(t, e.ID).Kind)
				h.run()
				require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
				h.run()
				require.Equal(t, 1, downloads)
				require.EqualValues(t, 1, h.calls.Load())
				require.EqualValues(t, 1, h.sends.Load(), "autonomous images never send a processing notification")
				require.Equal(t, "replied", h.job(t, e.ID).DecisionReason)

				var body map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(h.request.Load().(string)), &body))
				require.Empty(t, body["tools"], "vision interjections must not expose image generation tools")
				key, imageType := "messages", "image_url"
				if protocol == "responses" {
					key, imageType = "input", "input_image"
				}
				var messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}
				require.NoError(t, json.Unmarshal(body[key], &messages))
				require.GreaterOrEqual(t, len(messages), 3)
				require.JSONEq(t, `"`+h.c.SystemPrompt+`"`, string(messages[0].Content))
				last := messages[len(messages)-1]
				require.Equal(t, "user", last.Role)
				var parts []map[string]any
				require.NoError(t, json.Unmarshal(last.Content, &parts))
				require.Len(t, parts, 2)
				require.Equal(t, imageType, parts[1]["type"])
				var imageURL string
				if protocol == "responses" {
					imageURL, _ = parts[1]["image_url"].(string)
				} else {
					image, ok := parts[1]["image_url"].(map[string]any)
					require.True(t, ok)
					imageURL, _ = image["url"].(string)
				}
				require.Equal(t, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data), imageURL, "the original GIF bytes are passed through; this does not assert animation understanding")
				require.NotContains(t, string(body[key]), "image.example")
				var personal, calls int
				require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage WHERE scope='member:alice'`).Scan(&personal))
				require.Zero(t, personal)
				require.NoError(t, h.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE scope='all' AND kind='chat'`).Scan(&calls))
				require.Equal(t, 1, calls)
			})
		}
	}
}

func TestAutonomousImageIdentityAndNestedCaption(t *testing.T) {
	h := newAutoHarness(t)
	h.output.Store(`{"action":"skip"}`)
	h.c.Autonomous.SamplePercent = 100
	h.c.Autonomous.FollowupSeconds = 0
	h.save(t)
	h.s.imageDownload = func(_ context.Context, _ string) ([]byte, error) { return testInputPNG(t), nil }
	for i, source := range []string{"one", "two", "two", "nested"} {
		h.resetCooldown(t)
		e := Event{ID: fmt.Sprintf("image-%d", i), GroupID: "group", Attachments: []Attachment{{URL: "https://image.example/" + source, ContentType: "image/png"}}}
		e.Author.MemberID = "alice"
		if source == "nested" {
			e.MsgElements = []MessageElement{{Content: "看看", MsgElements: []MessageElement{{Attachments: e.Attachments}}}}
			e.Attachments = nil
		}
		h.enqueueEvent(t, e)
		h.run()
		if i == 2 {
			require.EqualValues(t, 2, h.calls.Load(), "identical image redelivery under a new message ID is still a recent repeat")
			var count int
			require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE message_id=$1`, e.ID).Scan(&count))
			require.Zero(t, count)
		}
	}
	require.EqualValues(t, 3, h.calls.Load(), "different images with identical empty captions must not be deduplicated as the same text")
	require.Zero(t, h.sends.Load())
	require.Contains(t, h.request.Load().(string), "看看")
}

func TestAutonomousImageDownloadFailureStaysSilentAndReservesOnce(t *testing.T) {
	h := newAutoHarness(t)
	h.c.Autonomous.SamplePercent = 100
	h.save(t)
	var downloads int
	h.s.imageDownload = func(_ context.Context, _ string) ([]byte, error) {
		downloads++
		return nil, errors.New("temporary download error with private signed URL")
	}
	e := Event{ID: "broken-image", GroupID: "group", Attachments: []Attachment{{URL: "https://image.example/broken", ContentType: "image/png"}}}
	e.Author.MemberID = "alice"
	p := h.enqueueEvent(t, e)
	h.run()
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, p))
	h.run()
	require.Equal(t, 1, downloads)
	require.Zero(t, h.calls.Load())
	require.Zero(t, h.sends.Load())
	j := h.job(t, e.ID)
	require.Equal(t, "image_error", j.DecisionReason)
	require.NotContains(t, j.Error, "private signed URL")
	var calls int
	require.NoError(t, h.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE scope='all' AND kind='chat'`).Scan(&calls))
	require.Equal(t, 1, calls, "a failed image attempt cannot repeatedly reserve or invoke the model")
}

func TestAutonomousImageAdmissionPreservesFiltersAndBudgets(t *testing.T) {
	for _, mode := range []string{"sampling", "global_quota", "disabled_group", "other_attachment", "quote", "mention", "secret"} {
		t.Run(mode, func(t *testing.T) {
			h := newAutoHarness(t)
			h.c.Autonomous.SamplePercent = 100
			e := Event{ID: "filtered-image", GroupID: "group", Attachments: []Attachment{{URL: "https://image.example/picture", ContentType: "image/png"}}}
			e.Author.MemberID = "alice"
			switch mode {
			case "sampling":
				h.c.Autonomous.SamplePercent = 0
			case "global_quota":
				h.c.TotalDailyChatLimit = 0
			case "disabled_group":
				h.c.Autonomous.Groups = []string{"other-group"}
			case "other_attachment":
				e.Attachments = append(e.Attachments, Attachment{URL: "https://image.example/file", ContentType: "application/pdf"})
			case "quote":
				e.MsgElements = []MessageElement{{MessageType: 103, Content: "其他群友的原话"}}
			case "mention":
				e.Mentions = []Mention{{MemberID: "bob"}}
			case "secret":
				e.MsgElements = []MessageElement{{Content: "我的密码是 123456"}}
			}
			h.save(t)
			var downloads int
			h.s.imageDownload = func(_ context.Context, _ string) ([]byte, error) {
				downloads++
				return testInputPNG(t), nil
			}
			h.enqueueEvent(t, e)
			h.run()
			require.Zero(t, downloads)
			require.Zero(t, h.calls.Load())
			require.Zero(t, h.sends.Load())
		})
	}
}

func TestAutonomousImageFollowupsAreBounded(t *testing.T) {
	h := newAutoHarness(t)
	h.c.Autonomous.SamplePercent = 0
	h.save(t)
	h.s.imageDownload = func(_ context.Context, _ string) ([]byte, error) { return testInputPNG(t), nil }
	h.enqueue(t, "question", "这张截图要怎么处理？", "alice")
	h.run()
	for i := range 4 {
		e := Event{ID: fmt.Sprintf("followup-image-%d", i), GroupID: "group", Attachments: []Attachment{{URL: fmt.Sprintf("https://image.example/followup-%d", i), ContentType: "image/png"}}}
		e.Author.MemberID = "alice"
		h.enqueueEvent(t, e)
		h.run()
		if i < 3 {
			require.Equal(t, "followup", h.job(t, e.ID).TriggerReason)
		}
	}
	require.EqualValues(t, 4, h.calls.Load(), "three image followups can bypass sampling and cooldown, then the normal gates resume")
	require.EqualValues(t, 4, h.sends.Load())
}
