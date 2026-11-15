//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotMembershipDeliveryDiagnosticsAndRewards(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, source, reason string
		hasInviter           bool
		applyOffset          time.Duration
		wantReward           float64
	}{
		{"delayed-invitation", "invited", "invitation_recorded", true, -time.Minute, 1},
		{"self-apply", "self_apply", "not_an_invitation", false, -time.Minute, 0},
		{"no-inviter", "invited", "missing_inviter", false, -time.Minute, 0},
		{"after-first-join", "invited", "existing_inviter_or_late_invitation", true, time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureMembershipLogs(t)
			inviter, invitee := user(t, db), user(t, db)
			memberID, inviterID := tc.name+"-member", tc.name+"-inviter"
			bind(t, db, inviter, inviterID)
			bind(t, db, invitee, memberID)
			joined := time.Now().Truncate(time.Second)
			deliver := func(eventType, body string) string {
				t.Helper()
				p := Payload{ID: randomID(), Type: eventType, Data: json.RawMessage(body)}
				require.NoError(t, s.Enqueue(ctx, c, p))
				// A retry before processing must not create a second task or reward.
				require.NoError(t, s.Enqueue(ctx, c, p))
				var jobID string
				require.NoError(t, db.QueryRow(`SELECT id FROM qq_bot_jobs WHERE event_key=$1`, p.ID).Scan(&jobID))
				s.runOne(ctx)
				var status, payload, detail string
				require.NoError(t, db.QueryRow(`SELECT status,payload::text,error FROM qq_bot_jobs WHERE id=$1`, jobID).Scan(&status, &payload, &detail))
				require.Equal(t, "completed", status, detail)
				require.JSONEq(t, `{}`, payload, "diagnostics must not retain callback bodies")
				var count int
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_key=$1`, p.ID).Scan(&count))
				require.Equal(t, 1, count)
				return jobID
			}
			// Exercise the documented wire format and delivery out of order.
			deliver("GROUP_MEMBER_ADD", fmt.Sprintf(`{"group_openid":"group","member_openid":%q,"timestamp":%d}`, memberID, joined.Unix()))
			invitedBy := ""
			if tc.hasInviter {
				invitedBy = inviterID
			}
			jobID := deliver("GROUP_JOIN_REQUEST", fmt.Sprintf(`{"group_openid":"group","member_openid":%q,"apply_source":%q,"invited_by":%q,"apply_at":%q}`, memberID, tc.source, invitedBy, joined.Add(tc.applyOffset).Format(time.RFC3339)))
			var balance float64
			require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
			require.Equal(t, tc.wantReward, balance)
			var attributed int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_members WHERE member_id=$1 AND inviter_id<>''`, memberID).Scan(&attributed))
			if tc.wantReward > 0 {
				require.Equal(t, 1, attributed)
			} else {
				require.Zero(t, attributed)
			}
			found, duplicates := false, 0
			for _, record := range membershipLogs(t, output) {
				if record["outcome"] == "duplicate" {
					duplicates++
				}
				if record["job_id"] == jobID && record["stage"] == "process" {
					found = true
					require.Equal(t, tc.reason, record["reason"])
					require.Equal(t, tc.source, record["apply_source"])
					require.Equal(t, tc.hasInviter, record["has_inviter"])
				}
			}
			require.True(t, found, "processing reason must remain visible after the payload is erased")
			require.Equal(t, 2, duplicates)
			require.NotContains(t, output.String(), memberID)
			require.NotContains(t, output.String(), inviterID)
		})
	}
}

func TestQQBotMembershipQueueAndWorkerRejectionsAreDiagnosable(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	output := captureMembershipLogs(t)
	p := Payload{ID: "membership-test", Type: "GROUP_MEMBER_ADD", Data: json.RawMessage(`{"group_openid":"group","member_openid":"member","timestamp":0}`)}
	blocked := c
	blocked.Enabled = false
	require.NoError(t, s.Enqueue(ctx, blocked, p))
	blocked.Enabled, blocked.AllowedGroups = true, nil
	require.NoError(t, s.Enqueue(ctx, blocked, p))
	require.NoError(t, s.Enqueue(ctx, c, p))
	s.runOne(ctx)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE event_key=$1`, p.ID).Scan(&status))
	require.Equal(t, "failed", status)
	p.ID = "disabled-after-queue"
	require.NoError(t, s.Enqueue(ctx, c, p))
	c.Enabled = false
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	s.runOne(ctx)
	reasons := make(map[string]int)
	for _, record := range membershipLogs(t, output) {
		reasons[record["stage"].(string)+":"+record["reason"].(string)]++
	}
	for _, reason := range []string{"enqueue:bot_disabled", "enqueue:group_not_allowed", "process:invalid_membership_time", "process:bot_disabled"} {
		require.Equal(t, 1, reasons[reason], reason)
	}
}
