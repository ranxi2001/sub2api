//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type moderationHarness struct {
	s       *Service
	c       Config
	db      *sql.DB
	server  *httptest.Server
	mu      sync.Mutex
	mutes   []time.Time
	recalls []string
	fail    atomic.Bool
}

func newModerationHarness(t *testing.T) *moderationHarness {
	t.Helper()
	db := testDB(t)
	s := savedBot(t, db)
	h := &moderationHarness{s: s, db: db}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			fmt.Fprint(w, `{"access_token":"test-only","expires_in":7200}`)
		case strings.HasSuffix(r.URL.Path, "/restrict_chat_setting"):
			require.Equal(t, http.MethodPost, r.Method)
			var body struct {
				Members []struct {
					Op     string `json:"op"`
					Member string `json:"member_openid"`
					Until  string `json:"mute_expire_at"`
				} `json:"members"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Len(t, body.Members, 1)
			require.Equal(t, "add", body.Members[0].Op)
			require.NotEmpty(t, body.Members[0].Member)
			until, err := time.Parse(time.RFC3339, body.Members[0].Until)
			require.NoError(t, err)
			h.mu.Lock()
			h.mutes = append(h.mutes, until)
			h.mu.Unlock()
			if h.fail.Load() {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"code":40062003}`)
			}
		case strings.Contains(r.URL.Path, "/messages/"):
			require.Equal(t, http.MethodDelete, r.Method)
			h.mu.Lock()
			h.recalls = append(h.recalls, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
			h.mu.Unlock()
		default:
			t.Errorf("unexpected outbound request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(h.server.Close)
	var err error
	h.c, err = s.Config(context.Background())
	require.NoError(t, err)
	h.c.HistoryMessages = 0
	h.c.Moderation.Enabled = true
	h.c.Moderation.Groups = []string{"group", "other-group"}
	h.save(t)
	h.configure(s)
	return h
}
func (h *moderationHarness) configure(s *Service) {
	s.qqURL = h.server.URL
	s.tokenURL = h.server.URL + "/token"
}
func (h *moderationHarness) save(t *testing.T) {
	t.Helper()
	_, err := h.s.SaveConfig(context.Background(), h.c)
	require.NoError(t, err)
}
func moderationEvent(id, content string) Event {
	e := Event{ID: id, GroupID: "group", Content: content}
	e.Author.MemberID = "alice"
	e.Author.MemberRole = "member"
	e.Timestamp, _ = json.Marshal(time.Now().Format(time.RFC3339Nano))
	return e
}
func (h *moderationHarness) enqueue(t *testing.T, e Event) {
	t.Helper()
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	require.NoError(t, h.s.Enqueue(context.Background(), h.c, Payload{Type: groupMessageCreate, Data: raw}))
}
func (h *moderationHarness) spam(t *testing.T, prefix string) {
	t.Helper()
	for i := 0; i < 4; i++ {
		h.enqueue(t, moderationEvent(fmt.Sprintf("%s%d", prefix, i), "same repeated text"))
	}
}
func (h *moderationHarness) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 105; i++ {
		var n int
		require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_moderation_operations WHERE status='queued'`).Scan(&n))
		if n == 0 {
			return
		}
		_, err := h.db.Exec(`UPDATE qq_bot_moderation_rates SET next_at='epoch'`)
		require.NoError(t, err)
		h.s.runModeration(context.Background())
	}
	t.Fatal("moderation queue did not drain")
}
func (h *moderationHarness) records(t *testing.T) []ModerationRecord {
	t.Helper()
	r, err := h.s.moderationRecords(context.Background(), h.c)
	require.NoError(t, err)
	return r
}

func TestModerationRepeatDedupeRestartAndEscalation(t *testing.T) {
	h := newModerationHarness(t)
	for i := 0; i < 4; i++ {
		e := moderationEvent(fmt.Sprintf("msg%d", i), "same repeated text")
		raw, _ := json.Marshal(e)
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		for j := 0; j < 12; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				kind := groupMessageCreate
				if j%2 == 0 {
					kind = groupAtMessageCreate
				}
				errs <- h.s.Enqueue(context.Background(), h.c, Payload{Type: kind, Data: raw})
			}(j)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
	}
	records := h.records(t)
	require.Len(t, records, 1)
	require.Equal(t, 3, records[0].RecallTotal)
	require.Equal(t, 300, records[0].MuteSeconds)
	var seen, queued int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_moderation_events`).Scan(&seen))
	require.Equal(t, 4, seen)
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE status='queued' AND message_id<>'msg0'`).Scan(&queued))
	require.Zero(t, queued)
	// A new service instance claims durable work; never waits for AI workers.
	h.s = New(h.db, testEncryptor{}, true, nil)
	h.configure(h.s)
	h.drain(t)
	h.mu.Lock()
	require.Len(t, h.mutes, 1)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), h.mutes[0], 3*time.Second)
	require.ElementsMatch(t, []string{"msg1", "msg2", "msg3"}, h.recalls)
	h.mu.Unlock()
	records = h.records(t)
	require.Equal(t, "succeeded", records[0].MuteStatus)
	require.Equal(t, 3, records[0].RecallSucceeded)
	// Simulate mute expiry without resetting the last successful punishment.
	_, err := h.db.Exec(`UPDATE qq_bot_moderation_members SET cooldown_until='epoch'; UPDATE qq_bot_moderation_events SET sent_at=NOW()-INTERVAL '6 minutes'`)
	require.NoError(t, err)
	h.spam(t, "again")
	records = h.records(t)
	require.Len(t, records, 2)
	require.Equal(t, 1800, records[0].MuteSeconds)
	h.drain(t)
	h.s.runModeration(context.Background())
	h.mu.Lock()
	require.Len(t, h.mutes, 2)
	h.mu.Unlock()
	runtime, err := h.s.Runtime(context.Background(), h.c)
	require.NoError(t, err)
	require.Len(t, runtime.ModerationRecords, 2)
}

func TestModerationBurstIsolationAndExemptions(t *testing.T) {
	h := newModerationHarness(t)
	for _, role := range []string{"owner", "admin", ""} {
		for i := 0; i < 12; i++ {
			e := moderationEvent(fmt.Sprintf("role-%s-%d", role, i), "spam")
			e.Author.MemberRole = role
			h.enqueue(t, e)
		}
	}
	for i := 0; i < 12; i++ {
		e := moderationEvent(fmt.Sprintf("bot-%d", i), "spam")
		e.Author.Bot = true
		h.enqueue(t, e)
	}
	h.c.Moderation.Exemptions = []ModerationExemption{{GroupID: "group", MemberID: "white"}}
	h.c.GroupAdmins = []GroupAdmin{{AppID: "app", GroupID: "group", MemberID: "bound-admin"}}
	h.save(t)
	for _, member := range []string{"white", "bound-admin"} {
		for i := 0; i < 12; i++ {
			e := moderationEvent(fmt.Sprintf("%s-%d", member, i), "spam")
			e.Author.MemberID = member
			h.enqueue(t, e)
		}
	}
	for _, stamp := range []string{time.Now().Add(-3 * time.Minute).Format(time.RFC3339), time.Now().Add(time.Minute).Format(time.RFC3339), "invalid"} {
		for i := 0; i < 12; i++ {
			e := moderationEvent(fmt.Sprintf("%s-%d", stamp, i), "spam")
			e.Timestamp, _ = json.Marshal(stamp)
			h.enqueue(t, e)
		}
	}
	for i := 0; i < 3; i++ {
		e := moderationEvent(fmt.Sprintf("a-%d", i), "same")
		h.enqueue(t, e)
		e.ID = fmt.Sprintf("b-%d", i)
		e.Author.MemberID = "bob"
		h.enqueue(t, e)
		e.ID = fmt.Sprintf("g-%d", i)
		e.Author.MemberID = "alice"
		e.GroupID = "other-group"
		h.enqueue(t, e)
	}
	require.Empty(t, h.records(t))
	// Empty image captions cannot cause a duplicate-text penalty, but images
	// still count as individual messages toward the rapid-message threshold.
	for i := 0; i < 10; i++ {
		e := moderationEvent(fmt.Sprintf("image-%d", i), "")
		e.Author.MemberID = "images"
		e.Attachments = []Attachment{{URL: fmt.Sprintf("https://example.invalid/%d.png", i)}}
		h.enqueue(t, e)
		if i < 9 {
			require.Empty(t, h.records(t))
		}
	}
	r := h.records(t)
	require.Len(t, r, 1)
	require.Equal(t, "burst", r[0].Reason)
	require.Equal(t, 10, r[0].RecallTotal)
}

func TestModerationPolicyRevocation(t *testing.T) {
	for _, change := range []string{"disable", "whitelist", "threshold", "sandbox", "group"} {
		t.Run(change, func(t *testing.T) {
			h := newModerationHarness(t)
			h.spam(t, "before")
			require.Len(t, h.records(t), 1)
			switch change {
			case "disable":
				h.c.Moderation.Enabled = false
			case "whitelist":
				h.c.Moderation.Exemptions = []ModerationExemption{{GroupID: "group", MemberID: "alice"}}
			case "threshold":
				h.c.Moderation.RepeatCount = 5
			case "sandbox":
				h.c.Sandbox = true
			case "group":
				h.c.Moderation.Groups = []string{"other-group"}
			}
			h.save(t)
			h.s.runModeration(context.Background())
			var pending int
			require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_moderation_operations WHERE status<>'cancelled'`).Scan(&pending))
			require.Zero(t, pending)
			h.mu.Lock()
			require.Empty(t, h.mutes)
			require.Empty(t, h.recalls)
			h.mu.Unlock()
		})
	}
}

func TestModerationFailuresExpiryAndRateLimits(t *testing.T) {
	h := newModerationHarness(t)
	h.fail.Store(true)
	h.spam(t, "fail")
	h.s.runModeration(context.Background())
	h.s.runModeration(context.Background())
	// First mute and first recall use different shared limits. A second recall
	// is blocked across two service instances during the same 200 ms window.
	other := New(h.db, testEncryptor{}, true, nil)
	h.configure(other)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); other.runModeration(context.Background()) }()
	}
	wg.Wait()
	h.mu.Lock()
	require.Len(t, h.mutes, 1)
	require.Len(t, h.recalls, 1)
	h.mu.Unlock()
	h.drain(t)
	r := h.records(t)
	require.Equal(t, "unconfirmed", r[0].MuteStatus)
	require.Contains(t, r[0].Error, "40062003")
	require.Equal(t, 3, r[0].RecallSucceeded)
	// A failed mute must not escalate the next offense.
	_, err := h.db.Exec(`UPDATE qq_bot_moderation_members SET cooldown_until='epoch';UPDATE qq_bot_moderation_events SET sent_at=NOW()-INTERVAL '2 minutes'`)
	require.NoError(t, err)
	h.spam(t, "expire")
	r = h.records(t)
	require.Equal(t, 300, r[0].MuteSeconds)
	_, err = h.db.Exec(`UPDATE qq_bot_moderation_operations SET expires_at=NOW()-INTERVAL '1 second' WHERE status='queued'`)
	require.NoError(t, err)
	h.drain(t)
	r = h.records(t)
	require.Equal(t, "expired", r[0].MuteStatus)
	_, err = h.db.Exec(`UPDATE qq_bot_moderation_operations SET status='started',started_at=NOW()-INTERVAL '2 minutes' WHERE kind='mute'`)
	require.NoError(t, err)
	h.s.maintainModeration(context.Background())
	h.s.runModeration(context.Background())
	h.mu.Lock()
	require.Len(t, h.mutes, 1)
	h.mu.Unlock()
	r = h.records(t)
	require.Equal(t, "unconfirmed", r[0].MuteStatus)
	require.Contains(t, r[0].Error, "结果未知")
}

func TestModerationContinuedSpamAndReplySuppression(t *testing.T) {
	h := newModerationHarness(t)
	ctx := context.Background()
	h.c.BaseURL = h.server.URL + "/v1"
	h.save(t)
	h.spam(t, "start")
	for i := 0; i < 105; i++ {
		e := moderationEvent(fmt.Sprintf("continued%d", i), "more spam")
		blocked, err := h.s.moderateIncoming(ctx, h.c, e)
		require.NoError(t, err)
		require.True(t, blocked)
	}
	records := h.records(t)
	require.Len(t, records, 1)
	require.Equal(t, 100, records[0].RecallTotal)
	// Even a delayed duplicate remains blocked without creating another action.
	e := moderationEvent("start3", "same repeated text")
	e.Timestamp, _ = json.Marshal(time.Now().Add(-3 * time.Minute).Format(time.RFC3339Nano))
	blocked, err := h.s.moderateIncoming(ctx, h.c, e)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, 100, h.records(t)[0].RecallTotal)

	// Simulate a concurrent enqueue committing after the incident cancelled jobs.
	e = moderationEvent("start3", "same repeated text")
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	_, err = h.db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,message_id,payload)
 VALUES('late-job','app','late-event',$1,'group','alice','start3',$2)`, groupAtMessageCreate, string(raw))
	require.NoError(t, err)
	h.s.runOne(ctx)
	var status, detail string
	require.NoError(t, h.db.QueryRow(`SELECT status,error FROM qq_bot_jobs WHERE id='late-job'`).Scan(&status, &detail))
	require.Equal(t, "skipped", status)
	require.Contains(t, detail, "防刷屏")
	err = h.s.sendFormatted(ctx, h.c, Job{GroupID: "group", MessageID: "start3", CreatedAt: time.Now()}, "reply", nil, 1)
	require.ErrorContains(t, err, "防刷屏")
	h.mu.Lock()
	require.Empty(t, h.mutes)
	require.Empty(t, h.recalls)
	h.mu.Unlock()
}

func TestModerationPolicyResetDoesNotReuseOldWindows(t *testing.T) {
	h := newModerationHarness(t)
	for i := 0; i < 3; i++ {
		h.enqueue(t, moderationEvent(fmt.Sprintf("old%d", i), "same repeated text"))
	}
	h.c.Moderation.Enabled = false
	h.save(t)
	h.c.Moderation.Enabled = true
	h.save(t)
	for i := 0; i < 3; i++ {
		e := moderationEvent(fmt.Sprintf("old%d", i), "same repeated text")
		blocked, err := h.s.moderateIncoming(context.Background(), h.c, e)
		require.NoError(t, err)
		require.False(t, blocked, "a policy reset must not block innocent old events")
	}
	for i := 0; i < 3; i++ {
		h.enqueue(t, moderationEvent(fmt.Sprintf("new%d", i), "same repeated text"))
		require.Empty(t, h.records(t))
	}
	h.enqueue(t, moderationEvent("new3", "same repeated text"))
	require.Len(t, h.records(t), 1)
	h.drain(t)
	h.mu.Lock()
	require.Len(t, h.mutes, 1)
	require.ElementsMatch(t, []string{"new1", "new2", "new3"}, h.recalls)
	h.mu.Unlock()
}

func TestModerationIndependentWindowsWithManyMessages(t *testing.T) {
	for _, reason := range []string{"repeat", "burst"} {
		t.Run(reason, func(t *testing.T) {
			h := newModerationHarness(t)
			h.c.Moderation.RepeatWindowSeconds = 60
			h.c.Moderation.BurstWindowSeconds = 1
			h.save(t)
			// More than 100 distinct texts in the long window are below both
			// thresholds when distributed across time. Recent spam must still hit.
			_, err := h.db.Exec(`INSERT INTO qq_bot_moderation_events
 (app_id,sandbox,group_id,member_id,message_id,policy_hash,content_hash,sent_at)
 SELECT 'app',FALSE,'group','alice','old-'||n,$1,'hash-'||n,NOW()-INTERVAL '5 seconds'-n*INTERVAL '400 milliseconds'
 FROM generate_series(1,101) n`, moderationPolicy(h.c))
			require.NoError(t, err)
			count := h.c.Moderation.RepeatCount
			if reason == "burst" {
				count = h.c.Moderation.BurstCount
			}
			for i := 0; i < count; i++ {
				content := "same repeated text"
				if reason == "burst" {
					content = fmt.Sprintf("distinct text %d", i)
				}
				h.enqueue(t, moderationEvent(fmt.Sprintf("fresh%d", i), content))
				if i < count-1 {
					require.Empty(t, h.records(t))
				}
			}
			records := h.records(t)
			require.Len(t, records, 1)
			require.Equal(t, reason, records[0].Reason)
			recalls := count
			if reason == "repeat" {
				recalls--
			}
			require.Equal(t, recalls, records[0].RecallTotal)
		})
	}
}
