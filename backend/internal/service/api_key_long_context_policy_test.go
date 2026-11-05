//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func longContextTestBilling(threshold int, inclusive bool) *BillingService {
	provider := ""
	if inclusive {
		provider = "xai"
	}
	return NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"context-policy-test": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, LongContextInputTokenThreshold: threshold, LongContextInputCostMultiplier: 2, LiteLLMProvider: provider},
	}})
}

func TestAPIKeyLongContextPolicyIsolationAndThresholds(t *testing.T) {
	ctx := context.Background()
	billing := longContextTestBilling(100, false)
	gateway := &GatewayService{billingService: billing}
	openai := &OpenAIGatewayService{billingService: billing}
	body := []byte(`{"model":"context-policy-test","input":"` + strings.Repeat("context ", 120) + `"}`)
	for _, check := range []func(context.Context, *APIKey, []byte, ...string) error{gateway.CheckAPIKeyLongContext, openai.CheckAPIKeyLongContext} {
		for _, enabled := range []bool{false, true, false} {
			err := check(ctx, &APIKey{AllowLongContext: enabled}, body, "context-policy-test")
			if enabled {
				require.NoError(t, err)
			} else {
				var policyErr *APIKeyLongContextError
				require.ErrorAs(t, err, &policyErr)
				require.Equal(t, "context_length_exceeded", policyErr.Code)
				require.Equal(t, 100, policyErr.Limit)
			}
		}
		require.NoError(t, check(ctx, &APIKey{}, []byte(`{"model":"context-policy-test","input":"hello"}`), "context-policy-test"))
		require.NoError(t, check(ctx, nil, body, "context-policy-test"))
	}
	require.Equal(t, 99, apiKeyLongContextLimit(longContextTestBilling(100, true), []string{"context-policy-test"}))
	require.Equal(t, APIKeyDefaultContextLimit, apiKeyLongContextLimit(billing, []string{"unknown-model"}))
	require.Equal(t, 100, apiKeyLongContextLimit(billing, []string{"public-alias", "context-policy-test"}))
	require.Equal(t, 272_000, apiKeyLongContextLimit(newTestBillingServiceWithOpenAILadderCatalog(t), []string{"gpt-5.4"}))
	require.Equal(t, 199_999, apiKeyLongContextLimit(newTestBillingService(), []string{"grok-4.3"}))
}

func TestAPIKeyLongContextPolicyCountsAllVisibleHistoryAndTools(t *testing.T) {
	gateway := &GatewayService{billingService: longContextTestBilling(100, false)}
	large := strings.Repeat("历史 Context ", 100)
	for name, body := range map[string]string{
		"responses":       `{"input":"` + large + `"}`,
		"chat":            `{"messages":[{"role":"system","content":"` + large + `"},{"role":"user","content":"hi"}]}`,
		"messages cached": `{"messages":[{"content":[{"type":"text","text":"` + large + `","cache_control":{"type":"ephemeral"}}]}]}`,
		"tools":           `{"input":"hi","tools":[{"type":"function","function":{"name":"test","description":"` + large + `"}}]}`,
		"gemini":          `{"systemInstruction":{"parts":[{"text":"` + large + `"}]},"contents":[{"parts":[{"text":"hi"}]}]}`,
		"duplicate input": `{"input":"hi","input":"` + large + `"}`,
		"case variant":    `{"input":"hi","Input":"` + large + `"}`,
		"WS envelope":     `{"type":"response.create","response":{"input":"` + large + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, []byte(body), "context-policy-test"), &policyErr)
			require.Equal(t, "context_length_exceeded", policyErr.Code)
		})
	}
}

func TestAPIKeyLongContextPolicyRejectsUnmeasurableContext(t *testing.T) {
	gateway := &GatewayService{}
	for name, body := range map[string]string{
		"previous response":   `{"previous_response_id":"resp_fixture","input":"hi"}`,
		"duplicate reference": `{"previous_response_id":"","Previous_Response_Id":"resp_fixture","input":"hi"}`,
		"conversation":        `{"conversation":{"id":"conv_fixture"},"input":"hi"}`,
		"item reference":      `{"input":[{"type":"item_reference","id":"msg_fixture"}]}`,
		"stored message":      `{"input":[{"type":"message","ID":"msg_fixture"}]}`,
		"stored reasoning":    `{"input":[{"type":"reasoning","id":"rs_fixture"}]}`,
		"cached content":      `{"cachedContent":"cachedContents/fixture","contents":[]}`,
		"file URI":            `{"contents":[{"parts":[{"fileData":{"fileUri":"gs://fixture/file"}}]}]}`,
		"file id":             `{"input":[{"type":"input_file","file_id":"file_fixture"}]}`,
		"inline chat file":    `{"messages":[{"content":[{"type":"file","file":{"file_data":"data:text/plain;base64,aGk="}}]}]}`,
		"video attachment":    `{"messages":[{"content":[{"type":"video_url","video_url":{"url":"https://fixture/video"}}]}]}`,
		"encrypted":           `{"input":[{"type":"reasoning","encrypted_content":"fixture"}]}`,
		"compaction":          `{"input":[{"type":"compaction","encrypted_content":"fixture"}]}`,
		"hosted search":       `{"tools":[{"type":"file_search","vector_store_ids":["vs_fixture"]}]}`,
		"hosted web fetch":    `{"tools":[{"type":"web_fetch_20250910","name":"web_fetch"}]}`,
		"hosted mcp":          `{"mcp_servers":[{"type":"url","url":"https://fixture/mcp"}],"messages":[]}`,
		"gemini search":       `{"tools":[{"googleSearch":{}}],"contents":[]}`,
		"gemini retrieval":    `{"tools":[{"retrieval":{"vertexAiSearch":{"datastore":"fixture"}}}],"contents":[]}`,
		"gemini URL context":  `{"tools":[{"urlContext":{}}],"contents":[]}`,
		"gemini code":         `{"tools":[{"codeExecution":{}}],"contents":[]}`,
		"WS session update":   `{"type":"session.update","session":{"instructions":"hi"}}`,
		"WS append":           `{"type":"response.append","input":"hi"}`,
		"stored prompt":       `{"prompt":{"id":"pmpt_fixture"},"input":"hi"}`,
		"prompt case variant": `{"prompt":{"ID":"pmpt_fixture"},"input":"hi"}`,
		"malformed":           `{"input":`,
	} {
		t.Run(name, func(t *testing.T) {
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, []byte(body)), &policyErr)
			require.Equal(t, "context_unverifiable", policyErr.Code)
			require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{AllowLongContext: true}, []byte(body)))
		})
	}
	// Function parameter names are schemas, not real context references.
	require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, []byte(`{"input":"hi","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"file_id":{"type":"string"},"conversation":{"type":"string"}}}}}]}`)))
	require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, []byte(`{"input":"hi","previous_response_id":null}`)))
	require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, []byte(`{"input":"hi","metadata":{"id":"msg_business"}}`)))
}
