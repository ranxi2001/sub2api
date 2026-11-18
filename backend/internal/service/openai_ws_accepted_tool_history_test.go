//go:build unit && bridgewarmup

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTPBridgeAcceptedHistoryRequiresCompletedDelivery(t *testing.T) {
	const input = `{"model":"gpt-6-astra","store":false,"input":[{"type":"function_call","call_id":"old_call","name":"fixture","arguments":"{}","status":"completed"},{"type":"function_call_output","call_id":"old_call","output":"receipt"}]}`
	for _, outcome := range []string{"failed", "completed", "write_failed"} {
		t.Run(outcome, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			manager, _, account := issue715WSManager(t, svc, context.Background())
			defer manager.abort()
			require.NoError(t, issue715WSBegin(t, manager, account, input, "http_bridge"))
			event := `{"type":"response.failed","response":{"id":"resp_history","status":"failed","error":{"code":"fixture"},"output":[]}}`
			if outcome != "failed" {
				event = `{"type":"response.completed","response":{"id":"resp_history","status":"completed","output":[]}}`
			}
			err := manager.write([]byte(event), func() error {
				if outcome == "write_failed" {
					return errors.New("fixture delivery failed")
				}
				return nil
			})
			if outcome == "write_failed" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			completed := outcome == "completed"
			call := gjson.Get(input, "input.0")
			require.Equal(t, completed, acceptsPriorToolPair(manager.acceptedHistoryTools, account, "gpt-6-astra", call, []byte(input)))
			require.False(t, acceptsPriorToolPair(manager.acceptedHistoryTools, account, "gpt-6-astra", call, []byte(strings.Replace(input, "receipt", "altered", 1))))
			require.False(t, acceptsPriorToolPair(manager.acceptedHistoryTools, account, "gpt-5.1", call, []byte(input)))
			other := *account
			other.ID++
			require.False(t, acceptsPriorToolPair(manager.acceptedHistoryTools, &other, "gpt-6-astra", call, []byte(input)))
			other = *account
			other.Credentials = map[string]any{"api_key": "fixture-rotated"}
			require.False(t, acceptsPriorToolPair(manager.acceptedHistoryTools, &other, "gpt-6-astra", call, []byte(input)))
			require.Empty(t, manager.deliveredTools, "accepted history must not masquerade as tools delivered by this generation")
			if completed {
				require.Len(t, manager.historicalToolIDs, 1)
			} else {
				require.Empty(t, manager.historicalToolIDs)
			}
		})
	}
}

func TestHTTPBridgeAcceptedHistoryRejectsRegeneratedCallID(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	manager, _, account := issue715WSManager(t, svc, context.Background())
	defer manager.abort()
	source := `{"model":"gpt-6-astra","store":false,"input":[{"type":"function_call","call_id":"old_call","name":"fixture","arguments":"{}","status":"completed"},{"type":"function_call_output","call_id":"old_call","output":"receipt"}]}`
	require.NoError(t, issue715WSBegin(t, manager, account, source, "http_bridge"))
	event := `{"type":"response.completed","response":{"id":"resp_reused_id","status":"completed","output":[{"type":"function_call","call_id":"old_call","name":"fixture","arguments":"{}","status":"completed"}]}}`
	require.NoError(t, manager.write([]byte(event), func() error { return nil }))
	observed, found := manager.deliveredTools["gpt-6-astra\x00old_call"]
	require.True(t, found)
	require.True(t, observed.conflicted, "a newly delivered call cannot reuse an old accepted call ID")
}

func TestHTTPBridgeAcceptedHistoryRejectsCrossGenerationCallIDReuse(t *testing.T) {
	for _, tool := range []struct{ kind, field string }{{"function_call", "arguments"}, {"custom_tool_call", "input"}} {
		for _, intervening := range []string{"none", "empty", "other_history"} {
			for _, reused := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/intervening=%s/reused=%t", tool.kind, intervening, reused), func(t *testing.T) {
					svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
					manager, c, account := issue715WSManager(t, svc, context.Background())
					SetOpenAIWSRecoveryPrincipal(c, 715, 715)
					defer manager.abort()
					oldCall := fmt.Sprintf(`{"type":%q,"call_id":"old_call","name":"fixture",%q:"{}","status":"completed"}`, tool.kind, tool.field)
					first := `{"model":"gpt-6-astra","store":false,"input":[` + oldCall + `,{"type":"` + tool.kind + `_output","call_id":"old_call","output":"receipt"}]}`
					require.NoError(t, issue715WSBegin(t, manager, account, first, "http_bridge"))
					require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_history","status":"completed","output":[]}}`), func() error { return nil }))
					require.True(t, acceptsPriorToolPair(manager.acceptedHistoryTools, account, "gpt-6-astra", gjson.Parse(oldCall), []byte(first)))
					require.Empty(t, manager.deliveredTools, "round one accepted context never grants delivery proof")

					const independent = `{"model":"gpt-6-astra","store":false,"input":[{"role":"user","content":"fixture independent task"}]}`
					if intervening != "none" {
						source := independent
						if intervening == "other_history" {
							source = strings.ReplaceAll(first, "old_call", "other_call")
						}
						require.NoError(t, issue715WSBegin(t, manager, account, source, "http_bridge"))
						require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_intervening","status":"completed","output":[]}}`), func() error { return nil }))
						require.NotContains(t, manager.acceptedHistoryTools, "gpt-6-astra\x00old_call", "an intervening task can replace the current context")
					}
					require.NoError(t, issue715WSBegin(t, manager, account, independent, "http_bridge"))
					call := oldCall
					if !reused {
						call = strings.ReplaceAll(call, "old_call", "unique_call")
					}
					require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_new_task","status":"completed","output":[`+call+`]}}`), func() error { return nil }))
					observed, found := manager.deliveredTools["gpt-6-astra\x00"+gjson.Get(call, "call_id").String()]
					require.True(t, found)
					require.Equal(t, reused, observed.conflicted, "only the reused historical identity must lose delivery proof")
					require.Empty(t, manager.acceptedHistoryTools)
					wantIDs := 1
					if intervening == "other_history" {
						wantIDs++
					}
					require.Len(t, manager.historicalToolIDs, wantIDs, "independent tasks cannot erase collision evidence")
					err := beginHTTPBridgeAcceptedHistoryContinuation(t, manager, account, call)
					if reused {
						var refusal *OpenAIAgentRecoveryError
						require.ErrorAs(t, err, &refusal, "round three cannot use the regenerated ID as delivered proof")
						require.Equal(t, "owner_conflict", refusal.Reason)
						require.Equal(t, "tool_provenance_unknown", refusal.InternalSubreason())
					} else {
						require.NoError(t, err, "a unique new call must retain normal tool-result continuation")
					}
				})
			}
		}
	}
}

func TestHTTPBridgeAcceptedHistoryCallIDRetentionBound(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
	manager, c, account := issue715WSManager(t, svc, context.Background())
	SetOpenAIWSRecoveryPrincipal(c, 715, 715)
	defer manager.abort()
	var pairs []string
	for i := 0; i < 1024; i++ {
		pairs = append(pairs, fmt.Sprintf(`{"type":"function_call","call_id":"history_%d","name":"fixture","arguments":"{}","status":"completed"},{"type":"function_call_output","call_id":"history_%d","output":"receipt"}`, i, i))
	}
	first := `{"model":"gpt-6-astra","store":false,"input":[` + strings.Join(pairs, ",") + `]}`
	require.NoError(t, issue715WSBegin(t, manager, account, first, "http_bridge"))
	require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_capacity","status":"completed","output":[]}}`), func() error { return nil }))
	require.Len(t, manager.historicalToolIDs, 1024)
	require.False(t, manager.historicalToolIDsExhausted)
	// Reaccepting the same identities consumes no additional capacity.
	require.NoError(t, issue715WSBegin(t, manager, account, first, "http_bridge"))
	require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_capacity_again","status":"completed","output":[]}}`), func() error { return nil }))
	require.Len(t, manager.historicalToolIDs, 1024)
	require.False(t, manager.historicalToolIDsExhausted)
	const extraCall = `{"type":"function_call","call_id":"history_overflow","name":"fixture","arguments":"{}","status":"completed"}`
	extra := `{"model":"gpt-6-astra","store":false,"input":[` + extraCall + `,{"type":"function_call_output","call_id":"history_overflow","output":"receipt"}]}`
	require.NoError(t, issue715WSBegin(t, manager, account, extra, "http_bridge"))
	require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_overflow","status":"completed","output":[]}}`), func() error { return nil }))
	require.Len(t, manager.historicalToolIDs, 1024, "identity retention must stay bounded")
	require.True(t, manager.historicalToolIDsExhausted)
	require.Error(t, beginHTTPBridgeAcceptedHistoryContinuation(t, manager, account, extraCall), "overflow cannot authorize an unretained identity as accepted context")

	// Saturation does not prevent a legitimate new independent task from completing.
	const independent = `{"model":"gpt-6-astra","store":false,"input":[{"role":"user","content":"fixture independent task"}]}`
	require.NoError(t, issue715WSBegin(t, manager, account, independent, "http_bridge"))
	require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_plain_task","status":"completed","output":[]}}`), func() error { return nil }))
	require.Empty(t, manager.acceptedHistoryTools)
	require.True(t, manager.historicalToolIDsExhausted, "context replacement must not reset exhaustion")
	// An identity omitted at capacity can never become a fresh delivery later.
	require.NoError(t, issue715WSBegin(t, manager, account, independent, "http_bridge"))
	require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_unretained","status":"completed","output":[`+extraCall+`]}}`), func() error { return nil }))
	observed, found := manager.deliveredTools["gpt-6-astra\x00history_overflow"]
	require.True(t, found)
	require.True(t, observed.conflicted)
	require.Error(t, beginHTTPBridgeAcceptedHistoryContinuation(t, manager, account, extraCall))
	require.Len(t, manager.historicalToolIDs, 1024)
}

func beginHTTPBridgeAcceptedHistoryContinuation(t *testing.T, manager *openAIWSAgentRecovery, account *Account, call string) error {
	t.Helper()
	result := fmt.Sprintf(`{"type":%q,"call_id":%q,"output":"receipt"}`, gjson.Get(call, "type").String()+"_output", gjson.Get(call, "call_id").String())
	source := fmt.Sprintf(`{"type":"response.create","model":"gpt-6-astra","store":false,"previous_response_id":%q,"input":[%s]}`, manager.completedResponse, result)
	body := `{"model":"gpt-6-astra","store":false,"input":[{"role":"user","content":"fixture independent task"},` + call + `,` + result + `]}`
	proof := &openAIWSPlaintextProjection{anchor: manager.completedBridge, accountID: account.ID, previous: manager.completedResponse,
		sourceInput: openAIAgentRecoveryInputDigest([]byte(source)), sourceControls: openAI409ControlsDigest([]byte(source)),
		input: openAIAgentRecoveryInputDigest([]byte(body)), controls: openAI409ControlsDigest([]byte(body)), history: []byte(body)}
	ctx := context.WithValue(context.Background(), openAIWSPlaintextProjectionKey{}, proof)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://native.example/v1/responses", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer fixture")
	return manager.begin(account, []byte(source), []byte(body), req, "http_bridge")
}
