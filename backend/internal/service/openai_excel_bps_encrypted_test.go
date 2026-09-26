package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const excelBPSInvalidCiphertext = `{"error":{"code":"invalid_encrypted_content","message":"private upstream diagnostic"}}`
const excelBPSInvalidCiphertextNoCode = `{"error":{"message":"The encrypted content fixture...end could not be verified. Reason: Encrypted content could not be decrypted or parsed."}}`

func TestExcelBPSInvalidEncryptedContentClassification(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"code", excelBPSInvalidCiphertext, true},
		{"diagnostic without code", excelBPSInvalidCiphertextNoCode, true},
		{"unrelated code", `{"error":{"code":"invalid_value","message":"The encrypted content could not be verified and could not be decrypted or parsed"}}`, false},
		{"echoed text", `{"error":{"message":"Invalid input. User said: the encrypted content could not be verified and could not be decrypted or parsed"}}`, false},
		{"unrelated request", `{"error":{"message":"Unsupported parameter: encrypted_content"}}`, false},
		{"invalid JSON", `{"error":{"code":"invalid_encrypted_content"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isExcelBPSInvalidEncryptedContent([]byte(tc.raw)))
		})
	}
}

func TestExcelBPSInvalidEncryptedRetryPreservesHistory(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","reasoning_effort":"xhigh","metadata":{"task_id":"task","turn_id":"turn","agent_iteration":"2"},"prompt_cache_key":"cache","context_management":[{"type":"compaction","compact_threshold":920000}],"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"keep task"}]},
		{"type":"reasoning","id":"rs_opaque","encrypted_content":"private-ciphertext","summary":[]},
		{"type":"function_call","call_id":"call_1","name":"run_officejs","arguments":"{\"value\":9007199254740993}"},
		{"type":"function_call_output","call_id":"call_1","output":"keep result"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"visible summary"}]},
		{"type":"message","role":"user","content":[{"type":"input_image","file_id":"attachment_1"}]},
		{"type":"compaction_trigger"}
	]}`)
	original := bytes.Clone(body)
	retry, changed := prepareExcelBPSInvalidEncryptedRetry(body, []byte(excelBPSInvalidCiphertext))
	require.True(t, changed)
	require.Equal(t, original, body, "do not mutate the canonical request")
	require.NotContains(t, string(retry), "private-ciphertext")
	require.NotContains(t, string(retry), "rs_opaque")
	require.Contains(t, string(retry), "9007199254740993")
	var before, after map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &before))
	require.NoError(t, json.Unmarshal(retry, &after))
	var input []json.RawMessage
	require.NoError(t, json.Unmarshal(before["input"], &input))
	input = append(input[:1], input[2:]...)
	expected, err := json.Marshal(input)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(after["input"]))
	delete(before, "input")
	delete(after, "input")
	require.Equal(t, before, after, "model and request identity must not change")
}

func TestExcelBPSInvalidEncryptedRetryNeverDropsRequiredContext(t *testing.T) {
	for _, item := range []string{
		`{"type":"compaction","encrypted_content":"only-copy-of-context"}`,
		`{"type":"compaction_summary","encrypted_content":"only-copy-of-context"}`,
		`{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"private-message"}]}`,
		`{"type":"function_call_output","output":[{"type":"encrypted_content","encrypted_content":"private-result"}]}`,
		`{"type":"function_call","encrypted_function_args":["message"],"arguments":"{}"}`,
	} {
		body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque"},` + item + `]}`)
		retry, changed := prepareExcelBPSInvalidEncryptedRetry(body, []byte(excelBPSInvalidCiphertext))
		require.False(t, changed)
		require.Equal(t, body, retry)
	}
	for _, body := range []string{
		`{"input":"hello"}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`,
		`{"input":[{"role":"developer","content":"injected protocol"},{"type":"reasoning","encrypted_content":"opaque"},{"type":"compaction_trigger"}]}`,
		`{"input":[{"role":"user","content":"hello"}]}`,
		`{`,
	} {
		retry, changed := prepareExcelBPSInvalidEncryptedRetry([]byte(body), []byte(excelBPSInvalidCiphertext))
		require.False(t, changed)
		require.Equal(t, body, string(retry))
	}
}

func excelBPSEncryptedHistoryRequest(stream bool) []byte {
	return []byte(fmt.Sprintf(`{"model":"gpt-5.4","stream":%t,"reasoning":{"effort":"max"},"prompt_cache_key":"encrypted-recovery-session","input":[{"role":"user","content":"continue original task"},{"type":"reasoning","encrypted_content":"private-ciphertext"},{"type":"function_call","call_id":"call_old","name":"inspect","arguments":"{}"},{"type":"function_call_output","call_id":"call_old","output":"original tool result"}]}`, stream))
}

func TestExcelBPSInvalidEncryptedContentRecoversSameRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, rejection := range []string{excelBPSInvalidCiphertext, excelBPSInvalidCiphertextNoCode} {
			t.Run(fmt.Sprintf("stream=%t/code=%t", stream, rejection == excelBPSInvalidCiphertext), func(t *testing.T) {
				first := &excelBPSRepairBody{Reader: strings.NewReader(rejection)}
				second := &excelBPSRepairBody{Reader: strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_recovered\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n")}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					{StatusCode: 400, Header: http.Header{"X-Request-Id": {"rejected_request"}}, Body: first},
					{StatusCode: 200, Header: http.Header{}, Body: second},
				}}
				checked := &excelBPSRepairUpstream{httpUpstreamRecorder: upstream, bodies: []*excelBPSRepairBody{first, second}}
				svc := openAIClientToolsTestService(upstream)
				svc.httpUpstream = checked
				account := excelAccount()
				account.Concurrency = 1
				account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.6-sol"}
				account.Proxy = &Proxy{Protocol: "http", Host: "127.0.0.1", Port: 7890}
				body := excelBPSEncryptedHistoryRequest(stream)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				result, err := svc.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.Equal(t, 200, rec.Code)
				require.Len(t, upstream.requests, 2)
				require.True(t, first.closed.Load())
				require.True(t, second.closed.Load())
				require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
				require.Equal(t, "gpt-5.4", result.Model)
				require.Equal(t, "gpt-5.6-sol", result.UpstreamModel)
				require.Equal(t, 10, result.Usage.InputTokens)
				require.True(t, account.Schedulable)
				require.Contains(t, string(upstream.bodies[0]), "private-ciphertext")
				require.NotContains(t, string(upstream.bodies[1]), "private-ciphertext")
				require.Contains(t, string(upstream.bodies[1]), "original tool result")
				for _, field := range []string{"model", "reasoning_effort", "metadata", "prompt_cache_key", "context_management"} {
					require.Equal(t, gjson.GetBytes(upstream.bodies[0], field).Raw, gjson.GetBytes(upstream.bodies[1], field).Raw, field)
				}
				for _, req := range upstream.requests {
					require.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
					require.Equal(t, "test-account", req.Header.Get("Chatgpt-Account-Id"))
					require.Equal(t, HTTPUpstreamProfileExcelBPS, HTTPUpstreamProfileFromContext(req.Context()))
				}
				events, exists := c.Get(OpsUpstreamErrorsKey)
				require.True(t, exists)
				attempts, ok := events.([]*OpsUpstreamErrorEvent)
				require.True(t, ok)
				require.Len(t, attempts, 1)
				require.Equal(t, "invalid_encrypted_content_retry", attempts[0].Kind)
				safe, err := json.Marshal(attempts)
				require.NoError(t, err)
				require.NotContains(t, string(safe), "private")
				require.Zero(t, c.GetInt(OpsUpstreamStatusCodeKey), "successful recovery must not retain a terminal 400")
			})
		}
	}
}

func TestExcelBPSInvalidEncryptedContentRetryIsBounded(t *testing.T) {
	for _, status := range []int{400, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(excelBPSInvalidCiphertext))},
				{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(excelBPSInvalidCiphertext))},
			}}
			svc := openAIClientToolsTestService(upstream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			account := excelAccount()
			_, err := svc.Forward(context.Background(), c, account, excelBPSEncryptedHistoryRequest(false))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, status, rec.Code)
			require.True(t, account.Schedulable)
			require.NotContains(t, rec.Body.String(), "private upstream diagnostic")
			if status == 400 {
				require.Equal(t, "invalid_encrypted_content", gjson.Get(rec.Body.String(), "error.code").String())
				require.Contains(t, rec.Body.String(), "original plaintext history")
			}
		})
	}
}

func TestExcelBPSInvalidEncryptedContentDoesNotRetryOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name, rejection, body string
		status                int
	}{
		{"ordinary 400", `{"error":{"code":"invalid_value"}}`, string(excelBPSEncryptedHistoryRequest(false)), 400},
		{"no removable reasoning", excelBPSInvalidCiphertext, `{"model":"gpt-5.6-sol","input":"continue"}`, 400},
		{"encrypted compaction", excelBPSInvalidCiphertext, `{"model":"gpt-5.6-sol","input":[{"role":"user","content":"continue"},{"type":"reasoning","encrypted_content":"opaque"},{"type":"compaction","encrypted_content":"context"}]}`, 400},
		{"non-400", excelBPSInvalidCiphertext, string(excelBPSEncryptedHistoryRequest(false)), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.rejection))}}
			svc := openAIClientToolsTestService(upstream)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			_, err := svc.Forward(context.Background(), c, excelAccount(), []byte(tc.body))
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, tc.status, rec.Code)
		})
	}
}

func TestExcelBPSInvalidEncryptedContentStopsOnDisconnect(t *testing.T) {
	for _, cancelBeforeRetry := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelBeforeRetry), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := &excelBPSRepairBody{Reader: strings.NewReader(excelBPSInvalidCiphertext)}
			calls := 0
			upstream := &bpsTestUpstream{send: func(req *http.Request, proxy string) (*http.Response, error) {
				calls++
				defer func() { _ = req.Body.Close() }()
				if calls == 1 {
					if cancelBeforeRetry {
						cancel()
					}
					return &http.Response{StatusCode: 400, Header: http.Header{}, Body: first}, nil
				}
				require.True(t, first.closed.Load(), "release the rejected request before retry")
				return nil, errors.New("private transport diagnostic")
			}}
			svc := openAIClientToolsTestService(&upstream.httpUpstreamRecorder)
			svc.httpUpstream = upstream
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			_, err := svc.Forward(ctx, c, excelAccount(), excelBPSEncryptedHistoryRequest(false))
			require.Error(t, err)
			require.True(t, first.closed.Load())
			if cancelBeforeRetry {
				require.Equal(t, 1, calls)
			} else {
				require.Equal(t, 2, calls)
				require.Equal(t, 502, rec.Code)
				require.Contains(t, rec.Body.String(), "basispoints_transport_error")
			}
			require.NotContains(t, rec.Body.String(), "private")
		})
	}
}
