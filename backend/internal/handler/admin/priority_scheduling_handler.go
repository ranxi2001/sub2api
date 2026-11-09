package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetPriorityScheduling(c *gin.Context) {
	cfg, err := h.settingService.GetPrioritySchedulingConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority scheduling configuration unavailable")
		return
	}
	response.Success(c, cfg)
}
func (h *SettingHandler) SavePriorityScheduling(c *gin.Context) {
	defaults := service.DefaultPrioritySchedulingConfig()
	var request struct {
		service.PrioritySchedulingConfig
		ErrorRateWeight            *float64 `json:"error_rate_weight"`
		ErrorRateWindowMinutes     *int     `json:"error_rate_window_minutes"`
		ErrorRatePriorSamples      *int     `json:"error_rate_prior_samples"`
		OAuthQuotaPriority         *bool    `json:"oauth_quota_priority"`
		OAuthQuotaThreshold        *int     `json:"oauth_quota_threshold"`
		BalanceProtocols           *bool    `json:"balance_protocols"`
		LatencyPenaltyMode         *string  `json:"latency_penalty_mode"`
		LatencyPenaltyMinutes      *int     `json:"latency_penalty_minutes"`
		LatencyPercentile          *int     `json:"latency_percentile"`
		LatencyMeasurement         *string  `json:"latency_measurement"`
		RecoveryExplorationPercent *int     `json:"recovery_exploration_percent"`
	}
	request.PrioritySchedulingConfig = defaults
	if c.ShouldBindJSON(&request) != nil {
		response.BadRequest(c, "Invalid scheduling configuration")
		return
	}
	cfg := request.PrioritySchedulingConfig
	if request.ErrorRateWeight == nil || request.ErrorRateWindowMinutes == nil || request.ErrorRatePriorSamples == nil || request.OAuthQuotaPriority == nil || request.OAuthQuotaThreshold == nil || request.BalanceProtocols == nil || request.LatencyPenaltyMode == nil || request.LatencyPenaltyMinutes == nil || request.LatencyPercentile == nil || request.LatencyMeasurement == nil || request.RecoveryExplorationPercent == nil {
		current, err := h.settingService.GetPrioritySchedulingConfig(c.Request.Context())
		if err != nil {
			response.Error(c, http.StatusServiceUnavailable, "Priority scheduling configuration unavailable")
			return
		}
		cfg.ErrorRateWeight = current.ErrorRateWeight
		cfg.ErrorRateWindowMinutes = current.ErrorRateWindowMinutes
		cfg.ErrorRatePriorSamples = current.ErrorRatePriorSamples
		cfg.BalanceProtocols = current.BalanceProtocols
		cfg.OAuthQuotaPriority = current.OAuthQuotaPriority
		cfg.OAuthQuotaThreshold = current.OAuthQuotaThreshold
		cfg.LatencyPenaltyMode = current.LatencyPenaltyMode
		cfg.LatencyPenaltyMinutes = current.LatencyPenaltyMinutes
		cfg.LatencyPercentile = current.LatencyPercentile
		cfg.LatencyMeasurement = current.LatencyMeasurement
		cfg.RecoveryExplorationPercent = current.RecoveryExplorationPercent
	}
	if request.ErrorRateWeight != nil {
		cfg.ErrorRateWeight = *request.ErrorRateWeight
	}
	if request.ErrorRateWindowMinutes != nil {
		cfg.ErrorRateWindowMinutes = *request.ErrorRateWindowMinutes
	}
	if request.ErrorRatePriorSamples != nil {
		cfg.ErrorRatePriorSamples = *request.ErrorRatePriorSamples
	}
	if request.OAuthQuotaPriority != nil {
		cfg.OAuthQuotaPriority = *request.OAuthQuotaPriority
	}
	if request.OAuthQuotaThreshold != nil {
		cfg.OAuthQuotaThreshold = *request.OAuthQuotaThreshold
	}
	if request.BalanceProtocols != nil {
		cfg.BalanceProtocols = *request.BalanceProtocols
	}
	if request.LatencyPenaltyMode != nil {
		if *request.LatencyPenaltyMode == "" {
			response.BadRequest(c, "Invalid latency penalty mode")
			return
		}
		cfg.LatencyPenaltyMode = *request.LatencyPenaltyMode
	}
	if request.LatencyPenaltyMinutes != nil {
		cfg.LatencyPenaltyMinutes = *request.LatencyPenaltyMinutes
	}

	if request.LatencyPercentile != nil {
		cfg.LatencyPercentile = *request.LatencyPercentile
	}
	if request.LatencyMeasurement != nil {
		cfg.LatencyMeasurement = *request.LatencyMeasurement
	}
	if request.RecoveryExplorationPercent != nil {
		cfg.RecoveryExplorationPercent = *request.RecoveryExplorationPercent
	}
	if err := service.ValidatePrioritySchedulingConfig(cfg); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.settingService.SavePrioritySchedulingConfig(c.Request.Context(), cfg); err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Could not save scheduling configuration")
		return
	}
	response.Success(c, cfg)
}
func (h *AccountHandler) PrioritySchedulingSnapshot(c *gin.Context) {
	response.Success(c, gin.H{"snapshot": h.openAIGatewayService.PrioritySchedulingSnapshot()})
}

func (h *AccountHandler) PrioritySchedulingAnalysis(c *gin.Context) {
	q := service.PriorityAnalysisQuery{Model: strings.TrimSpace(c.Query("model")), Minutes: 60}
	if raw, exists := c.GetQuery("minutes"); exists {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 5 || value > 1440 {
			response.BadRequest(c, "minutes must be between 5 and 1440")
			return
		}
		q.Minutes = value
	}
	if raw, exists := c.GetQuery("group_id"); exists {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			response.BadRequest(c, "group_id must be a positive integer")
			return
		}
		q.GroupID = &value
	}
	if q.Model == "" || len(q.Model) > 200 {
		response.BadRequest(c, "model is required (maximum 200 bytes)")
		return
	}
	ctx := c.Request.Context()
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		role, _ := middleware.GetUserRoleFromContext(c)
		identity, _ := json.Marshal([]any{subject.UserID, role, c.GetString("auth_method"), adminaccess.FromContext(ctx)})
		ctx = service.WithPriorityAnalysisPrincipal(ctx, string(identity))
	}
	result, err := h.openAIGatewayService.PriorityAnalysis(ctx, q)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority analysis read failed")
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) PrioritySchedulingLatency(c *gin.Context) {
	minutes := 60
	if raw := strings.TrimSpace(c.Query("minutes")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 5 || value > 1440 {
			response.BadRequest(c, "minutes must be between 5 and 1440")
			return
		}
		minutes = value
	}
	bucketMinutes := 5
	if raw := strings.TrimSpace(c.Query("bucket_minutes")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 60 {
			response.BadRequest(c, "bucket_minutes must be between 1 and 60")
			return
		}
		bucketMinutes = value
	}
	series, err := h.openAIGatewayService.PriorityLatencySeries(c.Request.Context(), minutes, bucketMinutes)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority latency series unavailable")
		return
	}
	response.Success(c, series)
}

func (h *AccountHandler) RecomputePriorityScheduling(c *gin.Context) {
	result, err := h.openAIGatewayService.RecomputePriorityScheduling(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority scheduling recompute unavailable")
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) PrioritySchedulingModels(c *gin.Context) {
	var group *int64
	if raw, exists := c.GetQuery("group_id"); exists {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			response.BadRequest(c, "group_id must be a positive integer")
			return
		}
		group = &value
	}
	models, err := h.openAIGatewayService.PriorityAnalysisModels(c.Request.Context(), group)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority model catalog read failed")
		return
	}
	response.Success(c, gin.H{"models": models})
}
