//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomCommandsPersistAndQueueOnce(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.CustomCommands = []CustomCommand{
		{Command: " a ", Reply: "第一行\n第二行", Enabled: true},
		{Command: "/关闭", Reply: "不会回复"},
	}
	saved, err := s.SaveBasicConfig(ctx, c.Public())
	require.NoError(t, err)
	require.Equal(t, "/a", saved.CustomCommands[0].Command)
	require.Empty(t, saved.AppSecret)
	// Activity writes preserve commands in the shared configuration document.
	invite, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	require.NoError(t, s.SaveInviteConfig(ctx, invite))
	c, err = s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, saved.CustomCommands, c.CustomCommands)

	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/messages":
			body := readQQTestReply(t, r)
			replies = append(replies, body.text())
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	enqueue := func(config Config, id, content, group string) Payload {
		e := Event{ID: id, Content: content, GroupID: group}
		e.Author.MemberID = "unbound-member"
		raw, marshalErr := json.Marshal(e)
		require.NoError(t, marshalErr)
		p := Payload{ID: "delivery-" + id, Type: groupMessageCreate, Data: raw}
		require.NoError(t, s.Enqueue(ctx, config, p))
		return p
	}
	p := enqueue(c, "custom", "a", "group")
	p.ID, p.Type = "mention-delivery", groupAtMessageCreate
	require.NoError(t, s.Enqueue(ctx, c, p))
	enqueue(c, "disabled-command", "/关闭", "group")
	enqueue(c, "prefix", "/abc", "group")
	enqueue(c, "group-denied", "/a", "not-allowed")
	disabled := c
	disabled.Enabled = false
	enqueue(disabled, "bot-disabled", "/a", "group")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs`).Scan(&count))
	require.Equal(t, 1, count)
	s.runOne(ctx)
	s.runOne(ctx)
	require.Equal(t, []string{"<qqbot-at-user id=\"unbound-member\" />\n第一行\n第二行"}, replies)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage`).Scan(&count))
	require.Zero(t, count)

	// A command disabled while queued must not send its old reply.
	enqueue(c, "queued-before-disable", "a", "group")
	c.CustomCommands[0].Enabled = false
	_, err = s.SaveBasicConfig(ctx, c.Public())
	require.NoError(t, err)
	s.runOne(ctx)
	require.Len(t, replies, 1)
	c.CustomCommands = nil
	_, err = s.SaveBasicConfig(ctx, c.Public())
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	require.Empty(t, c.CustomCommands)
}
