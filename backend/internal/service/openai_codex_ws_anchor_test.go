package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func anchorTestContext(key int64, session string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", session)
	group := int64(11)
	c.Set("api_key", &APIKey{ID: key, GroupID: &group, AllowLongContext: true})
	return c
}
func anchorTestService() (*OpenAIGatewayService, *Account) {
	cfg := &config.Config{}
	cfg.Gateway.CodexWSAnchor = config.CodexWSAnchorConfig{Enabled: true, AccountIDs: []int64{300}}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	s := &OpenAIGatewayService{cfg: cfg, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg)}
	a := &Account{ID: 300, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 10, Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}, Extra: map[string]any{"openai_oauth_responses_websockets_v2_enabled": true}}
	return s, a
}
func completeAnchorSeed(t *testing.T, s *OpenAIGatewayService, a *Account, c *gin.Context) {
	t.Helper()
	ctx, finish, err := s.prepareCodexWSAnchor(context.Background(), c, a, []byte(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	require.NotNil(t, finish)
	o := beginUpstreamResponseModelObservation(c)
	o.ObserveOpenAI([]byte(`{"type":"response.completed","response":{"id":"resp_seed","model":"gpt-6-astra","status":"completed","output":[{"content":[{"type":"output_text","text":"323"}]}]}}`), "response.completed")
	codexWSAnchorFromContext(ctx).qualified = true
	finish(&OpenAIForwardResult{ResponseID: "resp_seed", UpstreamResponseModel: "gpt-6-astra"}, nil)
}
func TestCodexWSAnchorIsolationAndDeadline(t *testing.T) {
	s, a := anchorTestService()
	c := anchorTestContext(1, "session-a")
	completeAnchorSeed(t, s, a, c)
	body := []byte(`{"model":"gpt-6-astra","previous_response_id":"resp_seed"}`)
	for _, other := range []*gin.Context{anchorTestContext(2, "session-a"), anchorTestContext(1, "session-b")} {
		_, _, err := s.prepareCodexWSAnchor(context.Background(), other, a, body)
		require.Error(t, err)
	}
	original := a.Credentials["access_token"]
	a.Credentials["access_token"] = "changed"
	_, _, err := s.prepareCodexWSAnchor(context.Background(), c, a, body)
	require.Error(t, err)
	a.Credentials["access_token"] = original
	ctx, finish, err := s.prepareCodexWSAnchor(context.Background(), c, a, body)
	require.NoError(t, err)
	turn := codexWSAnchorFromContext(ctx)
	require.Equal(t, "resp_seed", turn.previousID)
	_, _, err = s.prepareCodexWSAnchor(context.Background(), c, a, body)
	require.ErrorContains(t, err, "active request")
	var deadline time.Time
	for _, e := range s.codexWSAnchors.entries {
		deadline = e.expires
	}
	turn.connID = "conn_qualified"
	turn.qualified = true
	finish(&OpenAIForwardResult{ResponseID: "resp_next", UpstreamResponseModel: "gpt-6-astra"}, nil)
	for _, e := range s.codexWSAnchors.entries {
		require.Equal(t, deadline, e.expires)
		require.Equal(t, "conn_qualified", e.connID)
	}
	_, _, err = s.prepareCodexWSAnchor(context.Background(), c, a, body)
	require.Error(t, err)
	for k, e := range s.codexWSAnchors.entries {
		e.expires = time.Now().Add(-time.Second)
		s.codexWSAnchors.entries[k] = e
	}
	_, _, err = s.prepareCodexWSAnchor(context.Background(), c, a, []byte(`{"model":"gpt-6-astra","previous_response_id":"resp_next"}`))
	require.Error(t, err)
}
func TestCodexWSAnchorRejectsFailureAndHonorsSwitches(t *testing.T) {
	s, a := anchorTestService()
	c := anchorTestContext(1, "session-a")
	completeAnchorSeed(t, s, a, c)
	body := []byte(`{"model":"gpt-6-astra","previous_response_id":"resp_seed"}`)
	s.cfg.Gateway.OpenAIWS.ForceHTTP = true
	_, _, err := s.prepareCodexWSAnchor(context.Background(), c, a, body)
	require.ErrorContains(t, err, "WSv2")
	s.cfg.Gateway.OpenAIWS.ForceHTTP = false
	_, finish, err := s.prepareCodexWSAnchor(context.Background(), c, a, body)
	require.NoError(t, err)
	finish(nil, errors.New("upstream failure"))
	require.Empty(t, s.codexWSAnchors.entries)
	_, _, err = s.prepareCodexWSAnchor(context.Background(), anchorTestContext(1, ""), a, []byte(`{"model":"gpt-6-astra"}`))
	require.Error(t, err)
	s.cfg.Gateway.CodexWSAnchor.Enabled = false
	_, finish, err = s.prepareCodexWSAnchor(context.Background(), c, a, body)
	require.NoError(t, err)
	require.Nil(t, finish)
}
func TestCodexWSAnchorCookiePreservesIdentity(t *testing.T) {
	h := http.Header{"Cookie": []string{"__oailb=old; __cflb=target", "extra=keep; __oailb=duplicate"}, "Authorization": []string{"Bearer target"}, "X-Codex-Turn-State": []string{"target-state"}}
	replaceCodexWSAnchorCookie(h, "qualified")
	require.Equal(t, "__cflb=target; extra=keep; __oailb=qualified", h.Get("Cookie"))
	require.Equal(t, "Bearer target", h.Get("Authorization"))
	require.Equal(t, "target-state", h.Get("X-Codex-Turn-State"))
}

func TestCodexWSAnchorNativeSeedThenPinnedWS(t *testing.T) {
	s, a := anchorTestService()
	a.Status = StatusActive
	a.Schedulable = true
	a.GroupIDs = []int64{11}
	s.cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	s.cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	s.cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 2
	s.cache = &stubGatewayCache{}
	s.toolCorrector = NewCodexToolCorrector()
	completed := func(id string) string {
		return `{"type":"response.completed","response":{"id":"` + id + `","model":"gpt-6-astra","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"323"}]}],"usage":{"input_tokens":5,"output_tokens":2}}}`
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "Set-Cookie": []string{"__oailb=route-test; Path=/; Secure; Max-Age=3600"}}, Body: io.NopCloser(strings.NewReader("data: " + completed("resp_seed") + "\n\n"))}}
	s.httpUpstream = upstream
	conn := &openAIWSCaptureConn{events: [][]byte{[]byte(completed("resp_seed")), []byte(completed("resp_ws")), []byte(completed("resp_ws_next"))}}
	dialer := &openAIWSCaptureDialer{conn: conn}
	pool := newOpenAIWSConnPool(s.cfg)
	pool.setClientDialerForTest(dialer)
	s.openaiWSPool = pool
	t.Cleanup(pool.Close)
	c := anchorTestContext(1, "seed-session")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	result, err := s.Forward(t.Context(), c, a, []byte(`{"model":"gpt-6-astra","stream":false,"input":[{"role":"user","content":"17*19"}]}`))
	require.NoError(t, err)
	require.Equal(t, "resp_seed", result.ResponseID)
	require.True(t, result.OpenAIWSMode)
	for _, previous := range []string{"resp_seed", "resp_ws"} {
		c = anchorTestContext(1, "seed-session")
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		result, err = s.Forward(t.Context(), c, a, []byte(`{"model":"gpt-6-astra","stream":false,"previous_response_id":"`+previous+`","input":[{"role":"user","content":"again"}]}`))
		require.NoError(t, err)
		require.True(t, result.OpenAIWSMode)
	}
	require.Equal(t, 1, dialer.DialCount())
	require.Empty(t, dialer.lastHeaders.Get("Cookie"))
	require.Equal(t, "Bearer test-token", dialer.lastHeaders.Get("Authorization"))
	require.Equal(t, "resp_seed", conn.writes[1]["previous_response_id"])
	require.Equal(t, "resp_ws", conn.writes[2]["previous_response_id"])
	var bound codexWSAnchorEntry
	for _, e := range s.codexWSAnchors.entries {
		bound = e
	}
	require.NotEmpty(t, bound.connID)
	ap := pool.getOrCreateAccountPool(a.ID)
	ap.mu.Lock()
	require.NotZero(t, ap.conns[bound.connID].anchorUntilNano.Load())
	evicted := pool.cleanupAccountLocked(ap, time.Now(), 2)
	ap.mu.Unlock()
	require.Empty(t, evicted)
	pool.evictConn(a.ID, bound.connID)
	c = anchorTestContext(1, "seed-session")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	_, err = s.Forward(t.Context(), c, a, []byte(`{"model":"gpt-6-astra","stream":false,"previous_response_id":"resp_ws_next","input":[{"role":"user","content":"again"}]}`))
	require.Error(t, err)
	require.Equal(t, 1, dialer.DialCount(), "lost pinned connection must not redial or fall back")
	require.Empty(t, s.codexWSAnchors.entries)

}

func TestCodexWSAnchorPreservesProxyAcrossTurns(t *testing.T) {
	s, a := anchorTestService()
	c := anchorTestContext(1, "affinity")
	ctx, finish, err := s.prepareCodexWSAnchor(t.Context(), c, a, []byte(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	turn := codexWSAnchorFromContext(ctx)
	turn.proxyURL = "http://source.invalid:8080"
	turn.connID = "source-connection"
	turn.qualified = true
	finish(&OpenAIForwardResult{ResponseID: "resp_seed", UpstreamResponseModel: "gpt-6-astra"}, nil)
	ctx, finish, err = s.prepareCodexWSAnchor(t.Context(), c, a, []byte(`{"model":"gpt-6-astra","previous_response_id":"resp_seed"}`))
	require.NoError(t, err)
	turn = codexWSAnchorFromContext(ctx)
	require.Equal(t, "http://source.invalid:8080", turn.proxyURL)
	require.Equal(t, "source-connection", turn.connID)
	finish(nil, errors.New("test cleanup"))
}
