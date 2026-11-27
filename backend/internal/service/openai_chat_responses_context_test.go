//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestChatResponsesContextRejection(t *testing.T) {
	const rejection = `{"message":"Your input exceeds the context window of this model. Please adjust your input and try again."}`
	for _, tc := range []struct {
		name              string
		status            int
		stream, committed bool
		wire              string
	}{
		{"http", 502, true, false, `{"error":` + rejection + `}`},
		{"canonical", 502, true, false, `{"error":{"code":"context_length_exceeded"}}`},
		{"buffered terminal", 200, false, false, "data: " + `{"type":"response.failed","response":{"status":"failed","error":` + rejection + `}}` + "\n\n"},
		{"first terminal", 200, true, false, "data: " + `{"type":"response.failed","response":{"status":"failed","error":` + rejection + `}}` + "\n\n"},
		{"committed", 200, true, true, "data: " + `{"type":"error","error":` + rejection + `}` + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stream":%t}`, tc.stream))
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tc.committed {
				c.Header("Content-Type", "text/event-stream")
				c.Writer.Flush()
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.wire))}}}
			svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, structuredInputRetryTestAccount(), body, "", "")
			if tc.status == http.StatusOK {
				require.NotNil(t, result)
				require.True(t, result.SLAExcluded)
			}
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "/v1/responses", upstream.requests[0].URL.Path)
			require.ErrorIs(t, err, errGatewayContextRejection)
			require.Equal(t, tc.status, c.GetInt(OpsUpstreamStatusCodeKey))
			require.True(t, IsOpsRequestScopedError(c))
			if tc.committed {
				require.Equal(t, 200, rec.Code)
				require.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
				require.Contains(t, rec.Body.String(), "data: [DONE]")
				marker, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.True(t, marker.RequestScoped)
				require.Equal(t, http.StatusBadRequest, marker.IntendedStatus)
			} else {
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Equal(t, "context_length_exceeded", gjson.Get(rec.Body.String(), "error.code").String())
			}
		})
	}
}
