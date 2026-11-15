package handler

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *QQBotHandler) ListActivityRecords(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	kind := c.Param("kind")
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, sizeErr := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	search := strings.TrimSpace(c.Query("search"))
	if (kind != "checkin" && kind != "lottery" && kind != "invite") || pageErr != nil || sizeErr != nil || page < 1 || page > 1000000 || size < 1 || size > 100 || len(search) > 254 {
		response.BadRequest(c, "活动类型、分页或搜索参数不正确")
		return
	}
	out, err := h.bot.ListActivityRecords(c.Request.Context(), kind, search, page, size)
	if err != nil {
		response.InternalError(c, "读取活动记录失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
