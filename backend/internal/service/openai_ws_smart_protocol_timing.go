package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
)

func recordSmartProtocolDecision(ctx context.Context, d openAISmartProtocolDecision) {
	requesttiming.SetProtocolDecision(ctx, requesttiming.ProtocolDecision{
		Ingress: "ws", Upstream: d.Protocol, Candidate: d.Candidate, Mode: d.Mode,
		Reason: d.Reason, PredictionSource: d.Source, PredictedOutput: d.PredictedOutput, Samples: d.Samples,
	})
	// Fixed enum values and numeric aggregates only. No caller-supplied labels.
	slog.InfoContext(ctx, "openai_smart_protocol_decision", "ingress_protocol", "ws", "upstream_protocol", d.Protocol, "candidate_protocol", d.Candidate, "mode", d.Mode, "reason", d.Reason, "prediction_source", d.Source, "predicted_output", d.PredictedOutput, "samples", d.Samples)
}

func (s *OpenAIGatewayService) wrapSmartProtocolHooks(ctx context.Context, account *Account, smartSession *openAISmartProtocolSession, hooks *OpenAIWSIngressHooks) *OpenAIWSIngressHooks {
	if smartSession == nil {
		return hooks
	}

	prior := hooks
	wrapped := OpenAIWSIngressHooks{}
	if prior != nil {
		wrapped = *prior
	}
	wrapped.AfterTurn = func(turn int, result *OpenAIForwardResult, err error) {
		if result != nil {
			result.UpstreamProtocol = smartSession.Decision.Protocol
			result.ProtocolDecisionReason = smartSession.Decision.Reason
			var usageAvailable *bool
			if smartSession.Decision.Mode != "shadow" {
				available := !result.UsageUnavailable
				usageAvailable = &available
			}
			requesttiming.RecordWSTurn(ctx, requesttiming.WSTurnTiming{Turn: turn, Upstream: result.UpstreamProtocol, FirstUpstreamEventMS: result.FirstUpstreamEventMs, FirstActualContentMS: result.FirstActualContentMs, FirstActualContentKind: result.FirstActualContentKind, UsageAvailable: usageAvailable})
			if err == nil && !result.UsageUnavailable && result.UpstreamTerminalEvent == "response.completed" {
				effort := ""
				if result.ReasoningEffort != nil {
					effort = *result.ReasoningEffort
				}
				model := result.UpstreamModel
				if model == "" {
					model = normalizeOpenAIModelForUpstream(account, account.GetMappedModel(result.Model))
				}
				key := openAISmartProtocolHistoryKey{account.ID, account.AuthConnectionID, account.AuthConnectionRevision, model, effort, result.UpstreamProtocol}
				s.smartProtocolHistory.observe(key, result.Usage.OutputTokens, time.Now())
			}
		}
		if prior != nil && prior.AfterTurn != nil {
			prior.AfterTurn(turn, result, err)
		}
	}
	recordSmartProtocolDecision(ctx, smartSession.Decision)
	return &wrapped
}
