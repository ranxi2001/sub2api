package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

func (s *Service) pendingImagePlan(ctx context.Context, c Config, j Job) (*pendingImagePlan, error) {
	var p pendingImagePlan
	err := s.db.QueryRowContext(ctx, `SELECT p.action,p.prompt,p.request_quote,p.job_id,p.source_message_id
 FROM qq_bot_pending_image_plans p JOIN qq_bot_jobs q ON q.id=p.job_id
 WHERE p.app_id=$1 AND p.group_id=$2 AND p.member_id=$3 AND p.expires_at>NOW() AND q.status='completed'`, c.AppID, j.GroupID, j.MemberID).Scan(&p.Action, &p.Prompt, &p.RequestQuote, &p.JobID, &p.SourceMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func withPendingImagePlan(messages []Message, p *pendingImagePlan) []Message {
	if p == nil {
		return messages
	}
	raw, _ := json.Marshal(p)
	// Data, never a system instruction or executable request.
	return append(messages, Message{Role: "user", Content: "【本用户待确认图片方案，仅作上下文，是否执行以当前消息为准】\n" + string(raw)})
}

func (s *Service) clearPendingImagePlan(ctx context.Context, c Config, j Job) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM qq_bot_pending_image_plans WHERE app_id=$1 AND group_id=$2 AND member_id=$3`, c.AppID, j.GroupID, j.MemberID)
	return err
}

func (s *Service) saveImageDecision(ctx context.Context, j *Job, d imageDecision) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE qq_bot_jobs SET image_decision=$2,input_tokens=$3,output_tokens=$4 WHERE id=$1`, j.ID, string(raw), j.InputTokens, j.OutputTokens)
	if err == nil {
		j.ImageDecision = raw
	}
	return err
}

func (s *Service) imageExecution(ctx context.Context, j *Job, status string) error {
	var d imageDecision
	if len(j.ImageDecision) == 0 {
		return nil
	}
	if err := json.Unmarshal(j.ImageDecision, &d); err != nil {
		return err
	}
	d.Execution = status
	return s.saveImageDecision(ctx, j, d)
}

func (s *Service) reviewImageTurn(ctx context.Context, c Config, j *Job, e Event, current string, input Message, ref *botMessageReference, pending *pendingImagePlan, turn chatTurnResult) (chatTurnResult, error) {
	action, proposed := chatImageToolName, turn.ImagePrompt
	if turn.ImagePrompt != "" && turn.ImageOutputFormat != "" && turn.ImageOutputFormat != "auto" {
		proposed += "\n输出格式：" + turn.ImageOutputFormat
	}
	if turn.EditImageIndex > 0 {
		action = chatEditImageToolName
	}
	if turn.ImagePlan != nil {
		action = "plan"
		proposed = turn.ImagePlan.Prompt
	}
	d := imageDecision{Tool: action, Review: "pending", Reason: "review_pending", Execution: "not_started", MessageRef: groupHistoryID(c.AppID, j.GroupID, e.ID)}
	if action == "plan" {
		d.Tool = chatTextToolName
	}
	if pending != nil {
		d.PlanRef = groupHistoryID(c.AppID, j.GroupID, pending.SourceMessageID)
	}
	j.InputTokens, j.OutputTokens = turn.InputTokens, turn.OutputTokens
	if err := s.saveImageDecision(ctx, j, d); err != nil {
		return turn, err
	}
	review, usage, err := s.reviewImageIntent(ctx, c, e, current, input, ref, pending, action, proposed, turn.EditImageIndex)
	turn.InputTokens += usage.InputTokens
	turn.OutputTokens += usage.OutputTokens
	j.InputTokens, j.OutputTokens = turn.InputTokens, turn.OutputTokens
	if err != nil {
		d.Review, d.Reason = "error", "review_failed"
		turn.Text, turn.ImagePrompt, turn.ImagePlan, turn.EditImageIndex = "暂时无法确认图片请求，请稍后明确描述需要的图片。", "", nil, 0
		if clearErr := s.clearPendingImagePlan(ctx, c, *j); clearErr != nil {
			return turn, clearErr
		}
		if saveErr := s.saveImageDecision(ctx, j, d); saveErr != nil {
			return turn, saveErr
		}
		return turn, err
	}
	d.Review, d.Basis = review.Disposition, review.Basis
	authorized := (review.Basis == "explicit" || review.Basis == "continuation") && strings.TrimSpace(review.Evidence) != "" && strings.Contains(current, review.Evidence)
	if review.Basis == "continuation" && (pending == nil || pending.Action != action) {
		authorized = false
	}
	if action == "plan" && (turn.ImagePlan == nil || review.Basis != "explicit" || !strings.Contains(current, turn.ImagePlan.RequestQuote)) {
		authorized = false
	}
	d.Reason = "not_image_request"
	switch {
	case review.Disposition == "execute" && action != "plan" && authorized:
		// Consume the exact live plan before quota/image execution. Never leave an
		// old approval available after a failed or uncertain generation.
		if review.Basis == "continuation" {
			// Ignore a conflicting quote rather than substituting the latest plan.
			if e.referenceID() != "" {
				var matches bool
				matchErr := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_message_references r WHERE r.app_id=$1 AND r.group_id=$2 AND (r.ref_idx=$3 OR r.message_id=$3) AND r.source_job_id=$4 AND NOT r.is_image AND r.created_at>NOW()-INTERVAL '24 hours')`, c.AppID, j.GroupID, e.referenceID(), pending.JobID).Scan(&matches)
				if matchErr != nil {
					return turn, matchErr
				}
				if !matches {
					review.Disposition, review.Text = "clarify", "这条引用与待确认的图片方案不一致，请明确需要生成或修改什么图片。"
					authorized = false
				}
			}
			result, consumeErr := s.db.ExecContext(ctx, `DELETE FROM qq_bot_pending_image_plans WHERE app_id=$1 AND group_id=$2 AND member_id=$3 AND job_id=$4 AND expires_at>NOW()`, c.AppID, j.GroupID, j.MemberID, pending.JobID)
			if consumeErr != nil {
				return turn, consumeErr
			}
			n, consumeErr := result.RowsAffected()
			if consumeErr != nil {
				return turn, consumeErr
			}
			authorized = authorized && n == 1
		} else if clearErr := s.clearPendingImagePlan(ctx, c, *j); clearErr != nil {
			return turn, clearErr
		}
		if authorized {
			d.Reason = "current_request"
			if review.Basis == "continuation" {
				d.Reason = "pending_plan_confirmed"
			}
			d.EvidenceHash = codeHash(review.Evidence)[:16]
			break
		}
		fallthrough
	case review.Disposition == "clarify":
		d.Review, d.Reason = "clarify", "missing_image_intent"
		turn.Text = review.Text
		if turn.Text == "" {
			turn.Text = "请具体说一下需要生成或修改什么图片。"
		}
		turn.ImagePrompt, turn.ImagePlan, turn.EditImageIndex = "", nil, 0
		if clearErr := s.clearPendingImagePlan(ctx, c, *j); clearErr != nil {
			return turn, clearErr
		}
	case review.Disposition == "plan" && action == "plan" && authorized:
		p := turn.ImagePlan
		_, saveErr := s.db.ExecContext(ctx, `INSERT INTO qq_bot_pending_image_plans(app_id,group_id,member_id,job_id,source_message_id,action,prompt,request_quote,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NOW()+INTERVAL '10 minutes') ON CONFLICT(app_id,group_id,member_id) DO UPDATE SET job_id=EXCLUDED.job_id,source_message_id=EXCLUDED.source_message_id,action=EXCLUDED.action,prompt=EXCLUDED.prompt,request_quote=EXCLUDED.request_quote,expires_at=EXCLUDED.expires_at`, c.AppID, j.GroupID, j.MemberID, j.ID, e.ID, p.Action, p.Prompt, p.RequestQuote)
		if saveErr != nil {
			return turn, saveErr
		}
		d.Reason = "pending_confirmation"
		d.EvidenceHash = codeHash(review.Evidence)[:16]
	default:
		d.Review = "text"
		if review.Disposition != "text" {
			d.Review, d.Reason = "clarify", "invalid_authorization"
		}
		turn.Text = review.Text
		if turn.Text == "" {
			turn.Text = "请明确描述需要生成或修改的图片。"
		}
		turn.ImagePrompt, turn.ImagePlan, turn.EditImageIndex = "", nil, 0
		if clearErr := s.clearPendingImagePlan(ctx, c, *j); clearErr != nil {
			return turn, clearErr
		}
	}
	if err = s.saveImageDecision(ctx, j, d); err != nil {
		return turn, err
	}
	return turn, nil
}
