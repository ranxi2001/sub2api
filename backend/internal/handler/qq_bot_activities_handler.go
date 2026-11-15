package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/gin-gonic/gin"
)

func (h *QQBotHandler) GetInviteConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	cfg, err := h.bot.InviteConfig(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取邀请奖励配置失败")
		return
	}
	c.Header("ETag", `"`+qqbot.ConfigVersion(cfg)+`"`)
	c.Header("Cache-Control", "no-store")
	response.Success(c, cfg)
}
func (h *QQBotHandler) SaveInviteConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	cfg := qqbot.InviteConfig{RechargeRequirement: qqbot.RechargeRequirement{RechargeWindowDays: qqbot.DefaultRechargeWindowDays}}
	if c.ShouldBindJSON(&cfg) != nil {
		response.BadRequest(c, "邀请奖励配置格式不正确")
		return
	}
	if err := h.bot.SaveInviteConfig(qqbot.WithConfigVersion(c.Request.Context(), c.GetHeader("If-Match")), cfg); err != nil {
		qqBotConfigError(c, err)
		return
	}
	saved, readErr := h.bot.InviteConfig(c.Request.Context())
	if readErr != nil {
		response.InternalError(c, "配置已保存，重新加载失败")
		return
	}
	c.Header("ETag", `"`+qqbot.ConfigVersion(saved)+`"`)
	c.Header("Cache-Control", "no-store")
	response.Success(c, saved)
}
func (h *QQBotHandler) GetCheckinConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	cfg, err := h.bot.CheckinConfig(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取签到配置失败")
		return
	}
	c.Header("ETag", `"`+qqbot.ConfigVersion(cfg)+`"`)
	for _, section := range []string{"checkin", "tasks", "periodic_tasks"} {
		c.Header("X-QQBot-"+section+"-Version", qqbot.CheckinSectionVersion(cfg, section))
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, cfg)
}
func (h *QQBotHandler) SaveCheckinConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	cfg := qqbot.CheckinConfig{RechargeRequirement: qqbot.RechargeRequirement{RechargeWindowDays: qqbot.DefaultRechargeWindowDays}}
	if c.ShouldBindJSON(&cfg) != nil {
		response.BadRequest(c, "签到配置格式不正确")
		return
	}
	if err := h.bot.SaveCheckinConfig(qqbot.WithCheckinSection(qqbot.WithConfigVersion(c.Request.Context(), c.GetHeader("If-Match")), c.GetHeader("X-QQBot-Section")), cfg); err != nil {
		qqBotConfigError(c, err)
		return
	}
	saved, readErr := h.bot.CheckinConfig(c.Request.Context())
	if readErr != nil {
		response.InternalError(c, "配置已保存，重新加载失败")
		return
	}
	c.Header("ETag", `"`+qqbot.ConfigVersion(saved)+`"`)
	for _, section := range []string{"checkin", "tasks", "periodic_tasks"} {
		c.Header("X-QQBot-"+section+"-Version", qqbot.CheckinSectionVersion(saved, section))
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, saved)
}
func (h *QQBotHandler) GetTransferConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	cfg, err := h.bot.TransferConfig(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取转账配置失败")
		return
	}
	c.Header("ETag", `"`+qqbot.ConfigVersion(cfg)+`"`)
	c.Header("Cache-Control", "no-store")
	response.Success(c, cfg)
}
func (h *QQBotHandler) SaveTransferConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var cfg qqbot.TransferConfig
	if c.ShouldBindJSON(&cfg) != nil {
		response.BadRequest(c, "转账配置格式不正确")
		return
	}
	if err := h.bot.SaveTransferConfig(qqbot.WithConfigVersion(c.Request.Context(), c.GetHeader("If-Match")), cfg); err != nil {
		qqBotConfigError(c, err)
		return
	}
	saved, readErr := h.bot.TransferConfig(c.Request.Context())
	if readErr != nil {
		response.InternalError(c, "配置已保存，重新加载失败")
		return
	}
	c.Header("ETag", `"`+qqbot.ConfigVersion(saved)+`"`)
	c.Header("Cache-Control", "no-store")
	response.Success(c, saved)
}

func (h *QQBotHandler) GetLotteryConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	cfg, err := h.bot.LotteryConfig(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取抽奖配置失败")
		return
	}
	c.Header("ETag", `"`+qqbot.ConfigVersion(cfg)+`"`)
	c.Header("Cache-Control", "no-store")
	response.Success(c, cfg)
}
func (h *QQBotHandler) SaveLotteryConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	var cfg qqbot.LotteryConfig
	if c.ShouldBindJSON(&cfg) != nil {
		response.BadRequest(c, "抽奖配置格式不正确")
		return
	}
	if err := h.bot.SaveLotteryConfig(qqbot.WithConfigVersion(c.Request.Context(), c.GetHeader("If-Match")), cfg); err != nil {
		qqBotConfigError(c, err)
		return
	}
	saved, readErr := h.bot.LotteryConfig(c.Request.Context())
	if readErr != nil {
		response.InternalError(c, "配置已保存，重新加载失败")
		return
	}
	c.Header("ETag", `"`+qqbot.ConfigVersion(saved)+`"`)
	c.Header("Cache-Control", "no-store")
	response.Success(c, saved)
}
func (h *QQBotHandler) LotteryState(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	uid, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || uid <= 0 {
		response.BadRequest(c, "网站用户 ID 不正确")
		return
	}
	state, err := h.bot.LotteryState(c.Request.Context(), uid)
	if err != nil {
		response.BadRequest(c, "读取抽奖次数失败，请确认网站用户 ID")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, state)
}
func (h *QQBotHandler) ListLotteryDraws(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	search := strings.TrimSpace(c.Query("search"))
	if err != nil || page < 1 || page > 1000000 || len(search) > 254 {
		response.BadRequest(c, "分页或搜索参数不正确")
		return
	}
	out, err := h.bot.ListLotteryDraws(c.Request.Context(), search, page, 20)
	if err != nil {
		response.InternalError(c, "读取抽奖记录失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
