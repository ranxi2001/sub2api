package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsUpstreamSSETerminalAttemptScope(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	first := beginOpsUpstreamSSETerminal(c)
	first.observe([]byte(`{"type":"response.failed","response":{"error":{"type":"invalid_request_error"}}}`), "response.failed")
	require.False(t, HasOpsUpstreamSSETerminal(c), "a partial event is not a completed terminal")
	first.complete()
	require.True(t, HasOpsUpstreamSSETerminal(c))
	next := beginOpsUpstreamSSETerminal(c)
	require.False(t, HasOpsUpstreamSSETerminal(c), "a replacement reader must not inherit failure provenance")
	next.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`), "response.completed")
	next.complete()
	require.False(t, HasOpsUpstreamSSETerminal(c))
}

func TestOpsUpstreamSSETerminalRejectsMalformed(t *testing.T) {
	for _, payload := range []string{`{broken`, `{"response":{"error":"invalid_request_error"}}`, `{"message":"upstream invalid_request_error"}`} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		terminal := beginOpsUpstreamSSETerminal(c)
		terminal.observe([]byte(payload), "response.failed")
		terminal.complete()
		require.False(t, HasOpsUpstreamSSETerminal(c))
	}
}
