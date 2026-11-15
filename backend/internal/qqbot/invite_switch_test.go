package qqbot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInviteSwitchDefaultsAndLegacyConfiguration(t *testing.T) {
	require.False(t, DefaultConfig().InviteEnabled)
	require.False(t, InviteConfig{}.Enabled)
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"legacy enabled", `{"invite_reward":1.25,"invite_daily_limit":10}`, true},
		{"legacy zero amount", `{"invite_reward":0,"invite_daily_limit":10}`, false},
		{"legacy zero limit", `{"invite_reward":1.25,"invite_daily_limit":0}`, false},
		{"legacy empty", `{}`, false},
		{"explicit disabled", `{"invite_enabled":false,"invite_reward":1.25,"invite_daily_limit":10}`, false},
		{"explicit enabled", `{"invite_enabled":true,"invite_reward":1.25,"invite_daily_limit":10}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &c))
			require.Equal(t, tc.want, c.InviteEnabled)
			raw, err := json.Marshal(c)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"invite_enabled":`)
			var restored Config
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.Equal(t, c.InviteEnabled, restored.InviteEnabled)
		})
	}
	for _, raw := range []string{`{"invite_enabled":"false"}`, `{"invite_enabled":1}`} {
		var c Config
		require.Error(t, json.Unmarshal([]byte(raw), &c))
	}
}

func TestInviteRulesAndRecordStatusRespectIndependentSwitch(t *testing.T) {
	c := DefaultConfig()
	c.Enabled, c.InviteReward, c.InviteDailyLimit = true, 1.25, 10
	c.AllowedGroups = []string{"group"}
	joined := time.Now()
	invited := joined.Add(-time.Minute)
	r := ActivityRecord{
		GroupID: "group", InvitedAt: &invited, JoinedAt: &joined,
		Participant: ActivityParticipant{Bound: true, UserID: 2, UserStatus: "active"},
		Inviter:     &ActivityParticipant{Bound: true, UserID: 1, UserStatus: "active"},
	}
	require.Contains(t, siteInviteRules(c), "邀请活动未开启")
	require.Equal(t, "disabled", invitationRecordStatus(c, r, 0, false, 0, true))
	// Existing credits retain their status when future awards are switched off.
	require.Equal(t, "rewarded", invitationRecordStatus(c, r, 1, true, 0, true))
	c.InviteEnabled = true
	require.Contains(t, siteInviteRules(c), "USD 1.25")
	require.Equal(t, "pending_reward", invitationRecordStatus(c, r, 0, false, 0, true))
}
