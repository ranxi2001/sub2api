//go:build unit

package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const deepSeekSSETestPrefix = `data: {"choices":[{"delta":{},"finish_reason":null,"index":0}],"created":1795245068,"id":"chatcmpl-synthetic","model":"deepseek-flash","object":"chat.completion.chunk"}`
const deepSeekSSETestStart = `data: {"id":"real-id","object":"chat.completion.chunk","created":1795245067,"model":"deepseek-flash","choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}],"usage":null}`
const deepSeekSSETestEnd = `data: {"id":"real-id","object":"chat.completion.chunk","model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10}}` + "\n\ndata: [DONE]\n\n"

func deepSeekSSETestAccount() *Account {
	a := rawChatCompletionsTestAccount()
	a.Platform = PlatformDeepseek
	a.Credentials["base_url"] = "https://deepseek-proxy.example.com"
	return a
}

func deepSeekSSEReadLines(r io.Reader, a *Account) (string, error) {
	s := newDeepSeekChatSSEPrefixScanner(bufio.NewScanner(r), a, "deepseek-flash")
	var out strings.Builder
	for s.Scan() {
		out.WriteString(s.Text())
		out.WriteByte('\n')
	}
	return out.String(), s.Err()
}

func TestDeepSeekChatSSEPrefixScope(t *testing.T) {
	broken := deepSeekSSETestPrefix + "\n" + deepSeekSSETestStart + "\n\n" + deepSeekSSETestEnd
	for _, tc := range []struct {
		name, platform, accountType, base string
		repair                            bool
	}{
		{"deepseek proxy", PlatformDeepseek, AccountTypeAPIKey, "https://deepseek-proxy.example.com", true},
		{"version path", PlatformDeepseek, AccountTypeAPIKey, "https://deepseek-proxy.example.com/v1", true},
		{"custom port", PlatformDeepseek, AccountTypeAPIKey, "https://deepseek-proxy.example.com:8443", true},
		{"another upstream", PlatformDeepseek, AccountTypeAPIKey, "https://another-upstream.example.com/v1", true},
		{"official", PlatformDeepseek, AccountTypeAPIKey, "https://api.deepseek.com", true},
		{"other platform", PlatformOpenAI, AccountTypeAPIKey, "https://deepseek-proxy.example.com", false},
		{"other type", PlatformDeepseek, AccountTypeUpstream, "https://deepseek-proxy.example.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := deepSeekSSETestAccount()
			a.Platform, a.Type, a.Credentials["base_url"] = tc.platform, tc.accountType, tc.base
			got, err := deepSeekSSEReadLines(strings.NewReader(broken), a)
			require.NoError(t, err)
			want := broken
			if tc.repair {
				want = deepSeekSSETestStart + "\n\n" + deepSeekSSETestEnd
			}
			require.Equal(t, want, got)
		})
	}
	got, err := deepSeekSSEReadLines(strings.NewReader(broken), nil)
	require.NoError(t, err)
	require.Equal(t, broken, got)
}

func TestDeepSeekChatSSEPrefixPreservesOtherEvents(t *testing.T) {
	for name, wire := range map[string]string{
		"already separated":      deepSeekSSETestPrefix + "\n\n" + deepSeekSSETestStart + "\n\n",
		"normal role start":      deepSeekSSETestStart + "\n\n" + deepSeekSSETestEnd,
		"prefix later in stream": deepSeekSSETestStart + "\n\n" + deepSeekSSETestPrefix + "\n" + deepSeekSSETestStart + "\n\n",
		"leading comment":        ": keepalive\n\n" + deepSeekSSETestPrefix + "\n" + deepSeekSSETestStart + "\n\n",
		"real content":           strings.Replace(deepSeekSSETestPrefix, `"delta":{}`, `"delta":{"content":"important"}`, 1) + "\n" + deepSeekSSETestStart + "\n\n",
		"same id":                deepSeekSSETestPrefix + "\n" + strings.Replace(deepSeekSSETestStart, "real-id", "chatcmpl-synthetic", 1) + "\n\n",
		"different model":        deepSeekSSETestPrefix + "\n" + strings.Replace(deepSeekSSETestStart, "deepseek-flash", "deepseek-v4-pro", 1) + "\n\n",
		"unknown metadata":       strings.Replace(deepSeekSSETestPrefix, `"created":`, `"vendor_info":1,"created":`, 1) + "\n" + deepSeekSSETestStart + "\n\n",
		"tool call":              deepSeekSSETestPrefix + "\n" + strings.Replace(deepSeekSSETestStart, `"content":""`, `"content":"","tool_calls":[{"index":0}]`, 1) + "\n\n",
		"error":                  deepSeekSSETestPrefix + "\nevent: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n",
		"done":                   deepSeekSSETestPrefix + "\ndata: [DONE]\n\n",
		"invalid json":           deepSeekSSETestPrefix + "\ndata: {invalid}\n\n",
		"valid multiline json":   "data: {\"choices\":\ndata: [],\"usage\":{\"prompt_tokens\":9}}\n\n",
		"eof after prefix":       deepSeekSSETestPrefix + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := deepSeekSSEReadLines(strings.NewReader(wire), deepSeekSSETestAccount())
			require.NoError(t, err)
			require.Equal(t, wire, got)
		})
	}
}

func TestDeepSeekChatSSEPrefixPreservesNonDeepSeekModel(t *testing.T) {
	wire := strings.ReplaceAll(deepSeekSSETestPrefix+"\n"+deepSeekSSETestStart+"\n\n", "deepseek-flash", "other-model")
	s := newDeepSeekChatSSEPrefixScanner(bufio.NewScanner(strings.NewReader(wire)), deepSeekSSETestAccount(), "other-model")
	var got strings.Builder
	for s.Scan() {
		got.WriteString(s.Text())
		got.WriteByte('\n')
	}
	require.NoError(t, s.Err())
	require.Equal(t, wire, got.String())
}

type deepSeekSSEErrorReader struct{ err error }

func (r deepSeekSSEErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestDeepSeekChatSSEPrefixPreservesReadError(t *testing.T) {
	for _, err := range []error{io.ErrUnexpectedEOF, context.Canceled} {
		r := io.MultiReader(strings.NewReader(deepSeekSSETestPrefix+"\n"), deepSeekSSEErrorReader{err})
		got, gotErr := deepSeekSSEReadLines(r, deepSeekSSETestAccount())
		require.Equal(t, deepSeekSSETestPrefix+"\n", got)
		require.ErrorIs(t, gotErr, err)
	}
	_, err := deepSeekSSEReadLines(strings.NewReader(deepSeekSSETestPrefix+"\n"+strings.Repeat("x", 70000)), deepSeekSSETestAccount())
	require.True(t, errors.Is(err, bufio.ErrTooLong))
}

func TestDeepSeekChatSSERawStreamEventJSONAndUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"deepseek-flash", "deepseek-v4-pro"} {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(model+"/"+strings.ReplaceAll(newline, "\n", "LF"), func(t *testing.T) {
				body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"Reply OK"}],"stream":true}`)
				wire := deepSeekSSETestPrefix + "\n" + deepSeekSSETestStart + "\n\n" + deepSeekSSETestEnd
				wire = strings.ReplaceAll(strings.ReplaceAll(wire, "deepseek-flash", model), "\n", newline)
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				result, err := svc.forwardAsRawChatCompletions(context.Background(), c, deepSeekSSETestAccount(), body, "")
				require.NoError(t, err)
				require.Equal(t, 9, result.Usage.InputTokens)
				require.Equal(t, 1, result.Usage.OutputTokens)
				require.Equal(t, strings.ReplaceAll(deepSeekSSETestStart+"\n\n"+deepSeekSSETestEnd, "deepseek-flash", model), w.Body.String())
				// Validate per SSE event, not per data line: the production failure
				// passes line-based JSON tests but fails this client contract.
				for _, event := range strings.Split(strings.TrimSpace(w.Body.String()), "\n\n") {
					var data []string
					for _, line := range strings.Split(event, "\n") {
						if strings.HasPrefix(line, "data:") {
							data = append(data, strings.TrimSpace(line[5:]))
						}
					}
					payload := strings.Join(data, "\n")
					if payload != "[DONE]" {
						require.True(t, json.Valid([]byte(payload)), payload)
					}
				}
			})
		}
	}
}
