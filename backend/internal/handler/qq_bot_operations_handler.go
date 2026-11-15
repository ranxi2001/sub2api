package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/gin-gonic/gin"
)

func (h *QQBotHandler) SaveConfigSection(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	var req struct {
		Version string          `json:"version"`
		Config  json.RawMessage `json:"config"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "配置格式不正确")
		return
	}
	var config qqbot.Config
	var fields map[string]json.RawMessage
	if json.Unmarshal(req.Config, &config) != nil || json.Unmarshal(req.Config, &fields) != nil || fields == nil {
		response.BadRequest(c, "配置格式不正确")
		return
	}
	_, hasBudget := fields["reward_budget_daily"]
	out, err := h.bot.SaveConfigSection(c.Request.Context(), c.Param("section"), req.Version, config, !hasBudget)
	if errors.Is(err, qqbot.ErrConfigConflict) {
		response.Error(c, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) ConfigHistory(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	out, err := h.bot.ConfigHistory(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取配置历史失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) Overview(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	out, err := h.bot.Overview(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取运行总览失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) ListJobs(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	f := qqbot.JobFilter{Page: 1, PageSize: 20}
	if c.ShouldBindQuery(&f) != nil {
		response.BadRequest(c, "筛选参数不正确")
		return
	}
	out, err := h.bot.ListJobs(c.Request.Context(), f)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) Diagnose(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	out, err := h.bot.Diagnose(c.Request.Context(), c.Query("group_id"), c.Query("member_id"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) Commands(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	out, err := h.bot.Commands(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取指令失败")
		return
	}
	response.Success(c, out)
}

func (h *QQBotHandler) PreviewActivities(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	uid, err := strconv.ParseInt(c.Query("user_id"), 10, 64)
	if err != nil || uid <= 0 {
		response.BadRequest(c, "网站用户 ID 不正确")
		return
	}
	out, err := h.bot.PreviewActivities(c.Request.Context(), uid)
	if err != nil {
		response.BadRequest(c, "读取活动预览失败，请检查用户及数据库状态")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) TransferHistory(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		response.BadRequest(c, "分页参数不正确")
		return
	}
	out, err := h.bot.TransferHistory(c.Request.Context(), c.Query("search"), page)
	if err != nil {
		response.BadRequest(c, "读取转账记录失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}

func qqBotConfigError(c *gin.Context, err error) {
	if errors.Is(err, qqbot.ErrConfigConflict) {
		response.Error(c, http.StatusConflict, err.Error())
		return
	}
	response.BadRequest(c, err.Error())
}

func (h *QQBotHandler) ListModerationRecords(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	f := qqbot.JobFilter{Page: 1, PageSize: 20}
	if c.ShouldBindQuery(&f) != nil {
		response.BadRequest(c, "筛选参数不正确")
		return
	}
	out, err := h.bot.ListModerationRecords(c.Request.Context(), f)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
