//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotJobClaimsStayBoundedAcrossInstances(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	for i := 0; i < 6; i++ {
		for turn := 0; turn < 2; turn++ {
			e := sharedTestEvent(fmt.Sprintf("%d-%d", i, turn), "group", fmt.Sprint(i), "问题")
			enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
		}
	}
	instances := []*Service{s, New(db, testEncryptor{}, true, nil)}
	type result struct {
		job Job
		err error
	}
	results := make(chan result, 12)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		workers.Add(1)
		go func(instance *Service) {
			defer workers.Done()
			<-start
			job, claimErr := instance.claimJob(ctx)
			results <- result{job, claimErr}
		}(instances[i%len(instances)])
	}
	close(start)
	workers.Wait()
	close(results)
	var claimed []Job
	for result := range results {
		if errors.Is(result.err, sql.ErrNoRows) {
			continue
		}
		require.NoError(t, result.err)
		claimed = append(claimed, result.job)
	}
	require.Len(t, claimed, 2, "all instances share the same two slots")
	require.NotEqual(t, claimed[0].ID, claimed[1].ID, "a job must never be claimed twice")
	require.NotEqual(t, claimed[0].MemberID, claimed[1].MemberID, "one member cannot occupy both slots")
	var running, queued int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FILTER (WHERE status='running'),COUNT(*) FILTER (WHERE status='queued') FROM qq_bot_jobs`).Scan(&running, &queued))
	require.Equal(t, 2, running)
	require.Equal(t, 10, queued)
}

func TestQQBotSlowMemberDoesNotBlockOtherMembers(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			c, err := s.Config(ctx)
			require.NoError(t, err)
			type modelCall struct {
				current groupHistoryEntry
				history []Message
			}
			calls := make(chan modelCall, 4)
			replies := make(chan qqTestReply, 4)
			slow := make(chan struct{})
			releaseSlow := sync.OnceFunc(func() { close(slow) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"qq-test-only","expires_in":7200}`))
				case "/v1/chat/completions", "/v1/responses":
					var request struct {
						Messages []Message `json:"messages"`
						Input    []Message `json:"input"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					messages := request.Messages
					if protocol == "responses" {
						messages = request.Input
					}
					require.NotEmpty(t, messages)
					var current groupHistoryEntry
					require.NoError(t, json.Unmarshal([]byte(messages[len(messages)-1].Content), &current))
					calls <- modelCall{current, messages[:len(messages)-1]}
					if current.Text == "slow question" {
						select {
						case <-slow:
						case <-r.Context().Done():
							return
						}
					}
					answer := testChatEnvelope("answer to " + current.Text)
					if protocol == "responses" {
						_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output_text": answer})
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": answer}}}})
					}
				case "/v2/groups/group/messages":
					reply := readQQTestReply(t, r)
					replies <- reply
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "reply-" + reply.MsgID})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			var workers sync.WaitGroup
			defer func() {
				releaseSlow()
				cancel()
				workers.Wait()
				server.Close()
			}()
			c.BaseURL, c.ChatProtocol = server.URL+"/v1", protocol
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			events := []Event{
				sharedTestEvent("a", "group", "alice", "slow question"),
				sharedTestEvent("b", "group", "alice", "follow up"),
				sharedTestEvent("c", "group", "bob", "fast question"),
			}
			for _, e := range events {
				enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
			}
			startWorker := func() <-chan struct{} {
				done := make(chan struct{})
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer close(done)
					s.runOne(ctx)
				}()
				return done
			}
			waitWorker := func(done <-chan struct{}) {
				t.Helper()
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("worker did not finish")
				}
			}
			nextCall := func() modelCall {
				t.Helper()
				select {
				case call := <-calls:
					return call
				case <-ctx.Done():
					t.Fatal("model request did not start")
					return modelCall{}
				}
			}
			assertStatus := func(id, want string) {
				t.Helper()
				var status string
				require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM qq_bot_jobs WHERE message_id=$1`, id).Scan(&status))
				require.Equal(t, want, status, id)
			}
			slowDone := startWorker()
			require.Equal(t, "slow question", nextCall().current.Text)
			waitWorker(startWorker())
			fast := nextCall()
			require.Equal(t, "fast question", fast.current.Text)
			require.Equal(t, groupSpeaker(c.AppID, "group", "bob"), fast.current.Speaker)
			require.NotContains(t, sharedHistoryText(fast.history), "slow question")
			require.NotContains(t, sharedHistoryText(fast.history), "follow up")
			require.NotContains(t, sharedHistoryText(fast.history), "answer to slow question")
			assertStatus("a", "running")
			assertStatus("b", "queued")
			assertStatus("c", "completed")
			// One slot is now free, but Alice's follow-up must still wait for her
			// slow answer. Bob's reply was delivered while Alice was blocked.
			waitWorker(startWorker())
			require.Empty(t, calls)
			assertStatus("b", "queued")
			require.Len(t, replies, 1)
			require.Equal(t, "c", (<-replies).MsgID)
			releaseSlow()
			waitWorker(slowDone)
			assertStatus("a", "completed")
			waitWorker(startWorker())
			followup := nextCall()
			require.Equal(t, "follow up", followup.current.Text)
			require.Contains(t, sharedHistoryText(followup.history), "answer to slow question")
			require.NotContains(t, sharedHistoryText(followup.history), "fast question")
			require.NotContains(t, sharedHistoryText(followup.history), "answer to fast question", "later turns must not leak into an earlier queued question")
			assertStatus("b", "completed")
			require.Len(t, replies, 2)
			require.Equal(t, "a", (<-replies).MsgID)
			require.Equal(t, "b", (<-replies).MsgID)
			for _, e := range events {
				var raw string
				require.NoError(t, db.QueryRowContext(ctx, `SELECT content FROM qq_bot_group_messages WHERE message_key=$1`, "assistant:reply-"+e.ID).Scan(&raw))
				var entry groupHistoryEntry
				require.NoError(t, json.Unmarshal([]byte(raw), &entry))
				require.Equal(t, groupHistoryID(c.AppID, e.GroupID, e.ID), entry.ReplyTo)
				enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
			}
			waitWorker(startWorker())
			require.Empty(t, calls, "duplicate events must not run again")
			require.Empty(t, replies)
		})
	}
}
