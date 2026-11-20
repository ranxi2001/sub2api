package handler

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// TryOpenAIVideoGeneration leaves the existing unsupported response in place
// for OpenAI groups with no account that opted into the endpoint.
func (h *OpenAIGatewayHandler) TryOpenAIVideoGeneration(c *gin.Context) bool {
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || key == nil || h == nil || h.gatewayService == nil || !service.GroupAllowsImageGeneration(key.Group) {
		return false
	}
	platform := service.PlatformOpenAI
	if key.Group != nil && key.Group.Platform == service.PlatformQwen {
		platform = service.PlatformQwen
	}
	enabled, err := h.gatewayService.HasOpenAIVideoAccount(c.Request.Context(), key.GroupID, platform)
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Could not check video availability")
		return true
	}
	if !enabled {
		return false
	}
	setActualUpstreamEndpoint(c, "/v1/videos")
	h.handleGrokMedia(c, service.OpenAIVideoEndpointCreate, "")
	return true
}

func (h *OpenAIGatewayHandler) OpenAIVideoLookup(c *gin.Context) {
	endpoint := service.OpenAIVideoEndpointStatus
	if strings.HasSuffix(c.FullPath(), "/content") {
		endpoint = service.OpenAIVideoEndpointContent
	}
	// Composite status/content calls have no model for the creation router to
	// resolve. The task namespace identifies the quota platform.
	platform := service.PlatformOpenAI
	if key, ok := middleware.GetAPIKeyFromContext(c); ok && key != nil && key.Group != nil && key.Group.Platform == service.PlatformQwen {
		platform = service.PlatformQwen
	}
	c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
	setActualUpstreamEndpoint(c, "/v1/videos")
	h.handleGrokMedia(c, endpoint, c.Param("request_id"))
}
