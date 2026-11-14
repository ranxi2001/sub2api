//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	preContentHandlerCreated = `data: {"type":"response.created","response":{"id":"resp_pc","object":"response","model":"gpt-5.4","status":"in_progress","output":[]}}` + "\n\n"
	preContentHandlerFailed  = `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"An error occurred while processing your request."}}}` + "\n\n"
	preContentHandlerSuccess = preContentHandlerCreated +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}` + "\n\n" +
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"answer from second account"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"resp_pc","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}}` + "\n\n"
)

// preContentFailoverUpstream answers the first request with a stream that stalls
// past one keepalive tick and then fails before any content; later requests
// complete normally.
type preContentFailoverUpstream struct {
	service.HTTPUpstream
	mu   sync.Mutex
	hits []int64
}

func (u *preContentFailoverUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
	u.mu.Lock()
	u.hits = append(u.hits, accountID)
	first := len(u.hits) == 1
	u.mu.Unlock()
	header := http.Header{"Content-Type": []string{"text/event-stream"}}
	if !first {
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(preContentHandlerSuccess))}, nil
	}
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, preContentHandlerCreated)
		time.Sleep(1300 * time.Millisecond)
		_, _ = io.WriteString(pw, preContentHandlerFailed)
		_ = pw.Close()
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: pr}, nil
}

func (u *preContentFailoverUpstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.hits)
}

func servePreContentFailoverMessages(t *testing.T, accountCount int) (*httptest.ResponseRecorder, *preContentFailoverUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	groupID := int64(9101)
	accounts := make([]service.Account, 0, accountCount)
	for i := 0; i < accountCount; i++ {
		accounts = append(accounts, service.Account{
			ID: int64(9201 + i), Name: "openai-apikey", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: i + 1,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
		})
	}
	repo := &grokCredentialHandlerRepo{accounts: accounts, missingOnGet: map[int64]bool{}}
	upstream := &preContentFailoverUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.MaxAccountSwitches = 3
	cfg.Gateway.StreamKeepaliveInterval = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billingCache, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	apiKey := &service.APIKey{
		ID: 9102, GroupID: &groupID,
		User:  &service.User{ID: 9103, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.POST("/v1/messages", h.Messages)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(
		`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec, upstream
}

func TestOpenAIMessagesFailsOverAfterKeepaliveOnlyWrite(t *testing.T) {
	rec, upstream := servePreContentFailoverMessages(t, 2)

	body := rec.Body.String()
	require.Equal(t, 2, upstream.calls(), "body: %q", body)
	require.Equal(t, 1, strings.Count(body, "event: message_start"), "body: %q", body)
	require.Contains(t, body, "answer from second account")
}

func TestOpenAIMessagesKeepaliveThenFailoverExhaustedEndsWithSSEError(t *testing.T) {
	rec, upstream := servePreContentFailoverMessages(t, 1)

	body := rec.Body.String()
	require.Equal(t, 1, upstream.calls(), "body: %q", body)
	require.Contains(t, body, "event: ping")
	lastEvent := body[strings.LastIndex(strings.TrimRight(body, "\n"), "\n\n")+2:]
	require.True(t, strings.HasPrefix(lastEvent, "event: error\ndata: {"), "last event: %q", lastEvent)
	require.Contains(t, lastEvent, `"type":"error"`)
	for _, line := range strings.Split(body, "\n") {
		require.False(t, strings.HasPrefix(line, "{"), "bare JSON line in SSE body: %q", line)
	}
}
