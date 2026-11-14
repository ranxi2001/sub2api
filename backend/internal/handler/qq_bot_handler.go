package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

type QQBotHandler struct{ bot *qqbot.Service }

func NewQQBotHandler(bot *qqbot.Service) *QQBotHandler { return &QQBotHandler{bot: bot} }

func (h *QQBotHandler) Webhook(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.Status(http.StatusRequestEntityTooLarge)
		return
	}
	if h == nil || h.bot == nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	cfg, err := h.bot.Config(c.Request.Context())
	if err != nil || cfg.AppSecret == "" {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	if c.GetHeader("X-Bot-Appid") != cfg.AppID {
		c.Status(http.StatusUnauthorized)
		return
	}
	var p qqbot.Payload
	if json.Unmarshal(raw, &p) != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	// QQ's initial callback challenge is unsigned. No events or model calls are
	// accepted through this branch; it only proves possession of the AppSecret.
	if p.Op == 13 {
		result, err := qqbot.Challenge(cfg.AppSecret, p.Data)
		if err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	if !qqbot.VerifySignature(cfg.AppSecret, c.GetHeader("X-Signature-Timestamp"), c.GetHeader("X-Signature-Ed25519"), raw, time.Now()) {
		c.Status(http.StatusUnauthorized)
		return
	}
	if p.Op == 0 {
		if err = h.bot.Enqueue(c.Request.Context(), cfg, p); err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"op": 12})
}
func (h *QQBotHandler) GetConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	cfg, err := h.bot.Config(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("ETag", `"`+qqbot.ConfigVersion(cfg)+`"`)
	response.Success(c, cfg.Public())
}
func (h *QQBotHandler) SaveConfig(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	var cfg qqbot.Config
	if c.ShouldBindJSON(&cfg) != nil {
		response.BadRequest(c, "配置格式不正确")
		return
	}
	saved, err := h.bot.SaveBasicConfig(qqbot.WithConfigVersion(c.Request.Context(), c.GetHeader("If-Match")), cfg)
	if err != nil {
		qqBotConfigError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, saved)
}
func (h *QQBotHandler) Runtime(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	cfg, err := h.bot.Config(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	r, err := h.bot.Runtime(c.Request.Context(), cfg)
	if err != nil {
		response.InternalError(c, "读取机器人运行记录失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, r)
}

func (h *QQBotHandler) DeleteDiscoveredGroup(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var req struct {
		GroupID string `json:"group_id"`
	}
	if c.ShouldBindJSON(&req) != nil || req.GroupID == "" || len(req.GroupID) > 128 || strings.ContainsAny(req.GroupID, " \r\n\t") {
		response.BadRequest(c, "群 OpenID 格式不正确")
		return
	}
	cfg, err := h.bot.Config(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取机器人配置失败")
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"qq_app_id": cfg.AppID, "qq_group_openid": req.GroupID})
	if err = h.bot.DeleteDiscoveredGroup(c.Request.Context(), cfg.AppID, req.GroupID); err != nil {
		response.InternalError(c, "删除自动识别群失败")
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

func requireQQBotAdmin(c *gin.Context) bool {
	role, ok := middleware.GetUserRoleFromContext(c)
	if !ok || role != "admin" {
		response.Forbidden(c, "仅管理员可管理 QQ 机器人")
		return false
	}
	return true
}

func (h *QQBotHandler) ListBindings(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, sizeErr := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	search := strings.TrimSpace(c.Query("search"))
	if err != nil || sizeErr != nil || page < 1 || page > 1000000 || pageSize < 1 || pageSize > 100 || len(search) > 254 {
		response.BadRequest(c, "分页或搜索参数不正确")
		return
	}
	out, err := h.bot.ListBindings(c.Request.Context(), search, page, pageSize)
	if err != nil {
		response.InternalError(c, "读取 QQ 绑定列表失败")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, out)
}

func (h *QQBotHandler) Unbind(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	user, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	var req qqbot.UnbindRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	if err != nil || user <= 0 || c.ShouldBindJSON(&req) != nil || req.AppID == "" || len(req.AppID) > 128 || req.MemberID == "" || len(req.MemberID) > 128 || req.CreatedAt.IsZero() {
		response.BadRequest(c, "请确认要解除的绑定身份")
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"target_user_id": user, "qq_app_id": req.AppID, "qq_member_id": req.MemberID})
	if err = h.bot.Unbind(c.Request.Context(), user, req); err != nil {
		if errors.Is(err, qqbot.ErrBindingChanged) {
			response.Error(c, http.StatusConflict, err.Error())
			return
		}
		response.InternalError(c, "解除绑定失败，请重试")
		return
	}
	response.Success(c, gin.H{"unbound": true})
}
func (h *QQBotHandler) Probe(c *gin.Context) {
	if !requireQQBotAdmin(c) {
		return
	}
	if !requireQQBotProbeConfirmation(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 220*time.Second)
	defer cancel()
	result, err := h.bot.Probe(ctx, c.Param("kind"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"message": result})
}
func (h *QQBotHandler) Binding(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "请先登录")
		return
	}
	cfg, err := h.bot.Config(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取机器人配置失败")
		return
	}
	b, p, err := h.bot.Binding(c.Request.Context(), cfg.AppID, subject.UserID)
	if err != nil {
		response.InternalError(c, "读取绑定状态失败")
		return
	}
	response.Success(c, gin.H{"enabled": cfg.Enabled, "binding": b, "pending": p})
}
func (h *QQBotHandler) CreateBindingCode(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "请先登录")
		return
	}
	r, err := h.bot.CreateBindingCode(c.Request.Context(), subject.UserID)
	if err != nil {
		response.BadRequest(c, "无法生成绑定码，请检查机器人是否启用")
		return
	}
	response.Success(c, r)
}
func (h *QQBotHandler) ConfirmBinding(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "请先登录")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var req struct {
		MemberID string `json:"member_id"`
	}
	if c.ShouldBindJSON(&req) != nil || req.MemberID == "" || len(req.MemberID) > 128 {
		response.BadRequest(c, "请确认待绑定 QQ 身份")
		return
	}
	if err := h.bot.ConfirmBinding(c.Request.Context(), subject.UserID, req.MemberID); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	// Binding remains successful if a reward is temporarily unavailable; the
	// group command can safely retry settlement using the same unique ledger key.
	_ = h.bot.SettleInvites(c.Request.Context(), req.MemberID)
	response.Success(c, gin.H{"bound": true})
}

func (h *QQBotHandler) PreviewEmailBinding(c *gin.Context) { h.emailBinding(c, false) }
func (h *QQBotHandler) ConfirmEmailBinding(c *gin.Context) { h.emailBinding(c, true) }

func (h *QQBotHandler) emailBinding(c *gin.Context, confirm bool) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var req struct {
		Token    string `json:"token"`
		MemberID string `json:"member_id"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "绑定请求格式不正确")
		return
	}
	if !confirm {
		p, err := h.bot.PreviewEmailBinding(c.Request.Context(), req.Token)
		if err != nil {
			response.BadRequest(c, "绑定链接无效、已过期或暂不可用，请回群内重新发送绑定指令")
			return
		}
		response.Success(c, p)
		return
	}
	member, err := h.bot.ConfirmEmailBinding(c.Request.Context(), req.Token, req.MemberID)
	if err != nil {
		response.BadRequest(c, "绑定链接无效、已过期或已使用，请回群内重新发送绑定指令")
		return
	}
	_ = h.bot.SettleInvites(c.Request.Context(), member)
	response.Success(c, gin.H{"bound": true})
}

func (h *QQBotHandler) PreviewTransfer(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var req struct {
		Token string `json:"token"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Token == "" {
		response.BadRequest(c, "转账令牌格式不正确")
		return
	}
	p, err := h.bot.PreviewTransfer(c.Request.Context(), req.Token)
	if err != nil {
		response.BadRequest(c, "转账链接无效、已过期或转账功能未启用")
		return
	}
	response.Success(c, p)
}

func (h *QQBotHandler) ConfirmTransfer(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var req struct {
		Token string `json:"token"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Token == "" {
		response.BadRequest(c, "转账令牌格式不正确")
		return
	}
	if err := h.bot.ConfirmTransfer(c.Request.Context(), req.Token); err != nil {
		if errors.Is(err, qqbot.ErrTransferInvalid) {
			response.BadRequest(c, "转账链接无效、已过期或已使用，请重新发起转账")
			return
		}
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"transferred": true})
}
