package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/gin-gonic/gin"
)

func (h *QQBotHandler) ListMembers(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	f := qqbot.MemberDirectoryFilter{Page: 1, PageSize: 20}
	if c.ShouldBindQuery(&f) != nil {
		response.BadRequest(c, "成员目录筛选参数不正确")
		return
	}
	out, err := h.bot.ListMembers(c.Request.Context(), f)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
