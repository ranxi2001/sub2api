package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *QQBotHandler) GroupInfo(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	out, err := h.bot.GroupInfo(c.Request.Context(), c.Query("app_id"), c.Param("group_id"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
