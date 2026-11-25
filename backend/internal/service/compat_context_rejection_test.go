//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompatWrappedContextErrorStopsBeforeFailoverAndPolicy(t *testing.T) {
	for _, status := range []int{400, 422, 502} {
		writer := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(writer)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		body := []byte(`{"error":{"type":"server_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`)
		resp := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body)))}
		svc := &OpenAIGatewayService{}
		require.Nil(t, svc.failoverOpenAIUpstreamHTTPError(context.Background(), c, account, resp, body, "too long", "synthetic-model"))
		// Even an explicit passthrough rule cannot turn this into an outage.
		bindPassthroughRule(c, PlatformOpenAI, []string{"context_length_exceeded"}, 503)
		_, err := svc.handleCompatErrorResponse(resp, c, account, writeChatCompletionsError)
		require.ErrorIs(t, err, errGatewayContextRejection)
		require.Equal(t, 400, writer.Code)
		require.Equal(t, "context_length_exceeded", gjson.Get(writer.Body.String(), "error.code").String())
		require.Equal(t, "invalid_request_error", gjson.Get(writer.Body.String(), "error.type").String())
		require.True(t, IsOpsRequestScopedError(c))
	}
}
