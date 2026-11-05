package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyLongContextFinalSendHandlesInternalProbeRequest(t *testing.T) {
	s := &OpenAIGatewayService{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = (&http.Request{Header: make(http.Header)}).WithContext(context.Background())
	body := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_stored","input":"small"}`)
	// The real background probe uses a header-only request without a customer key.
	require.NoError(t, s.checkAPIKeyLongContextBeforeSend(context.Background(), c, body, "gpt-5.4"))
	c.Set("api_key", &APIKey{AllowLongContext: true})
	require.NoError(t, s.checkAPIKeyLongContextBeforeSend(context.Background(), c, body, "gpt-5.4"))
}

func TestAPIKeyLongContextFinalSendMissingURLPreservesRestriction(t *testing.T) {
	s := &OpenAIGatewayService{}
	for _, transport := range []OpenAIClientTransport{OpenAIClientTransportHTTP, OpenAIClientTransportWS} {
		t.Run(string(transport), func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = (&http.Request{Header: make(http.Header)}).WithContext(context.Background())
			c.Set("api_key", &APIKey{AllowLongContext: false})
			SetOpenAIClientTransport(c, transport)
			require.NoError(t, s.checkAPIKeyLongContextBeforeSend(context.Background(), c, []byte(`{"model":"gpt-5.4","input":"small"}`), "gpt-5.4"))
			err := s.checkAPIKeyLongContextBeforeSend(context.Background(), c, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_stored","input":"small"}`), "gpt-5.4")
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, err, &policyErr)
			require.Equal(t, "context_unverifiable", policyErr.Code)
			if transport == OpenAIClientTransportWS {
				var closeErr *OpenAIWSClientCloseError
				require.ErrorAs(t, err, &closeErr)
				require.Equal(t, coderws.StatusPolicyViolation, closeErr.statusCode)
			} else {
				require.True(t, IsOpenAITurnAdmissionError(err))
			}
		})
	}
}

func TestAPIKeyLongContextFinalSendChecksExpandedPayload(t *testing.T) {
	s := &OpenAIGatewayService{}
	for _, transport := range []OpenAIClientTransport{OpenAIClientTransportHTTP, OpenAIClientTransportWS} {
		t.Run(string(transport), func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			c.Set("api_key", &APIKey{AllowLongContext: false})
			SetOpenAIClientTransport(c, transport)
			// Ingress may have inferred this reference after the raw-frame check.
			err := s.checkAPIKeyLongContextBeforeSend(context.Background(), c, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_inferred","input":"small"}`), "gpt-5.4")
			require.Error(t, err)
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, err, &policyErr)
			require.Equal(t, "context_unverifiable", policyErr.Code)
			if transport == OpenAIClientTransportWS {
				var closeErr *OpenAIWSClientCloseError
				require.True(t, errors.As(err, &closeErr))
				require.Equal(t, coderws.StatusPolicyViolation, closeErr.statusCode)
			} else {
				require.True(t, IsOpenAITurnAdmissionError(err))
			}
		})
	}
	for _, path := range []string{"/v1/images/generations", "/v1/embeddings", "/v1/audio/speech"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", path, nil)
		c.Set("api_key", &APIKey{AllowLongContext: false})
		require.NoError(t, s.checkAPIKeyLongContextBeforeSend(context.Background(), c, []byte(`{"file_id":"file_remote"}`), "model"))
	}
}
