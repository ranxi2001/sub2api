package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func priorityLifecycleScope(c *gin.Context) (service.PriorityLifecycleScope, int64, bool) {
	scope := service.PriorityLifecycleScope{Model: strings.TrimSpace(c.Query("model")), Endpoint: service.PriorityLifecycleEndpoint(c.Query("endpoint"))}
	if measurement := c.Query("measurement_schema"); measurement != "" {
		if len(measurement) > 120 {
			response.BadRequest(c, "invalid measurement_schema")
			return scope, 0, false
		}
		scope.MeasurementSchema = measurement
	}
	if scope.Model == "" || len(scope.Model) > 200 || c.Query("endpoint") == "" {
		response.BadRequest(c, "model and explicit ingress endpoint are required")
		return scope, 0, false
	}
	if raw := c.Query("group_id"); raw != "" && raw != "ungrouped" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "invalid group_id")
			return scope, 0, false
		}
		scope.GroupID = &id
	}
	kind, err := strconv.ParseInt(c.Query("request_type"), 10, 16)
	if err != nil || !service.RequestType(kind).IsValid() {
		response.BadRequest(c, "explicit request_type required")
		return scope, 0, false
	}
	scope.RequestType = service.RequestType(kind)
	after := int64(0)
	if raw := c.Query("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			response.BadRequest(c, "invalid after sequence")
			return scope, 0, false
		}
	}
	return scope, after, true
}

func (h *AccountHandler) PriorityLifecycleState(c *gin.Context) {
	scope, _, ok := priorityLifecycleScope(c)
	if !ok {
		return
	}
	state, err := h.openAIGatewayService.PriorityLifecycleState(c.Request.Context(), scope)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority lifecycle state unavailable")
		return
	}
	response.Success(c, state)
}
func (h *AccountHandler) PriorityLifecycleDecisions(c *gin.Context) {
	h.priorityLifecycleHistory(c, false)
}
func (h *AccountHandler) PriorityLifecycleEvents(c *gin.Context) { h.priorityLifecycleHistory(c, true) }
func (h *AccountHandler) QueuePriorityLifecycleRefresh(c *gin.Context) {
	scope, _, ok := priorityLifecycleScope(c)
	if !ok {
		return
	}
	job, err := h.openAIGatewayService.QueuePriorityLifecycleRefresh(c.Request.Context(), scope)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority collection queue unavailable")
		return
	}
	response.Success(c, job)
}
func (h *AccountHandler) PriorityLifecycleRefreshJob(c *gin.Context) {
	id := c.Param("job_id")
	if _, err := uuid.Parse(id); err != nil {
		response.BadRequest(c, "invalid job_id")
		return
	}
	job, err := h.openAIGatewayService.ReadPriorityLifecycleRefresh(c.Request.Context(), id)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority collection job unavailable")
		return
	}
	response.Success(c, job)
}
func (h *AccountHandler) priorityLifecycleHistory(c *gin.Context, events bool) {
	scope, after, ok := priorityLifecycleScope(c)
	if !ok {
		return
	}
	history, err := h.openAIGatewayService.PriorityLifecycleHistory(c.Request.Context(), scope, after, events)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Priority lifecycle history unavailable")
		return
	}
	response.Success(c, gin.H{"items": history})
}
