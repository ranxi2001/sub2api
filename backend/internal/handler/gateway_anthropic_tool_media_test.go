//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type anthropicToolMediaUpstream struct{ compatEffortPolicyUpstream }

func (u *anthropicToolMediaUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxy, id, concurrency, nil)
}

func TestGatewayAnthropicChatToolMediaRejectsBeforeFlattening(t *testing.T) {
	testAnthropicChatToolMedia(t, false)
}

func TestNativeAnthropicChatToolMediaRejectsBeforeFlattening(t *testing.T) {
	testAnthropicChatToolMedia(t, true)
}

func testAnthropicChatToolMedia(t *testing.T, native bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, role := range []string{"tool", "function"} {
			for _, field := range []string{"image", "file"} {
				for _, tc := range []struct {
					name string
					uri  string
					want string
				}{
					{"video", "data:video/mp4;base64,cHJpdmF0ZQ==", "reject"},
					{"audio", "data:audio/mpeg;base64,cHJpdmF0ZQ==", "reject"},
					{"unknown", "data:application/octet-stream;base64,cHJpdmF0ZQ==", "reject"},
					{"malformed_header", "data:image/png,private-payload", "reject"},
					{"malformed_base64", "data:image/png;base64,private-payload!", "reject"},
					{"empty", "data:image/png;base64,", "reject"},
					{"png", "data:image/png;base64,c3ludGhldGlj", "image"},
					{"jpeg", "data:image/jpeg;base64,c3ludGhldGlj", "image"},
					{"gif", "data:image/gif;base64,c3ludGhldGlj", "image"},
					{"webp", "data:image/webp;base64,c3ludGhldGlj", "image"},
					{"pdf", "data:application/pdf;base64,c3ludGhldGlj", "document"},
					{"plain", "data:text/plain;base64,c3ludGhldGlj", "document"},
					{"string_control", "literal data:video/mp4;base64,cHJpdmF0ZQ==", "string"},
				} {
					t.Run(fmt.Sprintf("stream=%t/%s/%s/%s", stream, role, field, tc.name), func(t *testing.T) {
						cfg := &config.Config{RunMode: config.RunModeSimple}
						platform, model := service.PlatformAnthropic, "claude-sonnet-4-5"
						if native {
							platform, model = service.PlatformKimi, "k3"
						}
						groupID := int64(3131)
						group := &service.Group{ID: groupID, Hydrated: true, Platform: platform, Status: service.StatusActive, RateMultiplier: 1}
						account := &service.Account{
							ID: 9811, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
							Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID},
							Credentials:   map[string]any{"api_key": "test-key", "api_protocol": service.APIProtocolAnthropic, "base_url": "https://anthropic.example"},
							AccountGroups: []service.AccountGroup{{AccountID: 9811, GroupID: groupID}},
						}
						upstream := &anthropicToolMediaUpstream{}
						usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
						billingRepo := &compatStreamUsageBillingRepo{}
						billing := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil, cfg, nil)
						t.Cleanup(billing.Stop)
						media := map[string]any{"type": "image_url", "image_url": map[string]string{"url": tc.uri}}
						if field == "file" {
							media = map[string]any{"type": "file", "file": map[string]string{"file_data": tc.uri}}
						}
						var content any = []any{map[string]string{"type": "text", "text": "tool text"}, media}
						if tc.want == "string" {
							content = tc.uri
						}
						assistant := map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]string{"name": "read", "arguments": "{}"}}}}
						result := map[string]any{"role": role, "tool_call_id": "call_1", "content": content}
						if role == "function" {
							assistant = map[string]any{"role": "assistant", "function_call": map[string]string{"name": "read", "arguments": "{}"}}
							result = map[string]any{"role": role, "name": "read", "content": content}
						}
						body, err := json.Marshal(map[string]any{"model": model, "stream": stream, "messages": []any{map[string]string{"role": "user", "content": "read"}, assistant, result}})
						require.NoError(t, err)
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						ctx := context.WithValue(context.Background(), ctxkey.Group, group)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
						c.Request.Header.Set("Content-Type", "application/json")
						// Match the new-key default so context admission does not
						// reject opaque media before the MIME/preservation assertions.
						c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 99, UserID: 100, GroupID: &groupID, Group: group, AllowLongContext: true, User: &service.User{ID: 100, Balance: 100}})
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 100})
						if native {
							gateway := service.NewOpenAIGatewayService(
								&openAIWSUsageHandlerAccountRepoStub{account: *account}, nil, usage, billingRepo, nil, nil, nil, nil, cfg,
								nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, &service.DeferredService{},
								nil, nil, nil, nil, nil, nil, nil,
							)
							t.Cleanup(gateway.CloseOpenAIWSPool)
							h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
							h.ChatCompletions(c)
						} else {
							snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
							gateway := service.NewGatewayService(
								nil, &fakeGroupRepo{group: group}, usage, billingRepo, nil, nil, nil, nil, cfg,
								snapshot, nil, service.NewBillingService(cfg, nil), nil, nil, nil, upstream,
								&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
							)
							h := &GatewayHandler{gatewayService: gateway, billingCacheService: billing, cfg: cfg, maxAccountSwitches: 1,
								concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0)}
							h.ChatCompletions(c)
						}
						if tc.want == "reject" {
							require.Equal(t, http.StatusBadRequest, recorder.Code)
							require.Contains(t, recorder.Body.String(), "invalid_request_error")
							require.NotContains(t, recorder.Body.String(), "private")
							require.NotContains(t, recorder.Body.String(), "cHJpdmF0ZQ")
							require.Empty(t, upstream.bodies)
							require.Empty(t, usage.created)
							require.Empty(t, billingRepo.commands)
							return
						}
						require.Equal(t, http.StatusOK, recorder.Code)
						require.Len(t, upstream.bodies, 1)
						toolContent := gjson.GetBytes(upstream.bodies[0], "messages.2.content.0.content")
						if tc.want == "string" {
							require.Equal(t, tc.uri, toolContent.String())
							return
						}
						require.Equal(t, "tool text", toolContent.Get("0.text").String())
						require.Equal(t, tc.want, toolContent.Get("1.type").String())
						wantSource, wantData := "base64", "c3ludGhldGlj"
						if tc.name == "plain" {
							wantSource, wantData = "text", "synthetic"
						}
						require.Equal(t, wantSource, toolContent.Get("1.source.type").String())
						require.Equal(t, wantData, toolContent.Get("1.source.data").String())
						require.Equal(t, gjson.GetBytes(upstream.bodies[0], "messages.1.content.0.id").String(), gjson.GetBytes(upstream.bodies[0], "messages.2.content.0.tool_use_id").String())
					})
				}
			}
		}
	}
}
