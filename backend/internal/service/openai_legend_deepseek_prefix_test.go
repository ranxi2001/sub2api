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

const legendTestPrefix = `data: {"choices":[{"delta":{},"finish_reason":null,"index":0}],"created":1795245068,"id":"chatcmpl-synthetic","model":"deepseek-flash","object":"chat.completion.chunk"}`
const legendTestStart = `data: {"id":"real-id","object":"chat.completion.chunk","created":1795245067,"model":"deepseek-flash","choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}],"usage":null}`
const legendTestEnd = `data: {"id":"real-id","object":"chat.completion.chunk","model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10}}` + "\n\ndata: [DONE]\n\n"

func legendTestAccount() *Account {
	a := rawChatCompletionsTestAccount()
	a.Platform = PlatformDeepseek
	a.Credentials["base_url"] = "https://svip.aicmp.cc"
	return a
}

func legendReadLines(r io.Reader, a *Account) (string, error) {
	s := newLegendDeepSeekPrefixScanner(bufio.NewScanner(r), a, "deepseek-flash")
	var out strings.Builder
	for s.Scan() {
		out.WriteString(s.Text())
		out.WriteByte('\n')
	}
	return out.String(), s.Err()
}

func TestLegendDeepSeekPrefixScope(t *testing.T) {
	broken := legendTestPrefix + "\n" + legendTestStart + "\n\n" + legendTestEnd
	for _, tc := range []struct {
		name, platform, accountType, base string
		repair                            bool
	}{
		{"legend deepseek", PlatformDeepseek, AccountTypeAPIKey, "https://svip.aicmp.cc", true},
		{"version path", PlatformDeepseek, AccountTypeAPIKey, "https://svip.aicmp.cc/v1", true},
		{"https port", PlatformDeepseek, AccountTypeAPIKey, "https://SVIP.AICMP.CC:443", true},
		{"qwen token", PlatformDeepseek, AccountTypeAPIKey, "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1", false},
		{"official", PlatformDeepseek, AccountTypeAPIKey, "https://api.deepseek.com", false},
		{"legend gpt", PlatformOpenAI, AccountTypeAPIKey, "https://svip.aicmp.cc", false},
		{"other type", PlatformDeepseek, AccountTypeUpstream, "https://svip.aicmp.cc", false},
		{"hostname suffix", PlatformDeepseek, AccountTypeAPIKey, "https://svip.aicmp.cc.example.com", false},
		{"hostname in path", PlatformDeepseek, AccountTypeAPIKey, "https://example.com/svip.aicmp.cc", false},
		{"other port", PlatformDeepseek, AccountTypeAPIKey, "https://svip.aicmp.cc:8443", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := legendTestAccount()
			a.Platform, a.Type, a.Credentials["base_url"] = tc.platform, tc.accountType, tc.base
			got, err := legendReadLines(strings.NewReader(broken), a)
			require.NoError(t, err)
			want := broken
			if tc.repair {
				want = legendTestStart + "\n\n" + legendTestEnd
			}
			require.Equal(t, want, got)
		})
	}
	got, err := legendReadLines(strings.NewReader(broken), nil)
	require.NoError(t, err)
	require.Equal(t, broken, got)
}

func TestLegendDeepSeekPrefixPreservesOtherEvents(t *testing.T) {
	for name, wire := range map[string]string{
		"already separated":      legendTestPrefix + "\n\n" + legendTestStart + "\n\n",
		"normal role start":      legendTestStart + "\n\n" + legendTestEnd,
		"prefix later in stream": legendTestStart + "\n\n" + legendTestPrefix + "\n" + legendTestStart + "\n\n",
		"leading comment":        ": keepalive\n\n" + legendTestPrefix + "\n" + legendTestStart + "\n\n",
		"real content":           strings.Replace(legendTestPrefix, `"delta":{}`, `"delta":{"content":"important"}`, 1) + "\n" + legendTestStart + "\n\n",
		"same id":                legendTestPrefix + "\n" + strings.Replace(legendTestStart, "real-id", "chatcmpl-synthetic", 1) + "\n\n",
		"different model":        legendTestPrefix + "\n" + strings.Replace(legendTestStart, "deepseek-flash", "deepseek-v4-pro", 1) + "\n\n",
		"unknown metadata":       strings.Replace(legendTestPrefix, `"created":`, `"vendor_info":1,"created":`, 1) + "\n" + legendTestStart + "\n\n",
		"tool call":              legendTestPrefix + "\n" + strings.Replace(legendTestStart, `"content":""`, `"content":"","tool_calls":[{"index":0}]`, 1) + "\n\n",
		"error":                  legendTestPrefix + "\nevent: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n",
		"done":                   legendTestPrefix + "\ndata: [DONE]\n\n",
		"invalid json":           legendTestPrefix + "\ndata: {invalid}\n\n",
		"valid multiline json":   "data: {\"choices\":\ndata: [],\"usage\":{\"prompt_tokens\":9}}\n\n",
		"eof after prefix":       legendTestPrefix + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := legendReadLines(strings.NewReader(wire), legendTestAccount())
			require.NoError(t, err)
			require.Equal(t, wire, got)
		})
	}
}

type legendErrorReader struct{ err error }

func (r legendErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestLegendDeepSeekPrefixPreservesReadError(t *testing.T) {
	for _, err := range []error{io.ErrUnexpectedEOF, context.Canceled} {
		r := io.MultiReader(strings.NewReader(legendTestPrefix+"\n"), legendErrorReader{err})
		got, gotErr := legendReadLines(r, legendTestAccount())
		require.Equal(t, legendTestPrefix+"\n", got)
		require.ErrorIs(t, gotErr, err)
	}
	_, err := legendReadLines(strings.NewReader(legendTestPrefix+"\n"+strings.Repeat("x", 70000)), legendTestAccount())
	require.True(t, errors.Is(err, bufio.ErrTooLong))
}

func TestLegendDeepSeekRawStreamEventJSONAndUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"deepseek-flash", "deepseek-v4-pro"} {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(model+"/"+strings.ReplaceAll(newline, "\n", "LF"), func(t *testing.T) {
				body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"Reply OK"}],"stream":true}`)
				wire := legendTestPrefix + "\n" + legendTestStart + "\n\n" + legendTestEnd
				wire = strings.ReplaceAll(strings.ReplaceAll(wire, "deepseek-flash", model), "\n", newline)
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				result, err := svc.forwardAsRawChatCompletions(context.Background(), c, legendTestAccount(), body, "")
				require.NoError(t, err)
				require.Equal(t, 9, result.Usage.InputTokens)
				require.Equal(t, 1, result.Usage.OutputTokens)
				require.Equal(t, strings.ReplaceAll(legendTestStart+"\n\n"+legendTestEnd, "deepseek-flash", model), w.Body.String())
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
