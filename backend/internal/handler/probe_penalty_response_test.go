//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func probeTestWriter(t *testing.T, stream bool) (*probePenaltyResponse, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(r)
	w, err := newProbePenaltyResponse(c.Writer, stream)
	require.NoError(t, err)
	t.Cleanup(w.close)
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	return w, r
}
func probeTestResult() *service.ForwardResult {
	return &service.ForwardResult{Usage: service.ClaudeUsage{InputTokens: 10, OutputTokens: 13}, ProbePenalty: &service.ProbePenaltyReceipt{MinimumCharge: .001, Adjustment: .000685, ActualCost: .001, OriginalOutputTokens: 13, BillingOutputTokens: 49, OutputTokenPrice: .0000195}}
}
func TestProbePenaltyJSONPreservesObservedUsage(t *testing.T) {
	w, r := probeTestWriter(t, false)
	_, err := w.WriteString(`{"type":"message","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":13}}`)
	require.NoError(t, err)
	require.Empty(t, r.Body.String())
	require.NoError(t, w.validate())
	require.NoError(t, w.finish(probeTestResult()))
	body := r.Body.String()
	require.Equal(t, int64(49), gjson.Get(body, "usage.output_tokens").Int())
	require.Equal(t, int64(13), gjson.Get(body, "upstream_usage.output_tokens").Int())
	require.Equal(t, "OK", gjson.Get(body, "content.0.text").String())
	require.Equal(t, .001, gjson.Get(body, "billing.probe_penalty.actual_cost").Float())
}
func TestProbePenaltySSEStreamsTextAndHoldsTerminalUsage(t *testing.T) {
	w, r := probeTestWriter(t, true)
	start := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n"
	text := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"OK\"}}\n\n"
	tail := "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":13}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, b := range []byte(start + text + tail) {
		_, err := w.Write([]byte{b})
		require.NoError(t, err)
		w.Flush()
	}
	require.Contains(t, r.Body.String(), "OK")
	require.NotContains(t, r.Body.String(), "message_stop")
	require.NotContains(t, r.Body.String(), "end_turn")
	require.NoError(t, w.validate())
	require.NoError(t, w.finish(probeTestResult()))
	require.Equal(t, 1, strings.Count(r.Body.String(), `"type":"message_stop"`))
	require.Contains(t, r.Body.String(), `"output_tokens":49`)
	require.Contains(t, r.Body.String(), `"upstream_usage":{"output_tokens":13}`)
}
func TestProbePenaltyRejectsTruncatedAndFailedSettlement(t *testing.T) {
	w, r := probeTestWriter(t, true)
	_, err := w.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":13}}\n\n")
	require.NoError(t, err)
	require.Error(t, w.validate())
	w.settlementError()
	require.NotContains(t, r.Body.String(), "message_delta")
	require.Equal(t, 503, r.Code)
}
func TestProbePenaltyDisabledAdjustmentLeavesBodyUnchanged(t *testing.T) {
	w, r := probeTestWriter(t, false)
	body := `{"type":"message","usage":{"input_tokens":10,"output_tokens":10000}}`
	_, err := w.WriteString(body)
	require.NoError(t, err)
	require.NoError(t, w.finish(&service.ForwardResult{}))
	require.Equal(t, body, r.Body.String())
}

type probeHandlerBilling struct{ httpDeliveryBilling }

func (r *probeHandlerBilling) ApplyStagedUsageBilling(ctx context.Context, key service.UsageBillingEventKey) (*service.UsageBillingRecovery, error) {
	recovered, err := r.httpDeliveryBilling.ApplyStagedUsageBilling(ctx, key)
	if recovered != nil {
		recovered.Settled = true
	}
	return recovered, err
}
func TestProbePenaltyActualGatewayProtocols(t *testing.T) {
	for _, endpoint := range []string{"messages", "chat/completions", "responses"} {
		for _, fail := range []bool{false, true} {
			t.Run(endpoint+map[bool]string{false: "/settled", true: "/settlement-failed"}[fail], func(t *testing.T) {
				repo := &probeHandlerBilling{}
				if fail {
					repo.failure = errors.New("injected settlement error")
				}
				extra := map[string]any{service.AccountProbePenaltyExtraKey: map[string]any{"enabled": true, "minimum_charge": .001}}
				run := runCompatStreamUsageHandlerWithDelivery(t, endpoint, compatStreamUsagePartialStream+compatStreamUsageCompletion, true, repo, "", extra)
				require.Equal(t, 1, run.upstream.calls)
				require.NotNil(t, repo.event, "actual handler must synchronously stage the charge")
				require.Equal(t, .001, repo.event.Command.BalanceCost)
				require.Equal(t, 7, repo.event.Usage.OutputTokens)
				require.Equal(t, 12, repo.event.Usage.InputTokens)
				body := run.rec.Body.String()
				require.Contains(t, body, "partial")
				if fail {
					require.NotContains(t, body, `"usage_kind":"billing_equivalent"`)
					require.Contains(t, body, "do not automatically retry")
				} else {
					require.Contains(t, body, `"usage_kind":"billing_equivalent"`)
					require.Contains(t, body, `"actual_cost":0.001`)
					require.True(t, repo.applied)
				}
			})
		}
	}
}

func TestProbePenaltyDownstreamSub2APIChargesEquivalentUsage(t *testing.T) {
	extra := map[string]any{service.AccountProbePenaltyExtraKey: map[string]any{"enabled": true, "minimum_charge": .001}}
	supplier := &probeHandlerBilling{}
	upstream := runCompatStreamUsageHandlerWithDelivery(t, "messages", compatStreamUsagePartialStream+compatStreamUsageCompletion, true, supplier, "", extra)
	require.NotNil(t, supplier.event)
	require.Contains(t, upstream.rec.Body.String(), `"usage_kind":"billing_equivalent"`)
	for _, endpoint := range []string{"messages", "chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			// A second real Sub2API handler consumes the supplier's standard usage
			// with no penalty configuration. No custom receipt parser is required.
			downstream := runCompatStreamUsageHandlerWithDelivery(t, endpoint, upstream.rec.Body.String(), true, nil, "")
			downstream.billingRepo.mu.Lock()
			defer downstream.billingRepo.mu.Unlock()
			require.Len(t, downstream.billingRepo.commands, 1)
			charge := downstream.billingRepo.commands[0].BalanceCost
			require.GreaterOrEqual(t, charge, .001)
			require.Less(t, charge, .001+supplier.event.Usage.CustomerCostDetails.ProbePenalty.OutputTokenPrice)
			require.Greater(t, downstream.billingRepo.commands[0].OutputTokens, 7)
			require.Equal(t, 7, supplier.event.Usage.OutputTokens, "supplier retains real model consumption")
		})
	}
}

func TestProbePenaltyProtocolUsageKeepsCacheDetails(t *testing.T) {
	for _, body := range []string{
		`{"object":"chat.completion","usage":{"prompt_tokens":120,"completion_tokens":13,"total_tokens":133,"prompt_tokens_details":{"cached_tokens":100}}}`,
		`{"type":"response.incomplete","response":{"object":"response","usage":{"input_tokens":120,"output_tokens":13,"total_tokens":133,"input_tokens_details":{"cached_tokens":100}}}}`,
	} {
		out, err := rewriteProbeUsage([]byte(body), probeTestResult())
		require.NoError(t, err)
		require.Contains(t, string(out), `"cached_tokens":100`)
		require.Contains(t, string(out), `"total_tokens":169`)
		require.Contains(t, string(out), `"upstream_usage"`)
	}
}

func TestProbePenaltyStreamingRequestJSONFallback(t *testing.T) {
	w, r := probeTestWriter(t, true)
	_, err := w.WriteString(`{"type":"message","usage":{"input_tokens":10,"output_tokens":13}}`)
	require.NoError(t, err)
	require.NoError(t, w.validate())
	require.Empty(t, r.Body.String())
	require.NoError(t, w.finish(probeTestResult()))
	require.Equal(t, "application/json", r.Header().Get("Content-Type"))
	require.Equal(t, int64(49), gjson.Get(r.Body.String(), "usage.output_tokens").Int())
}

func TestProbePenaltySparseTerminalUsageAcrossGateways(t *testing.T) {
	for _, usage := range []string{`{"output_tokens":0}`, `{"cache_read_input_tokens":2}`, `null`, ``} {
		for _, endpoint := range []string{"messages", "chat/completions", "responses"} {
			t.Run(endpoint+"/"+usage, func(t *testing.T) {
				suffix := `{"type":"message_delta","delta":{"stop_reason":"end_turn"}`
				if usage != "" {
					suffix += `,"usage":` + usage
				}
				suffix += `}`
				stream := compatStreamUsagePartialStream + "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":13}}\n\n" + "event: message_delta\ndata: " + suffix + "\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				repo := &probeHandlerBilling{}
				run := runCompatStreamUsageHandlerWithDelivery(t, endpoint, stream, true, repo, "", map[string]any{service.AccountProbePenaltyExtraKey: map[string]any{"enabled": true, "minimum_charge": .001}})
				require.NotNil(t, repo.event)
				require.Equal(t, 13, repo.event.Usage.OutputTokens)
				require.Equal(t, .001, repo.event.Command.BalanceCost)
				expected := repo.event.Usage.CustomerCostDetails.ProbePenalty.BillingOutputTokens
				found := false
				for _, block := range strings.Split(run.rec.Body.String(), "\n\n") {
					payload := probeEventData([]byte(block))
					prefix := ""
					if gjson.GetBytes(payload, "type").String() == "response.completed" {
						prefix = "response."
					}
					if !gjson.GetBytes(payload, prefix+"billing.probe_penalty").Exists() {
						continue
					}
					output := gjson.GetBytes(payload, prefix+"usage.output_tokens")
					if !output.Exists() {
						output = gjson.GetBytes(payload, prefix+"usage.completion_tokens")
					}
					require.Equal(t, int64(expected), output.Int())
					found = true
				}
				require.True(t, found, "the last sparse delta must contain canonical cumulative billing usage")
			})
		}
	}
}

func TestProbePenaltyRequiresTerminalUsageCarrier(t *testing.T) {
	w, _ := probeTestWriter(t, true)
	_, err := w.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	require.NoError(t, err)
	require.Error(t, w.validate(), "a malformed stream cannot settle without a terminal frame to carry the equivalent usage")
}
