//go:build unit

package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOAuthNormalEOFBeforeOutputPreservesAccount(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, pool := range []bool{false, true} {
			for _, prefix := range []string{"", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_metadata\"}}\n\n"} {
				t.Run(fmt.Sprintf("passthrough=%t/pool=%t/metadata=%t", passthrough, pool, prefix != ""), func(t *testing.T) {
					c, rec := newOpenAIUpstreamErrorTestContext(t)
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"pool_mode": pool}}
					svc := &OpenAIGatewayService{cfg: &config.Config{}}
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(prefix))}
					var err error
					if passthrough {
						_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.1", "gpt-5.1")
					} else {
						_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.1", "gpt-5.1")
					}
					var failure *UpstreamFailoverError
					require.ErrorAs(t, err, &failure)
					require.True(t, failure.SameAccountRetryOnly())
					require.True(t, failure.RetryableOnSameAccount)
					require.False(t, failure.QuotaRejectedBeforeExecution)
					require.Empty(t, rec.Body.String(), "pre-output EOF cannot publish buffered metadata")
				})
			}
		}
	}
}

func TestOAuthNormalEOFAfterOutputCannotReplay(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\",\"output_index\":0}\n\n"))}
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.1", "gpt-5.1")
			} else {
				_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.1", "gpt-5.1")
			}
			require.Error(t, err)
			var failure *UpstreamFailoverError
			require.False(t, errors.As(err, &failure))
			require.Contains(t, rec.Body.String(), "partial")
		})
	}
}

func TestSSEQuotaTerminalDoesNotGrantHTTPRejectionProof(t *testing.T) {
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"pool_mode": true}}
	payload := []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"insufficient_quota","message":"Quota exhausted"}}}`)
	failure := svc.newOpenAIStreamFailoverError(c, account, true, "fixture-sse-quota", payload, "Quota exhausted")
	require.False(t, failure.QuotaRejectedBeforeExecution, "HTTP200 terminal events do not grant the HTTP429 history-replay exception")
	require.False(t, failure.RetryableOnSameAccount, "explicit quota must not enter a transient retry window through SSE")
}
