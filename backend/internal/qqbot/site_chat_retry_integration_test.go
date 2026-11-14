//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type siteChatRetryHarness struct {
	s          *Service
	c          Config
	db         *sql.DB
	calls      atomic.Int32
	deliveries atomic.Int32
	mu         sync.Mutex
	replies    []string
}

func newSiteChatRetryHarness(t *testing.T, protocol string, deliveryStatus int, respond func(http.ResponseWriter, *http.Request, int32)) *siteChatRetryHarness {
	t.Helper()
	db := testDB(t)
	s := savedBot(t, db)
	h := &siteChatRetryHarness{s: s, db: db}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			fmt.Fprint(w, `{"access_token":"retry-test-only","expires_in":7200}`)
		case "/v1/chat/completions", "/v1/responses":
			respond(w, r, h.calls.Add(1))
		case "/v2/groups/group/messages":
			reply := readQQTestReply(t, r)
			h.mu.Lock()
			h.replies = append(h.replies, reply.text())
			h.mu.Unlock()
			n := h.deliveries.Add(1)
			if deliveryStatus != http.StatusOK {
				w.WriteHeader(deliveryStatus)
				return
			}
			fmt.Fprintf(w, `{"id":"retry-sent-%d","ext_info":{"ref_idx":"retry-ref-%d"}}`, n, n)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	c, err := s.Config(context.Background())
	require.NoError(t, err)
	c.BaseURL, c.ChatProtocol = server.URL+"/v1", protocol
	c.CooldownSeconds, c.ImageEnabled = 0, false
	_, err = s.SaveConfig(context.Background(), c)
	require.NoError(t, err)
	h.c = c
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	return h
}

func (h *siteChatRetryHarness) enqueue(t *testing.T, text string) Event {
	t.Helper()
	e := sharedTestEvent("retry-question", "group", "member", text)
	enqueueSharedTestEvent(t, h.s, h.c, e, groupAtMessageCreate)
	return e
}

func (h *siteChatRetryHarness) result(t *testing.T) (Job, ChatExecution) {
	t.Helper()
	j, err := scanJob(h.db.QueryRow(`SELECT ` + jobColumns + ` FROM qq_bot_jobs WHERE message_id='retry-question'`))
	require.NoError(t, err)
	var execution ChatExecution
	require.NotEmpty(t, j.ChatExecution)
	require.NoError(t, json.Unmarshal(j.ChatExecution, &execution))
	return j, execution
}

func (h *siteChatRetryHarness) requireSingleQuota(t *testing.T) {
	t.Helper()
	for _, scope := range []string{"all", "member:member", "group:group"} {
		var count int
		require.NoError(t, h.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE app_id=$1 AND scope=$2 AND kind='chat'`, h.c.AppID, scope).Scan(&count))
		require.Equal(t, 1, count, "retries must not reserve another %s quota", scope)
	}
}

func (h *siteChatRetryHarness) sentText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.replies, "\n")
}

func TestQQSiteChatRetryRecoversBeforeSingleFinalDelivery(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			const answer = "重试成功，只发送这条回复。"
			h := newSiteChatRetryHarness(t, protocol, http.StatusOK, func(w http.ResponseWriter, _ *http.Request, attempt int32) {
				if attempt < 3 {
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, `{"error":{"message":"private provider failure"}}`)
					return
				}
				_, _ = w.Write(textToolTestJSON(t, protocol, answer))
			})
			e := h.enqueue(t, "请继续回答我的问题")
			h.s.runOne(context.Background())
			j, execution := h.result(t)
			require.Equal(t, "completed", j.Status)
			require.Equal(t, answer, j.ResultText)
			require.Equal(t, answer, h.sentText())
			require.Empty(t, j.Error)
			require.EqualValues(t, 3, h.calls.Load())
			require.EqualValues(t, 1, h.deliveries.Load())
			require.Equal(t, 3, execution.Attempts)
			require.Equal(t, 2, execution.Retries)
			require.False(t, execution.UsageComplete, "HTTP failures have unknown usage")
			require.NotEmpty(t, execution.LastErrorCode)
			require.EqualValues(t, 11, j.InputTokens)
			require.EqualValues(t, 7, j.OutputTokens)
			h.requireSingleQuota(t)
			// QQ redelivery of the same incoming event cannot repeat the recovered turn.
			enqueueSharedTestEvent(t, h.s, h.c, e, groupAtMessageCreate)
			h.s.runOne(context.Background())
			require.EqualValues(t, 3, h.calls.Load())
			require.EqualValues(t, 1, h.deliveries.Load())
			h.requireSingleQuota(t)
		})
	}
}

func TestQQSiteChatRetryFailureStopsAndRepliesOnce(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, failure := range []struct {
			name     string
			status   int
			attempts int
		}{{"transient_exhausted", http.StatusServiceUnavailable, 3}, {"permanent_auth", http.StatusUnauthorized, 1}} {
			t.Run(protocol+"/"+failure.name, func(t *testing.T) {
				h := newSiteChatRetryHarness(t, protocol, http.StatusOK, func(w http.ResponseWriter, _ *http.Request, _ int32) {
					w.WriteHeader(failure.status)
					fmt.Fprint(w, `{"error":{"message":"private provider credential"}}`)
				})
				h.enqueue(t, "请回答我的问题")
				h.s.runOne(context.Background())
				j, execution := h.result(t)
				require.Equal(t, "failed", j.Status)
				require.Equal(t, "回复生成失败，请稍后重试。", h.sentText())
				require.NotContains(t, j.Error, "private provider")
				require.EqualValues(t, failure.attempts, h.calls.Load())
				require.EqualValues(t, 1, h.deliveries.Load())
				require.Equal(t, failure.attempts, execution.Attempts)
				require.Equal(t, failure.attempts-1, execution.Retries)
				require.False(t, execution.UsageComplete)
				require.NotEmpty(t, execution.LastErrorCode)
				h.requireSingleQuota(t)
			})
		}
	}
}

func TestQQSiteChatRetryPartialQueryExecutesOnlyCompletedAction(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			args := `{"kind":"channel_status","model":"","activity":"","page":1}`
			h := newSiteChatRetryHarness(t, protocol, http.StatusOK, func(w http.ResponseWriter, _ *http.Request, attempt int32) {
				switch attempt {
				case 1:
					w.WriteHeader(http.StatusServiceUnavailable)
				case 2:
					w.Header().Set("Content-Type", "text/event-stream")
					stream := string(siteQueryTestResponse(t, protocol, args, true))
					if protocol == "responses" {
						stream = strings.Split(stream, `data: {"type":"response.completed"`)[0]
						stream += "data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n"
					} else {
						stream = strings.Split(stream, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"`)[0]
						stream += "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n"
					}
					fmt.Fprint(w, stream)
				default:
					_, _ = w.Write(siteQueryTestResponse(t, protocol, args, false))
				}
			})
			var queries atomic.Int32
			h.s.SetChannelStatusProvider(channelStatusProviderFunc(func(context.Context) (*ChannelStatusReport, error) {
				require.EqualValues(t, 3, h.calls.Load(), "partial tools must not execute")
				queries.Add(1)
				return channelStatusFixture(), nil
			}))
			h.enqueue(t, "现在渠道稳不稳？")
			h.s.runOne(context.Background())
			j, execution := h.result(t)
			require.Equal(t, "completed", j.Status)
			require.Equal(t, "site_query:channel_status", j.DecisionReason)
			require.EqualValues(t, 3, h.calls.Load())
			require.EqualValues(t, 1, queries.Load())
			require.EqualValues(t, 1, h.deliveries.Load())
			require.Contains(t, h.sentText(), "查询时间：")
			require.NotContains(t, h.sentText(), "回复生成失败")
			require.Equal(t, 3, execution.Attempts)
			require.Equal(t, 2, execution.Retries)
			require.False(t, execution.UsageComplete)
			require.EqualValues(t, 16, j.InputTokens, "known usage includes the incomplete attempt")
			require.EqualValues(t, 9, j.OutputTokens)
			h.requireSingleQuota(t)
		})
	}
}

func TestQQSiteChatDeliveryFailureDoesNotRegenerateOrRetrySend(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			const answer = "已生成的答案必须保留。"
			h := newSiteChatRetryHarness(t, protocol, http.StatusServiceUnavailable, func(w http.ResponseWriter, _ *http.Request, _ int32) {
				_, _ = w.Write(textToolTestJSON(t, protocol, answer))
			})
			e := h.enqueue(t, "请回答我的问题")
			h.s.runOne(context.Background())
			j, execution := h.result(t)
			require.Equal(t, "delivery_failed", j.Status)
			require.Equal(t, answer, j.ResultText)
			require.Equal(t, answer, h.sentText())
			require.Equal(t, 1, execution.Attempts)
			require.Zero(t, execution.Retries)
			require.True(t, execution.UsageComplete)
			require.Empty(t, execution.LastErrorCode)
			require.EqualValues(t, 1, h.calls.Load())
			require.EqualValues(t, 1, h.deliveries.Load())
			h.requireSingleQuota(t)
			// Duplicate webhook events stay terminal even after QQ rejects delivery.
			enqueueSharedTestEvent(t, h.s, h.c, e, groupAtMessageCreate)
			h.s.runOne(context.Background())
			require.EqualValues(t, 1, h.calls.Load())
			require.EqualValues(t, 1, h.deliveries.Load())
			h.requireSingleQuota(t)
		})
	}
}
