package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
)

func (h *ChannelHandler) UpstreamChannelService() *service.ChannelService { return h.channelService }
func upstreamChannelError(c *gin.Context, e error) {
	if errors.Is(e, service.ErrUpstreamChannelValidation) || errors.Is(e, service.ErrUpstreamChannelConflict) || errors.Is(e, service.ErrUpstreamChannelNotFound) {
		response.ErrorFrom(c, e)
	} else {
		response.InternalError(c, "上游渠道操作失败，请重试")
	}
}
func (h *ChannelHandler) upstreamChannelRepo(c *gin.Context) (service.UpstreamChannelRepository, bool) {
	r, e := h.channelService.UpstreamChannelRepository()
	if e != nil {
		upstreamChannelError(c, e)
		return nil, false
	}
	return r, true
}
func (h *ChannelHandler) ListUpstreamChannels(c *gin.Context) {
	r, ok := h.upstreamChannelRepo(c)
	if !ok {
		return
	}
	p, s := response.ParsePagination(c)
	out, e := r.ListUpstreamChannels(c.Request.Context(), c.Query("search"), p, s)
	if e != nil {
		upstreamChannelError(c, e)
		return
	}
	response.Success(c, out)
}
func (h *ChannelHandler) GetUpstreamChannel(c *gin.Context) {
	r, ok := h.upstreamChannelRepo(c)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	out, e := r.GetUpstreamChannel(c.Request.Context(), id)
	if e != nil {
		upstreamChannelError(c, e)
		return
	}
	response.Success(c, out)
}
func auditUpstreamChannel(c *gin.Context, action string, id int64, version any, targets any) {
	raw, _ := json.Marshal(targets)
	hash := sha256.Sum256(raw)
	middleware.SetAuditAction(c, "admin.upstream_channel."+action)
	middleware.SetAuditExtra(c, map[string]any{"source": "upstream_channel", "target_channel_id": id, "config_version": version, "filter_hash": hex.EncodeToString(hash[:])})
}
func (h *ChannelHandler) MutateUpstreamChannel(c *gin.Context) {
	r, ok := h.upstreamChannelRepo(c)
	if !ok {
		return
	}
	var m service.UpstreamChannelMutation
	if c.ShouldBindJSON(&m) != nil {
		upstreamChannelError(c, service.ErrUpstreamChannelValidation)
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var out *service.UpstreamChannel
	var e error
	version := int64(0)
	if m.Revision != nil {
		version = *m.Revision
	}
	auditUpstreamChannel(c, strings.ToLower(c.Request.Method), id, version, id)
	switch c.Request.Method {
	case "POST":
		out, e = r.CreateUpstreamChannel(c.Request.Context(), m)
	case "PUT":
		out, e = r.UpdateUpstreamChannel(c.Request.Context(), id, m)
	case "DELETE":
		e = r.DeleteUpstreamChannel(c.Request.Context(), id, m)
	}
	if e != nil {
		upstreamChannelError(c, e)
		return
	}
	if out == nil {
		h.profitReports.clear()
		response.Success(c, gin.H{"deleted": id})
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"target_channel_id": out.ID, "config_version": out.Revision})
	h.profitReports.clear()
	response.Success(c, out)
}
func (h *ChannelHandler) ReadUpstreamChannelBindings(c *gin.Context) {
	r, ok := h.upstreamChannelRepo(c)
	if !ok {
		return
	}
	parts := strings.Split(c.Query("account_ids"), ",")
	ids := make([]int64, 0, len(parts))
	for _, v := range parts {
		id, e := strconv.ParseInt(v, 10, 64)
		if e != nil {
			upstreamChannelError(c, service.ErrUpstreamChannelValidation)
			return
		}
		ids = append(ids, id)
	}
	out, e := r.ReadUpstreamChannelBindings(c.Request.Context(), ids)
	if e != nil {
		upstreamChannelError(c, e)
		return
	}
	response.Success(c, gin.H{"items": out})
}
func (h *ChannelHandler) BindUpstreamChannelAccounts(c *gin.Context) {
	r, ok := h.upstreamChannelRepo(c)
	if !ok {
		return
	}
	var m service.UpstreamChannelBindingsMutation
	if c.ShouldBindJSON(&m) != nil {
		upstreamChannelError(c, service.ErrUpstreamChannelValidation)
		return
	}
	if e := service.ValidateUpstreamChannelBindings(m); e != nil {
		upstreamChannelError(c, e)
		return
	}
	auditUpstreamChannel(c, "bind", m.ChannelID, 0, m.Bindings)
	middleware.SetAuditExtra(c, map[string]any{"requested_count": len(m.Bindings)})
	out, e := r.BindUpstreamChannelAccounts(c.Request.Context(), m)
	if e != nil {
		upstreamChannelError(c, e)
		return
	}
	h.profitReports.clear()
	response.Success(c, gin.H{"items": out})
}
func (h *ChannelHandler) UpstreamChannelMembers(c *gin.Context) {
	r, ok := h.upstreamChannelRepo(c)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	p, s := response.ParsePagination(c)
	out, e := r.UpstreamChannelMembers(c.Request.Context(), id, p, s)
	if e != nil {
		upstreamChannelError(c, e)
		return
	}
	response.Success(c, out)
}
