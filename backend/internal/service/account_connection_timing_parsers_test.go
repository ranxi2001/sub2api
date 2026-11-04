//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountConnectionTimingParsers(t *testing.T) {
	svc := &AccountTestService{}
	for _, protocol := range []struct {
		name, metadata, content, terminal string
		parse                             func(*gin.Context, io.Reader) error
	}{
		{"responses", `{"type":"response.created"}`, `{"type":"response.output_text.delta","delta":"hello"}`, `{"type":"response.completed"}`, svc.processOpenAIStream},
		{"chat", `{"choices":[{"delta":{"role":"assistant"}}]}`, `{"choices":[{"delta":{"content":"hello"}}]}`, `[DONE]`, func(c *gin.Context, body io.Reader) error {
			return svc.processOpenAIChatCompletionsStream(c, body, nil)
		}},
		{"claude", `{"type":"message_start"}`, `{"type":"content_block_delta","delta":{"text":"hello"}}`, `{"type":"message_stop"}`, svc.processClaudeStream},
		{"gemini", `{"modelVersion":"synthetic"}`, `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`, `[DONE]`, svc.processGeminiStream},
	} {
		for _, variant := range []string{"content", "metadata-only", "incomplete"} {
			t.Run(protocol.name+"/"+variant, func(t *testing.T) {
				c, rec := newTestContext()
				collector, _ := beginAccountConnectionTiming(c)
				req, trace := requesttiming.StartAttempt(c.Request, 1, 0)
				trace.Response(&http.Response{StatusCode: 200, Header: http.Header{}}, nil)
				c.Request = req
				stream := ": heartbeat\n\ndata: " + protocol.metadata + "\n\n"
				if variant == "content" {
					stream += "data: " + protocol.content + "\n\n"
				}
				if variant != "incomplete" {
					stream += "data: " + protocol.terminal + "\n\n"
				}
				err := protocol.parse(c, strings.NewReader(stream))
				if variant == "incomplete" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				svc.finishAccountConnectionTiming(c, collector, err)
				var timing *requesttiming.AccountTestTiming
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event TestEvent
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
					if event.Timing != nil {
						timing = event.Timing
					}
				}
				require.NotNil(t, timing)
				if variant == "content" {
					require.NotNil(t, timing.FirstContent.DurationMS)
					require.NotNil(t, timing.StreamOutput.DurationMS)
				} else {
					require.Nil(t, timing.FirstContent.DurationMS)
					require.Equal(t, "no_content", timing.FirstContent.Reason)
				}
			})
		}
	}
}
