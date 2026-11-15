//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// Write the historical setting directly: an upgrade must also deactivate an
// already-enabled setting, independently of current client defaults.
func seedLegacySpamReportConfig(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`UPDATE settings SET value=jsonb_set(
 jsonb_set(value::jsonb,'{moderation,report_enabled}','true'::jsonb),
 '{moderation,groups}','["group"]'::jsonb)::text WHERE key=$1`, configKey)
	require.NoError(t, err)
	var enabled bool
	require.NoError(t, db.QueryRow(`SELECT (value::jsonb->'moderation'->>'report_enabled')::boolean FROM settings WHERE key=$1`, configKey).Scan(&enabled))
	require.True(t, enabled, "fixture must represent the pre-retirement saved setting")
}

func TestSpamReportRetirementIgnoresLegacyEnabledSettingAndSavesDisabled(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	seedLegacySpamReportConfig(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	require.False(t, c.Moderation.ReportEnabled)
	require.False(t, c.reportsSpam("group"))
	// A stale client or an internal caller cannot restore the retired feature.
	c.Moderation.ReportEnabled = true
	c, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	require.False(t, c.Moderation.ReportEnabled)
	var enabled bool
	require.NoError(t, db.QueryRow(`SELECT (value::jsonb->'moderation'->>'report_enabled')::boolean FROM settings WHERE key=$1`, configKey).Scan(&enabled))
	require.False(t, enabled)
}

func TestSpamReportRetirementDoesNotCaptureNewOriginalMessages(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.ChatDisabled, c.HistoryMessages = true, 0
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	seedLegacySpamReportConfig(t, db)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	require.False(t, c.Moderation.ReportEnabled)
	// An in-flight callback can retain the old enabled snapshot. The persisted
	// configuration must still prevent evidence capture.
	c.Moderation.ReportEnabled = true
	e := spamSourceTestEvent("new-original")
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	require.NoError(t, s.Enqueue(ctx, c, Payload{ID: "original-event", Type: groupMessageCreate, Data: raw}))
	var sources int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_spam_report_sources`).Scan(&sources))
	require.Zero(t, sources)
}

func TestSpamReportRetirementCancelsQueuedRecallAndMuteWithoutQQ(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	seedLegacySpamReportConfig(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	legacy := c
	legacy.Moderation.ReportEnabled = true
	policy := spamReportPolicy(legacy)
	_, err = db.Exec(`INSERT INTO qq_bot_moderation_incidents
 (id,app_id,sandbox,group_id,member_id,policy_hash,reason,mute_seconds)
 VALUES('legacy-report-incident',$1,$2,'group','target',$3,'spam_advertising',300)`, c.AppID, c.Sandbox, policy)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO qq_bot_spam_reports
 (id,app_id,sandbox,group_id,reporter_id,message_id,member_id,policy_hash,status,incident_id)
 VALUES('legacy-report',$1,$2,'group','reporter','reported-original','target',$3,'queued','legacy-report-incident')`, c.AppID, c.Sandbox, policy)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO qq_bot_moderation_operations(incident_id,kind,message_id,expires_at)
 VALUES('legacy-report-incident','recall','reported-original',NOW()+INTERVAL '1 minute'),
 ('legacy-report-incident','mute','',NOW()+INTERVAL '1 minute')`)
	require.NoError(t, err)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Errorf("retired reporting must not make QQ requests: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	s.runModeration(ctx)
	s.runModeration(ctx)
	rows, err := db.Query(`SELECT kind,status,started_at,finished_at FROM qq_bot_moderation_operations ORDER BY kind`)
	require.NoError(t, err)
	defer rows.Close()
	statuses := map[string]string{}
	for rows.Next() {
		var kind, status string
		var started, finished sql.NullTime
		require.NoError(t, rows.Scan(&kind, &status, &started, &finished))
		statuses[kind] = status
		require.False(t, started.Valid)
		require.True(t, finished.Valid)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[string]string{"recall": "cancelled", "mute": "cancelled"}, statuses)
	require.Zero(t, requests.Load())
}

func TestSpamReportRetirementQueuedCommandsNeverReviewOrCallModels(t *testing.T) {
	for _, eventType := range []string{groupMessageCreate, groupAtMessageCreate} {
		t.Run(eventType, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			var modelCalls, messageCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					_, _ = fmt.Fprint(w, `{"access_token":"test-only","expires_in":7200}`)
				case "/v2/groups/group/messages":
					messageCalls.Add(1)
					reply := readQQTestReply(t, r)
					require.Contains(t, reply.text(), "未知指令，请发送 帮助。")
					_, _ = fmt.Fprint(w, `{"id":"retired-command-reply"}`)
				default:
					modelCalls.Add(1)
					t.Errorf("retired command must not call a model or other QQ endpoint: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusForbidden)
				}
			}))
			t.Cleanup(server.Close)
			c, err := s.Config(ctx)
			require.NoError(t, err)
			c.BaseURL = server.URL + "/v1"
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			seedLegacySpamReportConfig(t, db)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			e := Event{ID: "legacy-command-message", GroupID: "group", Content: "/举报垃圾信息"}
			e.Author.MemberID = "reporter"
			raw, err := json.Marshal(e)
			require.NoError(t, err)
			// Seed a job queued before upgrade; current routing rejects new
			// unmentioned uses before they can reach the worker.
			_, err = db.Exec(`INSERT INTO qq_bot_jobs
 (id,app_id,event_key,event_type,group_id,member_id,message_id,payload,kind)
 VALUES('legacy-command','app','legacy-command',$1,'group','reporter',$2,$3,'spam_report')`, eventType, e.ID, string(raw))
			require.NoError(t, err)
			s.runOne(ctx)
			var status string
			var inputTokens, outputTokens, reports int
			require.NoError(t, db.QueryRow(`SELECT status,input_tokens,output_tokens FROM qq_bot_jobs WHERE id='legacy-command'`).Scan(&status, &inputTokens, &outputTokens))
			require.Equal(t, "completed", status)
			require.Zero(t, inputTokens)
			require.Zero(t, outputTokens)
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_spam_reports`).Scan(&reports))
			require.Zero(t, reports)
			require.Zero(t, modelCalls.Load())
			if eventType == groupMessageCreate {
				require.Zero(t, messageCalls.Load())
			} else {
				require.EqualValues(t, 1, messageCalls.Load())
			}
		})
	}
}
