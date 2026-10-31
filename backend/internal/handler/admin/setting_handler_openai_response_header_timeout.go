package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type updateOpenAIResponseHeaderTimeoutSettingsRequest struct {
	Enabled    *bool `json:"enabled"`
	Seconds    *int  `json:"seconds"`
	MaxRetries *int  `json:"max_retries"`
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
	maxRetries := 1
	if req.MaxRetries != nil {
		maxRetries = *req.MaxRetries
	} else {
		// Old admin clients omit this additive field. Preserve an existing
		// explicit zero or custom budget rather than silently resetting it.
		current, err := h.settingService.GetOpenAIResponseHeaderTimeoutSettings(c.Request.Context())
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		maxRetries = current.MaxRetries
	}
	if err := h.settingService.SetOpenAIResponseHeaderTimeoutSettings(c.Request.Context(), &service.OpenAIResponseHeaderTimeoutSettings{
		Enabled:    *req.Enabled,
		Seconds:    *req.Seconds,
		MaxRetries: maxRetries,
	}); err != nil {
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
