//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type moderationLoopbackUpstream struct{ *nativeImageRouteUpstream }

func (u moderationLoopbackUpstream) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}

func TestModerationUpstreamHTTPDispatchFamilies(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, platform string
		call                       func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{"chat", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"fixture input"}]}`, service.PlatformOpenAI, (*OpenAIGatewayHandler).ChatCompletions},
		{"messages", "/v1/messages", `{"model":"gpt-5.1","max_tokens":20,"messages":[{"role":"user","content":"fixture input"}]}`, service.PlatformOpenAI, (*OpenAIGatewayHandler).Messages},
		{"embeddings", "/v1/embeddings", `{"model":"text-embedding-3-small","input":"fixture input"}`, service.PlatformOpenAI, (*OpenAIGatewayHandler).Embeddings},
		{"alpha", "/v1/alpha/search", `{"model":"gpt-5.1","commands":{"search_query":[{"q":"fixture input"}]}}`, service.PlatformOpenAI, (*OpenAIGatewayHandler).AlphaSearch},
		{"grok-tts", "/v1/audio/speech", `{"input":"fixture input","voice":"eve"}`, service.PlatformGrok, func(h *OpenAIGatewayHandler, c *gin.Context) { h.GrokVoice(c, "tts") }},
	} {
		for _, selected := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/selected=%t", tc.name, selected), func(t *testing.T) {
				var audits, writes atomic.Int32
				audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					audits.Add(1)
					_, _ = io.WriteString(w, `{"results":[{"category_scores":{"sexual":0.99}}]}`)
				}))
				defer audit.Close()
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					writes.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"error":{"message":"fixture generation refused"}}`)
				}))
				defer provider.Close()
				target, err := url.Parse(provider.URL)
				require.NoError(t, err)
				upstream := moderationLoopbackUpstream{&nativeImageRouteUpstream{target: target, client: provider.Client()}}
				account := service.Account{ID: 1, Name: "fixture", Platform: tc.platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3131, 3132}, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://fixture.example"}}
				h := newOpenAIResponsesFailoverTestHandler(t, upstream, account)
				id := 99
				if selected {
					id = 1
				}
				raw := fmt.Sprintf(`{"enabled":true,"mode":"pre_block","all_groups":true,"sample_rate":100,"base_url":%q,"api_keys":["fixture"],"upstream_account_scope":"selected","upstream_account_ids":[%d],"upstream_account_bindings":{"1":{"platform":%q,"type":"apikey","group_ids":[3131,3132]}}}`, audit.URL, id, tc.platform)
				settings := &contentModerationHandlerSettingRepo{values: map[string]string{service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: raw}}
				h.contentModerationService = service.NewContentModerationService(settings, &contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil, nil, openAIImagesFailoverAccountRepo{accounts: []service.Account{account}})
				c, w := newOpenAIResponsesFailoverTestContext(t, context.Background())
				c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
				c.Request.Header.Set("Content-Type", "application/json")
				key, _ := middleware.GetAPIKeyFromContext(c)
				key.Group.Platform = tc.platform
				key.Group.AllowMessagesDispatch = true
				tc.call(h, c)
				if selected {
					require.Equal(t, 403, w.Code, w.Body.String())
					require.Equal(t, int32(1), audits.Load())
					require.Zero(t, writes.Load())
					require.True(t, json.Valid(w.Body.Bytes()), w.Body.String())
					require.Zero(t, h.gatewayService.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount)
				} else {
					require.Zero(t, audits.Load())
					require.Positive(t, writes.Load(), w.Body.String())
				}
			})
		}
	}
}
