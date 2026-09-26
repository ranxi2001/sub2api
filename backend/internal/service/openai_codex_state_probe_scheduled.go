package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OpenAICodexStateProbeQuestionKind 是定时测试 / 质量规则里的探针题型：
// 不下发题目，改为跑一次门票探针（ProbeOpenAICodexState），用「续接是否回新票」判满血/降智。
const OpenAICodexStateProbeQuestionKind = "state_probe"

// 探针题型的结果错误标记。降智用固定标记参与质量判定（qualityOutcome）；
// 无法判断带上失败分类，只作展示、不算降智证据。
const (
	openAICodexStateDegradedError         = "state_degraded"
	openAICodexStateInconclusiveErrPrefix = "state_probe_inconclusive"
)

func isOpenAICodexStateProbePlan(cfg *PelicanTestConfig) bool {
	return cfg != nil && cfg.QuestionKind == OpenAICodexStateProbeQuestionKind
}

// runOpenAICodexStateProbeScheduled 以定时测试结果的形态跑一次门票探针。
// 满血 = success；降智 = failed + state_degraded；无法判断 = failed + 失败分类（不算降智证据）。
// 质量规则场景下同时写入 QualityJudgment（correct/incorrect/unknown），不经过判题模型。
func (s *AccountTestService) runOpenAICodexStateProbeScheduled(ctx context.Context, accountID int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
	started := time.Now()
	probe, err := s.ProbeOpenAICodexState(ctx, accountID, model)
	if err != nil {
		if !errors.Is(err, ErrOpenAICodexStateProbeBusy) {
			return nil, err
		}
		// 同账号已有探针在跑（比如手动探针）：记一条无法判断，不让整个计划报错。
		probe = &OpenAICodexStateProbeResult{
			AccountID:  accountID,
			Model:      strings.TrimSpace(model),
			Verdict:    OpenAICodexStateInconclusive,
			Failure:    OpenAICodexStateFailureCancelled,
			Reason:     "该账号已有一次探针在进行中，本轮跳过",
			StartedAt:  started,
			FinishedAt: time.Now(),
		}
		probe.LatencyMs = probe.FinishedAt.Sub(probe.StartedAt).Milliseconds()
	}

	snapshot := *cfg
	snapshot.ModelID = probe.Model
	result := &ScheduledTestResult{
		Status:        "failed",
		ResponseText:  openAICodexStateScheduledSummary(probe),
		LatencyMs:     probe.LatencyMs,
		StartedAt:     probe.StartedAt,
		FinishedAt:    probe.FinishedAt,
		PelicanConfig: &snapshot,
	}
	switch probe.Verdict {
	case OpenAICodexStateHealthy:
		result.Status = "success"
	case OpenAICodexStateDegraded:
		result.ErrorMessage = openAICodexStateDegradedError
	default:
		result.ErrorMessage = openAICodexStateInconclusiveErrPrefix + ": " + probe.Failure
	}
	if cfg.Quality != nil {
		result.QualityJudgment = &QualityJudgment{Verdict: openAICodexStateQualityVerdict(probe.Verdict), Reason: probe.Reason, AccountID: accountID}
	}
	return result, nil
}

func openAICodexStateQualityVerdict(verdict OpenAICodexStateVerdict) string {
	switch verdict {
	case OpenAICodexStateHealthy:
		return "correct"
	case OpenAICodexStateDegraded:
		return "incorrect"
	default:
		return "unknown"
	}
}

// openAICodexStateScheduledSummary 把探针结果压成一段人读的多行文本，存进定时结果的 response_text。
func openAICodexStateScheduledSummary(r *OpenAICodexStateProbeResult) string {
	var b strings.Builder
	verdictLabel := map[OpenAICodexStateVerdict]string{
		OpenAICodexStateHealthy:      "满血",
		OpenAICodexStateDegraded:     "降智",
		OpenAICodexStateInconclusive: "无法判断",
	}[r.Verdict]
	fmt.Fprintf(&b, "判定：%s\n%s\n", verdictLabel, r.Reason)
	if r.Failure != "" {
		fmt.Fprintf(&b, "失败分类：%s\n", r.Failure)
	}
	if r.Detail != "" {
		fmt.Fprintf(&b, "上游报错：%s\n", r.Detail)
	}
	fmt.Fprintf(&b, "打票 HTTP %s / 续接 HTTP %s\n", openAICodexStateStatusLabel(r.MintStatus), openAICodexStateStatusLabel(r.ContinueStatus))
	if r.TicketLength > 0 {
		if r.ContinueTicketLength > 0 {
			fmt.Fprintf(&b, "门票长度：%d → %d；续接回新票：%s\n", r.TicketLength, r.ContinueTicketLength, openAICodexStateYesNo(r))
		} else {
			fmt.Fprintf(&b, "门票长度：%d；续接回新票：%s\n", r.TicketLength, openAICodexStateYesNo(r))
		}
	}
	if r.ReportedModel != "" {
		fmt.Fprintf(&b, "探测模型：%s；上游回报：%s\n", r.Model, r.ReportedModel)
	} else {
		fmt.Fprintf(&b, "探测模型：%s\n", r.Model)
	}
	fmt.Fprintf(&b, "耗时：%.1f 秒", float64(r.LatencyMs)/1000)
	return b.String()
}

func openAICodexStateStatusLabel(status int) string {
	if status <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d", status)
}

func openAICodexStateYesNo(r *OpenAICodexStateProbeResult) string {
	if r.Verdict == OpenAICodexStateInconclusive {
		return "—"
	}
	if r.NewTicket {
		return "是"
	}
	return "否"
}
