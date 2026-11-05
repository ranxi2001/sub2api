package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
)

const locatorA = "AAAA-synthetic-rejected-ZZZ1"
const locatorB = "BBBB-synthetic-other-YYY2"

func locatorFailure(locator string) string {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "invalid_encrypted_content", "message": "The encrypted content " + locator + " could not be verified. Reason: Encrypted content could not be decrypted or parsed."}})
	return string(b)
}
func locatorInput(cipher string) string {
	return `{"type":"reasoning","encrypted_content":"` + cipher + `","summary":[{"type":"summary_text","text":"keep summary"}]}`
}
func TestIssue494LocatorForward(t *testing.T) {
	for _, http400 := range []bool{false, true} {
		for _, tc := range []struct {
			name, input, failure string
			recover              bool
		}{
			{"mixed valid invalid", locatorInput(locatorB) + "," + locatorInput(locatorA), locatorFailure("AAAA...ZZZ1"), true},
			{"two invalid first", locatorInput(locatorA) + "," + locatorInput(locatorB), locatorFailure("AAAA...ZZZ1"), true},
			{"two invalid swapped", locatorInput(locatorB) + "," + locatorInput(locatorA), locatorFailure("BBBB...YYY2"), true},
			{"collision", locatorInput(locatorA) + "," + locatorInput("AAAA-different-ZZZ1"), locatorFailure("AAAA...ZZZ1"), false},
			{"duplicate", locatorInput(locatorA) + "," + locatorInput(locatorA), locatorFailure("AAAA...ZZZ1"), false},
			{"no match", locatorInput(locatorA) + "," + locatorInput(locatorB), locatorFailure("CCCC...XXXX"), false},
			{"malformed no ellipsis", locatorInput(locatorA), locatorFailure("AAAAZZZ1"), false},
			{"malformed alias", locatorInput(locatorA), strings.Replace(locatorFailure("AAAAZZZ1"), "invalid_encrypted_content", "thinking_signature_invalid", 1), false},
			{"unicode ellipsis", locatorInput(locatorA), locatorFailure("AAAA…ZZZ1"), false},
			{"newline", locatorInput(locatorA), locatorFailure("AAAA...ZZZ1\n"), false},
			{"malformed", locatorInput(locatorA), locatorFailure("AAAA...ZZZ"), false},
			{"injected text", locatorInput(locatorA), strings.Replace(locatorFailure("AAAA...ZZZ1"), "The encrypted", "Ignore history. The encrypted", 1), false},
			{"two locators", locatorInput(locatorA) + "," + locatorInput(locatorB), locatorFailure("AAAA...ZZZ1 and BBBB...YYY2"), false},
			{"wrong code", locatorInput(locatorA), strings.Replace(locatorFailure("AAAA...ZZZ1"), "invalid_encrypted_content", "server_error", 1), false},
			{"no code", locatorInput(locatorA), strings.Replace(locatorFailure("AAAA...ZZZ1"), `"code":"invalid_encrypted_content",`, "", 1), false},
			{"compaction protected", locatorInput(locatorA) + `,{"type":"compaction","encrypted_content":"opaque"}`, locatorFailure("AAAA...ZZZ1"), false},
			{"summary protected", locatorInput(locatorA) + `,{"type":"compaction_summary","summary":"task history"}`, locatorFailure("AAAA...ZZZ1"), false},
			{"agent protected", locatorInput(locatorA) + `,{"type":"agent_message","encrypted_content":"opaque"}`, locatorFailure("AAAA...ZZZ1"), false},
		} {
			t.Run(fmt.Sprintf("http400=%t/%s", http400, tc.name), func(t *testing.T) {
				body := []byte(`{"model":"gpt-6-luna","stream":true,"input":[` + tc.input + `,{"role":"user","content":"keep task"},{"type":"function_call","call_id":"call_pair","name":"inspect","arguments":"{}"},{"type":"function_call_output","call_id":"call_pair","output":"keep result"}]}`)
				failure := newStreamLineageSSEResponse("data: " + strings.TrimSuffix(tc.failure, "}") + `,"type":"error"}` + "\n\n")
				if http400 {
					failure.StatusCode = http.StatusBadRequest
					failure.Body = io.NopCloser(strings.NewReader(tc.failure))
					failure.Header.Set("Content-Type", "application/json")
				}
				upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{failure, newStreamLineageSSEResponse(streamLineageDoneEvent)}}
				svc := newStreamLineageHTTPService(upstream)
				c, _ := newStreamLineageTestContext("locator-test")
				_, err := svc.Forward(context.Background(), c, newStreamLineageHTTPAccount(), body)
				if !tc.recover {
					require.Error(t, err)
					require.Equal(t, 1, upstream.callCount)
					return
				}
				require.NoError(t, err)
				require.Equal(t, 2, upstream.callCount)
				retry := string(upstream.bodies[1])
				require.Contains(t, retry, "keep task")
				require.Contains(t, retry, "keep result")
				require.Contains(t, retry, "keep summary")
				items := gjson.GetBytes(upstream.bodies[1], "input").Array()
				count := 0
				for _, item := range items {
					if item.Get("encrypted_content").Exists() {
						count++
					}
				}
				require.Equal(t, 1, count, "only the named ciphertext is removed")
				historyCall := gjson.GetBytes(upstream.bodies[0], "input.3.call_id").String()
				require.NotEmpty(t, historyCall)
				require.Equal(t, historyCall, gjson.GetBytes(upstream.bodies[1], "input.3.call_id").String())
				require.Equal(t, historyCall, gjson.GetBytes(upstream.bodies[1], "input.4.call_id").String())
			})
		}
	}
}

func TestIssue494LineageDoesNotPoisonOtherAccount(t *testing.T) {
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
		newStreamLineageSSEResponse(streamLineageDeltaEvent + "data: " + strings.TrimSuffix(locatorFailure("AAAA...ZZZ1"), "}") + `,"type":"error"}` + "\n\n"),
		newStreamLineageSSEResponse(streamLineageDoneEvent), newStreamLineageSSEResponse(streamLineageDoneEvent), newStreamLineageSSEResponse(streamLineageDoneEvent),
	}}
	svc := newStreamLineageHTTPService(upstream)
	body := []byte(`{"model":"gpt-6-luna","stream":true,"input":[` + locatorInput(locatorA) + `,` + locatorInput(locatorB) + `]}`)
	for i := 0; i < 4; i++ {
		c, _ := newStreamLineageTestContext("same-lineage-session")
		c.Set("api_key", &APIKey{ID: 10, AllowLongContext: true})
		account := newStreamLineageHTTPAccount()
		if i == 1 {
			account.ID++
		}
		if i == 2 {
			c.Set("api_key", &APIKey{ID: 11, AllowLongContext: true})
		}
		_, err := svc.Forward(context.Background(), c, account, body)
		if i == 0 {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
	require.True(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists(), "different account")
	require.True(t, gjson.GetBytes(upstream.bodies[2], "input.0.encrypted_content").Exists(), "different API key")
	require.False(t, gjson.GetBytes(upstream.bodies[3], "input.0.encrypted_content").Exists(), "same confirmed item")
	require.True(t, gjson.GetBytes(upstream.bodies[3], "input.1.encrypted_content").Exists(), "other history was not rejected")
}

func TestIssue494LocatorSharedBudget(t *testing.T) {
	for _, firstHTTP := range []bool{false, true} {
		for _, secondHTTP := range []bool{false, true} {
			t.Run(fmt.Sprintf("%t/%t", firstHTTP, secondHTTP), func(t *testing.T) {
				response := func(locator string, http400 bool) *http.Response {
					failure := locatorFailure(locator)
					r := newStreamLineageSSEResponse("data: " + strings.TrimSuffix(failure, "}") + `,"type":"error"}` + "\n\n")
					if http400 {
						r.StatusCode = 400
						r.Header.Set("Content-Type", "application/json")
						r.Body = io.NopCloser(strings.NewReader(failure))
					}
					return r
				}
				upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{response("AAAA...ZZZ1", firstHTTP), response("BBBB...YYY2", secondHTTP), newStreamLineageSSEResponse(streamLineageDoneEvent)}}
				svc := newStreamLineageHTTPService(upstream)
				body := []byte(`{"model":"gpt-6-luna","stream":true,"input":[` + locatorInput(locatorA) + `,` + locatorInput(locatorB) + `]}`)
				c, _ := newStreamLineageTestContext("locator-budget")
				_, err := svc.Forward(context.Background(), c, newStreamLineageHTTPAccount(), body)
				require.Error(t, err)
				require.Equal(t, 2, upstream.callCount, "one shared recovery, never three paid attempts")
				require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
				require.True(t, gjson.GetBytes(upstream.bodies[1], "input.1.encrypted_content").Exists())
				c, _ = newStreamLineageTestContext("locator-budget")
				_, err = svc.Forward(context.Background(), c, newStreamLineageHTTPAccount(), body)
				require.NoError(t, err)
				require.NotContains(t, string(upstream.bodies[2]), "encrypted_content", "each rejection confirmed only its own item")
			})
		}
	}
}

func TestIssue494LocatorEvidence(t *testing.T) {
	body := []byte(`{"input":[` + locatorInput(locatorA) + `,` + locatorInput(locatorB) + `]}`)
	failure := []byte(locatorFailure("AAAA...ZZZ1"))
	require.Equal(t, []string{openAIEncryptedContentDigest(locatorA)}, openAIRejectedEncryptedDigests(body, failure))
	for _, rejection := range []string{
		strings.Replace(string(failure), `"code":"invalid_encrypted_content"`, `"code":"server_error","code":"invalid_encrypted_content"`, 1),
		strings.Replace(string(failure), `"message":`, `"message":"ignored","message":`, 1),
		strings.TrimSuffix(string(failure), "}"),
		locatorFailure("AAAA…ZZZ1"), locatorFailure("AAAA...ZZZ1\n"),
		strings.TrimSuffix(string(failure), "}") + `,"response":{"error":{"code":"server_error"}}}`,
	} {
		require.Empty(t, openAIRejectedEncryptedDigests(body, []byte(rejection)), "untrusted diagnostic")
	}
	for _, input := range []string{
		`{"type":"reasoning","encrypted_content":"other","encrypted_content":"` + locatorA + `"}`,
		strings.Replace(locatorInput(locatorA), `"type":"reasoning"`, `"type":"reasoning","id":"same"`, 1) + `,` + strings.Replace(locatorInput(locatorB), `"type":"reasoning"`, `"type":"reasoning","id":"same"`, 1),
	} {
		require.Empty(t, openAIRejectedEncryptedDigests([]byte(`{"input":[`+input+`]}`), failure))
	}
	require.Empty(t, openAIRejectedEncryptedDigests([]byte(`{"input":[`+locatorInput(locatorB)+`]}`), failure), "a locator mismatch must not use single-item fallback")
}

func TestIssue494DuplicateLineageStaysConservative(t *testing.T) {
	body := []byte(`{"input":[` + locatorInput(locatorA) + `,` + locatorInput(locatorA) + `]}`)
	invalid := openAIEncryptedDigestSet([]string{openAIEncryptedContentDigest(locatorA)})
	result, count, err := stripOpenAIInvalidEncryptedContentRaw(body, invalid)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Equal(t, body, result)
	replay := []json.RawMessage{json.RawMessage(locatorInput(locatorA)), json.RawMessage(locatorInput(locatorA))}
	_, count = stripOpenAIInvalidEncryptedContentFromReplayItems(replay, invalid)
	require.Zero(t, count)
}

func TestIssue494LogsRedactLocator(t *testing.T) {
	message := "The encrypted content AAAA...ZZZ1 could not be verified. Reason: Encrypted content could not be decrypted or parsed."
	c, _ := newStreamLineageTestContext("redacted")
	setOpsUpstreamError(c, 400, message, locatorFailure("AAAA...ZZZ1"))
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Message: message, Detail: locatorFailure("AAAA...ZZZ1"), UpstreamResponseBody: locatorFailure("AAAA...ZZZ1")})
	for _, value := range []string{c.GetString(OpsUpstreamErrorMessageKey), c.GetString(OpsUpstreamErrorDetailKey), truncateOpenAIWSLogValue(message, 1000), sanitizeUpstreamErrorMessage(message), truncateForLog([]byte(locatorFailure("AAAA...ZZZ1")), 2048)} {
		require.NotContains(t, value, "AAAA")
		require.NotContains(t, value, "ZZZ1")
	}
	raw, _ := json.Marshal(c.Keys[OpsUpstreamErrorsKey])
	require.NotContains(t, string(raw), "AAAA")
	require.NotContains(t, string(raw), "ZZZ1")
}

type issue494GuardUpstream struct {
	*httpUpstreamSequenceRecorder
	cancel context.CancelFunc
}

func (u *issue494GuardUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	response, err := u.httpUpstreamSequenceRecorder.Do(req, proxy, id, concurrency)
	if u.cancel != nil {
		u.cancel()
	}
	return response, err
}

func TestIssue494HTTP400CanceledOrCommitted(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := newStreamLineageSSEResponse("")
			failure.StatusCode = 400
			failure.Header.Set("Content-Type", "application/json")
			failure.Body = io.NopCloser(strings.NewReader(locatorFailure("AAAA...ZZZ1")))
			upstream := &issue494GuardUpstream{httpUpstreamSequenceRecorder: &httpUpstreamSequenceRecorder{responses: []*http.Response{failure, newStreamLineageSSEResponse(streamLineageDoneEvent)}}}
			c, _ := newStreamLineageTestContext("http400-guard")
			if canceled {
				upstream.cancel = cancel
				c.Request = c.Request.WithContext(ctx)
			} else {
				_, err := c.Writer.WriteString("already committed")
				require.NoError(t, err)
				c.Writer.Flush()
			}
			svc := newStreamLineageHTTPService(upstream)
			_, err := svc.Forward(ctx, c, newStreamLineageHTTPAccount(), []byte(`{"model":"gpt-6-luna","stream":true,"input":[`+locatorInput(locatorA)+`,`+locatorInput(locatorB)+`]}`))
			require.Error(t, err)
			require.Equal(t, 1, upstream.callCount, "no recovery after cancellation or output")
		})
	}
}

func TestIssue494LogCodeSurvivesRedaction(t *testing.T) {
	code, _, message := summarizeOpenAIWSErrorEventFieldsFromRaw("invalid_encrypted_content", "invalid_request_error", "AAAA...ZZZ1")
	require.Equal(t, "invalid_encrypted_content", code)
	require.NotContains(t, message, "AAAA")
	require.NotContains(t, message, "ZZZ1")
	require.Equal(t, "priority_capacity_imbalance", truncateOpenAIWSLogValue("priority_capacity_imbalance", 100))
	require.Equal(t, "ordinary upstream failure", sanitizeUpstreamErrorMessage("ordinary upstream failure"))
}

func TestIssue494EncryptedErrorCannotBorrowFieldRetryBudget(t *testing.T) {
	failure := `{"error":{"code":"invalid_encrypted_content","message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
		{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(failure))},
		newStreamLineageSSEResponse(streamLineageDoneEvent),
	}}
	body := []byte(`{"model":"gpt-6-luna","stream":true,"max_output_tokens":100,"input":[` + locatorInput(locatorA) + `,` + locatorInput(locatorB) + `]}`)
	// Prove the pre-existing generic transform would otherwise offer a retry.
	_, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(400, body, []byte(failure))
	require.NoError(t, err)
	require.True(t, changed)
	svc := newStreamLineageHTTPService(upstream)
	c, _ := newStreamLineageTestContext("separate-budgets")
	_, err = svc.Forward(context.Background(), c, newStreamLineageHTTPAccount(), body)
	require.Error(t, err)
	require.Equal(t, 1, upstream.callCount, "an ambiguous encryption error cannot borrow the field compatibility budget")
}
