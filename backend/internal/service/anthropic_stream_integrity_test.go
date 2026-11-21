//go:build unit

package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func issue985Frame(kind, data string) string { return "event: " + kind + "\ndata: " + data + "\n\n" }

func issue985Stream() string {
	return issue985Frame("message_start", `{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":"fixture-model","content":[],"usage":{"input_tokens":17,"output_tokens":0}}}`) +
		issue985Frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		issue985Frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"fixture text"}}`) +
		issue985Frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		issue985Frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`) +
		issue985Frame("message_stop", `{"type":"message_stop"}`)
}

// The real production readers and converters run over a real loopback HTTP
// response. These assertions must be identical for fixed-reader RED and GREEN.
func issue985Read(t *testing.T, route, wire string) (*ForwardResult, error, string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, wire)
	}))
	defer upstream.Close()
	resp, err := upstream.Client().Get(upstream.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+route, nil)
	svc := &GatewayService{}
	var result *ForwardResult
	if route == "chat" {
		result, err = svc.handleCCStreamingFromAnthropic(resp, c, "client-model", "fixture-model", nil, time.Now())
	} else {
		result, err = svc.handleResponsesStreamingResponse(resp, c, "client-model", "fixture-model", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	}
	return result, err, rec.Body.String()
}

func TestIssue985Framing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	canonical := issue985Stream()
	for name, wire := range map[string]string{
		"canonical": canonical,
		"id":        strings.ReplaceAll(canonical, "\ndata:", "\nid: fixture\ndata:"),
		"comment":   strings.ReplaceAll(canonical, "\ndata:", "\n: fixture\ndata:"),
		"multidata": strings.ReplaceAll(canonical, `,"`, ",\ndata: \""),
	} {
		for _, route := range []string{"chat", "responses"} {
			t.Run(route+"/"+name, func(t *testing.T) {
				result, err, body := issue985Read(t, route, wire)
				require.NoError(t, err, "legal SSE must succeed")
				require.NotNil(t, result)
				require.Equal(t, 17, result.Usage.InputTokens, "legal SSE must preserve input 17")
				require.Equal(t, 3, result.Usage.OutputTokens, "legal SSE must preserve output 3")
				require.Contains(t, body, "fixture text")
			})
		}
	}
}

func TestIssue985Failure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	canonical := issue985Stream()
	stop := issue985Frame("message_stop", `{"type":"message_stop"}`)
	for name, wire := range map[string]string{
		"empty":             "",
		"terminal_only":     stop,
		"missing_usage":     strings.ReplaceAll(strings.ReplaceAll(canonical, `,"usage":{"input_tokens":17,"output_tokens":0}`, ""), `,"usage":{"output_tokens":3}`, ""),
		"stop_reason_error": strings.TrimSuffix(canonical, stop) + issue985Frame("error", `{"type":"error","error":{"type":"overloaded_error","message":"synthetic"}}`),
	} {
		for _, route := range []string{"chat", "responses"} {
			t.Run(route+"/"+name, func(t *testing.T) {
				_, err, body := issue985Read(t, route, wire)
				require.Error(t, err, "incomplete or errored stream must fail")
				require.NotContains(t, body, "[DONE]", "failure must not emit success")
				require.NotContains(t, body, "response.completed", "failure must not emit success")
				require.NotContains(t, body, `"finish_reason":"stop"`, "failure must not emit success")
			})
		}
	}
}
