//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
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
		input += `,{"type":"function_call","name":"update_ledger","call_id":"call_715_once","arguments":"{\"add\":7}"},{"type":"function_call_output","call_id":"call_715_once","output":"ledger balance 18; applied once"},{"type":"message","role":"user","content":"Continue the same task and confirm the existing balance"}`
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
	if !bytes.Contains([]byte(input), []byte("fixture-task-reference-715")) || !bytes.Contains([]byte(input), []byte("Keep ledger balance 11")) {
		return response(400, "application/json", `{"error":{"code":"missing_fixture_task_or_context"}}`)
	}
	if mode == "partial" {
		return response(200, "text/event-stream", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"started existing task\"}\n\nevent: error\ndata: {\"type\":\"error\",\"code\":\"unsupported_encrypted_agent_message\",\"message\":\"fixture failure after output\"}\n\n")
	}
	output := `[{"type":"function_call","call_id":"call_715_once","name":"update_ledger","arguments":"{\"add\":7}"}]`
	if gjson.GetBytes(body, "input.#(type==function_call_output).output").String() == "ledger balance 18; applied once" {
		output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Same task: ledger balance 18; update already applied once"}]}]`
	}
	status := "completed"
	if mode == "incomplete" {
		status = "incomplete"
	}
	payload := fmt.Sprintf(`{"id":"resp_715","status":%q,"error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, status, output)
	if gjson.GetBytes(body, "stream").Bool() {
		event := "response.completed"
		if mode == "incomplete" {
			event = "response.incomplete"
		}
		return response(200, "text/event-stream", fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"response\":%s}\n\ndata: [DONE]\n\n", event, event, payload))
	}
	return response(200, "application/json", payload)
}

func issue715Accounts() []service.Account {
	return []service.Account{
		{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"access_token": "fixture-token-one"}},
		{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"access_token": "fixture-token-two"}},
	}
}

func issue715Request(t *testing.T, handler *OpenAIGatewayHandler, body string, keyID int64, session string, canceled bool) (int, string) {
	t.Helper()
	c, rec := newAstraProFailoverContext(t, body)
	c.Request.Header.Set("session_id", session)
	key, _ := c.Get(string(middleware.ContextKeyAPIKey))
	key.(*service.APIKey).ID = keyID
	if canceled {
		ctx, cancel := context.WithCancel(c.Request.Context())
		cancel()
		c.Request = c.Request.WithContext(ctx)
	}
	handler.Responses(c)
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
			require.Equal(t, 200, status)
			require.Contains(t, first, "update_ledger")
			// Execute actual function-call outputs in a fake client. Replay of
			// call_715_once is a test failure, not silently deduplicated here.
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
						require.Equal(t, "call_715_once", output.Get("call_id").String())
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
