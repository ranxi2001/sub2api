package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Uses the real handler, scheduler and forwarding transport. Both providers are
// loopback fixtures; counting actual received frames distinguishes admission
// from a configuration-only assertion.
func TestModerationUpstreamScopeUnselectedDoesNotAudit(t *testing.T) {
	var audits, generationWrites atomic.Int32
	audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		audits.Add(1)
		_, _ = w.Write([]byte(`{"results":[{"category_scores":{"sexual":0.99}}]}`))
	}))
	defer audit.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		generationWrites.Add(1)
		_ = conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_scope","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`))
		_, _, _ = conn.Read(ctx)
	}))
	defer upstream.Close()
	h := newOpenAIWSPassthroughHandlerHarness(t, upstream.URL, map[string]string{
		service.SettingKeyContentModerationConfig: fmt.Sprintf(`{"enabled":true,"mode":"pre_block","all_groups":true,"sample_rate":100,"base_url":%q,"api_keys":["fixture-key"],"upstream_account_scope":"selected","upstream_account_ids":[9952]}`, audit.URL),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic fixture text"}`)))
	_, event, err := h.clientConn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(0), audits.Load(), "unselected upstream account must make zero audit calls")
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	require.Equal(t, int32(1), generationWrites.Load())
}
