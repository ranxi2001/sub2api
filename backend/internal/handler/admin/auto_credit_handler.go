package admin

import (
	"encoding/json"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *UserHandler) SetAutoCredit(repo service.AutoCreditRepository) { h.autoCredit = repo }

func (h *UserHandler) autoCreditAuthority(c *gin.Context) (int64, int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	role, _ := middleware.GetUserRoleFromContext(c)
	if !ok || subject.UserID <= 0 || role != service.RoleAdmin || adminaccess.FromContext(c.Request.Context()) != nil {
		response.Forbidden(c, "Administrator session required")
		return 0, 0, false
	}
	uid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || uid <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return 0, 0, false
	}
	if h.autoCredit == nil {
		response.Error(c, 503, "Auto credit unavailable")
		return 0, 0, false
	}
	return uid, subject.UserID, true
}
func (h *UserHandler) GetAutoCredit(c *gin.Context) {
	uid, _, ok := h.autoCreditAuthority(c)
	if !ok {
		return
	}
	before, err := strconv.ParseInt(c.DefaultQuery("before_id", "0"), 10, 64)
	if err != nil || before < 0 {
		response.BadRequest(c, "Invalid history cursor")
		return
	}
	state, err := h.autoCredit.GetPage(c.Request.Context(), uid, c.DefaultQuery("status", "all"), before)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
func (h *UserHandler) SaveAutoCredit(c *gin.Context) {
	uid, actor, ok := h.autoCreditAuthority(c)
	if !ok {
		return
	}
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil {
		response.BadRequest(c, "Invalid auto-credit configuration")
		return
	}
	// Require a version even for version 0. Null is never an implicit version.
	var version *int64
	if json.Unmarshal(fields["version"], &version) != nil || version == nil {
		response.BadRequest(c, "Configuration version required")
		return
	}
	for k := range fields {
		if k != "version" && k != "enabled" && k != "threshold" && k != "amount" && k != "cap" {
			response.BadRequest(c, "Unknown auto-credit field")
			return
		}
	}
	var enabled *bool
	if raw, exists := fields["enabled"]; exists {
		if json.Unmarshal(raw, &enabled) != nil || enabled == nil {
			response.BadRequest(c, "Enabled must be a boolean")
			return
		}
	}
	state, err := h.autoCredit.Save(c.Request.Context(), uid, actor, service.AutoCreditPatch{Version: *version, Enabled: enabled, Threshold: fields["threshold"], Amount: fields["amount"], Cap: fields["cap"]})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
func (h *UserHandler) SettleAutoCredit(c *gin.Context) {
	uid, actor, ok := h.autoCreditAuthority(c)
	if !ok {
		return
	}
	lid, err := strconv.ParseInt(c.Param("entry_id"), 10, 64)
	if err != nil || lid <= 0 {
		response.BadRequest(c, "Invalid receivable ID")
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if c.ShouldBindJSON(&req) != nil || !req.Confirm {
		response.BadRequest(c, "Full collection confirmation required; partial settlement is unsupported")
		return
	}
	state, err := h.autoCredit.Settle(c.Request.Context(), uid, lid, actor)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
