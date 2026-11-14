package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutomaticNotificationMentionAllDelivery(t *testing.T) {
	for _, tc := range []struct {
		name        string
		event       string
		priceAtAll  bool
		healthAtAll bool
		wantPrefix  string
	}{
		{name: "price default", event: priceNotificationEvent},
		{name: "price everyone", event: priceNotificationEvent, priceAtAll: true, wantPrefix: "<qqbot-at-everyone />\n"},
		{name: "price independent of health", event: priceNotificationEvent, healthAtAll: true},
		{name: "health default", event: healthNotificationEvent},
		{name: "health everyone", event: healthNotificationEvent, healthAtAll: true, wantPrefix: "<qqbot-at-everyone />\n"},
		{name: "health independent of price", event: healthNotificationEvent, priceAtAll: true},
		{name: "ranking only mentions winner", event: rankingNotificationEvent, priceAtAll: true, healthAtAll: true, wantPrefix: "<qqbot-at-user id=\"winner\" />\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			c.Enabled, c.AppID, c.AppSecret = true, "app", "test"
			c.AllowedGroups = []string{"group"}
			c.PriceNotifications.Enabled, c.PriceNotifications.MentionAll = true, tc.priceAtAll
			c.HealthNotifications.Enabled, c.HealthNotifications.MentionAll = true, tc.healthAtAll
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":"7200"}`))
					return
				}
				requests++
				require.Equal(t, "/v2/groups/group/messages", r.URL.Path)
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.EqualValues(t, 2, body["msg_type"])
				markdown, ok := body["markdown"].(map[string]any)
				require.True(t, ok, "markdown must be an object")
				require.Equal(t, tc.wantPrefix+"通知正文", markdown["content"])
				for _, key := range []string{"msg_id", "event_id", "msg_seq", "content"} {
					require.NotContains(t, body, key)
				}
				_, _ = w.Write([]byte(`{"id":"notice"}`))
			}))
			defer server.Close()
			s := New(nil, nil, false, nil)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			j := Job{AppID: "app", GroupID: "group", MemberID: "winner", EventType: tc.event}
			require.NoError(t, s.sendPriceNotification(context.Background(), c, j, "通知正文"))
			require.Equal(t, 1, requests)
		})
	}
}

func TestAutomaticNotificationMentionAllLengthBoundary(t *testing.T) {
	c := DefaultConfig()
	c.Enabled, c.AppID = true, "app"
	c.AllowedGroups = []string{"group"}
	c.PriceNotifications.Enabled, c.PriceNotifications.MentionAll = true, true
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":"7200"}`))
			return
		}
		requests++
		var body struct {
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, []rune(body.Markdown.Content), 1800)
		_, _ = w.Write([]byte(`{"id":"notice"}`))
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	j := Job{AppID: "app", GroupID: "group", EventType: priceNotificationEvent}
	limit := 1800 - len([]rune("<qqbot-at-everyone />\n"))
	require.NoError(t, s.sendPriceNotification(context.Background(), c, j, strings.Repeat("字", limit)))
	require.ErrorContains(t, s.sendPriceNotification(context.Background(), c, j, strings.Repeat("字", limit+1)), "通知内容过长")
	require.Equal(t, 1, requests)
}
