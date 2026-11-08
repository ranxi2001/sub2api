//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This is an offline compatibility oracle, not encryption/decryption or a live
// provider. Only account 1 knows this synthetic task reference. Its acceptance
// requires the original task, user context and tool result on every next turn.
const issue715Agent = `{"type":"agent_message","author":"/root","recipient":"/root/worker","content":[{"type":"input_text","text":"Task: update ledger\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"fixture-task-reference-715"}]}`

func issue715Body(followup bool, stream bool) string {
	input := issue715Agent + `,{"type":"message","role":"user","content":"Keep ledger balance 11; add 7 exactly once"}`
	if followup {
		input += `,{"type":"function_call","name":"update_ledger","call_id":"fc_715_once","arguments":"{\"add\":7}"},{"type":"function_call_output","call_id":"fc_715_once","output":"ledger balance 18; applied once"},{"type":"message","role":"user","content":"Continue the same task and confirm the existing balance"}`
	}
	return fmt.Sprintf(`{"model":"gpt-5.1","stream":%t,"input":[%s]}`, stream, input)
}

type issue715Upstream struct {
	service.HTTPUpstream
	mu     sync.Mutex
	ids    []int64
	bodies [][]byte
	mode   string
}

func (u *issue715Upstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.ids = append(u.ids, accountID)
	u.bodies = append(u.bodies, body)
	mode := u.mode
	u.mu.Unlock()
	response := func(status int, contentType, payload string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(bytes.NewBufferString(payload))}, nil
	}
	if mode == "timeout" {
		return nil, context.DeadlineExceeded
	}
	if accountID != 1 {
		return response(400, "application/json", `{"error":{"code":"unsupported_encrypted_agent_message","type":"invalid_request_error","param":"input[0].content[1].encrypted_content","message":"fixture incompatible route"}}`)
	}
	input := gjson.GetBytes(body, "input").Raw
	if (!bytes.Contains([]byte(input), []byte("fixture-task-reference-715")) && !strings.HasPrefix(mode, "generated")) || !bytes.Contains([]byte(input), []byte("Keep ledger balance 11")) {
		return response(400, "application/json", `{"error":{"code":"missing_fixture_task_or_context"}}`)
	}
	if mode == "pool_failure" {
		return response(520, "application/json", `{"error":{"type":"server_error","message":"fixture result submission interrupted"}}`)
	}
	if mode == "partial" {
		return response(200, "text/event-stream", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"started existing task\"}\n\nevent: error\ndata: {\"type\":\"error\",\"code\":\"unsupported_encrypted_agent_message\",\"message\":\"fixture failure after output\"}\n\n")
	}
	output := `[{"type":"function_call","call_id":"fc_715_once","name":"update_ledger","arguments":"{\"add\":7}"}]`
	if gjson.GetBytes(body, "input.#(type==function_call_output).output").String() == "ledger balance 18; applied once" {
		output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Same task: ledger balance 18; update already applied once"}]}]`
	}
	if strings.HasPrefix(mode, "generated") && !strings.Contains(input, "fixture-task-reference-715") {
		output = "[" + issue715Agent + "," + strings.TrimPrefix(output, "[")
		if strings.Contains(mode, "two") {
			output = "[" + strings.Replace(issue715Agent, "fixture-task-reference-715", "fixture-second-task", 1) + "," + strings.TrimPrefix(output, "[")
		}
	}
	status := "completed"
	if mode == "incomplete" {
		status = "incomplete"
	}
	payload := fmt.Sprintf(`{"id":"resp_715","status":%q,"error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, status, output)
	if gjson.GetBytes(body, "stream").Bool() || mode == "aggregate" || strings.HasPrefix(mode, "generated-stream") {
		if mode == "generated-stream-items" && !strings.Contains(input, "fixture-task-reference-715") {
			payload = strings.Replace(payload, output, `[]`, 1)
			tool := gjson.Parse(output).Array()[1].Raw
			return response(200, "text/event-stream", fmt.Sprintf("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":%s}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", issue715Agent, tool, payload))
		}
		if mode == "generated-stream-multiline" && !strings.Contains(input, "fixture-task-reference-715") {
			payload = strings.Replace(payload, `,"output":`, ",\n data: "+`"output":`, 1)
			payload = strings.ReplaceAll(payload, "\n data:", "\ndata:")
			return response(200, "text/event-stream", fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", payload))
		}
		event := "response.completed"
		if mode == "incomplete" {
			event = "response.incomplete"
		}
		prefix := ""
		if mode == "generated-stream-keepalive" {
			prefix = "data:\n\n"
		}
		return response(200, "text/event-stream", prefix+fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"response\":%s}\n\ndata: [DONE]\n\n", event, event, payload))
	}
	return response(200, "application/json", payload)
}

func issue715Accounts() []service.Account {
	return []service.Account{
		{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"access_token": "fixture-token-one"}},
		{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"access_token": "fixture-token-two"}},
	}
}

func newIssue715Context(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, rec := newAstraProFailoverContext(t, body)
	key, _ := c.Get(string(middleware.ContextKeyAPIKey))
	key.(*service.APIKey).AllowLongContext = true
	return c, rec
}

func issue715Request(t *testing.T, handler *OpenAIGatewayHandler, body string, keyID int64, session string, canceled bool) (int, string) {
	t.Helper()
	c, rec := newIssue715Context(t, body)
	c.Request.Header.Set("session_id", session)
	key, _ := c.Get(string(middleware.ContextKeyAPIKey))
	key.(*service.APIKey).ID = keyID
	if canceled {
		ctx, cancel := context.WithCancel(c.Request.Context())
		cancel()
		c.Request = c.Request.WithContext(ctx)
	}
	handler.Responses(c)
	if rec.Code >= 400 {
		t.Logf("fixture HTTP %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Code, rec.Body.String()
}

func TestIssue715HTTPResumesSameTaskAcrossTwoTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			accounts := issue715Accounts()
			upstream := &issue715Upstream{}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
			status, first := issue715Request(t, handler, issue715Body(false, stream), 99, "fixture-715-thread", false)
			require.Equal(t, 200, status, first)
			require.Contains(t, first, "update_ledger")
			// Execute actual function-call outputs in a fake client. Replay of
			// fc_715_once is a test failure, not silently deduplicated here.
			toolExecutions, ledgerBalance := 0, 11
			executeTools := func(answer string) {
				payload := answer
				if stream {
					for _, line := range strings.Split(answer, "\n") {
						if strings.HasPrefix(line, "data: ") && gjson.Get(strings.TrimPrefix(line, "data: "), "type").String() == "response.completed" {
							payload = gjson.Get(strings.TrimPrefix(line, "data: "), "response").Raw
						}
					}
				}
				for _, output := range gjson.Get(payload, "output").Array() {
					if output.Get("type").String() == "function_call" {
						require.Equal(t, "fc_715_once", output.Get("call_id").String())
						require.Equal(t, int64(7), gjson.Get(output.Get("arguments").String(), "add").Int())
						toolExecutions++
						ledgerBalance += 7
					}
				}
			}
			executeTools(first)
			require.Equal(t, 18, ledgerBalance, "original delegated operation must actually execute")
			accounts[0].Priority, accounts[1].Priority = 100, 0
			for turn := 0; turn < 2; turn++ {
				status, answer := issue715Request(t, handler, issue715Body(true, stream), 99, "fixture-715-thread", false)
				require.Contains(t, answer, "Same task: ledger balance 18; update already applied once", "same task and tool context must survive route drift")
				require.Equal(t, 200, status)
				executeTools(answer)
				require.NotContains(t, answer, `"type":"function_call"`, "already executed tool must not be requested again")
			}
			require.Equal(t, 1, toolExecutions)
			require.Equal(t, 18, ledgerBalance)
			require.Equal(t, []int64{1, 1, 1}, upstream.ids, "successful native owner must survive scheduler preference changes")
		})
	}
}

func TestIssue715HTTPRecoveryKeepsHardRestrictionsAndScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, change := range []string{"disabled", "credentials", "other_key", "other_session", "new_task", "canceled", "timeout"} {
		t.Run(change, func(t *testing.T) {
			accounts := issue715Accounts()
			upstream := &issue715Upstream{}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
			status, _ := issue715Request(t, handler, issue715Body(false, false), 99, "fixture-715-thread", false)
			require.Equal(t, 200, status)
			accounts[0].Priority, accounts[1].Priority = 100, 0
			body, key, session := issue715Body(true, false), int64(99), "fixture-715-thread"
			switch change {
			case "disabled":
				accounts[0].Schedulable = false
			case "credentials":
				accounts[0].Credentials = map[string]any{"access_token": "fixture-replaced-identity"}
			case "other_key":
				key++
			case "other_session":
				session += "-other"
			case "new_task":
				body = string(bytes.ReplaceAll([]byte(body), []byte("fixture-task-reference-715"), []byte("fixture-unseen-task")))
			case "timeout":
				upstream.mode = "timeout"
			}
			status, answer := issue715Request(t, handler, body, key, session, change == "canceled")
			require.NotContains(t, answer, "Same task: ledger balance 18", "unproved or unavailable route must not report recovered work")
			if change == "disabled" || change == "credentials" || change == "canceled" {
				require.Len(t, upstream.ids, 1, "hard rejection/cancellation must not send another task")
			} else if change == "timeout" {
				require.Equal(t, []int64{1, 1}, upstream.ids, "ambiguous submitted request must never move to a different account")
			} else {
				require.GreaterOrEqual(t, len(upstream.ids), 2)
				require.Equal(t, int64(2), upstream.ids[1], "a new scope or unseen task must not inherit a compatibility grant")
			}
			if change != "canceled" {
				require.NotEqual(t, 200, status)
			}
		})
	}
}

func TestIssue715HTTPPartialOutputNeverReplays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := issue715Accounts()
	upstream := &issue715Upstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
	status, _ := issue715Request(t, handler, issue715Body(false, true), 99, "fixture-715-thread", false)
	require.Equal(t, 200, status)
	accounts[0].Priority, accounts[1].Priority = 100, 0
	upstream.mode = "partial"
	_, answer := issue715Request(t, handler, issue715Body(true, true), 99, "fixture-715-thread", false)
	require.Contains(t, answer, "started existing task", "the regression must cross the real committed-output boundary")
	require.Equal(t, []int64{1, 1}, upstream.ids, "output followed by a rejection must never replay any generation")
}

func TestIssue715HTTPIncompleteNeverGrantsCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := issue715Accounts()
	upstream := &issue715Upstream{mode: "incomplete"}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
	issue715Request(t, handler, issue715Body(false, false), 99, "fixture-715-thread", false)
	require.Equal(t, []int64{1}, upstream.ids)
	accounts[0].Priority, accounts[1].Priority = 100, 0
	upstream.mode = ""
	issue715Request(t, handler, issue715Body(true, false), 99, "fixture-715-thread", false)
	require.GreaterOrEqual(t, len(upstream.ids), 2)
	require.Equal(t, int64(2), upstream.ids[1], "incomplete is not a successful compatibility observation")
}

// Exercise the actual Responses -> Forward -> JSON/SSE aggregation write chain.
// Failure happens during delivery, after the upstream has fully completed.
type issue715DeliveryWriter struct {
	gin.ResponseWriter
	mode   string
	cancel context.CancelFunc
	writes int
}

func (w *issue715DeliveryWriter) Write(body []byte) (int, error) {
	w.writes++
	switch w.mode {
	case "error":
		return 0, io.ErrClosedPipe
	case "short":
		return len(body) - 1, nil
	default:
		n, err := w.ResponseWriter.Write(body)
		w.cancel()
		return n, err
	}
}

func TestIssue715HTTPFailedDeliveryNeverGrantsOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, format := range []string{"json", "aggregate", "generated-json", "generated-stream"} {
		for _, failure := range []string{"error", "short", "cancel"} {
			t.Run(format+"/"+failure, func(t *testing.T) {
				accounts := issue715Accounts()
				upstream := &issue715Upstream{}
				if format != "json" {
					upstream.mode = format
				}
				handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
				firstBody := issue715Body(false, false)
				if strings.HasPrefix(format, "generated") {
					firstBody = strings.Replace(firstBody, issue715Agent+",", "", 1)
				}
				c, _ := newIssue715Context(t, firstBody)
				c.Request.Header.Set("session_id", "fixture-715-thread")
				key, _ := c.Get(string(middleware.ContextKeyAPIKey))
				key.(*service.APIKey).ID = 99
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				writer := &issue715DeliveryWriter{ResponseWriter: c.Writer, mode: failure, cancel: cancel}
				c.Writer = writer
				handler.Responses(c)
				require.Positive(t, writer.writes, "must fail inside actual downstream delivery")
				require.Equal(t, []int64{1}, upstream.ids, "a downstream failure cannot replay the request")
				accounts[0].Priority, accounts[1].Priority = 100, 0
				status, answer := issue715Request(t, handler, issue715Body(true, false), 99, "fixture-715-thread", false)
				if strings.HasPrefix(format, "generated") {
					require.Equal(t, http.StatusConflict, status, answer)
					require.Len(t, upstream.ids, 1, "failed generated delivery must fence ordinary routing without granting an owner")
				} else {
					require.GreaterOrEqual(t, len(upstream.ids), 2)
					require.Equal(t, int64(2), upstream.ids[1], "failed downstream delivery must not grant a successful owner")
				}
			})
		}
	}
}

func TestIssue715HTTPRecoveryErrorsIdentifyCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, change := range []string{"disabled", "credentials"} {
			t.Run(fmt.Sprint(stream)+"/"+change, func(t *testing.T) {
				accounts := issue715Accounts()
				upstream := &issue715Upstream{}
				handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
				status, _ := issue715Request(t, handler, issue715Body(false, stream), 99, "fixture-715-thread", false)
				require.Equal(t, 200, status)
				code := "encrypted_agent_recovery_owner_unavailable"
				if change == "disabled" {
					accounts[0].Schedulable = false
				} else {
					accounts[0].Credentials = map[string]any{"access_token": "fixture-rotated"}
					code = "encrypted_agent_recovery_route_changed"
				}
				status, answer := issue715Request(t, handler, issue715Body(true, stream), 99, "fixture-715-thread", false)
				require.Equal(t, http.StatusConflict, status, "compatibility refusal must not be a generic empty-pool response")
				require.Equal(t, code, gjson.Get(answer, "error.code").String())
				require.Equal(t, "invalid_request_error", gjson.Get(answer, "error.type").String())
				require.Contains(t, gjson.Get(answer, "error.message").String(), "Preserve this task and context")
				require.Equal(t, []int64{1}, upstream.ids)
			})
		}
	}
}

func TestIssue715RecoveryErrorCommittedSSEEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &OpenAIGatewayHandler{}
	c, rec := newIssue715Context(t, issue715Body(true, true))
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.WriteHeaderNow()
	require.True(t, handler.handleOpenAIAgentRecoveryError(c, &service.OpenAIAgentRecoveryError{Reason: "wire_changed"}, false))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "event: response.failed")
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			require.Equal(t, "encrypted_agent_recovery_wire_changed", gjson.Get(payload, "response.error.code").String())
			require.Equal(t, "failed", gjson.Get(payload, "response.status").String())
		}
	}
}

func TestIssue715HTTPGeneratedTaskReturnsToItsProducer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, format := range []string{"generated-json", "generated-stream", "generated-stream-items"} {
		t.Run(format, func(t *testing.T) {
			accounts := issue715Accounts()
			upstream := &issue715Upstream{mode: format}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
			stream := format == "generated-stream-items"
			firstBody := strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1)
			status, answer := issue715Request(t, handler, firstBody, 99, "fixture-715-thread", false)
			require.Equal(t, 200, status, answer)
			require.Contains(t, answer, "fixture-task-reference-715", "the task must be generated before its first consumption")
			firstPayload := answer
			if stream {
				for _, line := range strings.Split(answer, "\n") {
					if strings.HasPrefix(line, "data: ") && gjson.Get(strings.TrimPrefix(line, "data: "), "type").String() == "response.completed" {
						firstPayload = gjson.Get(strings.TrimPrefix(line, "data: "), "response").Raw
					}
				}
			}
			toolExecutions, balance := 0, 11
			for _, item := range gjson.Get(firstPayload, "output").Array() {
				if item.Get("type").String() == "function_call" {
					require.Equal(t, "fc_715_once", item.Get("call_id").String())
					balance += int(gjson.Get(item.Get("arguments").String(), "add").Int())
					toolExecutions++
				}
			}
			require.Equal(t, 1, toolExecutions, "generated task must actually execute the delegated tool once")
			require.Equal(t, 18, balance)
			accounts[0].Priority, accounts[1].Priority = 100, 0
			upstream.mode = ""
			for turn := 0; turn < 2; turn++ {
				status, answer = issue715Request(t, handler, issue715Body(true, stream), 99, "fixture-715-thread", false)
				require.Contains(t, answer, "Same task: ledger balance 18", "newly generated task must return to its producer before any prior successful input")
				require.NotContains(t, answer, `"type":"function_call"`, "completed generated task must not request its tool again")
				require.Equal(t, 200, status, answer)
			}
			require.Equal(t, []int64{1, 1, 1}, upstream.ids)
			require.Equal(t, 1, toolExecutions)
			require.Equal(t, 18, balance)
		})
	}
}

func TestIssue715HTTPConcurrentToolResultsKeepContextWithoutReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := issue715Accounts()
	upstream := &issue715Upstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
	status, answer := issue715Request(t, handler, issue715Body(false, false), 99, "fixture-715-thread", false)
	require.Equal(t, 200, status, answer)
	require.Contains(t, answer, "update_ledger")
	accounts[0].Priority, accounts[1].Priority = 100, 0
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, answer := issue715Request(t, handler, issue715Body(true, false), 99, "fixture-715-thread", false)
			require.Equal(t, 200, status, answer)
			require.Contains(t, answer, "Same task: ledger balance 18")
			require.NotContains(t, answer, `"type":"function_call"`)
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, []int64{1, 1, 1}, upstream.ids, "two client submissions must cause exactly two upstream submissions")
	for _, body := range upstream.bodies[1:] {
		require.Contains(t, string(body), "fixture-task-reference-715")
		require.Contains(t, string(body), "ledger balance 18; applied once")
		require.Equal(t, "fc_715_once", gjson.GetBytes(body, "input.#(type==function_call_output).call_id").String())
	}
}

func TestIssue715HTTPInterruptedToolResultDoesNotReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failure := range []string{"error", "cancel", "pool_failure"} {
		t.Run(failure, func(t *testing.T) {
			accounts := issue715Accounts()
			if failure == "pool_failure" {
				accounts[0].Type = service.AccountTypeAPIKey
				accounts[0].Credentials = map[string]any{"api_key": "fixture-key", "pool_mode": true, "pool_mode_retry_count": 2, "pool_mode_retry_status_codes": []any{520}}
			}
			upstream := &issue715Upstream{}
			handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
			status, answer := issue715Request(t, handler, issue715Body(false, false), 99, "fixture-715-thread", false)
			require.Equal(t, 200, status, answer)
			call := gjson.Get(answer, "output.0")
			require.Equal(t, "function_call", call.Get("type").String())
			toolExecutions, balance := 0, 11
			toolExecutions++
			balance += int(gjson.Get(call.Get("arguments").String(), "add").Int())
			require.Equal(t, 18, balance)
			if failure == "pool_failure" {
				upstream.mode = "pool_failure"
				issue715Request(t, handler, issue715Body(true, false), 99, "fixture-715-thread", false)
			} else {
				c, _ := newIssue715Context(t, issue715Body(true, false))
				c.Request.Header.Set("session_id", "fixture-715-thread")
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				c.Writer = &issue715DeliveryWriter{ResponseWriter: c.Writer, mode: failure, cancel: cancel}
				handler.Responses(c)
			}
			require.Equal(t, []int64{1, 1}, upstream.ids, "interrupted result submission must not automatically replay generation")
			upstream.mode = ""
			status, answer = issue715Request(t, handler, issue715Body(true, false), 99, "fixture-715-thread", false)
			require.Equal(t, 200, status, answer)
			require.Contains(t, answer, "Same task: ledger balance 18")
			require.NotContains(t, answer, `"type":"function_call"`)
			require.Equal(t, 1, toolExecutions)
			require.Equal(t, 18, balance)
			require.Equal(t, []int64{1, 1, 1}, upstream.ids)
		})
	}
}

func TestIssue715HTTPGeneratedOwnerSurvivesApplicationRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	defer client.Close()
	accounts := issue715Accounts()
	upstream := &issue715Upstream{mode: "generated-json"}
	producer := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, repository.NewGatewayCache(client), accounts...)
	firstBody := strings.Replace(issue715Body(false, false), issue715Agent+",", "", 1)
	status, answer := issue715Request(t, producer, firstBody, 99, "fixture-715-thread", false)
	require.Equal(t, 200, status, answer)
	require.Contains(t, answer, "fixture-task-reference-715")
	accounts[0].Priority, accounts[1].Priority = 100, 0
	upstream.mode = ""
	// Fresh service/adapter, with no process-local state. Ordinary sticky changes
	// are deliberately overwritten and cannot establish this compatibility grant.
	for node := 0; node < 2; node++ {
		cache := repository.NewGatewayCache(client)
		resumed := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, cache, accounts...)
		probe, _ := newIssue715Context(t, issue715Body(true, false))
		probe.Request.Header.Set("session_id", "fixture-715-thread")
		sessionHash := resumed.gatewayService.GenerateSessionHash(probe, []byte(issue715Body(true, false)))
		require.NoError(t, cache.SetSessionAccountID(context.Background(), 3132, "openai:"+sessionHash, 2, time.Minute))
		status, answer = issue715Request(t, resumed, issue715Body(true, false), 99, "fixture-715-thread", false)
		require.Equal(t, 200, status, answer)
		require.Contains(t, answer, "Same task: ledger balance 18")
	}
	require.Equal(t, []int64{1, 1, 1}, upstream.ids, "shared generated ownership must survive fresh services and scheduler drift")
	// Deleting/expiring authoritative records cannot resurrect a local grant.
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, "openai_agent_owner:") {
			mr.Del(key)
		}
	}
	cache := repository.NewGatewayCache(client)
	fresh := newOpenAIResponsesFailoverTestHandlerWithCache(t, upstream, cache, accounts...)
	probe, _ := newIssue715Context(t, issue715Body(true, false))
	probe.Request.Header.Set("session_id", "fixture-715-thread")
	sessionHash := fresh.gatewayService.GenerateSessionHash(probe, []byte(issue715Body(true, false)))
	require.NoError(t, cache.SetSessionAccountID(context.Background(), 3132, "openai:"+sessionHash, 2, time.Minute))
	issue715Request(t, fresh, issue715Body(true, false), 99, "fixture-715-thread", false)
	require.Equal(t, int64(2), upstream.ids[3], "lost shared evidence cannot grant the prior owner")
}

func TestIssue715HTTPRecoveryCannotBypassLongContextPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := issue715Accounts()
	upstream := &issue715Upstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
	status, answer := issue715Request(t, handler, issue715Body(false, false), 99, "fixture-715-thread", false)
	require.Equal(t, 200, status, answer)
	c, rec := newIssue715Context(t, issue715Body(true, false))
	c.Request.Header.Set("session_id", "fixture-715-thread")
	key, _ := c.Get(string(middleware.ContextKeyAPIKey))
	key.(*service.APIKey).AllowLongContext = false
	handler.Responses(c)
	require.Equal(t, 400, rec.Code)
	require.Equal(t, "context_unverifiable", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, []int64{1}, upstream.ids, "compatibility ownership must not override API-key permissions")
}

func TestIssue715HTTPFinalWireChangeHasCompatibilityEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := issue715Accounts()
	credentials := func(org string) map[string]any {
		return map[string]any{"api_key": "fixture-key", "header_override_enabled": true, "header_overrides": map[string]any{"openai-organization": org}}
	}
	accounts[0].Type = service.AccountTypeAPIKey
	accounts[0].Credentials = credentials("fixture-org-one")
	upstream := &issue715Upstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
	status, answer := issue715Request(t, handler, issue715Body(false, false), 99, "fixture-715-thread", false)
	require.Equal(t, 200, status, answer)
	accounts[0].Credentials = credentials("fixture-org-two")
	status, answer = issue715Request(t, handler, issue715Body(true, false), 99, "fixture-715-thread", false)
	require.Equal(t, 409, status, "final-send wire refusal must be a classified compatibility error")
	require.Equal(t, "encrypted_agent_recovery_wire_changed", gjson.Get(answer, "error.code").String())
	require.Equal(t, []int64{1}, upstream.ids, "changed final wire must be refused before upstream submission")
}

func TestIssue715HTTPFailedDeliveryKeepsCompletedUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, format := range []string{"json", "aggregate"} {
		for _, failure := range []string{"error", "short", "cancel"} {
			t.Run(format+"/"+failure, func(t *testing.T) {
				accounts := issue715Accounts()
				upstream := &issue715Upstream{}
				if format == "aggregate" {
					upstream.mode = "aggregate"
				}
				handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
				body := issue715Body(false, false)
				c, _ := newIssue715Context(t, body)
				c.Request.Header.Set("session_id", "fixture-715-thread")
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				c.Writer = &issue715DeliveryWriter{ResponseWriter: c.Writer, mode: failure, cancel: cancel}
				result, err := handler.gatewayService.Forward(c.Request.Context(), c, &accounts[0], []byte(body))
				require.Error(t, err)
				require.NotNil(t, result, "downstream delivery failure must retain completed upstream accounting")
				require.True(t, result.ClientDisconnect)
				require.EqualValues(t, 5, result.Usage.InputTokens)
				require.EqualValues(t, 3, result.Usage.OutputTokens)
				require.Equal(t, []int64{1}, upstream.ids)
			})
		}
	}
}

func TestIssue715HTTPPoolCustomToolResultNeverReplays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := issue715Accounts()
	accounts[0].Type = service.AccountTypeAPIKey
	accounts[0].Credentials = map[string]any{"api_key": "fixture-key", "pool_mode": true, "pool_mode_retry_count": 2, "pool_mode_retry_status_codes": []any{520}}
	upstream := &issue715Upstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream, accounts...)
	status, answer := issue715Request(t, handler, issue715Body(false, false), 99, "fixture-715-thread", false)
	require.Equal(t, 200, status, answer)
	body := strings.TrimSuffix(issue715Body(false, false), `]}`) + `,{"type":"custom_tool_call","name":"fixture_note","call_id":"custom_fixture_715","input":"note balance 18"},{"type":"custom_tool_call_output","call_id":"custom_fixture_715","output":"fixture note already written"}]}`
	upstream.mode = "pool_failure"
	issue715Request(t, handler, body, 99, "fixture-715-thread", false)
	require.Equal(t, []int64{1, 1}, upstream.ids, "a custom tool result must also stop pool-mode auto replay")
	require.Contains(t, string(upstream.bodies[1]), "fixture note already written")
}
