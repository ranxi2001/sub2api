package handler

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

func (h *QQBotHandler) SetupOptions(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "请先登录")
		return
	}
	out, err := h.bot.SetupOptions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) SetupChecks(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "请先登录")
		return
	}
	out, err := h.bot.SetupChecks(c.Request.Context(), subject.UserID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) SaveSetup(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "请先登录")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	var req qqbot.SetupRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "配置格式不正确")
		return
	}
	out, err := h.bot.SaveSetup(c.Request.Context(), subject.UserID, req)
	if err != nil {
		qqBotConfigError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}

func (h *QQBotHandler) SetupProbe(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 220*time.Second)
	defer cancel()
	result, err := h.bot.SetupProbe(ctx, c.Param("kind"))
	if err != nil {
		qqBotConfigError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, result)
}

func (h *QQBotHandler) ActivityTemplates(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	out, err := h.bot.ActivityTemplates(c.Request.Context())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}
func (h *QQBotHandler) ApplyActivityTemplate(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var req qqbot.ActivityTemplateRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "模板参数不正确")
		return
	}
	_, err := h.bot.ApplyActivityTemplate(c.Request.Context(), req)
	if err != nil {
		qqBotConfigError(c, err)
		return
	}
	out, err := h.bot.Config(c.Request.Context())
	if err != nil {
		response.InternalError(c, "模板已保存，重新读取配置失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out.Public())
}
