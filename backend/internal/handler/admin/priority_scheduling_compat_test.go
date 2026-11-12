package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type priorityCompatSettings struct {
	service.SettingRepository
	value string
}

func (r *priorityCompatSettings) GetValue(context.Context, string) (string, error) {
	return r.value, nil
}
func (r *priorityCompatSettings) Set(_ context.Context, _ string, value string) error {
	r.value = value
	return nil
}

func TestPrioritySavePreservesOmittedProtocolPreference(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		cfg := service.DefaultPrioritySchedulingConfig()
		cfg.BalanceProtocols = false
		raw, err := json.Marshal(cfg)
		require.NoError(t, err)
		repo := &priorityCompatSettings{value: string(raw)}
		h := &SettingHandler{settingService: service.NewSettingService(repo, nil)}
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		delete(body, "balance_protocols")
		if explicit {
			body["balance_protocols"] = true
		}
		raw, err = json.Marshal(body)
		require.NoError(t, err)
		router := gin.New()
		router.PUT("/config", h.SavePriorityScheduling)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var saved service.PrioritySchedulingConfig
		require.NoError(t, json.Unmarshal([]byte(repo.value), &saved))
		require.Equal(t, explicit, saved.BalanceProtocols)
	}
}

func TestPrioritySavePreservesAndDisablesOAuthQuotaPolicy(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		cfg := service.DefaultPrioritySchedulingConfig()
		cfg.OAuthQuotaPriority = true
		cfg.OAuthQuotaThreshold = 85
		raw, err := json.Marshal(cfg)
		require.NoError(t, err)
		repo := &priorityCompatSettings{value: string(raw)}
		h := &SettingHandler{settingService: service.NewSettingService(repo, nil)}
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		delete(body, "oauth_quota_priority")
		delete(body, "oauth_quota_threshold")
		if explicit {
			body["oauth_quota_priority"] = false
			body["oauth_quota_threshold"] = 90
		}
		raw, err = json.Marshal(body)
		require.NoError(t, err)
		router := gin.New()
		router.PUT("/config", h.SavePriorityScheduling)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var saved service.PrioritySchedulingConfig
		require.NoError(t, json.Unmarshal([]byte(repo.value), &saved))
		require.Equal(t, !explicit, saved.OAuthQuotaPriority)
		if explicit {
			require.Equal(t, 90, saved.OAuthQuotaThreshold)
		} else {
			require.Equal(t, 85, saved.OAuthQuotaThreshold)
		}
	}
}

func TestPrioritySavePreservesOrExplicitlyChangesLatencyPenalty(t *testing.T) {
	for _, value := range []int{-1, 0, 20, 1441} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			cfg := service.DefaultPrioritySchedulingConfig()
			cfg.LatencyPenaltyMinutes = 15
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			repo := &priorityCompatSettings{value: string(raw)}
			h := &SettingHandler{settingService: service.NewSettingService(repo, nil)}
			var body map[string]any
			require.NoError(t, json.Unmarshal(raw, &body))
			delete(body, "latency_penalty_minutes")
			want := 15
			if value >= 0 {
				body["latency_penalty_minutes"] = value
				want = value
			}
			raw, err = json.Marshal(body)
			require.NoError(t, err)
			router := gin.New()
			router.PUT("/config", h.SavePriorityScheduling)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			if value > 1440 {
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				return
			}
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var saved service.PrioritySchedulingConfig
			require.NoError(t, json.Unmarshal([]byte(repo.value), &saved))
			require.Equal(t, want, saved.LatencyPenaltyMinutes)
		})
	}
}

func TestPrioritySaveQuantileCompatibilityAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, patch  string
		status, want int
	}{
		{"omitted", `{}`, http.StatusOK, 70},
		{"p50", `{"latency_percentile":50,"latency_measurement":"legacy_forward"}`, http.StatusOK, 50},
		{"p95", `{"latency_percentile":95,"latency_measurement":"attributed_v1"}`, http.StatusOK, 95},
		{"zero", `{"latency_percentile":0}`, http.StatusBadRequest, 0},
		{"unsupported", `{"latency_percentile":99}`, http.StatusBadRequest, 0},
		{"unknown_measurement", `{"latency_measurement":"unknown"}`, http.StatusBadRequest, 0},
		{"unbounded_recovery", `{"recovery_exploration_percent":11}`, http.StatusBadRequest, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := service.DefaultPrioritySchedulingConfig()
			cfg.LatencyPercentile, cfg.LatencyMeasurement, cfg.RecoveryExplorationPercent = 70, "attributed_v1", 5
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			repo := &priorityCompatSettings{value: string(raw)}
			h := &SettingHandler{settingService: service.NewSettingService(repo, nil)}
			var body, patch map[string]any
			require.NoError(t, json.Unmarshal(raw, &body))
			delete(body, "latency_percentile")
			delete(body, "latency_measurement")
			delete(body, "recovery_exploration_percent")
			require.NoError(t, json.Unmarshal([]byte(tc.patch), &patch))
			for key, value := range patch {
				body[key] = value
			}
			raw, err = json.Marshal(body)
			require.NoError(t, err)
			router := gin.New()
			router.PUT("/config", h.SavePriorityScheduling)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(raw))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			var saved service.PrioritySchedulingConfig
			require.NoError(t, json.Unmarshal([]byte(repo.value), &saved))
			if tc.status == http.StatusOK {
				require.Equal(t, tc.want, saved.LatencyPercentile)
				require.Equal(t, 5, saved.RecoveryExplorationPercent)
				if measurement, ok := patch["latency_measurement"]; ok {
					require.Equal(t, measurement, saved.LatencyMeasurement)
				} else {
					require.Equal(t, "attributed_v1", saved.LatencyMeasurement)
				}
			} else {
				require.Equal(t, cfg, saved, "invalid configuration must not be persisted")
			}
		})
	}
}

func TestPriorityErrorConfigLegacyAndSaveCompatibility(t *testing.T) {
	repo := &priorityCompatSettings{value: `{"enabled":true}`}
	settings := service.NewSettingService(repo, nil)
	legacy, err := settings.GetPrioritySchedulingConfig(context.Background())
	require.NoError(t, err)
	require.Zero(t, legacy.ErrorRateWeight)
	require.Equal(t, 10, legacy.ErrorRateWindowMinutes)
	require.Equal(t, 5, legacy.ErrorRatePriorSamples)
	for _, explicit := range []bool{false, true} {
		cfg := service.DefaultPrioritySchedulingConfig()
		cfg.ErrorRateWeight = 70
		cfg.ErrorRateWindowMinutes = 20
		cfg.ErrorRatePriorSamples = 15
		raw, err := json.Marshal(cfg)
		require.NoError(t, err)
		repo.value = string(raw)
		var request map[string]any
		require.NoError(t, json.Unmarshal(raw, &request))
		delete(request, "error_rate_weight")
		delete(request, "error_rate_window_minutes")
		delete(request, "error_rate_prior_samples")
		if explicit {
			request["error_rate_weight"] = 0
			request["error_rate_window_minutes"] = 1
			request["error_rate_prior_samples"] = 1
		}
		raw, err = json.Marshal(request)
		require.NoError(t, err)
		h := &SettingHandler{settingService: settings}
		router := gin.New()
		router.PUT("/config", h.SavePriorityScheduling)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		saved, err := settings.GetPrioritySchedulingConfig(context.Background())
		require.NoError(t, err)
		if explicit {
			require.Zero(t, saved.ErrorRateWeight)
			require.Equal(t, 1, saved.ErrorRateWindowMinutes)
			require.Equal(t, 1, saved.ErrorRatePriorSamples)
		} else {
			require.Equal(t, 70.0, saved.ErrorRateWeight)
			require.Equal(t, 20, saved.ErrorRateWindowMinutes)
			require.Equal(t, 15, saved.ErrorRatePriorSamples)
		}
	}
}

func TestPrioritySaveLatencyPenaltyModes(t *testing.T) {
	for _, stored := range []string{"", service.PriorityLatencyDeprioritize, service.PriorityLatencyStop} {
		for _, sent := range []string{"omitted", service.PriorityLatencyDeprioritize, service.PriorityLatencyStop, "invalid", ""} {
			t.Run(stored+"/"+sent, func(t *testing.T) {
				cfg := service.DefaultPrioritySchedulingConfig()
				cfg.LatencyPenaltyMinutes = 10
				body, _ := json.Marshal(cfg)
				var initial map[string]any
				require.NoError(t, json.Unmarshal(body, &initial))
				if stored == "" {
					delete(initial, "latency_penalty_mode")
				} else {
					initial["latency_penalty_mode"] = stored
				}
				body, _ = json.Marshal(initial)
				repo := &priorityCompatSettings{value: string(body)}
				h := &SettingHandler{settingService: service.NewSettingService(repo, nil)}
				if sent == "omitted" {
					delete(initial, "latency_penalty_mode")
				} else {
					initial["latency_penalty_mode"] = sent
				}
				body, _ = json.Marshal(initial)
				router := gin.New()
				router.PUT("/config", h.SavePriorityScheduling)
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(recorder, request)
				if sent == "invalid" || sent == "" {
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					return
				}
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				var saved service.PrioritySchedulingConfig
				require.NoError(t, json.Unmarshal([]byte(repo.value), &saved))
				expected := sent
				if sent == "omitted" {
					expected = stored
					if expected == "" {
						expected = service.PriorityLatencyDeprioritize
					}
				}
				require.Equal(t, expected, saved.LatencyPenaltyMode)
				require.Equal(t, 10, saved.LatencyPenaltyMinutes)
			})
		}
	}
}

func TestPriorityCacheWeightSaveCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    any
		supplied bool
		want     float64
		status   int
	}{
		{"omitted", nil, false, 25, 200}, {"enabled", 60.0, true, 60, 200}, {"disabled", 0.0, true, 0, 200},
		{"negative", -1.0, true, 25, 400}, {"too large", 101.0, true, 25, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := service.DefaultPrioritySchedulingConfig()
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			var request map[string]any
			require.NoError(t, json.Unmarshal(raw, &request))
			request["cache_weight"] = 25.0
			stored, err := json.Marshal(request)
			require.NoError(t, err)
			repo := &priorityCompatSettings{value: string(stored)}
			settings := service.NewSettingService(repo, nil)
			h := &SettingHandler{settingService: settings}
			delete(request, "cache_weight")
			if tc.supplied {
				request["cache_weight"] = tc.value
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			router := gin.New()
			router.PUT("/config", h.SavePriorityScheduling)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			saved, err := settings.GetPrioritySchedulingConfig(context.Background())
			require.NoError(t, err)
			encoded, err := json.Marshal(saved)
			require.NoError(t, err)
			var actual map[string]any
			require.NoError(t, json.Unmarshal(encoded, &actual))
			require.Equal(t, tc.want, actual["cache_weight"])
		})
	}
	legacy := &priorityCompatSettings{value: `{"enabled":true}`}
	current, err := service.NewSettingService(legacy, nil).GetPrioritySchedulingConfig(context.Background())
	require.NoError(t, err)
	raw, err := json.Marshal(current)
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, json.Unmarshal(raw, &values))
	require.Equal(t, 0.0, values["cache_weight"])
}
