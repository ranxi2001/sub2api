package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestProxyForwardUnavailableDoesNotSendOutsideSelectedPool(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n"))}}
	svc := openAIClientToolsTestService(upstream)
	account := excelAccount()
	account.Extra["openai_excel_bps"] = false
	account.Extra["openai_proxy_mode"] = "request"
	account.Extra["openai_proxy_ids"] = []int64{987654321}
	account.Proxy = &Proxy{Protocol: "http", Host: "unused.example", Port: 8080}
	body := []byte(`{"model":"gpt-5.4","input":"test","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	_, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "openai_proxy_unavailable")
	require.Nil(t, upstream.lastReq, "selected pool failure must not send directly or through the old account proxy")
}

func TestOpenAIRequestProxyCompatibilityUnavailableNeverSends(t *testing.T) {
	for _, entry := range []string{"chat", "messages", "input_tokens", "count_tokens"} {
		t.Run(entry, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc := openAIClientToolsTestService(upstream)
			account := excelAccount()
			account.Extra["openai_excel_bps"] = false
			account.Extra["openai_proxy_mode"] = "request"
			account.Extra["openai_proxy_ids"] = []int64{987654321}
			body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"test"}],"input":"test","max_tokens":32}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/"+entry, bytes.NewReader(body))
			var err error
			switch entry {
			case "chat":
				_, err = svc.ForwardAsChatCompletions(t.Context(), c, account, body, "", "")
			case "messages":
				_, err = svc.ForwardAsAnthropic(t.Context(), c, account, body, "", "")
			case "input_tokens":
				err = svc.ForwardResponsesInputTokens(t.Context(), c, account, body)
			case "count_tokens":
				err = svc.ForwardCountTokensAsAnthropic(t.Context(), c, account, body, "")
			}
			require.Error(t, err)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			require.Contains(t, rec.Body.String(), "openai_proxy_unavailable")
			require.Empty(t, upstream.requests)
		})
	}
}

func TestOpenAIRequestProxyWebSocketExplicitlyRejectsBeforeUpstream(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	svc := openAIClientToolsTestService(upstream)
	account := excelAccount()
	account.Extra["openai_excel_bps"] = false
	account.Extra["openai_proxy_mode"] = "request"
	account.Extra["openai_proxy_ids"] = []int64{1, 2}
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r
		serverErr <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "test-token", []byte(`{"type":"response.create","model":"gpt-5.4","input":"test"}`), nil)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	select {
	case err := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Contains(t, err.Error(), "requires HTTP/SSE")
	case <-ctx.Done():
		t.Fatal("WebSocket request proxy rejection did not finish")
	}
	require.Empty(t, upstream.requests)
}

func TestOpenAIRequestProxyExplicitSingleKeepsExistingProxy(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"single\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))}}
	svc := openAIClientToolsTestService(upstream)
	account := excelAccount()
	account.Extra["openai_excel_bps"] = false
	account.Extra["openai_proxy_mode"] = "single"
	account.Extra["openai_proxy_ids"] = []int64{987654321}
	account.Extra["openai_excel_bps_proxy_mode"] = "request"
	account.Extra["openai_excel_bps_proxy_ids"] = []int64{987654321}
	proxyID := int64(77)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{Protocol: "http", Host: "single.example", Port: 8080}
	body := []byte(`{"model":"gpt-5.4","input":"test","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	_, err := svc.Forward(t.Context(), c, account, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
}
