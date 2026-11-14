//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type helpInteractionRecorder struct {
	mu      sync.Mutex
	codes   []int
	replies []qqTestReply
}

func mockHelpInteractions(t *testing.T, s *Service, ackFails bool) *helpInteractionRecorder {
	t.Helper()
	recorder := &helpInteractionRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		switch {
		case r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case strings.HasPrefix(r.URL.Path, "/interactions/"):
			require.Equal(t, http.MethodPut, r.Method)
			var body struct {
				Code int `json:"code"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			recorder.codes = append(recorder.codes, body.Code)
			if body.Code == 0 {
				var count int
				require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_key=$1`, interactionCreate+":"+strings.TrimPrefix(r.URL.Path, "/interactions/")).Scan(&count))
				require.Equal(t, 1, count, "a success acknowledgement requires a committed durable job")
			}
			if ackFails {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/v2/groups/group/messages":
			recorder.replies = append(recorder.replies, readQQTestReply(t, r))
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("button unexpectedly called endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	return recorder
}

func TestHelpInteractionClickerIdentityAndConcurrentDeduplication(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	original, clicker := user(t, db), user(t, db)
	bind(t, db, original, "help-requester")
	bind(t, db, clicker, "clicker")
	recorder := mockHelpInteractions(t, s, false)
	p := helpInteractionPayload(t, testHelpInteraction("checkin-click", "checkin", "clicker"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(p.Data, &body))
	body["member_openid"] = "help-requester"
	body["author"] = map[string]string{"member_openid": "help-requester"}
	body["data"].(map[string]any)["resolved"].(map[string]any)["user_id"] = "help-requester"
	p.Data, err = json.Marshal(body)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.Enqueue(ctx, c, p) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	s.runOne(ctx)
	p.ID = "redelivery-with-another-outer-id"
	require.NoError(t, s.Enqueue(ctx, c, p))
	s.runOne(ctx)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, clicker).Scan(&balance))
	require.Equal(t, .1, balance)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, original).Scan(&balance))
	require.Zero(t, balance)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE status='completed'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage`).Scan(&count))
	require.Zero(t, count)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	require.Equal(t, []int{0}, recorder.codes)
	require.Len(t, recorder.replies, 1)
	require.Equal(t, "outer-checkin-click", recorder.replies[0].EventID)
	require.Contains(t, recorder.replies[0].text(), `<qqbot-at-user id="clicker" />`)
	require.Contains(t, recorder.replies[0].text(), "签到成功")
}

func TestRetiredLotteryCallbackCannotSpend(t *testing.T) {
	s, db, c, uid, _ := lotteryFixture(t, 20)
	recorder := mockHelpInteractions(t, s, false)
	ctx := context.Background()
	old := testHelpInteraction("lottery-click", "lottery", "member")
	old.Data.Resolved.ButtonData = "qqhelp:v1:lottery"
	require.NoError(t, s.Enqueue(ctx, c, helpInteractionPayload(t, old)))
	s.runOne(ctx)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Zero(t, state.Used)
	require.EqualValues(t, 2, state.Remaining)
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM qq_bot_jobs").Scan(&count))
	require.Zero(t, count)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	require.Equal(t, []int{1}, recorder.codes)
	require.Empty(t, recorder.replies)
}

func TestHelpInteractionUsesFreshConfiguration(t *testing.T) {
	for _, change := range []string{"custom reordered", "custom disabled", "group removed", "bot disabled", "expired"} {
		t.Run(change, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			c, err := s.Config(ctx)
			require.NoError(t, err)
			c.CustomCommands = []CustomCommand{
				{Command: "/first", Reply: "first reply", Enabled: true},
				{Command: "/second", Reply: "second reply", Enabled: true},
				{Command: "/custom", Reply: "custom reply", Enabled: true},
			}
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			recorder := mockHelpInteractions(t, s, false)
			action := "custom:/custom"
			i := testHelpInteraction("action", action, "clicker")
			i.Data.Resolved.ButtonID = "" // QQ's documented group example omits this field.
			i.Scene = ""                  // Older payloads identify the scene using chat_type.
			require.NoError(t, s.Enqueue(ctx, c, helpInteractionPayload(t, i)))
			switch change {
			case "custom reordered":
				c.CustomCommands = []CustomCommand{{Command: "/other", Reply: "wrong reply", Enabled: true}, {Command: "/custom", Reply: "updated reply", Enabled: true}}
			case "custom disabled":
				c.CustomCommands[len(c.CustomCommands)-1].Enabled = false
			case "group removed":
				c.AllowedGroups = []string{"other-group"}
			case "bot disabled":
				c.Enabled = false
			case "expired":
				_, err = db.Exec(`UPDATE qq_bot_jobs SET created_at=$1`, time.Now().Add(-5*time.Minute))
				require.NoError(t, err)
			}
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			s.runOne(ctx)
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			if change == "custom reordered" {
				require.Len(t, recorder.replies, 1)
				require.Contains(t, recorder.replies[0].text(), "updated reply")
				require.NotContains(t, recorder.replies[0].text(), "wrong reply")
			} else {
				require.Empty(t, recorder.replies)
			}
		})
	}
}
