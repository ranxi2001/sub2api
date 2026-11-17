//go:build unit && issue905

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Synthetic provider contract, not a reproduction of the private supplier's
// owner store. Exercise the real handler, two socket hops and Ops persistence.
func TestIssue905WSFollowup(t *testing.T) {
	for _, outcome := range []string{"completed", "error_event", "close_only", "unknown_close", "response_failed", "multiple_failures"} {
		for _, pointer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pointer=%v", outcome, pointer), func(t *testing.T) {
				runIssue905WSFollowup(t, outcome, pointer, coderws.StatusPolicyViolation, false)
			})
		}
	}
}

// The identical oracle runs on the reviewed 6f018b85 production and candidate.
func TestIssue905WSReviewRepair(t *testing.T) {
	for _, status := range []coderws.StatusCode{coderws.StatusNormalClosure, coderws.StatusGoingAway} {
		for _, outcome := range []string{"close_only", "unknown_close", "completed_normal"} {
			for _, pointer := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/pointer=%v", status, outcome, pointer), func(t *testing.T) {
					runIssue905WSFollowup(t, outcome, pointer, status, true)
				})
			}
		}
	}
	for _, outcome := range []string{"close_only", "unknown_close", "error_event", "response_failed", "multiple_failures"} {
		t.Run("1008/"+outcome, func(t *testing.T) {
			runIssue905WSFollowup(t, outcome, false, coderws.StatusPolicyViolation, true)
		})
	}
	t.Run("1005/unknown_close", func(t *testing.T) {
		runIssue905WSFollowup(t, "unknown_close", false, coderws.StatusNoStatusRcvd, true)
	})
}

func TestIssue905WSUpstreamCloseAttribution(t *testing.T) {
	for _, outcome := range []string{"close_only", "completed_normal", "completed"} {
		t.Run(outcome, func(t *testing.T) {
			runIssue905WSFollowup(t, outcome, false, coderws.StatusNormalClosure, true)
		})
	}
}

func runIssue905WSFollowup(t *testing.T, outcome string, pointer bool, status coderws.StatusCode, captureLogs bool) {
	t.Helper()
	var logPath string
	if captureLogs {
		handlerStructuredLogCaptureMu.Lock()
		t.Cleanup(handlerStructuredLogCaptureMu.Unlock)
		logPath = filepath.Join(t.TempDir(), "application.jsonl")
		require.NoError(t, logger.Init(logger.InitOptions{
			Level:       "debug",
			Format:      "json",
			Environment: "test",
			Output:      logger.OutputOptions{ToFile: true, FilePath: logPath},
			Sampling:    logger.SamplingOptions{Enabled: false},
		}))
		t.Cleanup(func() {
			require.NoError(t, logger.Init(logger.InitOptions{Level: "info", Output: logger.OutputOptions{ToStdout: true}}))
		})
	}
	setupOpsErrorLogTestQueue(t, 8)
	const code = "encrypted_agent_recovery_owner_unavailable"
	const tools = `[{"type":"function","name":"fixture_once","parameters":{"type":"object","properties":{}}}]`
	const call = `{"type":"function_call","call_id":"call_fixture_once","name":"fixture_once","arguments":"{}","status":"completed"}`
	result := `{"type":"function_call_output","call_id":"call_fixture_once","output":"fixture receipt 1"}`
	input := "[" + result + "]"
	anchor := `,"previous_response_id":"resp_fixture_first"`
	if !pointer {
		input = "[" + call + "," + result + "]"
		anchor = ""
	}
	second := `{"type":"response.create","model":"gpt-5.1","service_tier":"priority","reasoning":{"effort":"high"},"tools":` + tools + `,"input":` + input + anchor + `}`
	const event = `{"type":"error", "status":409, "error":{"type":"invalid_request_error","code":"encrypted_agent_recovery_owner_unavailable","message":"fixture owner unavailable"}}`
	failedEvent := func(turn int32) []byte {
		return []byte(fmt.Sprintf(`{"type":"response.failed", "response":{"id":"resp_fixture_failed_%d","status":"failed","error":{"type":"invalid_request_error","code":"encrypted_agent_recovery_owner_unavailable","message":"fixture owner unavailable"}}}`, turn))
	}
	expectedSends := int32(2)
	if outcome == "multiple_failures" {
		expectedSends = 3
	}
	var sends, connections atomic.Int32
	seen := make(chan []byte, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		connections.Add(1)
		defer conn.CloseNow()
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			n := sends.Add(1)
			seen <- body
			if n == 1 {
				if err := conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_fixture_first","status":"completed","output":[`+call+`],"usage":{"input_tokens":2,"output_tokens":1}}}`)); err != nil {
					return
				}
				continue
			}
			if n > expectedSends {
				t.Error("generation was replayed")
				return
			}
			if outcome == "completed" || outcome == "completed_normal" {
				_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_fixture_second","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture receipt accepted"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}`))
				if outcome == "completed_normal" {
					_ = conn.Close(status, "")
					return
				}
				_, _, _ = conn.Read(r.Context())
				return
			}
			if outcome == "error_event" {
				_ = conn.Write(r.Context(), coderws.MessageText, []byte(event))
			}
			if outcome == "response_failed" || outcome == "multiple_failures" {
				_ = conn.Write(r.Context(), coderws.MessageText, failedEvent(n))
				if n < expectedSends {
					continue
				}
			}
			reason := code + ": fixture-private-close-canary"
			if outcome == "unknown_close" {
				reason = "fixture-private-close-canary"
			}
			if status == coderws.StatusNoStatusRcvd {
				reason = "" // RFC 6455: empty close frame, no reason on the wire.
			}
			_ = conn.Close(status, reason)
			return
		}
	}))
	defer provider.Close()
	repo := &ingressRejectOpsRepo{}
	h := newOpenAIWSPassthroughHandlerHarnessWithAuth(t, provider.URL, http.Header{"Session_id": {"fixture-905"}, "Thread-Id": {"fixture-905-thread"}, "Authorization": {"Bearer sk-handler-cyber-test"}},
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}, nil, nil,
		func(h *OpenAIGatewayHandler) {
			h.opsService = service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h.maxAccountSwitches = 2
		},
		func(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
			key.Status, key.AllowLongContext = service.StatusActive, true
			key.User.Concurrency = 1
			key.Group = &service.Group{ID: *key.GroupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			return service.NewAPIKeyService(&wsRevalidationKeyRepoStub{current: key}, nil, nil, nil, nil, nil, cfg)
		})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","tools":`+tools+`,"input":"Run the fixture once"}`)))
	_, first, err := h.clientConn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
	require.Equal(t, "call_fixture_once", gjson.GetBytes(first, "response.output.0.call_id").String())
	// Submit the synthetic result for the call actually delivered above.
	require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(second)))
	completed := false
	delivered := 0
	for {
		_, raw, err := h.clientConn.Read(ctx)
		if err != nil {
			if outcome != "completed_normal" {
				require.Equal(t, coderws.StatusInternalError, coderws.CloseStatus(err))
			} else {
				require.NoError(t, ctx.Err(), "successful terminal must be followed by prompt transport shutdown")
			}
			break
		}
		if outcome == "completed" || outcome == "completed_normal" {
			require.Equal(t, "response.completed", gjson.GetBytes(raw, "type").String())
			require.Equal(t, "fixture receipt accepted", gjson.GetBytes(raw, "response.output.0.content.0.text").String())
			completed = true
			if outcome == "completed_normal" {
				continue // Read the provider's normal close after terminal delivery.
			}
			require.NoError(t, h.clientConn.Close(coderws.StatusNormalClosure, ""))
			break
		}
		delivered++
		if outcome == "response_failed" || outcome == "multiple_failures" {
			require.Equal(t, failedEvent(int32(delivered+1)), raw, "failure event must arrive byte-for-byte")
			if outcome == "multiple_failures" && delivered == 1 {
				require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(second)))
			}
		} else {
			require.Equal(t, []byte(event), raw, "error event must arrive byte-for-byte")
		}
	}
	select {
	case <-h.handlerDone:
	case <-ctx.Done():
		t.Fatal("handler did not finish")
	}
	finalHeaders := <-h.finalHeaders
	require.Equal(t, "fixture-905", finalHeaders.Get("Session_id"))
	require.Equal(t, "fixture-905-thread", finalHeaders.Get("Thread-Id"))
	require.EqualValues(t, expectedSends, sends.Load())
	require.EqualValues(t, 1, connections.Load())
	<-seen
	forwarded := <-seen
	require.JSONEq(t, input, gjson.GetBytes(forwarded, "input").Raw)
	require.JSONEq(t, tools, gjson.GetBytes(forwarded, "tools").Raw)
	require.Equal(t, "priority", gjson.GetBytes(forwarded, "service_tier").String())
	require.Equal(t, "high", gjson.GetBytes(forwarded, "reasoning.effort").String())
	require.Equal(t, gjson.Get(second, "previous_response_id").String(), gjson.GetBytes(forwarded, "previous_response_id").String())
	if expectedSends == 3 {
		third := <-seen
		require.Equal(t, forwarded, third, "later failure must not mutate or replay an earlier turn")
	}
	checkLogs := func() {
		if !captureLogs {
			return
		}
		logger.Sync()
		encoded, err := os.ReadFile(logPath)
		require.NoError(t, err)
		logs := string(encoded)
		// Assert on the complete encoded application output, including message,
		// error, close_reason and all relay trace fields, not just Ops bodies.
		require.NotContains(t, logs, "fixture-private-close-canary", "ISSUE906_PRIVATE_CLOSE_REASON_LOGGED")
		require.Contains(t, logs, "relay_trace")
		if outcome == "completed" || outcome == "completed_normal" {
			require.Contains(t, logs, "relay_completed")
			require.Contains(t, logs, "terminal_event=response.completed")
			require.Contains(t, logs, `"msg":"openai.websocket_ingress_closed"`)
			require.NotContains(t, logs, "relay_failed")
			require.NotContains(t, logs, "openai.websocket_proxy_failed")
		} else {
			require.Contains(t, logs, "stage=read_upstream_fail direction=upstream_to_client")
		}
		if outcome == "close_only" || outcome == "unknown_close" {
			require.Contains(t, logs, "relay_failed")
			require.Contains(t, logs, "openai.websocket_proxy_failed", "ISSUE906_UPSTREAM_CLOSE_MISCLASSIFIED")
			require.NotContains(t, logs, "openai.websocket_ingress_closed_normally")
			require.Contains(t, logs, fmt.Sprintf("status=%d", status))
			expectedReason := "upstream_websocket_closed"
			if outcome == "close_only" {
				expectedReason = code
			}
			require.Contains(t, logs, "reason="+expectedReason)
			require.Contains(t, logs, `"close_reason":"`+expectedReason+`"`)
		}
	}
	for len(opsErrorLogQueue) > 0 {
		flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
	}
	if outcome == "completed" || outcome == "completed_normal" {
		require.True(t, completed, "second turn must actually complete")
		require.Empty(t, repo.entries)
		checkLogs()
		return
	}
	expectedEvents := 0
	if outcome == "error_event" || outcome == "response_failed" || outcome == "multiple_failures" {
		expectedEvents = int(expectedSends - 1)
	}
	require.Equal(t, expectedEvents, delivered, "every upstream failure event must reach the client")
	require.Len(t, repo.entries, int(expectedSends-1), "ISSUE906_UNFINISHED_CLOSE_MISSING: only active failure turns count; event + close must deduplicate")
	for i, entry := range repo.entries {
		require.Equal(t, "upstream_transport", entry.ErrorSource, "ISSUE905_UPSTREAM_SOURCE_LOST")
		require.Equal(t, "provider", entry.ErrorOwner)
		require.False(t, entry.IsBusinessLimited)
		require.Nil(t, entry.UpstreamStatusCode, "WS business status is not an HTTP status")
		expectedStatus, expectedCode := 409, code
		if outcome == "unknown_close" {
			expectedStatus, expectedCode = 502, "upstream_websocket_closed"
		}
		require.Equal(t, expectedStatus, entry.StatusCode)
		require.Equal(t, expectedCode, gjson.Get(entry.ErrorBody, "error.code").String())
		require.EqualValues(t, i+2, gjson.Get(entry.ErrorBody, "error.turn").Int())
		require.Equal(t, "websocket", gjson.Get(entry.ErrorBody, "error.protocol").String())
		require.Equal(t, "upstream", entry.ErrorPhase)
		require.Equal(t, "gpt-5.1", entry.Model)
		require.Equal(t, "gpt-5.1", entry.RequestedModel)
		require.EqualValues(t, 9951, *entry.AccountID)
		require.Equal(t, h.apiKey.ID, *entry.APIKeyID)
		require.Equal(t, h.apiKey.UserID, *entry.UserID)
		phase := "read_upstream"
		if expectedEvents > 0 {
			phase = "upstream_event"
			require.False(t, gjson.Get(entry.ErrorBody, "error.upstream_close_code").Exists(), "event-before-close must win")
			require.Equal(t, "fixture owner unavailable", gjson.Get(entry.ErrorBody, "error.message").String())
		} else {
			require.EqualValues(t, status, gjson.Get(entry.ErrorBody, "error.upstream_close_code").Int())
		}
		require.Equal(t, phase, gjson.Get(entry.ErrorBody, "error.phase").String())
		require.EqualValues(t, expectedStatus, gjson.Get(entry.ErrorBody, "error.status").Int())
		require.EqualValues(t, 101, gjson.Get(entry.ErrorBody, "error.wire_status").Int())
		require.Equal(t, "unknown", gjson.Get(entry.ErrorBody, "error.execution_state").String())
		require.False(t, gjson.Get(entry.ErrorBody, "error.request_written").Exists(), "lack of current-turn write proof must not become false")
		require.NotContains(t, entry.ErrorBody, "fixture-private-close-canary")
	}
	checkLogs()
}
