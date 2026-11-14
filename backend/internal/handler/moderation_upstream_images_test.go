//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestModerationUpstreamImagesJSONKeepaliveAndAsync(t *testing.T) {
	for _, scenario := range []string{"fast", "slow", "async"} {
		t.Run(scenario, func(t *testing.T) {
			var audits atomic.Int32
			release := make(chan struct{})

			audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				audits.Add(1)
				if scenario == "slow" {
					time.Sleep(1200 * time.Millisecond)
				}
				if scenario == "async" {
					<-release
				}
				_, _ = io.WriteString(w, `{"results":[{"category_scores":{"sexual":0.99}}]}`)
			}))
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				audit.Close()
			}()
			var h *OpenAIGatewayHandler
			store := &asyncImageMemoryStore{tasks: map[string]*service.ImageTaskRecord{}}
			server, calls := newNativeImageRoute(t, 0, 200, func(handler *OpenAIGatewayHandler, accounts service.AccountRepository, router *gin.Engine) {
				h = handler
				raw := fmt.Sprintf(`{"enabled":true,"mode":"pre_block","all_groups":true,"sample_rate":100,"timeout_ms":3000,"base_url":%q,"api_keys":["fixture"],"upstream_account_scope":"selected","upstream_account_ids":[1],"upstream_account_bindings":{"1":{"platform":"openai","type":"apikey","group_ids":[]}}}`, audit.URL)
				settings := &contentModerationHandlerSettingRepo{values: map[string]string{service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: raw}}
				h.contentModerationService = service.NewContentModerationService(settings, &contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil, nil, accounts)
				tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
				async := NewAsyncImageHandler(tasks, h)
				router.POST("/v1/images/generations/async", async.Submit)
				router.GET("/fixture-health", func(c *gin.Context) { c.JSON(200, gin.H{"fixture": "ok"}) })
			})
			endpoint := server.URL + "/v1/images/generations"
			if scenario == "async" {
				endpoint += "/async"
			}
			response, err := server.Client().Post(endpoint, "application/json", strings.NewReader(`{"model":"gpt-image-1","prompt":"fixture"}`))
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			require.NoError(t, err)
			require.True(t, json.Valid(body), string(body))
			require.NotContains(t, string(body), "event:")
			if scenario == "async" {
				require.Equal(t, 202, response.StatusCode)
				id := gjson.GetBytes(body, "id").String()
				require.NotEmpty(t, id)
				health, err := server.Client().Get(server.URL + "/fixture-health")
				require.NoError(t, err)
				healthBody, _ := io.ReadAll(health.Body)
				_ = health.Body.Close()
				require.JSONEq(t, `{"fixture":"ok"}`, string(healthBody))
				close(release)
				require.Eventually(t, func() bool {
					task, e := store.Get(context.Background(), id)
					return e == nil && task.Status == service.ImageTaskStatusFailed && strings.Contains(string(task.Error), "content_policy_violation")
				}, 5*time.Second, 10*time.Millisecond)
			} else {
				expected := 403
				if scenario == "slow" {
					expected = 200
					require.True(t, strings.HasPrefix(string(body), " \n"), "fixture must actually send a JSON keepalive")
				}
				require.Equal(t, expected, response.StatusCode)
				require.Contains(t, string(body), "content_policy_violation")
				require.Equal(t, 1, strings.Count(string(body), `"error":`))
			}
			require.Equal(t, int32(1), audits.Load())
			require.Zero(t, calls.Load())
			require.Zero(t, h.gatewayService.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount)
		})
	}
}
