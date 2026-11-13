//go:build unit

package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHTTP2CCSemanticFailureOwnershipAndSLA(t *testing.T) {
	for _, mode := range []string{"responses", "messages"} {
		t.Run(mode, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 8)
			var submissions atomic.Int32
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				submissions.Add(1)
				if r.ProtoMajor != 2 || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
					t.Error("fixture must use the real HTTP2 CC conversion boundary")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"synthetic\",\"model\":\"gpt-5.1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}\n\nevent: error\ndata: {\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"synthetic semantic failure\"}}\n\n")
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}))
			upstream.EnableHTTP2 = true
			upstream.StartTLS()
			defer upstream.Close()
			base, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			account := service.Account{ID: 1, Name: "synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
				Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://example.invalid"},
				Extra:       map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions)}}
			h := newHTTP2PeerResetHandler(t, &peerResetHTTPUpstream{client: upstream.Client(), base: base}, account)
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			endpoint := "/v1/" + mode
			body := `{"model":"gpt-5.1","stream":true,"input":"synthetic"}`
			handler := h.Responses
			if mode == "messages" {
				handler = h.Messages
				body = `{"model":"gpt-5.1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"synthetic"}]}`
			}
			router.POST(endpoint, func(c *gin.Context) {
				seed, _ := newOpenAIResponsesFailoverTestContext(t, c.Request.Context())
				for key, value := range seed.Keys {
					c.Set(key, value)
				}
				key, _ := middleware2.GetAPIKeyFromContext(c)
				key.Group.AllowMessagesDispatch = true
				handler(c)
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)))
			for len(opsErrorLogQueue) > 0 {
				flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
			}
			require.Equal(t, int32(1), submissions.Load(), "a semantic failure after submission must never replay generation: %s", rec.Body.String())
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, repo.entries, 1)
			entry := repo.entries[0]
			require.Equal(t, "client", entry.ErrorOwner, "F4 complete CC context rejection must retain client ownership after reset: %s", mode)
			require.True(t, entry.IsBusinessLimited, "F4 context rejection must retain its SLA exclusion after reset")
			require.NotEqual(t, "transport_error", entry.ErrorType)
			require.Contains(t, entry.ErrorBody, "context_length_exceeded")
			require.Equal(t, http.StatusBadRequest, entry.StatusCode)
			require.Contains(t, rec.Body.String(), "partial")
			require.Contains(t, rec.Body.String(), "context_length_exceeded")
			require.NotContains(t, rec.Body.String(), "upstream_http2_stream_error")
			require.NotContains(t, rec.Body.String(), "response.completed")
			require.NotContains(t, rec.Body.String(), "message_stop")
			terminal := "event: response.failed\n"
			if mode == "messages" {
				terminal = "event: error\n"
			}
			require.Equal(t, 1, strings.Count(rec.Body.String(), terminal))
		})
	}
}
