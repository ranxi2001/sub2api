package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func captureMembershipLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := slog.Default()
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func membershipLogs(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record["component"] == "qqbot.membership" {
			records = append(records, record)
		}
	}
	return records
}

func TestMembershipDiagnosticsExcludeCallbackAndAccountData(t *testing.T) {
	output := captureMembershipLogs(t)
	j := Job{ID: "internal-job-id", AppID: "private-app", GroupID: "private-group", MemberID: "private-member", EventType: "GROUP_JOIN_REQUEST"}
	e := Event{ApplySource: "unknown-private-source", InvitedBy: "private-inviter", ApplyAt: "private-invalid-time", Content: "email@example.test private-token"}
	logMembershipEvent(context.Background(), j, &e, "process", "ignored", "not_an_invitation")
	records := membershipLogs(t, output)
	require.Len(t, records, 1)
	require.Equal(t, "other", records[0]["apply_source"])
	require.Equal(t, true, records[0]["has_inviter"])
	require.Equal(t, "internal-job-id", records[0]["job_id"])
	for _, name := range []string{"app_ref", "group_ref", "member_ref"} {
		require.Len(t, records[0][name], 12)
	}
	for _, sensitive := range []string{j.AppID, j.GroupID, j.MemberID, e.InvitedBy, e.ApplySource, e.ApplyAt, "email@example.test", "private-token"} {
		require.NotContains(t, output.String(), sensitive)
	}
}

func TestMembershipDiagnosticsDoNotLogMessagesOrInventSource(t *testing.T) {
	output := captureMembershipLogs(t)
	j := Job{EventType: groupMessageCreate}
	logMembershipEvent(context.Background(), j, &Event{Content: "private-chat"}, "enqueue", "queued", "event_accepted")
	require.Empty(t, output.String())
	j.EventType = "GROUP_MEMBER_ADD"
	logMembershipEvent(context.Background(), j, nil, "process", "failed", "config_unavailable")
	records := membershipLogs(t, output)
	require.Len(t, records, 1)
	require.Equal(t, "WARN", records[0]["level"])
	require.NotContains(t, records[0], "apply_source")
	require.NotContains(t, records[0], "has_inviter")
}
