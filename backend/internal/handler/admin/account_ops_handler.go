package admin

import (
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

type AccountOpsHandler struct {
	svc   *service.AccountOpsService
	email *service.EmailService
}

func NewAccountOpsHandler(svc *service.AccountOpsService, email *service.EmailService) *AccountOpsHandler {
	return &AccountOpsHandler{svc: svc, email: email}
}
func (h *AccountOpsHandler) GetConfig(c *gin.Context) {
	cfg, err := h.svc.GetConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Account alert configuration unavailable")
		return
	}
	ready := false
	if h.email != nil {
		smtp, smtpErr := h.email.GetSMTPConfig(c.Request.Context())
		ready = smtpErr == nil && smtp != nil && smtp.Host != "" && smtp.From != ""
	}
	dropped, failures := h.svc.RuntimeCounters()
	response.Success(c, gin.H{"config": cfg, "smtp_configured": ready, "dropped_signals": dropped, "storage_failures": failures, "encryption_key_configured": h.svc.EncryptionKeyConfigured()})
}
func (h *AccountOpsHandler) SaveConfig(c *gin.Context) {
	var cfg service.AccountOpsConfig
	if c.ShouldBindJSON(&cfg) != nil {
		response.BadRequest(c, "Invalid alert configuration")
		return
	}
	if err := h.svc.SaveConfig(c.Request.Context(), cfg); err != nil {
		if errors.Is(err, service.ErrAccountOpsConfigValidation) {
			response.BadRequest(c, err.Error())
			return
		}
		response.Error(c, http.StatusServiceUnavailable, "Could not save account alert configuration")
		return
	}
	saved, err := h.svc.GetConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Configuration unavailable")
		return
	}
	response.Success(c, saved)
}
func (h *AccountOpsHandler) List(c *gin.Context) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		response.BadRequest(c, "Invalid offset")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 100 {
		response.BadRequest(c, "Invalid limit")
		return
	}
	events, err := h.svc.List(c.Request.Context(), offset, limit+1)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Account alerts unavailable")
		return
	}
	more := len(events) > limit
	if more {
		events = events[:limit]
	}
	response.Success(c, gin.H{"items": events, "has_more": more})
}

func (h *AccountOpsHandler) BalanceAccounts(c *gin.Context) {
	items, err := h.svc.BalanceAccounts(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Balance accounts unavailable")
		return
	}
	response.Success(c, gin.H{"items": items})
}
func (h *AccountOpsHandler) TestWebhook(c *gin.Context) {
	if err := h.svc.TestWebhook(c.Request.Context(), c.Param("id")); err != nil {
		response.BadRequest(c, "Saved robot test failed; check configuration and provider settings")
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *AccountOpsHandler) ThresholdAccounts(c *gin.Context) {
	items, err := h.svc.ThresholdAccounts(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Threshold accounts unavailable")
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *AccountOpsHandler) Groups(c *gin.Context) {
	groups, err := h.svc.ThresholdGroups(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Account groups unavailable")
		return
	}
	response.Success(c, gin.H{"groups": groups})
}

func (h *AccountOpsHandler) SaveGroups(c *gin.Context) {
	var v struct {
		Groups []service.AccountOpsGroup `json:"groups"`
	}
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid account groups")
		return
	}
	cfg, err := h.svc.SaveGroups(c.Request.Context(), v.Groups)
	h.scopedResult(c, cfg, err)
}

func (h *AccountOpsHandler) scopedResult(c *gin.Context, cfg service.AccountOpsConfig, err error) {
	if err != nil {
		if errors.Is(err, service.ErrAccountOpsConfigValidation) {
			response.BadRequest(c, err.Error())
		} else {
			response.Error(c, http.StatusServiceUnavailable, "Could not save account alert configuration")
		}
		return
	}
	response.Success(c, cfg)
}
func (h *AccountOpsHandler) SaveNotificationSettings(c *gin.Context) {
	var v service.AccountOpsNotificationSettings
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid notification settings")
		return
	}
	cfg, err := h.svc.SaveNotificationSettings(c.Request.Context(), v)
	h.scopedResult(c, cfg, err)
}
func (h *AccountOpsHandler) SaveWebhook(c *gin.Context) {
	// Scope-only DTO excludes ID, configured indicators, and all other categories.
	var v struct {
		Name            *string `json:"name"`
		Provider        string  `json:"provider"`
		Enabled         bool    `json:"enabled"`
		URL             string  `json:"url"`
		Secret          string  `json:"secret"`
		ClearSecret     bool    `json:"clear_secret"`
		MessageTemplate *string `json:"message_template"`
	}
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid robot configuration")
		return
	}
	cfg, err := h.svc.SaveWebhook(c.Request.Context(), c.Param("id"), service.AccountOpsWebhook{Name: v.Name, Provider: v.Provider, Enabled: v.Enabled, URL: v.URL, Secret: v.Secret, ClearSecret: v.ClearSecret, MessageTemplate: v.MessageTemplate})
	h.scopedResult(c, cfg, err)
}
func (h *AccountOpsHandler) DeleteWebhook(c *gin.Context) {
	cfg, err := h.svc.DeleteWebhook(c.Request.Context(), c.Param("id"))
	h.scopedResult(c, cfg, err)
}
func (h *AccountOpsHandler) SaveRule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var v service.AccountOpsRuleUpdate
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid threshold rule")
		return
	}
	cfg, err := h.svc.SaveRule(c.Request.Context(), id, v)
	h.scopedResult(c, cfg, err)
}
func (h *AccountOpsHandler) SaveRulesBatch(c *gin.Context) {
	var v struct {
		AccountIDs json.RawMessage `json:"account_ids"`
		Rule       json.RawMessage `json:"rule"`
		Groups     json.RawMessage `json:"groups"`
	}
	if c.ShouldBindJSON(&v) != nil {
		response.BadRequest(c, "Invalid batch threshold rule")
		return
	}
	var groups []service.AccountOpsRuleGroup
	if v.Groups != nil {
		if v.AccountIDs != nil || v.Rule != nil {
			response.BadRequest(c, "Use either groups or account_ids and rule")
			return
		}
		if json.Unmarshal(v.Groups, &groups) != nil {
			response.BadRequest(c, "Invalid batch threshold groups")
			return
		}
	} else {
		var group service.AccountOpsRuleGroup
		if json.Unmarshal(v.AccountIDs, &group.AccountIDs) != nil || json.Unmarshal(v.Rule, &group.Rule) != nil {
			response.BadRequest(c, "Invalid batch threshold rule")
			return
		}
		groups = []service.AccountOpsRuleGroup{group}
	}
	cfg, err := h.svc.SaveRuleGroupsBatch(c.Request.Context(), groups)
	h.scopedResult(c, cfg, err)
}
func (h *AccountOpsHandler) DeleteRule(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	cfg, err := h.svc.DeleteRule(c.Request.Context(), id, c.Query("metric"))
	h.scopedResult(c, cfg, err)
}
