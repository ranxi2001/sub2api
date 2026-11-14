//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func repairF3Revoke(c *Config, change string) {
	switch change {
	case "disabled":
		c.Enabled = false
	case "removed_group":
		c.AllowedGroups = []string{"other-group"}
	case "appid":
		c.AppID, c.AppSecret = "replacement-app", "replacement-secret"
	case "credential":
		c.AppSecret = "rotated-secret"
	case "model_credential":
		c.APIKey = "rotated-model-key"
	}
}

func TestRepairF3RunningGenerationCannotSendAfterRevocation(t *testing.T) {
	for _, kind := range []string{"text", "image"} {
		for _, change := range []string{"disabled", "removed_group", "appid", "credential", "model_credential"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				db := testDB(t)
				s := savedBot(t, db)
				writer := newMatureFixtureService(db, testEncryptor{}, true, nil) // another instance
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				var messages, files atomic.Int32
				entered, release := make(chan struct{}), make(chan struct{})
				var unblock sync.Once
				picture, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(probeImage(), "data:image/png;base64,"))
				require.NoError(t, err)
				peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v1/chat/completions":
						close(entered)
						<-release
						_, _ = w.Write(textToolTestJSON(t, "chat_completions", "synthetic completed answer"))
					case "/v1/images/generations":
						close(entered)
						<-release
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(picture)}}})
					case "/token":
						_, _ = w.Write([]byte(`{"access_token":"synthetic-qq-token","expires_in":7200}`))
					case "/v2/groups/group/files":
						files.Add(1)
						_, _ = w.Write([]byte(`{"file_info":"synthetic-file"}`))
					case "/v2/groups/group/messages":
						messages.Add(1)
						_, _ = w.Write([]byte(`{"id":"synthetic-delivery"}`))
					default:
						t.Errorf("unexpected F3 peer path %s", r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer peer.Close()
				defer unblock.Do(func() { close(release) })
				c, err := s.Config(ctx)
				require.NoError(t, err)
				c.BaseURL, c.ImageEnabled, c.ImageModel = peer.URL+"/v1", true, "synthetic-image"
				_, err = s.SaveConfig(ctx, c)
				require.NoError(t, err)
				c, err = s.Config(ctx)
				require.NoError(t, err)
				s.qqURL, s.tokenURL = peer.URL, peer.URL+"/token"
				content := "hello synthetic model"
				if kind == "image" {
					content = "/生图 synthetic square"
				}
				e := Event{ID: "repair-f3-message", GroupID: "group", Content: content}
				e.Author.MemberID = "unbound-member"
				raw, err := json.Marshal(e)
				require.NoError(t, err)
				require.NoError(t, s.Enqueue(ctx, c, Payload{ID: e.ID, Type: groupAtMessageCreate, Data: raw}))
				done := make(chan struct{})
				go func() { s.runOne(ctx); close(done) }()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("F3 running job never reached model barrier")
				}
				beforeMessages, beforeFiles := messages.Load(), files.Load()
				repairF3Revoke(&c, change)
				_, err = writer.SaveConfig(ctx, c)
				require.NoError(t, err, "F3 revocation must commit before model completion")
				unblock.Do(func() { close(release) })
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("F3 worker did not finish after model release")
				}
				require.Equal(t, beforeMessages, messages.Load(), "F3 committed revocation must prevent a new QQ messages request from the running job")
				require.Equal(t, beforeFiles, files.Load(), "F3 committed revocation must prevent a new QQ files request from the running job")
			})
		}
	}
}

// Start an upload while it is authorized; a second instance tries to revoke.
// The configuration transaction must wait for the external operation, then a
// new send using the old snapshot must fail. This detects check-then-send gaps.
func TestRepairF3ConfigSaveOrdersWithFinalHTTPBoundary(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	writerDB := repairIndependentPool(t, db)
	observer := repairIndependentPool(t, db)
	writer := newMatureFixtureService(writerDB, testEncryptor{}, true, nil)
	db.SetMaxOpenConns(1)
	writerDB.SetMaxOpenConns(1)
	var senderPID, writerPID int
	require.NoError(t, db.QueryRow(`SELECT pg_backend_pid()`).Scan(&senderPID))
	require.NoError(t, writerDB.QueryRow(`SELECT pg_backend_pid()`).Scan(&writerPID))
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"synthetic-token","expires_in":7200}`))
			return
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		_, _ = w.Write([]byte(`{"id":"synthetic-delivery"}`))
	}))
	defer peer.Close()
	defer unblock.Do(func() { close(release) })
	s.qqURL, s.tokenURL = peer.URL, peer.URL+"/token"
	c, err := s.Config(ctx)
	require.NoError(t, err)
	j := Job{AppID: c.AppID, GroupID: "group", MessageID: "synthetic-boundary", CreatedAt: time.Now(), TransientReply: true}
	sent := make(chan error, 1)
	go func() { sent <- s.send(ctx, c, j, "synthetic text", nil, 1) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("F3 external send never entered")
	}
	c.Enabled = false
	saved := make(chan error, 1)
	go func() { _, err := writer.SaveConfig(ctx, c); saved <- err }()
	repairWaitForConfigLock(t, ctx, observer, writerPID, senderPID, saved)
	unblock.Do(func() { close(release) })
	require.NoError(t, <-sent)
	require.NoError(t, <-saved)
	old, err := s.Config(ctx)
	require.NoError(t, err)
	old.Enabled = true
	require.Error(t, s.send(ctx, old, j, "must not start", nil, 2))
	require.EqualValues(t, 1, calls.Load(), "F3 no new HTTP send after revocation commit")
}

// Separate pools retain the same synthetic schema, with independent backend PIDs.
func repairIndependentPool(t *testing.T, original *sql.DB) *sql.DB {
	t.Helper()
	var schema string
	require.NoError(t, original.QueryRow(`SELECT current_schema()`).Scan(&schema))
	u, err := url.Parse(os.Getenv("QQBOT_TEST_DSN"))
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func repairWaitForConfigLock(t *testing.T, ctx context.Context, observer *sql.DB, waiterPID, senderPID int, saved <-chan error) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := observer.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM pg_locks waiter JOIN pg_locks holder
 ON holder.locktype=waiter.locktype AND holder.database=waiter.database
 AND holder.classid=waiter.classid AND holder.objid=waiter.objid AND holder.objsubid=waiter.objsubid
 JOIN pg_stat_activity activity ON activity.pid=waiter.pid
 WHERE waiter.pid=$1 AND holder.pid=$2 AND waiter.locktype='advisory'
 AND waiter.classid=0 AND waiter.objid=71427101 AND waiter.objsubid=1
 AND waiter.mode='ExclusiveLock' AND NOT waiter.granted AND holder.mode='ShareLock' AND holder.granted
 AND activity.wait_event_type='Lock' AND $2=ANY(pg_blocking_pids($1)))`, waiterPID, senderPID).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			t.Logf("R2 observed config backend %d waiting on sender backend %d advisory 71427101", waiterPID, senderPID)
			return
		}
		select {
		case err := <-saved:
			require.NoError(t, err)
			t.Fatal("F3 configuration committed while the final send authorization lock was absent")
		case <-ctx.Done():
			t.Fatal("R2 never observed actual config lock contention before deadline")
		case <-ticker.C:
		}
	}
}
