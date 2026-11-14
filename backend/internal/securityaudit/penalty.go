package securityaudit

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SessionKey is an authenticated API-key-scoped one-way hash. The absence of
// a stable conversation identity is deliberately preserved, never guessed.
type PenaltyRequestContext struct {
	SessionKey        string `json:"session_key,omitempty"`
	RequestID         string `json:"request_id,omitempty"`
	Exempt            bool   `json:"exempt,omitempty"`
	OriginalGroupID   *int64 `json:"original_group_id,omitempty"`
	OriginalGroupName string `json:"original_group_name,omitempty"`
}

func ProvidePenaltyCoordinator(legacy LegacyEngine, prompt PromptEngine, penalties *service.SecurityPenaltyService) *Coordinator {
	c := NewCoordinator(legacy, prompt)
	c.penalties = penalties
	if p, ok := prompt.(*PromptService); ok && p.repo != nil {
		p.repo.prepareFinding = func(ctx context.Context) (func(*sql.Tx, PromptSnapshot, *NormalizedResult) error, error) {
			cfg, err := penalties.GetConfig(ctx)
			if err != nil {
				return nil, err
			}
			return func(tx *sql.Tx, snapshot PromptSnapshot, result *NormalizedResult) error {
				if snapshot.PenaltyContext.Exempt || result == nil || result.Action != ActionBlock {
					return nil
				}
				return penalties.RecordViolationTx(ctx, tx, penaltyViolation(snapshot, "prompt_audit", strings.Join(result.MatchedScanners, ", ")), cfg)
			}, nil
		}
	}
	return c
}

func penaltyViolation(snapshot PromptSnapshot, source, reason string) service.SecurityPenaltyViolation {
	snapshot = snapshot.Redacted()
	groupID := snapshot.GroupID
	if snapshot.PenaltyContext.OriginalGroupID != nil {
		groupID = snapshot.PenaltyContext.OriginalGroupID
	}
	requestID := snapshot.PenaltyContext.RequestID
	if requestID == "" {
		requestID = snapshot.RequestID
	}
	return service.SecurityPenaltyViolation{UserID: snapshot.UserID, APIKeyID: snapshot.APIKeyID, GroupID: groupID, RequestID: requestID, SessionKey: snapshot.PenaltyContext.SessionKey, Source: source, Reason: reason, FullPrompt: snapshot.FullPrompt, Model: snapshot.Model, Endpoint: snapshot.Endpoint}
}

func (c *Coordinator) recordPenaltyFinding(ctx context.Context, req Request, decision Decision) {
	source, reason := "", ""
	if decision.Legacy != nil && decision.Legacy.Blocked {
		source = "content_moderation"
		reason = decision.Legacy.Category
		if reason == "" {
			reason = decision.Legacy.Action
		}
	} else if decision.Prompt != nil && decision.Prompt.Kind == DecisionBlock && decision.Prompt.Result != nil {
		source = "prompt_audit"
		reason = strings.Join(decision.Prompt.Result.MatchedScanners, ", ")
	}
	if source == "" {
		return
	}
	snapshot, err := ExtractPromptSnapshot(req)
	if err != nil {
		// Never fall back to storing a raw JSON request (which may include tokens).
		snapshot = PromptSnapshot{RequestID: req.RequestID, UserID: req.UserID, APIKeyID: req.APIKeyID, GroupID: req.GroupID, Model: req.Model, Endpoint: req.Endpoint, PenaltyContext: req.PenaltyContext, FullPrompt: "（此请求未能提取文本提示词）"}
	}
	if err = c.penalties.RecordViolation(ctx, penaltyViolation(snapshot, source, reason)); err != nil {
		slog.Warn("security_penalty.record_failed", "user_id", req.UserID, "request_id", req.RequestID)
	}
}

func penaltyBlockedDecision() Decision {
	return Decision{Kind: DecisionBlock, HTTPStatus: http.StatusForbidden, ErrorCode: "security_penalty_blocked", ClientMessage: "请求权限已受限，请登录个人资料查看安全记录和解除时间"}
}

func penaltyUnavailableDecision() Decision {
	return Decision{Kind: DecisionUnavailable, HTTPStatus: http.StatusServiceUnavailable, ErrorCode: "security_penalty_unavailable", ClientMessage: "请求权限检查暂时不可用，请稍后重试"}
}
