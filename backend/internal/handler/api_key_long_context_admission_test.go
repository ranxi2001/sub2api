package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyLongContextFinalAdmissionResponse(t *testing.T) {
	for _, code := range []string{"context_length_exceeded", "context_unverifiable"} {
		t.Run(code, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				policyErr := &service.APIKeyLongContextError{Code: code, Limit: 200_000, Reason: "stored input item reference"}
				err := fmt.Errorf("final send: %w", policyErr)
				h := &OpenAIGatewayHandler{}
				h.handleTurnAdmissionError(c, err, stream)
				require.Contains(t, w.Body.String(), code)
				require.NotContains(t, w.Body.String(), "admission_unavailable")
				require.True(t, service.HasOpsClientBusinessLimited(c))
				if !stream {
					require.Contains(t, w.Body.String(), "invalid_request_error")
					require.Equal(t, http.StatusBadRequest, w.Code)
				} else {
					require.Contains(t, w.Body.String(), "event: response.failed")
				}
				phase, limited, owner, _ := classifyOpsErrorLog(c, "invalid_request_error", policyErr.Error(), code, http.StatusBadRequest)
				require.Equal(t, "auth", phase)
				require.True(t, limited)
				require.Equal(t, "client", owner)
			}
		})
	}
}

func TestAPIKeyLongContextAnthropicFinalAdmissionResponse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		policyErr := &service.APIKeyLongContextError{Code: "context_length_exceeded", Limit: 200_000}
		h := &OpenAIGatewayHandler{}
		require.True(t, h.handleAnthropicAPIKeyLongContextError(c, fmt.Errorf("final send: %w", policyErr), stream))
		require.Contains(t, w.Body.String(), `"type":"error"`)
		require.Contains(t, w.Body.String(), `"type":"invalid_request_error"`)
		require.Contains(t, w.Body.String(), policyErr.Error())
		require.True(t, service.HasOpsClientBusinessLimited(c))
		if !stream {
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), "context_length_exceeded")
		}
	}
}
