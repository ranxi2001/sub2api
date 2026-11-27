//go:build unit && issue1085

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil/sse1085"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type issue1085DeliveryUpstream struct {
	service.HTTPUpstream
	body  *sse1085.Issue1085Body
	calls int
}

func (upstream *issue1085DeliveryUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	upstream.calls++
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: upstream.body}, nil
}

func TestIssue1085DeliveryMiddleware(t *testing.T) {
	for _, reader := range []string{"native_async", "passthrough"} {
		for _, failure := range []string{"success", "write", "flush"} {
			t.Run(reader+"/"+failure, func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 8)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				body := sse1085.NewIssue1085Body(
					"data: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture delta\"}\n\n",
					issue1085Failed+"data: [DONE]\n\n")
				defer body.Close()
				upstream := &issue1085DeliveryUpstream{body: body}
				accounts := issue715AccountsOfType(service.AccountTypeAPIKey)[:1]
				accounts[0].Extra = map[string]any{"openai_passthrough": reader == "passthrough", "openai_apikey_responses_websockets_v2_mode": "off"}
				h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, upstream, nil, nil, &readable409UsageRepo{}, accounts...)
				h.cfg.Gateway.StreamDataIntervalTimeout = 30
				h.maxAccountSwitches = 0
				repo := &ingressRejectOpsRepo{}
				ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				var writer *sse1085.Issue1085Writer
				router.Use(func(c *gin.Context) {
					writer = &sse1085.Issue1085Writer{ResponseWriter: c.Writer, Body: body, Context: ctx, Native: reader == "native_async", Failure: failure}
					c.Writer = writer
					c.Next()
				})
				router.Use(OpsErrorLoggerMiddleware(ops))
				var confirmed bool
				router.POST("/v1/responses", func(c *gin.Context) {
					groupID := int64(3131)
					c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, Key: "fixture-client", GroupID: &groupID,
						Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}, User: &service.User{ID: 100}})
					c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100})
					h.Responses(c)
					confirmed = service.HasOpsUpstreamSSETerminal(c)
				})
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","input":"fixture delta","stream":true}`)).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(httptest.NewRecorder(), req)
				require.Equal(t, 1, upstream.calls, "terminal delivery failure cannot replay")
				require.True(t, writer.DeltaFlushed)
				require.True(t, writer.TerminalWrite)
				require.NoError(t, writer.QueueWaitError)
				if reader == "native_async" {
					require.True(t, writer.QueueFilled)
				}
				if failure == "flush" {
					require.True(t, writer.TerminalFlush)
				}
				require.Equal(t, failure == "success", confirmed, "ISSUE1085_MIDDLEWARE_DELIVERY_EVIDENCE")
				issue1085FlushRecords()
				require.Len(t, repo.entries, 1, fmt.Sprintf("reader=%s failure=%s", reader, failure))
				entry := repo.entries[0]
				require.Equal(t, "provider", entry.ErrorOwner, "observed upstream error remains provider even when delivery fails")
				require.Equal(t, "upstream_http", entry.ErrorSource)
				require.Equal(t, "upstream", entry.ErrorPhase)
				if reader == "passthrough" && failure == "write" {
					// The event-name Write failed before its data reached the
					// capture writer. Preserve its existing unknown-status fallback.
					require.Equal(t, 502, entry.StatusCode)
					require.NotContains(t, entry.ErrorBody, issue1085Code)
				} else {
					require.Equal(t, 400, entry.StatusCode)
					require.Contains(t, entry.ErrorBody, issue1085Code)
				}
			})
		}
	}
}
