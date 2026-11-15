package qqbot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModerationConfigCompatibilityAndValidation(t *testing.T) {
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"app_id":"app","allowed_groups":["group"]}`), &c))
	require.Equal(t, DefaultModerationConfig(), c.Moderation)
	c = DefaultConfig()
	c.AppID = "app"
	c.AllowedGroups = []string{"group"}
	require.NoError(t, validateModeration(c))
	c.Moderation.Enabled = true
	require.Error(t, validateModeration(c))
	c.Moderation.Groups = []string{"group"}
	require.NoError(t, validateModeration(c))
	for _, change := range []func(*ModerationConfig){
		func(m *ModerationConfig) { m.RepeatCount = 1 }, func(m *ModerationConfig) { m.BurstCount = 101 },
		func(m *ModerationConfig) { m.RepeatWindowSeconds = 61 }, func(m *ModerationConfig) { m.MuteMinutes = 0 },
		func(m *ModerationConfig) { m.EscalatedMuteMinutes = 43201 }, func(m *ModerationConfig) { m.EscalatedMuteMinutes = 1 },
		func(m *ModerationConfig) { m.Groups = []string{"other"} }, func(m *ModerationConfig) { m.Groups = []string{"group", "group"} },
		func(m *ModerationConfig) {
			m.Exemptions = []ModerationExemption{{GroupID: "group", MemberID: "bad\nmember"}}
		},
		func(m *ModerationConfig) {
			m.Exemptions = []ModerationExemption{{GroupID: "other", MemberID: "member"}}
		},
	} {
		changed := c
		change(&changed.Moderation)
		require.Error(t, validateModeration(changed))
	}
}

func TestRetiredSpamReportSettingsAreIgnored(t *testing.T) {
	c := DefaultConfig()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var legacy map[string]any
	require.NoError(t, json.Unmarshal(raw, &legacy))
	moderation, ok := legacy["moderation"].(map[string]any)
	require.True(t, ok)
	moderation["report_enabled"] = true
	moderation["report_mute_minutes"] = 0
	raw, err = json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &c))
	require.False(t, c.Moderation.ReportEnabled)
	require.False(t, c.reportsSpam("group"))
	require.NoError(t, validateConfig(c), "legacy report fields must not require a model or moderation group")
}

func TestModerationWindowsAndFingerprints(t *testing.T) {
	c := DefaultConfig()
	c.AppSecret = "secret"
	c.AppID = "app"
	e := Event{Content: "hello\u200b   world", GroupID: "group"}
	hash := moderationTextHash(c, e)
	e.Content = "hello world"
	require.Equal(t, hash, moderationTextHash(c, e))
	e.GroupID = "other"
	require.NotEqual(t, hash, moderationTextHash(c, e))
	e.Content = ""
	require.Empty(t, moderationTextHash(c, e))
	e.Content = "same caption"
	e.Attachments = []Attachment{{URL: "https://example.invalid/image"}}
	require.Empty(t, moderationTextHash(c, e))
	e.Attachments = nil
	e.MessageType = 103
	require.Empty(t, moderationTextHash(c, e))
	now := time.Now()
	messages := []moderationMessage{
		{ID: "old", Hash: hash, SentAt: now.Add(-15 * time.Second)},
		{ID: "first", Hash: hash, SentAt: now.Add(-3 * time.Second)},
		{ID: "second", Hash: hash, SentAt: now.Add(-2 * time.Second)},
		{ID: "third", Hash: hash, SentAt: now.Add(-time.Second)},
	}
	reason, _ := moderationViolation(c.Moderation, messages, hash, now)
	require.Empty(t, reason)
	messages = append(messages, moderationMessage{ID: "fourth", Hash: hash, SentAt: now})
	reason, recalls := moderationViolation(c.Moderation, messages, hash, now)
	require.Equal(t, "repeat", reason)
	require.Equal(t, []string{"second", "third", "fourth"}, []string{recalls[0].ID, recalls[1].ID, recalls[2].ID})
	c.Moderation.BurstCount = 4
	reason, recalls = moderationViolation(c.Moderation, messages, hash, now)
	require.Equal(t, "burst", reason)
	require.Len(t, recalls, 4)
}
