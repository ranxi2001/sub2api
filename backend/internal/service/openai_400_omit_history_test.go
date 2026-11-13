package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const history400Input = `[{"type":"reasoning","encrypted_content":"AAAA-fixture-1111","summary":[{"type":"summary_text","text":"visible reasoning one"}]},{"type":"reasoning","encrypted_content":"BBBB-fixture-2222","summary":[{"type":"summary_text","text":"visible reasoning two"}]},{"type":"function_call","name":"inspect","call_id":"call_fixture","arguments":"{\"sequence\":9007199254740993}"},{"type":"function_call_output","call_id":"call_fixture","output":"already completed"},{"role":"user","content":"Explain the recorded result","sequence":9007199254740993}]`
const history400Failure = `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified."}}`

func history400Body(input string, stream bool) []byte {
	return []byte(fmt.Sprintf(`{"model":"gpt-5.1","stream":%t,"input":%s}`, stream, input))
}

type history400SettingsRepo struct {
	SettingRepository
	values map[string]string
}

func (s *history400SettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func (s *history400SettingsRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string)
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func enableHistory400(s *OpenAIGatewayService, enabled bool) {
	s.settingService = NewSettingService(&history400SettingsRepo{values: map[string]string{
		SettingKeyOpenAI400OmitEncryptedHistoryEnabled: fmt.Sprint(enabled),
	}}, s.cfg)
}

func TestOpenAI400HistoryOmission(t *testing.T) {
	body := history400Body(history400Input, false)
	canonical := append([]byte(nil), body...)
	clean, changed := omitOpenAIHistoricalCiphertextForRecovery(body, true)
	require.True(t, changed)
	require.Equal(t, canonical, body)
	require.NotContains(t, string(clean), "AAAA-fixture-1111")
	require.NotContains(t, string(clean), "BBBB-fixture-2222")
	require.Contains(t, string(clean), "visible reasoning one")
	require.Contains(t, string(clean), "visible reasoning two")
	require.Contains(t, string(clean), openAIHistoricalCiphertextOmitted)
	for _, path := range []string{"input.2", "input.3", "input.4", "model", "stream"} {
		require.JSONEq(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(clean, path).Raw, path)
	}
	_, changed = omitOpenAIHistoricalCiphertext(body)
	require.False(t, changed, "409 omission must not expand to reasoning")
	_, changed = omitOpenAIHistoricalCiphertextForRecovery(clean, true)
	require.False(t, changed, "no second omission when nothing remains")

	cases := map[string]string{
		"current agent":           `{"input":[{"type":"agent_message","encrypted_content":"old"},{"type":"agent_message","encrypted_content":"current","content":"visible header"}]}`,
		"current reasoning":       `{"input":[{"role":"user","content":"task"},{"type":"reasoning","encrypted_content":"current"}]}`,
		"current tool result":     `{"input":[{"type":"reasoning","encrypted_content":"old"},{"role":"user","content":"task"},{"type":"function_call_output","call_id":"x","output":[{"type":"encrypted_content","encrypted_content":"current"}]}]}`,
		"no readable task":        `{"input":[{"type":"reasoning","encrypted_content":"old"},{"role":"user","content":[]}]}`,
		"unknown reasoning field": `{"input":[{"type":"reasoning","encrypted_content":"old","instructions":"must survive"},{"role":"user","content":"task"}]}`,
		"unknown visible part":    `{"input":[{"type":"reasoning","encrypted_content":"old","summary":[{"type":"image","url":"fixture"}]},{"role":"user","content":"task"}]}`,
		"compaction":              `{"input":[{"type":"compaction","encrypted_content":"history"},{"type":"reasoning","encrypted_content":"old"},{"role":"user","content":"task"}]}`,
		"pointer":                 `{"previous_response_id":"resp_fixture","input":[{"type":"reasoning","encrypted_content":"old"},{"role":"user","content":"task"}]}`,
		"conversation":            `{"conversation":"conv_fixture","input":[{"type":"reasoning","encrypted_content":"old"},{"role":"user","content":"task"}]}`,
		"reference":               `{"input":[{"type":"item_reference","id":"opaque"},{"type":"reasoning","encrypted_content":"old"},{"role":"user","content":"task"}]}`,
		"duplicate keys":          `{"input":[],"input":[{"type":"reasoning","encrypted_content":"old"},{"role":"user","content":"task"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, changed := omitOpenAIHistoricalCiphertextForRecovery([]byte(body), true)
			require.False(t, changed)
			require.Equal(t, body, string(got), "refusal must not partially rewrite history")
		})
	}
}

func TestOpenAI400HistoryNativeForward(t *testing.T) {
	const located = `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content AAAA...1111 could not be verified. Reason: Encrypted content could not be decrypted or parsed."}}`
	const ordinary = `{"error":{"code":"invalid_request_error","message":"Unknown parameter: example"}}`
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name                                                string
			enabled, after409, rejectedAgain, preciseFirst, sse bool
			failure, input                                      string
			calls                                               int
			applied                                             bool
		}{
			{name: "disabled", calls: 1},
			{name: "enabled", enabled: true, calls: 2, applied: true},
			{name: "after409", enabled: true, after409: true, calls: 2, applied: true},
			{name: "409 alone", after409: true, calls: 1},
			{name: "one lossy retry only", enabled: true, rejectedAgain: true, calls: 2, applied: true},
			{name: "precise then lossy", enabled: true, preciseFirst: true, calls: 3, applied: true},
			{name: "ordinary400", enabled: true, failure: ordinary, calls: 1},
			{name: "nothing to omit", enabled: true, input: `[{"role":"user","content":"task"}]`, calls: 1},
			{name: "current agent", enabled: true, input: `[{"type":"reasoning","encrypted_content":"old"},{"type":"agent_message","encrypted_content":"current","content":"task"}]`, calls: 1},
			{name: "SSE terminal excluded", enabled: true, sse: true, calls: 1},
		} {
			if tc.sse && !stream {
				continue
			}
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				input := tc.input
				if input == "" {
					input = history400Input
				}
				body := history400Body(input, stream)
				canonical := append([]byte(nil), body...)
				failure := tc.failure
				if failure == "" {
					failure = history400Failure
				}
				response := func(status int, payload string) *http.Response {
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}
				}
				success := response(200, `{"id":"resp_fixture_success","status":"completed","model":"gpt-5.1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
				if stream {
					success = newStreamLineageSSEResponse(streamLineageDoneEvent)
				}
				responses := []*http.Response{response(400, failure), success}
				if tc.rejectedAgain {
					responses[1] = response(400, failure)
				}
				if tc.preciseFirst {
					responses = []*http.Response{response(400, located), response(400, failure), success}
				}
				if tc.sse {
					responses[0] = newStreamLineageSSEResponse(encryptedSSERetryEvent(encryptedSSERetryFailure))
				}
				upstream := &httpUpstreamSequenceRecorder{responses: responses}
				svc := newStreamLineageHTTPService(upstream)
				enableHistory400(svc, tc.enabled)
				c, rec := newStreamLineageTestContext("history400-fixture")
				c.Set("api_key", &APIKey{ID: 7, AllowLongContext: true})
				ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, body, "history400-fixture", "gpt-5.1")
				if tc.after409 {
					r := openAIAgentRecoveryFromContext(ctx)
					r.omittedHistory, r.inputDigest = true, openAIAgentRecoveryInputDigest(body)
				}
				c.Request = c.Request.WithContext(ctx)
				_, err := svc.Forward(ctx, c, newStreamLineageHTTPAccount(), body)
				require.Equal(t, tc.calls, upstream.callCount, "response: %s; error: %v", rec.Body.String(), err)
				require.Equal(t, canonical, body)
				if tc.applied {
					d := openAI400HistoryDiagnostic(c)
					require.NotNil(t, d)
					require.True(t, d.Applied)
					require.Equal(t, tc.after409, d.After409)
					last := upstream.bodies[len(upstream.bodies)-1]
					require.NotContains(t, string(last), "AAAA-fixture-1111")
					require.NotContains(t, string(last), "BBBB-fixture-2222")
					for _, path := range []string{"input.2", "input.3", "input.4"} {
						require.JSONEq(t, gjson.GetBytes(upstream.bodies[0], path).Raw, gjson.GetBytes(last, path).Raw)
					}
					for _, req := range upstream.reqs {
						require.Equal(t, upstream.reqs[0].Header.Get("Authorization"), req.Header.Get("Authorization"))
						require.Equal(t, upstream.reqs[0].URL.String(), req.URL.String())
					}
				}
				require.NotContains(t, rec.Body.String(), "encrypted_history_400")
				if tc.applied && !tc.rejectedAgain {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}

func TestOpenAI400HistorySendGuards(t *testing.T) {
	for _, mode := range []string{"unchanged", "task", "tool_result", "order", "account", "credential", "url", "budget", "canceled", "output_started", "delivery_pending", "owner_lookup"} {
		t.Run(mode, func(t *testing.T) {
			svc := newStreamLineageHTTPService(nil)
			enableHistory400(svc, true)
			c, rec := newStreamLineageTestContext("history400-guards")
			c.Set("api_key", &APIKey{ID: 7, AllowLongContext: true})
			body := history400Body(history400Input, false)
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, body, "history400-guards", "gpt-5.1")
			original := openAIAgentRecoveryFromContext(ctx)
			original.omittedHistory, original.inputDigest = true, openAIAgentRecoveryInputDigest(body)
			digest := original.inputDigest
			account := newStreamLineageHTTPAccount()
			request := func(body []byte, url string) *http.Request {
				req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer synthetic-fixture")
				return req
			}
			url := "https://api.openai.example/v1/responses"
			finish, err := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, request(body, url))
			require.NoError(t, err)
			defer finish(false)
			attempts := 1
			switch mode {
			case "budget":
				attempts = 3
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "output_started":
				c.Writer.WriteHeaderNow()
			case "delivery_pending":
				original.delivery.keys[[32]byte{1}] = true
			case "owner_lookup":
				original.lookupErr = fmt.Errorf("fixture unavailable")
			}
			next, clean, changed := svc.prepareOpenAI400HistoryRecovery(ctx, c, account, body, attempts)
			if mode == "budget" || mode == "canceled" || mode == "output_started" || mode == "delivery_pending" || mode == "owner_lookup" {
				require.False(t, changed)
				require.Equal(t, body, clean)
				return
			}
			require.True(t, changed, "%+v; response=%s", openAI400HistoryDiagnostic(c), rec.Body.String())
			require.Equal(t, digest, original.inputDigest, "source snapshot is immutable")
			frozen := openAIAgentRecoveryFromContext(next).inputDigest
			switch mode {
			case "task":
				clean, err = sjson.SetBytes(clean, "input.4.content", "changed task")
			case "tool_result":
				clean, err = sjson.SetBytes(clean, "input.3.output", "changed result")
			case "order":
				clean, err = sjson.DeleteBytes(clean, "input.2")
			case "account":
				account.ID++
			case "credential":
				account.Credentials["api_key"] = "different-synthetic-credential"
			case "url":
				url = "https://other.example/v1/responses"
			}
			require.NoError(t, err)
			finish, err = svc.prepareOpenAIAgentRecoveryAttempt(next, account, request(clean, url))
			defer finish(false)
			if mode == "unchanged" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, frozen, openAIAgentRecoveryFromContext(next).inputDigest)
		})
	}
}

func TestOpenAI400HistoryDiagnosticSnapshot(t *testing.T) {
	c, _ := newStreamLineageTestContext("history400-diagnostic")
	d := &OpenAI400HistoryDiagnostic{Applied: true, After409: true, Decision: "same_account_retry_scheduled"}
	c.Set(openAI400HistoryDiagnosticKey, d)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "retry", UpstreamStatusCode: 400, Message: "fixture"})
	d.Decision = "retry_exhausted"
	events, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	stored, ok := events.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, stored, 1, "diagnostics must not add fake attempts")
	require.Equal(t, "same_account_retry_scheduled", stored[0].EncryptedHistory400.Decision)
	entry := &OpsInsertErrorLogInput{UpstreamErrors: stored}
	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	require.NotNil(t, entry.UpstreamErrorsJSON)
	require.Contains(t, *entry.UpstreamErrorsJSON, "encrypted_history_400")
	require.Nil(t, sanitizeOpenAI400HistoryDiagnostic(&OpenAI400HistoryDiagnostic{Decision: "untrusted text"}))
	require.Nil(t, sanitizeOpenAI400HistoryDiagnostic(nil))
}
