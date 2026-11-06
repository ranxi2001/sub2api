// Package accounttimerapi keeps the admin timer API independently testable.
package accounttimerapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accounttimer"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	Repository func() accounttimer.Repository
	Identity   func(*gin.Context) (int64, string)
}

func (h *Handler) access(c *gin.Context) (accounttimer.Repository, int64, int64, bool) {
	actor, role := h.Identity(c)
	if actor <= 0 || role != "admin" {
		response.Forbidden(c, "Administrator access required")
		return nil, 0, 0, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return nil, 0, 0, false
	}
	repo := h.Repository()
	if repo == nil {
		response.Error(c, http.StatusServiceUnavailable, "Account timers unavailable")
		return nil, 0, 0, false
	}
	return repo, id, actor, true
}
func accountTimerError(c *gin.Context, err error) {
	var typed *accounttimer.Error
	if !errors.As(err, &typed) {
		response.ErrorWithDetails(c, http.StatusInternalServerError, "Account timer operation failed", "TIMER_internal", nil)
		return
	}
	status := http.StatusBadRequest
	if typed.Code == "version" || typed.Code == "conflict" {
		status = http.StatusConflict
	}
	if typed.Code == "account_deleted" {
		status = http.StatusNotFound
	}
	response.ErrorWithDetails(c, status, "Account timer: "+typed.Code, "TIMER_"+typed.Code, nil)
}
func (h *Handler) List(c *gin.Context) {
	repo, id, _, ok := h.access(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	result, err := repo.List(ctx, id)
	if err != nil {
		accountTimerError(c, err)
		return
	}
	response.Success(c, result)
}
func (h *Handler) Preview(c *gin.Context) {
	repo, _, _, ok := h.access(c)
	if !ok {
		return
	}
	var req accounttimer.Input
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid timer request")
		return
	}
	preview, err := repo.Preview(req)
	if err != nil {
		accountTimerError(c, err)
		return
	}
	response.Success(c, preview)
}
func (h *Handler) Save(c *gin.Context) {
	repo, id, actor, ok := h.access(c)
	if !ok {
		return
	}
	var req accounttimer.SaveRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid timer request")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	task, err := repo.Save(ctx, id, actor, req)
	if err != nil {
		accountTimerError(c, err)
		return
	}
	response.Success(c, task)
}

func (h *Handler) SaveDailyWindow(c *gin.Context) {
	repo, id, actor, ok := h.access(c)
	if !ok {
		return
	}
	windowRepo, ok := repo.(accounttimer.DailyWindowRepository)
	if !ok {
		response.Error(c, http.StatusServiceUnavailable, "Daily account timers unavailable")
		return
	}
	var req accounttimer.DailyWindowRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid daily timer request")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := windowRepo.SaveDailyWindow(ctx, id, actor, req)
	if err != nil {
		accountTimerError(c, err)
		return
	}
	response.Success(c, snapshot)
}
func (h *Handler) Cancel(c *gin.Context) {
	repo, id, actor, ok := h.access(c)
	if !ok {
		return
	}
	version, err := strconv.ParseInt(c.Query("version"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid timer version")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err = repo.Cancel(ctx, id, actor, c.Param("action"), version); err != nil {
		accountTimerError(c, err)
		return
	}
	response.Success(c, gin.H{"cancelled": true})
}
