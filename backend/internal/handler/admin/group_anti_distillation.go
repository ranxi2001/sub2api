package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func ProvideGroupHandlerWithProtection(adminService service.AdminService, dashboard *service.DashboardService, capacity *service.GroupCapacityService, cfg *config.Config, protection *service.GroupAntiDistillationService) *GroupHandler {
	h := NewGroupHandlerWithConfig(adminService, dashboard, capacity, cfg)
	h.antiDistillation = protection
	return h
}

func (h *GroupHandler) antiDistillationGroupID(c *gin.Context) (int64, bool) {
	if h.rejectUnsupportedSimpleModeOperation(c, "anti_distillation") {
		return 0, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "分组 ID 无效")
		return 0, false
	}
	if h.antiDistillation == nil {
		response.Error(c, http.StatusServiceUnavailable, "分组保护服务暂时不可用")
		return 0, false
	}
	return id, true
}

func (h *GroupHandler) GetAntiDistillation(c *gin.Context) {
	id, ok := h.antiDistillationGroupID(c)
	if !ok {
		return
	}
	cfg, err := h.antiDistillation.GetConfig(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *GroupHandler) SaveAntiDistillation(c *gin.Context) {
	id, ok := h.antiDistillationGroupID(c)
	if !ok {
		return
	}
	// Both fields are required: an incomplete or null body must not silently
	// disable protection or reset the pause duration.
	var input struct {
		Enabled      *bool `json:"enabled"`
		PauseMinutes *int  `json:"pause_minutes"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Enabled == nil || input.PauseMinutes == nil {
		response.BadRequest(c, "请提供 enabled 和 pause_minutes")
		return
	}
	cfg := service.GroupAntiDistillationConfig{Enabled: *input.Enabled, PauseMinutes: *input.PauseMinutes}
	if err := h.antiDistillation.SaveConfig(c.Request.Context(), id, cfg); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}
