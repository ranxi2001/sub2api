package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIssue999HTTPBridgeDeliveryControls(t *testing.T) {
	const message = `{"type":"message","id":"msg_fixture","role":"assistant","status":"completed","content":[{"type":"output_text","text":"fixture complete"}]}`
	done := `{"type":"response.output_item.done","output_index":0,"item":` + message + `}`
	completed := `{"type":"response.completed","response":{"id":"resp_fixture","status":"completed","output":[]}}`
	for _, tc := range []struct {
		name        string
		events      []string
		failWrite   string
		writeErr    error
		wantHistory bool
		wantError   bool
	}{
		{name: "completed", events: []string{done, completed}, wantHistory: true},
		{name: "full_terminal_only", events: []string{strings.Replace(completed, `"output":[]`, `"output":[`+message+`]`, 1)}, wantHistory: true},
		{name: "legacy_implicit_completed", events: []string{done, strings.Replace(completed, `"status":"completed",`, "", 1)}, wantHistory: true},
		{name: "failed", events: []string{done, `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","output":[]}}`}},
		{name: "incomplete", events: []string{done, `{"type":"response.incomplete","response":{"id":"resp_fixture","status":"incomplete","output":[]}}`}},
		{name: "completed_with_incomplete_status", events: []string{done, strings.Replace(completed, `"status":"completed"`, `"status":"incomplete"`, 1)}},
		{name: "cancelled", events: []string{done, strings.Replace(completed, `"status":"completed"`, `"status":"cancelled"`, 1)}},
		{name: "completed_with_error", events: []string{done, strings.Replace(completed, `"output":[]`, `"error":{"message":"fixture refusal"},"output":[]`, 1)}},
		{name: "eof_before_terminal", events: []string{done}, wantError: true},
		{name: "added_only", events: []string{strings.Replace(done, "output_item.done", "output_item.added", 1), completed}},
		{name: "unfinished_item", events: []string{strings.Replace(done, `"status":"completed"`, `"status":"in_progress"`, 1), completed}},
		{name: "failed_item", events: []string{strings.Replace(done, `"status":"completed"`, `"status":"failed"`, 1), completed}},
		{name: "invalid_index", events: []string{strings.Replace(done, `"output_index":0`, `"output_index":-1`, 1), completed}},
		{name: "fractional_index", events: []string{strings.Replace(done, `"output_index":0`, `"output_index":0.5`, 1), completed}},
		{name: "missing_index", events: []string{strings.Replace(done, `"output_index":0,`, "", 1), completed}},
		{name: "undelivered_item", events: []string{done, completed}, failWrite: "response.output_item.done", writeErr: io.EOF},
		{name: "undelivered_terminal", events: []string{done, completed}, failWrite: "response.completed", writeErr: io.EOF},
		{name: "item_write_error", events: []string{done, completed}, failWrite: "response.output_item.done", writeErr: errors.New("fixture write failed"), wantError: true},
		{name: "terminal_write_error", events: []string{done, completed}, failWrite: "response.completed", writeErr: errors.New("fixture write failed"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stream strings.Builder
			for _, event := range tc.events {
				fmt.Fprintf(&stream, "data: %s\n\n", event)
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream.String()))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 999, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-6.1-sol","store":false,"stream":true,"input":[{"role":"user","content":"fixture"}]}`)
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "fixture-token", payload, len(payload), "gpt-6.1-sol", "", "", "", "", 1, func(raw []byte) error {
				if gjson.GetBytes(raw, "type").String() == tc.failWrite {
					return tc.writeErr
				}
				return nil
			})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tc.wantHistory {
				require.NotNil(t, result)
				require.True(t, result.wsReplayInputExists)
				require.Len(t, result.wsReplayInput, 1)
				require.JSONEq(t, message, string(result.wsReplayInput[0]), "ISSUE999_ASSISTANT_HISTORY_LOST")
				require.Equal(t, result.wsReplayInput, result.wsAccountFailoverReplayInput)
			} else if result != nil {
				require.Empty(t, result.wsReplayInput, "unsuccessful or undelivered output cannot become history")
				require.Empty(t, result.wsAccountFailoverReplayInput, "failover cannot bypass delivery controls")
			}
		})
	}
}
