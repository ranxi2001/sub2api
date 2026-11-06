package admin

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/accounttimer"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin/accounttimerapi"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func accountManualSchedulingContext(c *gin.Context, explicit bool) context.Context {
	ctx := c.Request.Context()
	if explicit {
		if subject, ok := middleware.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
			return accounttimer.WithManualIntent(ctx, subject.UserID)
		}
	}
	return ctx
}
func (h *AccountHandler) schedulableTimerAPI() *accounttimerapi.Handler {
	return &accounttimerapi.Handler{
		Repository: func() accounttimer.Repository {
			if provider, ok := h.adminService.(interface {
				SchedulableTimers() accounttimer.Repository
			}); ok {
				return provider.SchedulableTimers()
			}
			return nil
		},
		Identity: func(c *gin.Context) (int64, string) {
			subject, _ := middleware.GetAuthSubjectFromContext(c)
			role, _ := middleware.GetUserRoleFromContext(c)
			return subject.UserID, role
		},
	}
}
func (h *AccountHandler) ListSchedulableTimers(c *gin.Context)   { h.schedulableTimerAPI().List(c) }
func (h *AccountHandler) PreviewSchedulableTimer(c *gin.Context) { h.schedulableTimerAPI().Preview(c) }
func (h *AccountHandler) SaveSchedulableTimer(c *gin.Context)    { h.schedulableTimerAPI().Save(c) }
func (h *AccountHandler) SaveDailySchedulableWindow(c *gin.Context) {
	h.schedulableTimerAPI().SaveDailyWindow(c)
}
func (h *AccountHandler) CancelSchedulableTimer(c *gin.Context) { h.schedulableTimerAPI().Cancel(c) }
