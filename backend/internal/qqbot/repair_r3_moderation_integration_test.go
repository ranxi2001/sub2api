//go:build integration

package qqbot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRepairR3ModerationRevocationAfterTokenWait(t *testing.T) {
	for _, kind := range []string{"mute", "recall"} {
		for _, change := range []string{"disabled", "removed_group", "appid", "credential", "unchanged"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				h := newModerationHarness(t)
				h.spam(t, "repair-r3")
				if kind == "recall" {
					_, err := h.db.Exec(`UPDATE qq_bot_moderation_operations SET status='cancelled' WHERE kind='mute'`)
					require.NoError(t, err)
				}
				writer := newMatureFixtureService(repairIndependentPool(t, h.db), testEncryptor{}, true, nil)
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				entered, release := make(chan struct{}), make(chan struct{})
				var unblock sync.Once
				var operations atomic.Int32
				peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						_, _ = io.Copy(io.Discard, r.Body)
						close(entered)
						<-release
						_, _ = io.WriteString(w, `{"access_token":"synthetic-r3-token","expires_in":7200}`)
						return
					}
					if (kind == "mute" && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/restrict_chat_setting")) || (kind == "recall" && r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/messages/")) {
						operations.Add(1)
					} else {
						t.Errorf("unexpected R3 operation: %s %s", r.Method, r.URL.Path)
					}
					_, _ = io.WriteString(w, `{}`)
				}))
				defer peer.Close()
				defer unblock.Do(func() { close(release) })
				h.s.qqURL, h.s.tokenURL = peer.URL, peer.URL+"/token"
				done := make(chan struct{})
				go func() { h.s.runModeration(ctx); close(done) }()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("R3 token request never entered after operation claim")
				}
				var started int
				require.NoError(t, h.db.QueryRow(`SELECT count(*) FROM qq_bot_moderation_operations WHERE status='started' AND kind=$1`, kind).Scan(&started))
				require.Equal(t, 1, started, "R3 must test a durably started operation, not a queued cancellation")
				c, err := writer.Config(ctx)
				require.NoError(t, err)
				if change != "unchanged" {
					repairF3Revoke(&c, change)
					_, err = writer.SaveConfig(ctx, c)
					require.NoError(t, err, "R3 token waiting cannot hold config lock")
				}
				unblock.Do(func() { close(release) })
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("R3 moderation did not complete after token release")
				}
				want := int32(0)
				if change == "unchanged" {
					want = 1
				}
				require.Equal(t, want, operations.Load(), "R3 committed revocation must prevent old moderation HTTP operation")
				if change != "unchanged" {
					h.s.runModeration(ctx)
					require.Zero(t, operations.Load(), "R3 revoked operations must not replay")
				}
			})
		}
	}
}

func TestRepairR3ModerationDispatchHoldsConfigFence(t *testing.T) {
	for _, kind := range []string{"mute", "recall"} {
		t.Run(kind, func(t *testing.T) {
			h := newModerationHarness(t)
			h.spam(t, "repair-r3-fence")
			if kind == "recall" {
				_, err := h.db.Exec(`UPDATE qq_bot_moderation_operations SET status='cancelled' WHERE kind='mute'`)
				require.NoError(t, err)
			}
			writerDB := repairIndependentPool(t, h.db)
			observer := repairIndependentPool(t, h.db)
			h.db.SetMaxOpenConns(1)
			writerDB.SetMaxOpenConns(1)
			writer := newMatureFixtureService(writerDB, testEncryptor{}, true, nil)
			var senderPID, writerPID int
			require.NoError(t, h.db.QueryRow(`SELECT pg_backend_pid()`).Scan(&senderPID))
			require.NoError(t, writerDB.QueryRow(`SELECT pg_backend_pid()`).Scan(&writerPID))
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var unblock sync.Once
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = io.WriteString(w, `{"access_token":"synthetic-token","expires_in":7200}`)
					return
				}
				close(entered)
				<-release
				_, _ = io.WriteString(w, `{}`)
			}))
			defer peer.Close()
			defer unblock.Do(func() { close(release) })
			h.s.qqURL, h.s.tokenURL = peer.URL, peer.URL+"/token"
			done := make(chan struct{})
			go func() { h.s.runModeration(ctx); close(done) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("R3 final HTTP operation never entered")
			}
			c := h.c
			c.Enabled = false
			saved := make(chan error, 1)
			go func() { _, err := writer.SaveConfig(ctx, c); saved <- err }()
			repairWaitForConfigLock(t, ctx, observer, writerPID, senderPID, saved)
			unblock.Do(func() { close(release) })
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("R3 operation did not exit")
			}
			require.NoError(t, <-saved)
		})
	}
}
