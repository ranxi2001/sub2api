package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
)

// Called only with the existing administrator settings group. Handlers also
// enforce a positive authenticated administrator identity as defense in depth.
func registerIPAuditRoutes(settings *gin.RouterGroup, h *admin.SettingHandler) {
	settings.GET("/key-risk", h.GetKeyRisk)
	settings.PUT("/key-risk", h.SaveKeyRisk)
	settings.POST("/key-risk/:id", h.ActOnKeyRisk)
	settings.GET("/ip-audit", h.GetIPAudit)
	settings.POST("/ip-audit/release", h.ReleaseIPAudit)
}
