package handler

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func isObserverUsageRequest(c *gin.Context) bool {
	role, _ := middleware.GetUserRoleFromContext(c)
	return role == service.RoleObserver
}

func (h *UsageHandler) ObserverFilterOptions(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !isObserverUsageRequest(c) {
		response.Forbidden(c, "Observer role required")
		return
	}
	kind := c.Query("kind")
	if kind != "api_key" && kind != "account" && kind != "group" {
		response.BadRequest(c, "Invalid filter kind")
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if len(query) > 200 {
		response.BadRequest(c, "Search query too long")
		return
	}
	includeErrors := h.settingService != nil && h.settingService.IsUserErrorViewAllowed(c.Request.Context())
	items, err := h.usageService.OwnUsageFilterOptions(c.Request.Context(), subject.UserID, kind, query, includeErrors)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// These extra dimensions narrow the authenticated user's records; user_id is
// always supplied by parseUserUsageFilters, never by the query string.
func applyObserverUsageFilters(c *gin.Context, filters *usagestats.UsageLogFilters) bool {
	if !isObserverUsageRequest(c) {
		return true
	}
	if value := strings.TrimSpace(c.Query("account_id")); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 0 {
			response.BadRequest(c, "Invalid account_id")
			return false
		}
		filters.AccountID = id
	}
	filters.RequestID = strings.TrimSpace(c.Query("request_id"))
	if value := strings.TrimSpace(c.Query("upstream_model_mismatch")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			response.BadRequest(c, "Invalid upstream_model_mismatch")
			return false
		}
		filters.UpstreamModelMismatch = &parsed
	}
	if value := strings.TrimSpace(c.Query("exact_total")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			response.BadRequest(c, "Invalid exact_total")
			return false
		}
		filters.ExactTotal = parsed
	}
	return true
}
