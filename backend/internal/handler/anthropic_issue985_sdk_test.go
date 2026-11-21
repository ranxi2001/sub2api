//go:build unit && issue985sdk

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The official Python SDK consumes the actual HTTP response with its own
// accumulator. The test fails if its pinned dependency is missing; no skips.
func TestIssue985SDKAccumulator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"text", "tool", "zero", "missing", "partial_error", "partial_protocol"} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", scenario, stream), func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					if scenario == "tool" {
						if n == 1 && gjson.GetBytes(body, "tools.0.input_schema.properties.city.type").String() != "string" {
							t.Error("SDK tool schema lost")
						}
						if n == 2 && (!strings.Contains(string(body), `"tool_use_id":"call_fixture"`) || !strings.Contains(string(body), "sunny")) {
							t.Error("SDK continuation lost")
						}
					}
					wire := compatStreamUsagePartialStream + compatStreamUsageCompletion
					switch scenario {
					case "tool":
						if n == 1 {
							wire = strings.ReplaceAll(wire, `{"type":"text","text":""}`, `{"type":"tool_use","id":"call_fixture","name":"weather","input":{}}`)
							wire = strings.ReplaceAll(wire, `{"type":"text_delta","text":"partial"}`, `{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}`)
							wire = strings.ReplaceAll(wire, `"end_turn"`, `"tool_use"`)
						}
					case "zero":
						wire = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(wire, `"input_tokens":12`, `"input_tokens":0`), `"output_tokens":1`, `"output_tokens":0`), `"output_tokens":7`, `"output_tokens":0`)
					case "missing":
						wire = strings.ReplaceAll(strings.ReplaceAll(wire, `,"usage":{"input_tokens":12,"output_tokens":1}`, ""), `,"usage":{"output_tokens":7}`, "")
					case "partial_error":
						wire = compatStreamUsagePartialStream + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"synthetic\"}}\n\n"
					case "partial_protocol":
						wire = compatStreamUsagePartialStream
					}
					wire = strings.ReplaceAll(wire, "\ndata:", "\nid: fixture\n: comment\ndata:")
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("X-Request-Id", fmt.Sprintf("fixture-sdk-%d", n))
					_, _ = io.WriteString(w, wire)
				}))
				defer upstream.Close()
				gateway, done := issue985Gateway(t, upstream.URL, &issue985Billing{})
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				cmd := exec.CommandContext(ctx, "python3", "../../../.github/scripts/anthropic_integrity_sdk.py", gateway.URL, scenario, strconv.FormatBool(stream))
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "SDK accumulator: %s", output)
				require.Contains(t, string(output), `"SDK_PASS": true`)
				want := int32(1)
				if scenario == "tool" {
					want = 2
				}
				require.Equal(t, want, calls.Load(), "SDK must not replay a submitted failure")
				for i := int32(0); i < want; i++ {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("handler lifecycle did not complete")
					}
				}
				t.Log(strings.TrimSpace(string(output)))
			})
		}
	}
}
