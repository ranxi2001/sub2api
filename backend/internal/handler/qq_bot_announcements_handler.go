package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (h *QQBotHandler) SendAnnouncement(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var req qqbot.AnnouncementRequest
	var extra any
	if decoder.Decode(&req) != nil || decoder.Decode(&extra) != io.EOF {
		response.BadRequest(c, "公告请求格式不正确")
		return
	}
	out, err := h.bot.SendAnnouncement(c.Request.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, qqbot.ErrAnnouncementConflict):
			response.Error(c, http.StatusConflict, err.Error())
		case errors.Is(err, qqbot.ErrAnnouncementInvalid), errors.Is(err, qqbot.ErrDisabled):
			response.BadRequest(c, err.Error())
		default:
			response.InternalError(c, "公告入队失败，请使用原公告重试")
		}
		return
	}
	result := "queued"
	if out.Duplicate {
		result = "duplicate"
	}
	middleware.SetAuditExtra(c, map[string]any{"qq_app_id": req.AppID, "event_id": out.BatchID, "group_count": len(out.Jobs), "result": result})
	response.Success(c, out)
}
