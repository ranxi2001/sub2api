//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type penaltyModeSettings struct {
	service.SettingRepository
	value string
}

func (r *penaltyModeSettings) GetValue(_ context.Context, key string) (string, error) {
	if key == "priority_scheduling_v1" {
		return r.value, nil
	}
	return "", service.ErrSettingNotFound
}
func (r *penaltyModeSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *penaltyModeSettings) Set(_ context.Context, _ string, value string) error {
	r.value = value
	return nil
}

type penaltyModeUsage struct{ service.UsageLogRepository }

func (r penaltyModeUsage) ReadPrioritySchedulingSignals(_ context.Context, q service.PrioritySchedulingQuery) (map[int64]service.PrioritySchedulingSignal, error) {
	average := 16000.0
	out := map[int64]service.PrioritySchedulingSignal{}
	for _, id := range q.AccountIDs {
		out[id] = service.PrioritySchedulingSignal{Performance: &service.AccountPerformance{AvgTTFTMs: &average, TTFTSamples: 20}}
	}
	return out, nil
}

func (penaltyModeUsage) ReadPriorityLatencySeries(context.Context, service.PriorityLatencySeriesQuery) ([]service.PriorityLatencyBucket, error) {
	return nil, nil
}

func TestPriorityLatencySeriesHandlerUsesEmptyArrayForNoHistory(t *testing.T) {
	cfg := service.DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.GroupIDs = []int64{11}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	settings := service.NewSettingService(&penaltyModeSettings{value: string(raw)}, nil)
	require.NoError(t, settings.SavePrioritySchedulingConfig(context.Background(), cfg))
	appCfg := &config.Config{RunMode: config.RunModeSimple}
	account := service.Account{ID: 901, Name: "synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{11}}
	gateway := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}, nil, penaltyModeUsage{}, nil, nil, nil, nil, nil, appCfg, nil, service.NewConcurrencyService(&penaltySlotCache{}), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil)
	t.Cleanup(func() { _ = gateway.CloseExcelBPSImages() })
	group := int64(11)
	ctx := service.WithPrioritySchedulingObservation(context.Background(), &group, "public-model", "/v1/responses")
	selected, _, err := gateway.SelectAccountWithSchedulerForCapability(ctx, &group, "", "", "gpt-test", nil, service.OpenAIUpstreamTransportAny, service.OpenAIEndpointCapabilityChatCompletions, false, false, true, service.PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, selected)

	admin := adminhandler.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	admin.SetOpenAIGatewayService(gateway)
	router := gin.New()
	router.GET("/admin/priority-scheduling/latency", admin.PrioritySchedulingLatency)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/priority-scheduling/latency", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"buckets":[]`, "the endpoint contract must not emit null for an account without history")
	if selected.ReleaseFunc != nil {
		selected.ReleaseFunc()
	}
}

type penaltySlotCache struct {
	fakeConcurrencyCache
	full            bool
	wait            bool
	calls, releases int
	beforeAcquire   func()
}

func (c *penaltySlotCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	c.calls++
	if c.full || (c.wait && c.calls == 1) {
		return false, nil
	}
	if c.beforeAcquire != nil {
		c.beforeAcquire()
	}
	return true, nil
}
func (c *penaltySlotCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.releases++
	return nil
}

func TestPriorityLatencyModesHandlerWaitAndAcquiredBoundary(t *testing.T) {
	for _, path := range []string{"quick slot", "queued slot", "already acquired"} {
		t.Run(path, func(t *testing.T) {
			cfg := service.DefaultPrioritySchedulingConfig()
			cfg.Enabled = true
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			settings := service.NewSettingService(&penaltyModeSettings{value: string(raw)}, nil)
			require.NoError(t, settings.SavePrioritySchedulingConfig(context.Background(), cfg))
			appCfg := &config.Config{RunMode: config.RunModeSimple}
			schedulingCache := &penaltySlotCache{full: path != "already acquired"}
			schedulingSlots := service.NewConcurrencyService(schedulingCache)
			account := service.Account{ID: 901, Name: "synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{11}}
			gateway := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}, nil, penaltyModeUsage{}, nil, nil, nil, nil, nil, appCfg, nil, schedulingSlots, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil)
			t.Cleanup(func() { _ = gateway.CloseExcelBPSImages() })
			group := int64(11)
			ctx := service.WithPrioritySchedulingObservation(context.Background(), &group, "public-model", "/v1/responses")
			selected, _, err := gateway.SelectAccountWithSchedulerForCapability(ctx, &group, "", "", "gpt-test", nil, service.OpenAIUpstreamTransportAny, service.OpenAIEndpointCapabilityChatCompletions, false, false, true, service.PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Eventually(t, func() bool {
				snapshot := gateway.PrioritySchedulingSnapshot()
				return snapshot != nil && snapshot.HistoryReady
			}, time.Second, time.Millisecond)
			// Re-select using loaded evidence; soft mode must retain the only slow account.
			if selected.ReleaseFunc != nil {
				selected.ReleaseFunc()
			}
			selected, _, err = gateway.SelectAccountWithSchedulerForCapability(ctx, &group, "", "", "gpt-test", nil, service.OpenAIUpstreamTransportAny, service.OpenAIEndpointCapabilityChatCompletions, false, false, true, service.PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, selected)
			harden := func() {
				hard := cfg
				hard.LatencyPenaltyMode = service.PriorityLatencyStop
				require.NoError(t, settings.SavePrioritySchedulingConfig(ctx, hard))
			}
			cache := &penaltySlotCache{wait: path == "queued slot", beforeAcquire: harden}
			if path == "already acquired" {
				harden()
			}
			h := &OpenAIGatewayHandler{gatewayService: gateway, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatClaude, 0)}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			streamStarted := false
			release, result := h.acquireResponsesAccountSlot(c, &group, "", selected, false, &streamStarted, zap.NewNop())
			require.Nil(t, release)
			require.Equal(t, openAISlotAcquireFailed, result)
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			require.Equal(t, "5", recorder.Header().Get("Retry-After"))
			require.Contains(t, recorder.Body.String(), "temporarily_unavailable")
			if path == "already acquired" {
				require.Equal(t, 2, schedulingCache.releases)
			} else {
				require.Equal(t, 1, cache.releases)
			}
		})
	}
}
func TestPriorityLatencyModesTemporaryUnavailableClassification(t *testing.T) {
	fallback := noAccountErrorClassification{Status: http.StatusNotFound, ModelNotFound: true}
	got := classifySelectionFailureError(service.ErrPriorityLatencyUnavailable, fallback)
	require.Equal(t, http.StatusServiceUnavailable, got.Status)
	require.Equal(t, "temporarily_unavailable", got.ErrType)
	require.Contains(t, got.Message, "5 seconds")
}
