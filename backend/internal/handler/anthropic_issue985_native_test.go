//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type issue985NativeCase struct {
	name, snapshot                 string
	input, output, cached, created int
	streamFailure                  bool
}

func issue985NativeCases() []issue985NativeCase {
	return []issue985NativeCase{
		{"unknown", "", 0, 0, 0, 0, true},
		{"explicit_zero", `{"input_tokens":0,"output_tokens":0}`, 0, 0, 0, 0, false},
		{"input_only", `{"input_tokens":17}`, 17, 0, 0, 0, true},
		{"output_only", `{"output_tokens":3}`, 0, 3, 0, 0, true},
		{"cache_only", `{"cache_read_input_tokens":7,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":5}}`, 0, 0, 7, 5, true},
		{"partial_full_error", `{"input_tokens":17,"output_tokens":3,"cache_read_input_tokens":7,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_1h_input_tokens":5}}`, 17, 3, 7, 5, true},
	}
}

func (tc issue985NativeCase) wire(bufferedMessages bool) string {
	usage := ""
	if tc.snapshot != "" {
		usage = `,"usage":` + tc.snapshot
	}
	message := `{"id":"msg_native_fixture","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"fixture text"}]` + usage + `}`
	if bufferedMessages {
		return message
	}
	// Streaming converters consume content_block events, not the buffered JSON
	// content array carried on message_start.
	message = strings.Replace(message, `"content":[{"type":"text","text":"fixture text"}]`, `"content":[]`, 1)
	wire := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + message + "}\n\n"
	wire += "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fixture text\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	if tc.name == "partial_full_error" {
		return wire + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"fixture\"}}\n\n"
	}
	return wire + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

// Each fixture gateway serves one request. Its in-memory admission balance
// and empty quota snapshot have no backing Redis entries to evict after debit.
type issue985NativeCache struct{ *handlerInflightCache }

func (*issue985NativeCache) InvalidateUserBalance(context.Context, int64) error { return nil }
func (*issue985NativeCache) DeleteUserPlatformQuotaCache(context.Context, int64, string) error {
	return nil
}

// Real OpenAI handler, scheduler, native adapter, HTTP transport and RecordUsage.
// No worker pool: the request completion signal follows synchronous billing.
func issue985NativeGateway(t *testing.T, upstreamURL string, billing service.UsageBillingRepository) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	cfg := &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 2}}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	groupID := int64(9400)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformDeepseek, Status: service.StatusActive, RateMultiplier: 1}
	account := service.Account{ID: 9401, Name: "native-fixture", Platform: service.PlatformDeepseek, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "fixture", "api_protocol": service.APIProtocolAnthropic, "base_url": upstreamURL}, GroupIDs: []int64{groupID}, AccountGroups: []service.AccountGroup{{AccountID: 9401, GroupID: groupID}}}
	repo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{&account}}, nil, nil, nil, nil)
	cache := service.NewBillingCacheService(&issue985NativeCache{newHandlerInflightCache(100)}, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(cache.Stop)
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 32)}
	gw := service.NewOpenAIGatewayService(repo, nil, usageRepo, billing, nil, nil, nil, nil, cfg, snapshot, nil, service.NewBillingService(cfg, nil), service.NewRateLimitService(nil, nil, cfg, nil, nil), cache, repository.NewHTTPUpstream(cfg), &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gw.StopOpenAICodexTicketHarvester)
	t.Cleanup(gw.CloseOpenAIWSPool)
	h := NewOpenAIGatewayHandler(gw, service.NewConcurrencyService(&fakeConcurrencyCache{}), cache, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.maxAccountSwitches = 2
	done := make(chan struct{}, 32)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		defer func() { done <- struct{}{} }()
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 9402, UserID: 9403, GroupID: &groupID, Group: group, User: &service.User{ID: 9403, Balance: 100, Status: service.StatusActive}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9403})
		c.Next()
	})
	router.POST("/v1/chat/completions", h.ChatCompletions)
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/messages", h.Messages)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, done
}

func issue985NativeRequest(t *testing.T, tc issue985NativeCase, route string, stream bool, requestID string, billing service.UsageBillingRepository) {
	t.Helper()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/messages" {
			t.Errorf("native adapter bypassed: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", requestID)
		if route == "messages" && !stream {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = io.WriteString(w, tc.wire(route == "messages" && !stream))
	}))
	defer upstream.Close()
	gateway, done := issue985NativeGateway(t, upstream.URL, billing)
	path := "/v1/" + route
	input := `"messages":[{"role":"user","content":"fixture"}],"max_tokens":32`
	if route == "chat" {
		path = "/v1/chat/completions"
	}
	if route == "responses" {
		input = `"input":"fixture","max_output_tokens":32`
	}
	resp, err := gateway.Client().Post(gateway.URL+path, "application/json", strings.NewReader(fmt.Sprintf(`{"model":"deepseek-v4-flash","stream":%t,%s}`, stream, input)))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	<-done
	require.Equal(t, int32(1), calls.Load(), "native generation must never replay")
	if tc.streamFailure && (stream || route != "messages") {
		require.Contains(t, string(body), `"error"`)
		require.NotContains(t, string(body), "[DONE]")
		require.NotContains(t, string(body), "response.completed")
		require.NotContains(t, string(body), `"finish_reason":"stop"`)
	} else {
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), "fixture text")
	}
}

func TestIssue985NativeHandlerUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"chat", "responses", "messages"} {
		for _, tc := range issue985NativeCases() {
			for _, stream := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", route, tc.name, stream), func(t *testing.T) {
					billing := &issue985Billing{}
					issue985NativeRequest(t, tc, route, stream, "issue985-native-handler", billing)
					events := billing.snapshot()
					if tc.name == "unknown" {
						require.Empty(t, events, "unknown usage cannot become an ordinary zero quote")
						return
					}
					require.Len(t, events, 1, "all known partial usage is recorded exactly once")
					u := events[0].Usage
					require.Equal(t, tc.input, u.InputTokens)
					require.Equal(t, tc.output, u.OutputTokens)
					require.Equal(t, tc.cached, u.CacheReadTokens)
					require.Equal(t, tc.created, u.CacheCreationTokens)
					require.Equal(t, tc.created, u.CacheCreation1hTokens)
					if tc.input+tc.output+tc.cached+tc.created > 0 {
						require.Positive(t, u.ActualCost)
					} else {
						require.Zero(t, u.ActualCost)
					}
				})
			}
		}
	}
}
