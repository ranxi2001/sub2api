package admin

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// User routes use JWT authentication, not the administrator gate. Ownership is
// always taken from the authenticated subject, never request parameters.
func (h *SettingHandler) keyRiskOwner(c *gin.Context) (int64, bool) {
	actor, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || actor.UserID <= 0 {
		response.Error(c, 401, "Authentication required")
		return 0, false
	}
	if h.keyRisk == nil {
		response.Error(c, 503, "Key risk service unavailable")
		return 0, false
	}
	return actor.UserID, true
}
func (h *SettingHandler) GetMyKeyRisks(c *gin.Context) {
	user, ok := h.keyRiskOwner(c)
	if !ok {
		return
	}
	before, err := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	if err != nil || before < 0 {
		response.BadRequest(c, "Invalid cursor")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	notices, err := h.keyRisk.UserNotices(ctx, user, before)
	if err != nil {
		response.Error(c, 503, "Risk notices unavailable")
		return
	}
	response.Success(c, gin.H{"items": notices})
}
func (h *SettingHandler) AcknowledgeMyKeyRisk(c *gin.Context) {
	user, ok := h.keyRiskOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	var req struct {
		Generation int64 `json:"generation"`
		Confirm    bool  `json:"confirm"`
	}
	if err != nil || id <= 0 || c.ShouldBindJSON(&req) != nil || req.Generation <= 0 || !req.Confirm {
		response.BadRequest(c, "Explicit confirmation and current generation required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	err = h.keyRisk.Acknowledge(ctx, user, id, req.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		response.Error(c, 404, "Risk notice unavailable; refresh records")
		return
	}
	if err != nil {
		response.Error(c, 503, "Unable to acknowledge risk notice")
		return
	}
	middleware.SetAuditAction(c, "user.key_risk.acknowledge")
	response.Success(c, gin.H{"acknowledged": true})
}
