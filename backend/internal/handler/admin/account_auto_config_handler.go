package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

func (h *AccountOpsHandler) GetAutoConfig(c *gin.Context) {
	cfg, err := h.svc.GetOAuthAutoConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Automatic configuration unavailable")
		return
	}
	response.Success(c, struct {
		service.OAuthAutoConfig
		RuntimeBlocked             bool                               `json:"runtime_blocked"`
		OpenAIAPIKeyFallbackStatus service.OpenAIAPIKeyFallbackStatus `json:"openai_apikey_fallback_status"`
	}{cfg, h.svc.AutoConfigBlocked(), h.svc.OpenAIAPIKeyFallbackStatus()})
}
func (h *AccountOpsHandler) SaveAutoConfig(c *gin.Context) {
	// Preserve fields unknown to older clients, including the BPS template.
	cfg, err := h.svc.GetOAuthAutoConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Automatic configuration unavailable")
		return
	}
	if c.ShouldBindJSON(&cfg) != nil {
		response.BadRequest(c, "Invalid automatic configuration")
		return
	}
	saved, err := h.svc.SaveOAuthAutoConfig(c.Request.Context(), cfg)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, saved)
}

func (h *AccountOpsHandler) ListAutoConfigEvents(c *gin.Context) {
	before, err := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	if err != nil || before < 0 {
		response.BadRequest(c, "Invalid history cursor")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit < 1 || limit > 100 {
		response.BadRequest(c, "Invalid limit")
		return
	}
	kind := c.Query("kind")
	if !service.ValidAutoConfigEventKind(kind) {
		response.BadRequest(c, "Invalid automatic configuration event kind")
		return
	}
	events, err := h.svc.ListAutoConfigEvents(c.Request.Context(), before, kind, limit+1)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Automatic configuration history unavailable")
		return
	}
	more := len(events) > limit
	if more {
		events = events[:limit]
	}
	response.Success(c, gin.H{"items": events, "has_more": more})
}
