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

// Use the real handlers, scheduler and converters, capturing only the HTTP boundary.
func TestGatewayAnthropicMediaHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"chat/completions", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, field := range []string{"image", "file"} {
				for _, tc := range []struct {
					name       string
					uri        string
					blockType  string
					sourceType string
				}{
					{"png", "data:image/png;base64,c3ludGhldGlj", "image", "base64"},
					{"jpeg", "data:image/jpeg;base64,c3ludGhldGlj", "image", "base64"},
					{"gif", "data:image/gif;base64,c3ludGhldGlj", "image", "base64"},
					{"webp", "data:image/webp;base64,c3ludGhldGlj", "image", "base64"},
					{"pdf", "data:application/pdf;base64,c3ludGhldGlj", "document", "base64"},
					{"plain", "data:text/plain;base64,c3ludGhldGlj", "document", "text"},
					{"url", "https://example.invalid/private.png", "", ""},
					{"video", "data:video/mp4;base64,cHJpdmF0ZQ==", "reject", ""},
					{"audio", "data:audio/mpeg;base64,cHJpdmF0ZQ==", "reject", ""},
					{"unknown", "data:application/octet-stream;base64,cHJpdmF0ZQ==", "reject", ""},
					{"malformed", "data:image/png,private-payload", "reject", ""},
					{"bad_base64", "data:image/png;base64,private-payload!", "reject", ""},
					{"empty", "data:image/png;base64,", "reject", ""},
				} {
					t.Run(fmt.Sprintf("%s/stream=%t/%s/%s", endpoint, stream, field, tc.name), func(t *testing.T) {
						cfg := &config.Config{}
						groupID := int64(9810)
						group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
						account := &service.Account{
							ID: 9811, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
							Status: service.StatusActive, Schedulable: true, Concurrency: 1,
							Credentials:   map[string]any{"api_key": "test-key"},
							AccountGroups: []service.AccountGroup{{AccountID: 9811, GroupID: groupID}},
						}
						snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
						upstream := &compatEffortPolicyUpstream{}
						usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 1)}
						billingRepo := &compatStreamUsageBillingRepo{}
						billing := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil, cfg, nil)
						t.Cleanup(billing.Stop)
						gateway := service.NewGatewayService(
							nil, &fakeGroupRepo{group: group}, usage, billingRepo, nil, nil, nil, nil, cfg,
							snapshot, nil, service.NewBillingService(cfg, nil), nil, nil, nil, upstream,
							&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
						)
						h := &GatewayHandler{gatewayService: gateway, billingCacheService: billing, cfg: cfg, maxAccountSwitches: 1,
							concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0)}
						part := map[string]any{"type": "input_image", "image_url": tc.uri}
						text := map[string]any{"type": "input_text", "text": "synthetic prompt"}
						if field == "file" {
							part = map[string]any{"type": "input_file", "file_data": tc.uri}
						}
						request := map[string]any{"model": "claude-sonnet-4-5", "stream": stream}
						if endpoint == "chat/completions" {
							text["type"] = "text"
							part = map[string]any{"type": "image_url", "image_url": map[string]string{"url": tc.uri}}
							if field == "file" {
								part = map[string]any{"type": "file", "file": map[string]string{"file_data": tc.uri}}
							}
							request["messages"] = []any{map[string]any{"role": "user", "content": []any{text, part}}}
						} else {
							request["input"] = []any{map[string]any{"role": "user", "content": []any{text, part}}}
						}
						body, err := json.Marshal(request)
						require.NoError(t, err)
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						ctx := context.WithValue(context.Background(), ctxkey.Group, group)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, bytes.NewReader(body)).WithContext(ctx)
						c.Request.Header.Set("Content-Type", "application/json")
						// Use the new-key default; restricted keys reject opaque media
						// during final context admission before this conversion oracle.
						c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 9812, UserID: 9813, GroupID: &groupID, Group: group, AllowLongContext: true, User: &service.User{ID: 9813, Balance: 100}})
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9813})
						if endpoint == "chat/completions" {
							h.ChatCompletions(c)
						} else {
							h.Responses(c)
						}
						if tc.blockType == "reject" {
							require.Equal(t, http.StatusBadRequest, recorder.Code)
							require.Contains(t, recorder.Body.String(), "invalid_request_error")
							require.NotEmpty(t, gjson.Get(recorder.Body.String(), "error.message").String())
							require.NotContains(t, recorder.Body.String(), "private")
							require.NotContains(t, recorder.Body.String(), "cHJpdmF0ZQ")
							require.Empty(t, upstream.bodies)
							require.Empty(t, usage.created)
							require.Empty(t, billingRepo.commands)
							return
						}
						require.Equal(t, http.StatusOK, recorder.Code)
						require.Len(t, upstream.bodies, 1)
						blocks := gjson.GetBytes(upstream.bodies[0], "messages.0.content").Array()
						if tc.blockType == "" {
							require.Len(t, blocks, 1) // preserve the legacy URL omission
						} else {
							require.Len(t, blocks, 2)
							require.Equal(t, tc.blockType, blocks[1].Get("type").String())
							require.Equal(t, tc.sourceType, blocks[1].Get("source.type").String())
							if tc.sourceType == "text" {
								require.Equal(t, "synthetic", blocks[1].Get("source.data").String())
							}
						}
					})
				}
			}
		}
	}
}
