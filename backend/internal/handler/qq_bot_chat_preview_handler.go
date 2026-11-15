package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/gin-gonic/gin"
)

func (h *QQBotHandler) PreviewChat(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var req qqbot.ChatPreviewRequest
	var extra any
	if decoder.Decode(&req) != nil || decoder.Decode(&extra) != io.EOF {
		response.BadRequest(c, "试问请求格式不正确")
		return
	}
	out, err := h.bot.PreviewChat(c.Request.Context(), req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, out)
}
