package admin

import (
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"strconv"
	"time"
)

func (h *AccountHandler) performanceAvailable(c *gin.Context) bool {
	if h.performance == nil {
		response.Error(c, 503, "Performance monitor unavailable")
		return false
	}
	return true
}
func performanceActor(c *gin.Context) int64 {
	if s, ok := middleware.GetAuthSubjectFromContext(c); ok {
		return s.UserID
	}
	return 0
}
func (h *AccountHandler) performanceAccount(c *gin.Context) (int64, bool) {
	if !h.performanceAvailable(c) {
		return 0, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account id")
		return 0, false
	}
	if _, err = h.adminService.GetAccount(c.Request.Context(), id); err != nil {
		response.Error(c, 404, "Account not found")
		return 0, false
	}
	return id, true
}
func (h *AccountHandler) AccountPerformanceBatch(c *gin.Context) {
	if !h.performanceAvailable(c) {
		return
	}
	var req struct {
		IDs []int64 `json:"account_ids"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.IDs) == 0 || len(req.IDs) > 100 {
		response.BadRequest(c, "Expected 1-100 account ids")
		return
	}
	for _, id := range req.IDs {
		if id <= 0 {
			response.BadRequest(c, "Invalid account id")
			return
		}
	}
	data, err := h.performance.Repo.Read(c.Request.Context(), req.IDs, time.Now())
	if err != nil {
		response.Error(c, 503, "Performance metrics unavailable")
		return
	}
	response.Success(c, data)
}
func performanceWriteResult(c *gin.Context, err error) {
	if errors.Is(err, service.ErrPerformanceConflict) {
		response.Error(c, 409, err.Error())
		return
	}
	if err != nil {
		response.Error(c, 503, "Performance monitor update failed")
		return
	}
	response.Success(c, gin.H{"ok": true})
}
func (h *AccountHandler) SaveAccountPerformance(c *gin.Context) {
	id, ok := h.performanceAccount(c)
	if !ok {
		return
	}
	var req struct {
		Config   service.PerformanceConfig `json:"config"`
		Revision *int64                    `json:"revision"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Revision == nil || *req.Revision < 0 {
		response.BadRequest(c, "Revision and valid configuration required")
		return
	}
	if err := req.Config.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	performanceWriteResult(c, h.performance.Repo.Save(c.Request.Context(), id, req.Config, *req.Revision, performanceActor(c)))
}
func (h *AccountHandler) ReleaseAccountPerformance(c *gin.Context) {
	id, ok := h.performanceAccount(c)
	if !ok {
		return
	}
	var req struct {
		Revision   *int64 `json:"revision"`
		Generation *int64 `json:"pause_generation"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Revision == nil || req.Generation == nil || *req.Revision < 0 || *req.Generation <= 0 {
		response.BadRequest(c, "Revision and pause generation required")
		return
	}
	performanceWriteResult(c, h.performance.Repo.Release(c.Request.Context(), id, *req.Revision, *req.Generation, performanceActor(c)))
}
func (h *AccountHandler) AccountPerformanceEvents(c *gin.Context) {
	id, ok := h.performanceAccount(c)
	if !ok {
		return
	}
	events, err := h.performance.Repo.Events(c.Request.Context(), id)
	if err != nil {
		response.Error(c, 503, "Performance audit unavailable")
		return
	}
	response.Success(c, events)
}
