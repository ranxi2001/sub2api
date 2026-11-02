package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GET /api/v1/admin/settings/account-performance
func (h *SettingHandler) GetAccountPerformanceSettings(c *gin.Context) {
	settings, err := h.settingService.GetAccountPerformanceSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// PUT /api/v1/admin/settings/account-performance
func (h *SettingHandler) UpdateAccountPerformanceSettings(c *gin.Context) {
	var req struct {
		WindowMinutes *int `json:"window_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.WindowMinutes == nil {
		response.BadRequest(c, "window_minutes is required")
		return
	}
	settings := service.AccountPerformanceSettings{WindowMinutes: *req.WindowMinutes}
	if err := h.settingService.SetAccountPerformanceSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
