package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/gin-gonic/gin"
)

func rewardCodeError(c *gin.Context, err error) {
	if errors.Is(err, qqbot.ErrRewardCodeRequest) {
		response.BadRequest(c, err.Error())
		return
	}
	response.InternalError(c, "奖励管理操作失败，请稍后重试")
}
func (h *QQBotHandler) ListRewardCodes(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		response.BadRequest(c, "分页参数不正确")
		return
	}
	out, err := h.bot.ListRewardCodes(c.Request.Context(), strings.TrimSpace(c.Query("search")), c.Query("status"), page)
	if err != nil {
		rewardCodeError(c, err)
		return
	}
	response.Success(c, out)
}
func (h *QQBotHandler) UpdateRewardCode(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		response.BadRequest(c, "奖励记录 ID 不正确")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var req struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "操作参数格式不正确")
		return
	}
	if err = h.bot.UpdateRewardCode(c.Request.Context(), id, req.Action, req.Note); err != nil {
		rewardCodeError(c, err)
		return
	}
	response.Success(c, gin.H{"success": true})
}
