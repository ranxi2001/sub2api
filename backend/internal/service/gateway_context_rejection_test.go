package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicContextRejectionStopsBeforeAccountPolicies(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{400, 422, 502} {
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			if stream {
				c.Header("Content-Type", "text/event-stream")
				c.Writer.WriteHeaderNow()
			}
			message := `{"error":{"code":"context_length_exceeded","message":"prompt is too long"}}`
			resp := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(message))}
			svc := &GatewayService{}
			require.True(t, svc.interceptAnthropicContextRejection(c, &Account{Platform: PlatformAnthropic}, resp))
			require.True(t, IsOpsRequestScopedError(c))
			if stream {
				require.Equal(t, 200, writer.Code)
				marker, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.True(t, marker.RequestScoped)
				require.Equal(t, 400, marker.IntendedStatus)
			} else {
				require.Equal(t, 400, writer.Code)
				require.Equal(t, "context_length_exceeded", gjson.Get(writer.Body.String(), "error.code").String())
			}
		}
	}
}

func TestAnthropicContextInspectionPreservesOtherResponses(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"temporary outage"}}`, strings.Repeat("x", 70000)} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		resp := &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader(body))}
		require.False(t, (&GatewayService{}).interceptAnthropicContextRejection(c, &Account{Platform: PlatformAnthropic}, resp))
		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(got))
		require.NoError(t, resp.Body.Close())
	}
}
