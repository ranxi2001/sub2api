package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const upstreamAuditStateKey = "content_moderation.upstream_state"

type upstreamAuditState struct {
	mu          sync.Mutex
	request     securityaudit.Request
	config      *service.ContentModerationConfig
	configErr   error
	legacy      *service.ContentModerationService
	coordinator *securityaudit.Coordinator
	log         *zap.Logger
	turn        int
	completed   *securityaudit.Decision
}

func prepareUpstreamAudit(c *gin.Context, log *zap.Logger, coordinator *securityaudit.Coordinator, legacy *service.ContentModerationService, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol, model string, body []byte, stage string) {
	if legacy == nil {
		return
	}
	turn, _ := securityAuditWSTurn(c)
	if old, ok := c.Get(upstreamAuditStateKey); ok {
		if state, ok := old.(*upstreamAuditState); ok && state.turn == turn && state.request.Stage == stage {
			bindUpstreamAuditGate(c, state)
			return
		}
	}
	cfg, err := legacy.UpstreamModerationConfig(c.Request.Context())
	state := &upstreamAuditState{request: buildSecurityAuditRequest(c, apiKey, subject, protocol, model, body, stage).Clone(), config: cfg, configErr: err, legacy: legacy, coordinator: coordinator, log: log, turn: turn}
	c.Set(upstreamAuditStateKey, state)
	bindUpstreamAuditGate(c, state)
}

func bindUpstreamAuditGate(c *gin.Context, state *upstreamAuditState) {
	// WS ingress owns protocol errors and per-turn selection checks. Its HTTP
	// bridge must never write an HTTP error to the upgraded response writer.
	if isSecurityAuditWebSocketStage(state.request.Stage) {
		service.SetUpstreamModerationGate(c, nil)
		return
	}
	service.SetUpstreamModerationGate(c, func(ctx context.Context, account *service.Account) error {
		decision := state.check(ctx, account)
		if decision == nil || decision.AllowNextStage {
			return nil
		}
		writeSelectedUpstreamAuditError(c, decision)
		return &service.UpstreamModerationDeniedError{Message: securityAuditMessage(decision)}
	})
}

func checkSelectedUpstreamAudit(c *gin.Context, ctx context.Context, account *service.Account) *securityaudit.Decision {
	if value, ok := c.Get(upstreamAuditStateKey); ok {
		if state, ok := value.(*upstreamAuditState); ok {
			return state.check(ctx, account)
		}
	}
	return nil
}

func (s *upstreamAuditState) check(ctx context.Context, account *service.Account) *securityaudit.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configErr != nil {
		return upstreamAuditUnavailable(s.log, account, s.configErr)
	}
	if s.config == nil {
		return nil
	}
	req := s.request.Clone()
	req.UpstreamAccount, req.UpstreamConfig, req.SkipPrompt = account, s.config, true
	// Keep the original request frozen; derive audit text only for transport
	// envelopes not understood by the legacy prompt extractor. Prompt audit
	// still receives the untouched ingress request at its existing stage.
	if req.Protocol == "openai_alpha_search" {
		req.Protocol = service.ContentModerationProtocolOpenAIResponses
		req.Body, _ = json.Marshal(map[string]string{"input": string(req.Body)})
	} else if req.Protocol == service.ContentModerationProtocolOpenAIImages && gjson.GetBytes(req.Body, "request.items").IsArray() {
		prompts := []string{}
		for _, item := range gjson.GetBytes(req.Body, "request.items").Array() {
			prompts = append(prompts, item.Get("prompt").String())
		}
		req.Body, _ = json.Marshal(map[string]string{"prompt": strings.Join(prompts, "\n")})
	} else if isSecurityAuditWebSocketStage(req.Stage) && gjson.GetBytes(req.Body, "response.input").Exists() {
		req.Body = []byte(gjson.GetBytes(req.Body, "response").Raw)
	}
	input := service.ContentModerationCheckInput{
		RequestID: req.RequestID, UserID: req.UserID, UserEmail: req.UserEmail,
		APIKeyID: req.APIKeyID, APIKeyName: req.APIKeyName, GroupID: req.GroupID, GroupName: req.GroupName,
		Endpoint: req.Endpoint, Provider: req.Provider, Model: req.Model, Protocol: req.Protocol, Body: req.Body,
		UpstreamAccount: account, UpstreamConfig: s.config,
	}
	if req.PenaltyContext.OriginalGroupID != nil {
		input.GroupID, input.GroupName = req.PenaltyContext.OriginalGroupID, req.PenaltyContext.OriginalGroupName
	}
	applies, err := s.legacy.UpstreamModerationApplies(ctx, s.config, input)
	if err != nil {
		return upstreamAuditUnavailable(s.log, account, err)
	}
	if !applies {
		return nil
	} // Never mark an unselected attempt as audited.
	if s.completed != nil {
		return s.completed
	}
	coordinator := s.coordinator
	if coordinator == nil {
		coordinator = securityaudit.NewCoordinator(securityaudit.NewLegacyModerationAdapter(s.legacy), nil)
	}
	logSecurityAuditStart(s.log, req, len(req.Body), false)
	decision := coordinator.Check(ctx, req)
	logSecurityAuditDone(s.log, req, decision, false)
	// One logical turn owns one immutable request. Nested protocol adapters and
	// failover attempts share its decision; a new WS turn owns a fresh state.
	s.completed = &decision
	return &decision
}

func upstreamAuditUnavailable(log *zap.Logger, account *service.Account, err error) *securityaudit.Decision {
	var id int64
	if account != nil {
		id = account.ID
	}
	if log != nil {
		log.Error("content_moderation.upstream_scope_unavailable", zap.Int64("account_id", id), zap.Error(err))
	}
	return &securityaudit.Decision{Kind: securityaudit.DecisionBlock, HTTPStatus: http.StatusServiceUnavailable, ErrorCode: "moderation_upstream_scope_unavailable", ClientMessage: "Upstream moderation scope is unavailable; administrator review required"}
}

func writeSelectedUpstreamAuditError(c *gin.Context, decision *securityaudit.Decision) {
	status, code, message := securityAuditStatus(decision), securityAuditErrorCode(decision), securityAuditMessage(decision)
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
	service.SetOpsLocalErrorType(c, "permission_error", code)
	payload := gin.H{"error": gin.H{"type": "permission_error", "code": code, "message": message}}
	if strings.HasSuffix(c.Request.URL.Path, "/messages") {
		payload["type"] = "error"
	}
	if strings.Contains(c.Request.URL.Path, "/v1beta/") {
		payload = gin.H{"error": gin.H{"code": status, "message": message, "status": "PERMISSION_DENIED"}}
	}
	if service.OpenAIImagesJSONKeepalivePresent(c) {
		committed := service.StopOpenAIImagesJSONKeepaliveCommitted(c)
		if committed {
			c.JSON(http.StatusOK, payload)
		} else {
			c.JSON(status, payload)
		}
	} else if c.Writer.Written() {
		service.MarkOpsStreamError(c, code, message, status)
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", data)
		_ = service.FlushGatewayResponse(c.Writer)
	} else {
		c.JSON(status, payload)
	}
	service.MarkResponseCommitted(c)
	c.Abort()
}
