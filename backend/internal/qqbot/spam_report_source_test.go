package qqbot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func spamSourceTestEvent(id string) Event {
	e := Event{ID: id, GroupID: "group", Content: "低价代充会员限时优惠，全网最低价格，立即下单请私聊我。"}
	e.Author.MemberID, e.Author.MemberRole = "target", "member"
	e.MessageScene.Ext = []string{"msg_idx=idx-" + id, "auth_token=must-not-be-retained"}
	e.Timestamp, _ = json.Marshal(time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano))
	return e
}

func TestSpamReportSourceTextEligibilityFailsClosed(t *testing.T) {
	require.True(t, spamReportTextEligible(spamSourceTestEvent("valid")))
	for _, tc := range []struct {
		name string
		edit func(*Event)
	}{
		{"unknown_role", func(e *Event) { e.Author.MemberRole = "" }},
		{"administrator", func(e *Event) { e.Author.MemberRole = "admin" }},
		{"owner", func(e *Event) { e.Author.MemberRole = "owner" }},
		{"robot", func(e *Event) { e.Author.Bot = true }},
		{"quote_id", func(e *Event) { e.MessageReference.MessageID = "quoted" }},
		{"quote_index", func(e *Event) { e.MessageScene.Ext = []string{"ref_msg_idx=quoted"} }},
		{"attachment", func(e *Event) { e.Attachments = []Attachment{{URL: "https://example.invalid/image.png"}} }},
		{"nested_content", func(e *Event) { e.MsgElements = []MessageElement{{Content: e.Content}} }},
		{"forwarded", func(e *Event) { e.MessageType = 102 }},
		{"card", func(e *Event) { e.MessageType = 3 }},
		{"empty", func(e *Event) { e.Content = " \n\t" }},
		{"too_many_characters", func(e *Event) { e.Content = strings.Repeat("a", 6001) }},
		{"invalid_utf8", func(e *Event) { e.Content = string([]byte{0xff, 0xfe}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := spamSourceTestEvent("blocked")
			tc.edit(&e)
			require.False(t, spamReportTextEligible(e))
		})
	}
}

func TestSpamReportSourcePolicyTracksModelAndSafetyChanges(t *testing.T) {
	c := DefaultConfig()
	c.Enabled, c.Moderation.ReportEnabled = true, true
	c.AppID, c.AppSecret = "app", "test-only"
	c.AllowedGroups, c.Moderation.Groups = []string{"group"}, []string{"group"}
	c.BaseURL, c.APIKey, c.ChatModel = "https://example.invalid", "test-key", "test-model"
	require.True(t, c.reportsSpam("group"), "reporting works without automatic anti-flood enforcement")
	require.False(t, c.moderates("group"))
	require.False(t, c.reportsSpam("another-group"))
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{"disable_bot", func(c *Config) { c.Enabled = false }},
		{"disable_reports", func(c *Config) { c.Moderation.ReportEnabled = false }},
		{"application", func(c *Config) { c.AppID = "another-app" }},
		{"sandbox", func(c *Config) { c.Sandbox = true }},
		{"secret", func(c *Config) { c.AppSecret = "another-secret" }},
		{"model", func(c *Config) { c.ChatModel = "another-model" }},
		{"protocol", func(c *Config) { c.ChatProtocol = "responses" }},
		{"endpoint", func(c *Config) { c.BaseURL = "https://another.invalid" }},
		{"model_key", func(c *Config) { c.APIKey = "another-key" }},
		{"whitelist", func(c *Config) {
			c.Moderation.Exemptions = []ModerationExemption{{GroupID: "group", MemberID: "target"}}
		}},
		{"bound_administrator", func(c *Config) { c.GroupAdmins = []GroupAdmin{{AppID: "app", GroupID: "group", MemberID: "target"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := c
			tc.edit(&changed)
			require.NotEqual(t, spamReportPolicy(c), spamReportPolicy(changed))
		})
	}
}
