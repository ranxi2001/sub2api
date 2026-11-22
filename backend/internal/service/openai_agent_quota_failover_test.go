//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAgentQuotaFailoverRetainsOtherReplayRestrictions(t *testing.T) {
	quota := []byte(`{"error":{"type":"usage_limit_reached","resets_at":4102444800}}`)
	for _, tc := range []struct {
		name    string
		request openAIAgentRecoveryRequest
		status  int
		body    []byte
		allowed bool
	}{
		{"quota", openAIAgentRecoveryRequest{disableOwnerBinding: true}, 429, quota, true},
		{"strict", openAIAgentRecoveryRequest{}, 429, quota, false},
		{"pointer", openAIAgentRecoveryRequest{disableOwnerBinding: true, previousResponseID: "resp_owned"}, 429, quota, false},
		{"conversation", openAIAgentRecoveryRequest{disableOwnerBinding: true, replaySource: []byte(`{"conversation":"conv_owned"}`)}, 429, quota, false},
		{"compaction", openAIAgentRecoveryRequest{disableOwnerBinding: true, nativeCompaction: true}, 429, quota, false},
		{"rewritten_history", openAIAgentRecoveryRequest{disableOwnerBinding: true, omittedHistory: true}, 429, quota, false},
		{"ambiguous_server_error", openAIAgentRecoveryRequest{disableOwnerBinding: true}, 520, quota, false},
		{"ordinary_rate_limit", openAIAgentRecoveryRequest{disableOwnerBinding: true}, 429, []byte(`{"error":{"code":"rate_limit_exceeded","resets_at":4102444800}}`), false},
		{"echoed_input", openAIAgentRecoveryRequest{disableOwnerBinding: true}, 429, []byte(`{"error":{"type":"server_error"},"input":{"error":{"type":"usage_limit_reached"}}}`), false},
		{"truncated", openAIAgentRecoveryRequest{disableOwnerBinding: true}, 429, []byte(`{"error":{"type":"usage_limit_reached"}`), false},
		{"duplicate_error", openAIAgentRecoveryRequest{disableOwnerBinding: true}, 429, []byte(`{"error":{"type":"usage_limit_reached"},"error":{"type":"server_error"}}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, &tc.request)
			require.Equal(t, tc.allowed, openAIAgentQuotaFailoverAllowed(ctx, tc.status, tc.body))
		})
	}
}

func TestAgentQuotaRejectionReleasesOnlyItsDiscoveryReceipt(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "own_receipt", true: "concurrent_receipt"}[changed], func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			proof := openAIToolProof{Version: 1, Call: "completed-discovery-call", Producer: "original-producer"}
			raw, err := json.Marshal(proof)
			require.NoError(t, err)
			request := &openAIAgentRecoveryRequest{toolProofs: map[string]openAIToolProofSnapshot{
				"discovery": {raw: string(raw), proof: proof, discoveryDigest: "same-result"},
			}}
			svc.agentMessageRecovery.toolProofs = map[string]openAIToolLocalRecord{
				"discovery": {raw: string(raw), until: time.Now().Add(time.Hour)},
			}
			delivery := newOpenAIAgentRecoveryDelivery(svc, context.Background(), request, openAIAgentRecoveryBinding{})
			require.NoError(t, delivery.bindDiscoveryResults())
			if changed {
				svc.agentMessageRecovery.toolProofs["discovery"] = openAIToolLocalRecord{raw: "concurrent-record", until: time.Now().Add(time.Hour)}
				require.Error(t, delivery.rejectBeforeExecution())
				delivery.finish(false)
				require.Equal(t, "concurrent-record", svc.agentMessageRecovery.toolProofs["discovery"].raw)
				return
			}
			require.NoError(t, delivery.rejectBeforeExecution())
			delivery.finish(false)
			require.Equal(t, string(raw), svc.agentMessageRecovery.toolProofs["discovery"].raw)
			next := newOpenAIAgentRecoveryDelivery(svc, context.Background(), request, openAIAgentRecoveryBinding{})
			require.NoError(t, next.bindDiscoveryResults(), "the same result can be submitted after a definitive rejection")
		})
	}
}
