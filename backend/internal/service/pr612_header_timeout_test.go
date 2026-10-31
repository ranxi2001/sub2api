package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPR612HeaderTimeoutSubmissionBoundary(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_http", true: "submitted"}[submitted], func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/responses", nil)
			account := &Account{ID: 1, Platform: PlatformOpenAI}
			req, finish := guardOpenAIResponseHeaderTimeout(req, account)
			if submitted {
				httptrace.ContextClientTrace(req.Context()).WroteRequest(httptrace.WroteRequestInfo{})
			}
			original := &net.DNSError{Err: "synthetic timeout", Name: "fixture.invalid", IsTimeout: true}
			err := finish(nil, original)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = req
			err = (&OpenAIGatewayService{}).handleOpenAIUpstreamTransportError(context.Background(), c, account, err, false)
			var failover *UpstreamFailoverError
			if submitted {
				require.ErrorAs(t, err, &failover, "header timeout after submission is replayable with surcharge")
				require.True(t, failover.HeaderTimeoutSubmitted)
				require.Equal(t, http.StatusGatewayTimeout, failover.StatusCode)
				require.False(t, c.Writer.Written(), "handler must not commit before failover")
			} else {
				require.ErrorAs(t, err, &failover, "pre-execution failure retains safe failover")
			}
		})
	}
}
