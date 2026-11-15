//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatExecutionMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	want := ChatExecution{Attempts: 3, Retries: 2, UsageComplete: false, LastErrorCode: "http_503"}
	raw, err := json.Marshal(want)
	require.NoError(t, err)
	for _, tc := range []struct {
		id  string
		raw json.RawMessage
	}{{"with-retry", raw}, {"without-model", nil}} {
		_, err = db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,payload,kind,status,input_tokens,output_tokens,chat_execution) VALUES($1,$2,$1,'GROUP_AT_MESSAGE_CREATE','group',$1,'{}','chat','completed',0,0,$3)`, tc.id, c.AppID, nullableChatExecution(tc.raw))
		require.NoError(t, err)
		stored, err := scanJob(db.QueryRow(`SELECT `+jobColumns+` FROM qq_bot_jobs WHERE id=$1`, tc.id))
		require.NoError(t, err)
		assertChatExecutionMetadata(t, tc.id, want, stored.ChatExecution)
	}
	page, err := s.ListJobs(ctx, JobFilter{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	for _, j := range page.Items {
		assertChatExecutionMetadata(t, j.ID, want, j.ChatExecution)
	}
	runtime, err := s.Runtime(ctx, c)
	require.NoError(t, err)
	require.Len(t, runtime.Jobs, 2)
	for _, j := range runtime.Jobs {
		assertChatExecutionMetadata(t, j.ID, want, j.ChatExecution)
	}
}

func assertChatExecutionMetadata(t *testing.T, id string, want ChatExecution, raw json.RawMessage) {
	t.Helper()
	if id == "without-model" {
		require.Empty(t, raw, "a task without a model call must retain null diagnostics")
		return
	}
	var got ChatExecution
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, want, got)
}
