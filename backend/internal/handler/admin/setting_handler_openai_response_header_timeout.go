package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type updateOpenAIResponseHeaderTimeoutSettingsRequest struct {
	Enabled       *bool    `json:"enabled"`
	Seconds       *int     `json:"seconds"`
	ImageGroupIDs *[]int64 `json:"image_group_ids"`
	ImageSeconds  *int     `json:"image_seconds"`
}

// GetOpenAIResponseHeaderTimeoutSettings returns the admin-configurable
// OpenAI/Codex response-header timeout.
// GET /api/v1/admin/settings/openai-response-header-timeout
func (h *SettingHandler) GetOpenAIResponseHeaderTimeoutSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAIResponseHeaderTimeoutSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateOpenAIResponseHeaderTimeoutSettings updates the timeout and refreshes
// the process-local cache; other nodes pick it up within the cache TTL.
// PUT /api/v1/admin/settings/openai-response-header-timeout
func (h *SettingHandler) UpdateOpenAIResponseHeaderTimeoutSettings(c *gin.Context) {
	var req updateOpenAIResponseHeaderTimeoutSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.Enabled == nil || req.Seconds == nil {
		response.BadRequest(c, "enabled and seconds are required")
		return
	}
	current, err := h.settingService.GetOpenAIResponseHeaderTimeoutSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Clients that predate the image fields must not clear the saved groups.
	settings := service.OpenAIResponseHeaderTimeoutSettings{
		Enabled:       *req.Enabled,
		Seconds:       *req.Seconds,
		ImageGroupIDs: current.ImageGroupIDs,
		ImageSeconds:  current.ImageSeconds,
	}
	if req.ImageGroupIDs != nil {
		settings.ImageGroupIDs = *req.ImageGroupIDs
	}
	if req.ImageSeconds != nil {
		settings.ImageSeconds = *req.ImageSeconds
	}
	if err := h.settingService.SetOpenAIResponseHeaderTimeoutSettings(c.Request.Context(), &settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	updated, err := h.settingService.GetOpenAIResponseHeaderTimeoutSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}
