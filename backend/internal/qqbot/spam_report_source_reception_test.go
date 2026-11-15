package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func spamReceptionTestConfig() Config {
	c := DefaultConfig()
	c.Enabled, c.Moderation.ReportEnabled = true, true
	c.AppID, c.AppSecret = "private-app", "private-app-secret"
	c.APIKey, c.BaseURL = "private-key", "https://private-connection.invalid"
	c.AllowedGroups, c.Moderation.Groups = []string{"group"}, []string{"group"}
	return c
}

func TestSpamReportSourceReceptionCapturesEarlyDropWithoutSensitiveData(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	c := spamReceptionTestConfig()
	for _, tc := range []struct {
		reason string
		edit   func(*Event)
	}{
		{"message_id_missing", func(e *Event) { e.ID = "" }},
		{"message_id_too_long", func(e *Event) { e.ID = strings.Repeat("m", 1025) }},
		{"member_id_missing", func(e *Event) { e.Author.MemberID = "" }},
		{"member_id_too_long", func(e *Event) { e.Author.MemberID = strings.Repeat("u", 129) }},
		{"member_role_too_long", func(e *Event) { e.Author.MemberRole = strings.Repeat("r", 33) }},
		{"timestamp_missing", func(e *Event) { e.Timestamp = nil }},
		{"timestamp_type_invalid", func(e *Event) { e.Timestamp = json.RawMessage(`123`) }},
		{"timestamp_empty", func(e *Event) { e.Timestamp = json.RawMessage(`null`) }},
		{"timestamp_invalid", func(e *Event) { e.Timestamp = json.RawMessage(`"private-invalid-timestamp"`) }},
		{"timestamp_expired", func(e *Event) {
			e.Timestamp, _ = json.Marshal(time.Now().Add(-spamReportSourceRetention - time.Second).Format(time.RFC3339Nano))
		}},
		{"timestamp_future", func(e *Event) {
			e.Timestamp, _ = json.Marshal(time.Now().Add(10 * time.Second).Format(time.RFC3339Nano))
		}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			output.Reset()
			e := spamSourceTestEvent("private-original-message")
			e.Content = "private body https://private-message.invalid"
			tc.edit(&e)
			s := &Service{} // Any accidental database access would fail this test.
			require.NoError(t, s.rememberSpamReportSource(context.Background(), c, e, groupMessageCreate))
			var record map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &record))
			require.Equal(t, tc.reason, record["reason"])
			require.Equal(t, "dropped", record["outcome"])
			require.Equal(t, "qqbot.spam_source", record["component"])
			for _, secret := range []string{c.AppID, c.AppSecret, c.APIKey, c.BaseURL, e.Content, "private-original-message", "must-not-be-retained", "private-invalid-timestamp", "https://private-message.invalid"} {
				require.NotContains(t, output.String(), secret)
			}
			for _, raw := range []string{e.ID, e.Author.MemberID, e.GroupID, e.MessageScene.value("msg_idx")} {
				if raw != "" {
					require.NotContains(t, output.String(), `:"`+raw+`"`)
				}
			}
		})
	}
	output.Reset()
	c.Moderation.ReportEnabled = false
	require.NoError(t, (&Service{}).rememberSpamReportSource(context.Background(), c, Event{GroupID: "group"}, groupMessageCreate))
	require.Empty(t, output.String())
}

func TestSpamReportSourceReceptionReferencesAreScopedAndComparable(t *testing.T) {
	c := spamReceptionTestConfig()
	ref := spamSourceReceptionRef(c, "group", "index", "same-index")
	require.Len(t, ref, 16)
	require.Equal(t, ref, spamSourceReceptionRef(c, "group", "index", "same-index"))
	require.NotEqual(t, ref, spamSourceReceptionRef(c, "other-group", "index", "same-index"))
	require.NotEqual(t, ref, spamSourceReceptionRef(c, "group", "message", "same-index"))
	c.Sandbox = true
	require.NotEqual(t, ref, spamSourceReceptionRef(c, "group", "index", "same-index"))
	c.AppSecret = ""
	require.Empty(t, spamSourceReceptionRef(c, "group", "index", "same-index"))
}

func TestSpamReportSourceReceptionThrottleIsBounded(t *testing.T) {
	var d spamSourceReceptionDiagnostics
	now := time.Now()
	n, ok := d.allow("same-reason", now)
	require.True(t, ok)
	require.Zero(t, n)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() { defer wg.Done(); d.allow("same-reason", now) }()
	}
	wg.Wait()
	n, ok = d.allow("same-reason", now.Add(spamSourceReceptionLogInterval))
	require.True(t, ok)
	require.Equal(t, uint64(50), n)
	for i := 0; i < spamSourceReceptionLogLimit*2; i++ {
		d.allow(fmt.Sprint(i), now)
	}
	require.LessOrEqual(t, len(d.entries), spamSourceReceptionLogLimit)
	_, ok = d.allow("new-after-expiry", now.Add(2*spamSourceReceptionLogInterval))
	require.True(t, ok)
	require.LessOrEqual(t, len(d.entries), spamSourceReceptionLogLimit)
}

func TestSpamReportSourceReceptionRejectsUnclassifiedLogInputs(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	s := &Service{}
	c, e := spamReceptionTestConfig(), spamSourceTestEvent("source")
	s.logSpamSourceReception(context.Background(), c, e, groupMessageCreate, "raw error https://secret.invalid")
	s.logSpamSourceReception(context.Background(), c, e, "private-event-name", "timestamp_invalid")
	s.logSpamSourceReception(context.Background(), c, e, groupMessageCreate, "original_stored")
	require.Empty(t, output.String(), "unknown input and default-level successful storage remain silent")
	s.logSpamSourceReception(context.Background(), c, e, groupMessageCreate, "source_write_failed")
	require.Contains(t, output.String(), `"level":"WARN"`)
	require.NotContains(t, output.String(), "secret.invalid")
}
