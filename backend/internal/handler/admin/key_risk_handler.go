package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/iprisk"
	"github.com/Wei-Shaw/sub2api/internal/keyrisk"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) StartKeyRisk(dsn string) (func(), error) {
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	h.keyRisk = &keyrisk.Service{DB: db, Mail: h.emailService}
	h.keyRisk.Start()
	return func() { h.keyRisk.Stop(); _ = db.Close() }, nil
}
func (h *SettingHandler) keyRiskReady(c *gin.Context) bool {
	if !ipAuditAdministrator(c) {
		return false
	}
	if h.keyRisk == nil {
		response.Error(c, 503, "Key risk service unavailable")
		return false
	}
	return true
}
func (h *SettingHandler) GetKeyRisk(c *gin.Context) {
	if !h.keyRiskReady(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	before, e := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	if e != nil || before < 0 {
		response.BadRequest(c, "Invalid cursor")
		return
	}
	cfg, e := h.keyRisk.Config(ctx)
	if e != nil {
		response.Error(c, 503, "Key risk configuration unavailable")
		return
	}
	events, e := h.keyRisk.Events(ctx, before)
	if e != nil {
		response.Error(c, 503, "Key risk records unavailable")
		return
	}
	var at sql.NullTime
	var issue string
	e = h.keyRisk.DB.QueryRowContext(ctx, `SELECT last_scan_at,last_error FROM key_risk_control WHERE singleton`).Scan(&at, &issue)
	if e != nil {
		response.Error(c, 503, "Key risk status unavailable")
		return
	}
	var pending, uncertain, sent int
	e = h.keyRisk.DB.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE state='pending'),count(*) FILTER(WHERE state='uncertain'),count(*) FILTER(WHERE state='sent') FROM key_risk_outbox`).Scan(&pending, &uncertain, &sent)
	if e != nil {
		response.Error(c, 503, "Key risk delivery status unavailable")
		return
	}
	response.Success(c, gin.H{"config": cfg, "items": events, "last_scan_at": at.Time, "coverage_issue": issue, "pending": pending, "uncertain": uncertain, "sent": sent})
}
func (h *SettingHandler) SaveKeyRisk(c *gin.Context) {
	if !h.keyRiskReady(c) {
		return
	}
	cfg := keyrisk.DefaultConfig()
	if e := c.ShouldBindJSON(&cfg); e != nil || cfg.Validate() != nil {
		response.BadRequest(c, "Invalid key risk configuration")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if e := h.keyRisk.Save(ctx, cfg); e != nil {
		response.Error(c, 503, "Unable to save key risk configuration")
		return
	}
	middleware.SetAuditAction(c, "admin.key_risk.configure")
	response.Success(c, cfg)
}
func (h *SettingHandler) ActOnKeyRisk(c *gin.Context) {
	if !h.keyRiskReady(c) {
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	var req struct {
		Action string `json:"action"`
	}
	if e != nil || id <= 0 || c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid risk action")
		return
	}
	switch req.Action {
	case "notify", "ignored", "resolved", "pending", "block":
	default:
		response.BadRequest(c, "Invalid risk action")
		return
	}
	actor, _ := middleware.GetAuthSubjectFromContext(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	queued := 0
	switch req.Action {
	case "block":
		var ip string
		e = h.keyRisk.DB.QueryRowContext(ctx, "SELECT evidence->>'ip' FROM key_risk_events WHERE id=$1 AND rule='cross_user_ip'", id).Scan(&ip)
		if e == nil {
			e = h.blockKeyRiskIP(ctx, c, ip)
		}
	case "notify":
		queued, e = h.keyRisk.NotifyUsers(ctx, id, actor.UserID)
	default:
		e = h.keyRisk.SetState(ctx, id, actor.UserID, req.Action)
	}
	if e != nil {
		response.Error(c, 409, "Risk action unavailable; refresh records")
		return
	}
	middleware.SetAuditAction(c, "admin.key_risk."+req.Action)
	response.Success(c, gin.H{"queued": queued > 0, "recipients": queued})
}

// Reuse the existing admission policy rather than add any new gateway IO.
func (h *SettingHandler) blockKeyRiskIP(ctx context.Context, c *gin.Context, raw string) error {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return err
	}
	addr = addr.Unmap()
	m := h.ipRiskManager(c)
	if m == nil {
		return fmt.Errorf("IP manager unavailable")
	}
	cfg, err := m.Config(ctx)
	if err != nil {
		return err
	}
	for _, s := range cfg.Whitelist {
		p, e := iprisk.ParseNetwork(s)
		if e == nil && p.Contains(addr) {
			return fmt.Errorf("IP is allowlisted")
		}
	}
	if err = m.CheckAdministrator(ctx, cfg, c.ClientIP(), c.GetHeader("User-Agent")); err != nil {
		return err
	}
	// Normalize both sides and collapse duplicates left by older versions.
	network, err := iprisk.ParseNetwork(raw)
	if err != nil {
		return err
	}
	replacement := iprisk.Rule{Network: network.String(), Note: "Administrator reviewed key risk", Enabled: true, ExpiresAt: time.Now().Add(24 * time.Hour).Unix()}
	rules := make([]iprisk.Rule, 0, len(cfg.Blacklist)+1)
	for _, rule := range cfg.Blacklist {
		existing, e := iprisk.ParseNetwork(rule.Network)
		if e != nil || existing != network {
			rules = append(rules, rule)
			continue
		}
		// Never shorten an existing active restriction, including permanent bans.
		if rule.Enabled && (rule.ExpiresAt == 0 || (replacement.ExpiresAt != 0 && rule.ExpiresAt > replacement.ExpiresAt)) {
			replacement.ExpiresAt = rule.ExpiresAt
		}
	}
	cfg.Blacklist = append(rules, replacement)
	// Check the modified policy as well, including when it targets this administrator.
	if err = m.CheckAdministrator(ctx, cfg, c.ClientIP(), c.GetHeader("User-Agent")); err != nil {
		return err
	}
	return m.Save(ctx, cfg)
}
