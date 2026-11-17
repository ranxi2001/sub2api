package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIssue907WSExhaustionPreservesModelError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r
		closeOpenAIWSFailoverExhausted(c, conn, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadRequest, Reason: "openai_ws_model_unavailable",
			ResponseBody: []byte(`{"error":{"code":"model_not_found","message":"private fixture upstream detail"}}`),
		})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	_, payload, err := client.Read(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 400, gjson.GetBytes(payload, "status").Int())
	require.Equal(t, "model_not_found", gjson.GetBytes(payload, "error.code").String())
	require.Contains(t, gjson.GetBytes(payload, "error.message").String(), "reconnect with complete context")
	require.NotContains(t, string(payload), "private fixture")
	_, _, err = client.Read(ctx)
	require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
}
