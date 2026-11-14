package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQTokenWaitHonorsInteractionDeadline(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
	}))
	defer server.Close()
	defer unblock()
	s := New(nil, nil, false, nil)
	s.tokenURL = server.URL
	done := make(chan error, 1)
	go func() { _, err := s.token(context.Background(), DefaultConfig()); done <- err }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := s.token(ctx, DefaultConfig())
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	unblock()
	require.NoError(t, <-done)
}

func testHelpInteraction(id, action, member string) buttonInteraction {
	chatType := 1
	i := buttonInteraction{ID: id, Type: 11, Scene: "group", ChatType: &chatType, AppID: "app", GroupID: "group", MemberID: member, Timestamp: time.Now().Format(time.RFC3339)}
	i.Data.Type = 11
	i.Data.Resolved.ButtonData = helpActionPrefix + action
	i.Data.Resolved.ButtonID = helpButtonID(action)
	return i
}

func helpInteractionPayload(t *testing.T, i buttonInteraction) Payload {
	t.Helper()
	raw, err := json.Marshal(i)
	require.NoError(t, err)
	return Payload{ID: "outer-" + i.ID, Op: 0, Type: interactionCreate, Data: raw}
}

func TestHelpInteractionRejectsUnsupportedOrUnauthorizedActions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*buttonInteraction, *Config)
		code   int // -1 means no acknowledgement is appropriate.
	}{
		{"another app", func(i *buttonInteraction, _ *Config) { i.AppID = "other" }, -1},
		{"not a button", func(i *buttonInteraction, _ *Config) { i.Type = 13 }, -1},
		{"missing id", func(i *buttonInteraction, _ *Config) { i.ID = "" }, -1},
		{"disabled", func(_ *buttonInteraction, c *Config) { c.Enabled = false }, 4},
		{"unapproved group", func(i *buttonInteraction, _ *Config) { i.GroupID = "other" }, 4},
		{"missing clicker", func(i *buttonInteraction, _ *Config) { i.MemberID = "" }, 4},
		{"private chat", func(i *buttonInteraction, _ *Config) { i.Scene = "c2c" }, 4},
		{"contradictory scene", func(i *buttonInteraction, _ *Config) { *i.ChatType = 2 }, 4},
		{"missing scene", func(i *buttonInteraction, _ *Config) { i.Scene = ""; i.ChatType = nil }, 4},
		{"retired pagination", func(i *buttonInteraction, _ *Config) {
			i.Data.Resolved.ButtonData = helpActionPrefix + "page:2"
			i.Data.Resolved.ButtonID = helpButtonID("page:2")
		}, 1},
		{"arbitrary command", func(i *buttonInteraction, _ *Config) { i.Data.Resolved.ButtonData = "/签到" }, 1},
		{"unsafe parameter action", func(i *buttonInteraction, _ *Config) {
			i.Data.Resolved.ButtonData = helpActionPrefix + "transfer"
			i.Data.Resolved.ButtonID = helpButtonID("transfer")
		}, 1},
		{"different button id", func(i *buttonInteraction, _ *Config) { i.Data.Resolved.ButtonID = helpButtonID("lottery") }, 1},
		{"disabled custom", func(i *buttonInteraction, c *Config) {
			c.CustomCommands = []CustomCommand{{Command: "/hello", Enabled: false}}
			i.Data.Resolved.ButtonData = helpActionPrefix + "custom:/hello"
			i.Data.Resolved.ButtonID = helpButtonID("custom:/hello")
		}, 1},
		{"stale callback", func(i *buttonInteraction, _ *Config) {
			i.Timestamp = time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
		}, 1},
		{"invalid timestamp", func(i *buttonInteraction, _ *Config) { i.Timestamp = "invalid" }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var codes []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
					return
				}
				require.Equal(t, "/interactions/interaction-id", r.URL.Path)
				require.Equal(t, http.MethodPut, r.Method)
				var body struct {
					Code int `json:"code"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				codes = append(codes, body.Code)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			s := New(nil, nil, false, nil) // Rejections must not touch storage or models.
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			c := DefaultConfig()
			c.AppID, c.Enabled, c.AllowedGroups = "app", true, []string{"group"}
			i := testHelpInteraction("interaction-id", "checkin", "clicker")
			tc.change(&i, &c)
			require.NoError(t, s.Enqueue(context.Background(), c, helpInteractionPayload(t, i)))
			if tc.code < 0 {
				require.Empty(t, codes)
			} else {
				require.Equal(t, []int{tc.code}, codes)
			}
		})
	}
}
