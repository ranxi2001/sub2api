package service

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTerminalReplayPreservesAuthoritativeOutput(t *testing.T) {
	const draft = `{"type":"message","id":"msg","content":[{"type":"output_text","text":"draft"}]}`
	const final = `{"type":"message","id":"msg","content":[{"type":"output_text","text":"final"}]}`
	const call = `{"type":"function_call","id":"fc","call_id":"call","name":"example","arguments":"{}"}`
	for _, tc := range []struct {
		name, status, output     string
		wantReplay, wantFailover []string
	}{
		{"completed", "completed", "[" + final + "," + call + "]", []string{final, call}, []string{draft, call}},
		{"empty", "completed", "[]", []string{draft}, []string{draft}},
		{"incomplete", "incomplete", "[" + final + "," + call + "]", []string{draft, call}, []string{draft, call}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := fmt.Sprintf("data: %s\n\ndata: %s\n\n",
				`{"type":"response.output_item.done","output_index":0,"item":`+draft+"}",
				`{"type":"response.completed","response":{"id":"resp_fixture","status":"`+tc.status+`","output":`+tc.output+"}}")
			cfg := &config.Config{}
			settings := &contentModerationTestSettingRepo{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}
			svc := &OpenAIGatewayService{
				cfg:            cfg,
				settingService: NewSettingService(settings, cfg),
				httpUpstream: &httpUpstreamStub{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(stream)),
				}},
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"fixture"}`)
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				t.Context(), c, account, "fixture", payload, len(payload), "gpt-5",
				"", "", "", "", 1, func([]byte) error { return nil },
			)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.wsReplayInputExists)
			require.Len(t, result.wsReplayInput, len(tc.wantReplay))
			for i, want := range tc.wantReplay {
				require.JSONEq(t, want, string(result.wsReplayInput[i]))
			}
			require.Len(t, result.wsAccountFailoverReplayInput, len(tc.wantFailover))
			for i, want := range tc.wantFailover {
				require.JSONEq(t, want, string(result.wsAccountFailoverReplayInput[i]))
			}
		})
	}
}

func BenchmarkOpenAIWSTerminalReplayOutput(b *testing.B) {
	for _, tc := range []struct {
		status string
		count  int
	}{
		{"completed", 0},
		{"completed", 1},
		{"completed", 32},
		{"completed", 256},
		{"incomplete", 32},
	} {
		b.Run(fmt.Sprintf("%s/%d", tc.status, tc.count), func(b *testing.B) {
			fields := make([]string, tc.count)
			for i := range fields {
				fields[i] = fmt.Sprintf(`{"type":"message","id":"msg_%d","role":"assistant","content":[{"type":"output_text","text":"fixture"}]}`, i)
			}
			event := []byte(`{"type":"response.completed","response":{"status":"` + tc.status + `","output":[` + strings.Join(fields, ",") + `]}}`)
			b.ReportAllocs()
			b.SetBytes(int64(len(event)))
			for b.Loop() {
				var collector openAIWSToolCallReplayCollector
				collector.AddEvent("response.completed", event)
				if len(collector.allItems) != tc.count {
					b.Fatalf("got %d replay items, want %d", len(collector.allItems), tc.count)
				}
			}
		})
	}
}
