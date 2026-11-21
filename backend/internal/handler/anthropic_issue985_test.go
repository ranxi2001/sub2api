//go:build unit

package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A synchronous durable-stage observer. The PG variant supplies the actual SQL
// repository; this observer records the unmodified billing event before commit.
type issue985Billing struct {
	service.UsageBillingRepository
	mu      sync.Mutex
	events  []service.UsageBillingEvent
	durable service.DurableUsageBillingRepository
}

func (b *issue985Billing) StageUsageBilling(ctx context.Context, c *service.UsageBillingCommand, u *service.UsageLog) error {
	b.mu.Lock()
	b.events = append(b.events, service.UsageBillingEvent{Version: 1, Command: *c, Usage: *u})
	b.mu.Unlock()
	if b.durable != nil {
		return b.durable.StageUsageBilling(ctx, c, u)
	}
	return nil
}
func (b *issue985Billing) ApplyStagedUsageBilling(ctx context.Context, k service.UsageBillingEventKey) (*service.UsageBillingRecovery, error) {
	if b.durable != nil {
		return b.durable.ApplyStagedUsageBilling(ctx, k)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return &service.UsageBillingRecovery{Event: b.events[len(b.events)-1], Result: service.UsageBillingApplyResult{Applied: true}, NeedsCache: true}, nil
}
func (b *issue985Billing) PendingUsageBilling(ctx context.Context, n int) ([]service.UsageBillingEventKey, error) {
	if b.durable != nil {
		return b.durable.PendingUsageBilling(ctx, n)
	}
	return nil, nil
}
func (b *issue985Billing) AcknowledgeUsageBillingCache(ctx context.Context, k service.UsageBillingEventKey) error {
	if b.durable != nil {
		return b.durable.AcknowledgeUsageBillingCache(ctx, k)
	}
	return nil
}
func (b *issue985Billing) snapshot() []service.UsageBillingEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]service.UsageBillingEvent(nil), b.events...)
}

// Uses production GatewayHandler, account selection, request converter, real HTTP
// upstream and client transports, and RecordUsage. No panic-recovery middleware.
func issue985Gateway(t *testing.T, upstreamURL string, billing service.UsageBillingRepository) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	cfg := &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 2}}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	groupID := int64(9300)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	account := &service.Account{ID: 9301, Name: "fixture", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture", "base_url": upstreamURL}, AccountGroups: []service.AccountGroup{{AccountID: 9301, GroupID: groupID}}}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	cache := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(cache.Stop)
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 32)}
	rateLimit := service.NewRateLimitService(nil, nil, cfg, nil, nil)
	gw := service.NewGatewayService(nil, &fakeGroupRepo{group: group}, usageRepo, billing, nil, nil, nil, nil, cfg, snapshot, nil, service.NewBillingService(cfg, nil), rateLimit, nil, nil, repository.NewHTTPUpstream(cfg), &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &GatewayHandler{gatewayService: gw, billingCacheService: cache, cfg: cfg, maxAccountSwitches: 2, apiKeyService: service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0)}
	done := make(chan struct{}, 32)
	router := gin.New()
	router.POST("/v1/:endpoint/*rest", func(c *gin.Context) {
		defer func() { done <- struct{}{} }()
		apiKey := &service.APIKey{ID: 9302, UserID: 9303, GroupID: &groupID, Group: group, User: &service.User{ID: 9303, Balance: 100, Status: service.StatusActive}}
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9303})
		if strings.HasPrefix(c.Request.URL.Path, "/v1/chat/") {
			h.ChatCompletions(c)
		} else {
			h.Responses(c)
		}
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, done
}

func issue985Request(t *testing.T, server *httptest.Server, done <-chan struct{}, body string) (int, string) {
	t.Helper()
	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	<-done
	return resp.StatusCode, string(data)
}

func TestIssue985HandlerUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	canonical := compatStreamUsagePartialStream + compatStreamUsageCompletion
	missing := strings.ReplaceAll(strings.ReplaceAll(canonical, `,"usage":{"input_tokens":12,"output_tokens":1}`, ""), `,"usage":{"output_tokens":7}`, "")
	for _, tc := range []struct {
		name, wire    string
		fail          bool
		input, output int
		billing       bool
	}{
		{"framed", strings.ReplaceAll(canonical, "\ndata:", "\nid: fixture\n: comment\ndata:"), false, 12, 7, true},
		{"missing", missing, true, 0, 0, false},
		{"zero", strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(canonical, `"input_tokens":12`, `"input_tokens":0`), `"output_tokens":1`, `"output_tokens":0`), `"output_tokens":7`, `"output_tokens":0`), false, 0, 0, true},
		{"partial_error", compatStreamUsagePartialStream + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"synthetic\"}}\n\n", true, 12, 1, true},
		{"partial_protocol", compatStreamUsagePartialStream, true, 12, 1, true},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, tc.wire)
				}))
				defer upstream.Close()
				billing := &issue985Billing{}
				gateway, done := issue985Gateway(t, upstream.URL, billing)
				code, wire := issue985Request(t, gateway, done, fmt.Sprintf(`{"model":"claude-sonnet-4.5","stream":%t,"messages":[{"role":"user","content":"fixture"}]}`, stream))
				require.Equal(t, int32(1), calls.Load(), "a submitted generation must not replay on incomplete usage")
				if tc.fail {
					require.Contains(t, wire, `"error"`)
					require.NotContains(t, wire, "[DONE]")
					require.NotContains(t, wire, `"finish_reason":"stop"`)
					if stream {
						require.Equal(t, 200, code)
						require.Equal(t, 1, strings.Count(wire, `data: {"error":`))
					} else {
						require.Equal(t, 502, code)
					}
				} else {
					require.Equal(t, 200, code)
					require.Contains(t, wire, "partial")
					if stream {
						require.Equal(t, 1, strings.Count(wire, "[DONE]"))
					}
				}
				events := billing.snapshot()
				if !tc.billing {
					require.Empty(t, events, "unknown usage must not persist zero success")
					return
				}
				require.Len(t, events, 1)
				require.Equal(t, tc.input, events[0].Usage.InputTokens)
				require.Equal(t, tc.output, events[0].Usage.OutputTokens)
				if tc.input > 0 {
					require.Greater(t, events[0].Command.BalanceCost, 0.0)
				} else {
					require.Zero(t, events[0].Command.BalanceCost)
				}
			})
		}
	}
}

func TestIssue985HandlerToolContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if n == 1 {
					if gjson.GetBytes(body, "tools.0.input_schema.properties.city.type").String() != "string" {
						t.Error("tool schema lost")
					}
				} else {
					if !strings.Contains(string(body), `"tool_use_id":"call_fixture"`) || !strings.Contains(string(body), "sunny") {
						t.Errorf("tool result continuation lost: %s", body)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-Id", fmt.Sprintf("fixture-tool-%d", n))
				if n == 1 {
					wire := strings.ReplaceAll(compatStreamUsagePartialStream, `{"type":"text","text":""}`, `{"type":"tool_use","id":"call_fixture","name":"weather","input":{}}`)
					wire = strings.ReplaceAll(wire, `{"type":"text_delta","text":"partial"}`, `{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}`)
					wire += strings.ReplaceAll(compatStreamUsageCompletion, `"end_turn"`, `"tool_use"`)
					_, _ = io.WriteString(w, strings.ReplaceAll(wire, "\ndata:", "\nid: fixture\ndata:"))
				} else {
					_, _ = io.WriteString(w, compatStreamUsagePartialStream+compatStreamUsageCompletion)
				}
			}))
			defer upstream.Close()
			gateway, done := issue985Gateway(t, upstream.URL, &issue985Billing{})
			request := fmt.Sprintf(`{"model":"claude-sonnet-4.5","stream":%t,"messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]}`, stream)
			status, wire := issue985Request(t, gateway, done, request)
			require.Equal(t, 200, status)
			var message apicompat.ChatMessage
			if stream {
				var name, id, args string
				scanner := bufio.NewScanner(strings.NewReader(wire))
				doneCount := 0
				for scanner.Scan() {
					line := scanner.Text()
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					payload := strings.TrimPrefix(line, "data: ")
					if payload == "[DONE]" {
						doneCount++
						continue
					}
					var chunk apicompat.ChatCompletionsChunk
					require.NoError(t, json.Unmarshal([]byte(payload), &chunk))
					require.Equal(t, "claude-sonnet-4.5", chunk.Model)
					for _, choice := range chunk.Choices {
						for _, call := range choice.Delta.ToolCalls {
							id += call.ID
							name += call.Function.Name
							args += call.Function.Arguments
						}
					}
				}
				require.NoError(t, scanner.Err())
				require.Equal(t, 1, doneCount)
				require.Equal(t, "call_fixture", id)
				require.Equal(t, "weather", name)
				require.JSONEq(t, `{"city":"Paris"}`, args)
				require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}`, id, name, args)), &message))
			} else {
				require.Equal(t, "call_fixture", gjson.Get(wire, "choices.0.message.tool_calls.0.id").String())
				require.JSONEq(t, `{"city":"Paris"}`, gjson.Get(wire, "choices.0.message.tool_calls.0.function.arguments").String())
				require.NoError(t, json.Unmarshal([]byte(gjson.Get(wire, "choices.0.message").Raw), &message))
			}
			history, _ := json.Marshal(message)
			next := fmt.Sprintf(`{"model":"claude-sonnet-4.5","stream":%t,"messages":[{"role":"user","content":"weather"},%s,{"role":"tool","tool_call_id":"call_fixture","content":"sunny"}]}`, stream, history)
			status, wire = issue985Request(t, gateway, done, next)
			require.Equal(t, 200, status)
			require.Contains(t, wire, "partial")
			require.Equal(t, int32(2), calls.Load())
		})
	}
}
