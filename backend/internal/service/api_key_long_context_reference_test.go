//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyLongContextCompleteHistoryWithItemIDs(t *testing.T) {
	gateway := &GatewayService{billingService: longContextTestBilling(100, false)}
	for name, item := range map[string]string{
		"message":         `{"id":"msg_history","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}`,
		"function call":   `{"id":"fc_history","type":"function_call","name":"lookup","call_id":"call_1","arguments":"{}"}`,
		"function output": `{"id":"fc_history","type":"function_call_output","call_id":"call_1","output":"visible result"}`,
		"empty arguments": `{"id":"fc_history","type":"function_call","name":"lookup","call_id":"call_1","arguments":""}`,
		"reasoning text":  `{"id":"rs_history","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"visible thought"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"context-policy-test","input":[` + item + `]}`)
			require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, body, "context-policy-test"))
		})
	}
	for _, item := range []string{
		`{"id":"msg_history","type":"message"}`,
		`{"id":"fc_history","type":"function_call","call_id":"call_1","name":"lookup"}`,
		`{"id":"rs_history","type":"reasoning","summary":[]}`,
		`{"id":"msg_history","type":"item_reference","content":"unused"}`,
	} {
		var policyErr *APIKeyLongContextError
		require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, []byte(`{"input":[`+item+`]}`)), &policyErr)
		require.Equal(t, "context_unverifiable", policyErr.Code)
	}
	// A complete history object is counted rather than ignored because it has an ID.
	body := []byte(`{"model":"context-policy-test","input":[{"id":"msg_history","type":"message","role":"assistant","content":"` + strings.Repeat("context ", 120) + `"}]}`)
	var policyErr *APIKeyLongContextError
	require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, body, "context-policy-test"), &policyErr)
	require.Equal(t, "context_length_exceeded", policyErr.Code)
}

func TestAPIKeyLongContextBusinessMetadataKeepsTextCounting(t *testing.T) {
	gateway := &GatewayService{billingService: longContextTestBilling(100, false)}
	key := &APIKey{AllowLongContext: false}
	body := []byte(`{"model":"context-policy-test","input":"hello","metadata":{"conversation_id":"business-id","file_id":"business-file","type":"input_file"}}`)
	require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), key, body, "context-policy-test"))
	// Metadata does not load remote content, but its visible strings still
	// participate in the same conservative request-size estimate.
	body = []byte(`{"model":"context-policy-test","input":"hello","metadata":{"file_id":"` + strings.Repeat("context ", 120) + `"}}`)
	var policyErr *APIKeyLongContextError
	require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), key, body, "context-policy-test"), &policyErr)
	require.Equal(t, "context_length_exceeded", policyErr.Code)
	// Only the standard top-level dictionary is exempt from reference semantics.
	for _, request := range []string{
		`{"metadata":{"file_id":"business-file"},"file_id":"actual-file"}`,
		`{"metadata":{"conversation_id":"business-id"},"conversation_id":"actual-conversation"}`,
		`{"client_metadata":{"file_id":"actual-file"}}`,
		`{"input":[{"metadata":{"file_id":"actual-file"}}]}`,
	} {
		require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), key, []byte(request), "context-policy-test"), &policyErr)
		require.Equal(t, "context_unverifiable", policyErr.Code)
	}
}

func TestAPIKeyLongContextToolDataKeepsTextCounting(t *testing.T) {
	gateway := &GatewayService{billingService: longContextTestBilling(300, false)}
	key := &APIKey{AllowLongContext: false}
	data := `{"id":"msg_business","file_id":"local-report","conversation_id":"business-id","type":"input_file","nested":{"encrypted_content":"business-value"},"text":%q}`
	for name, template := range map[string]string{
		"Anthropic tool arguments":  `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":` + data + `}]}]}`,
		"Gemini function arguments": `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":` + data + `}}]}]}`,
		"Gemini function results":   `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":` + data + `}}]}]}`,
		"Gemini snake case results": `{"contents":[{"parts":[{"function_response":{"name":"lookup","response":` + data + `}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), key, []byte(fmt.Sprintf(template, "hello")), "context-policy-test"))
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), key, []byte(fmt.Sprintf(template, strings.Repeat("context ", 500))), "context-policy-test"), &policyErr)
			require.Equal(t, "context_length_exceeded", policyErr.Code)
		})
	}
	// Real attachments in tool-result protocol blocks remain unmeasurable.
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_fixture","content":[{"type":"document","source":{"type":"file","file_id":"file_fixture"}}]}]}]}`,
		`{"contents":[{"parts":[{"functionResponse":{"name":"lookup","response":{"file_id":"business"}},"fileData":{"fileUri":"gs://fixture/file"}}]}]}`,
	} {
		var policyErr *APIKeyLongContextError
		require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), key, []byte(body)), &policyErr)
		require.Equal(t, "context_unverifiable", policyErr.Code)
	}
}

func TestAPIKeyLongContextRejectsRedactedThinking(t *testing.T) {
	gateway := &GatewayService{}
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque-fixture"},{"type":"text","text":"hello"}]},{"role":"user","content":"continue"}]}`)
	var policyErr *APIKeyLongContextError
	require.ErrorAs(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{}, body), &policyErr)
	require.Equal(t, "context_unverifiable", policyErr.Code)
	require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), &APIKey{AllowLongContext: true}, body))
}

func TestAPIKeyLongContextFinalUnknownTargetKeepsDefaultLimit(t *testing.T) {
	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.4": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, LongContextInputTokenThreshold: 272_000, LongContextInputCostMultiplier: 2},
	}})
	gateway := &GatewayService{billingService: billing}
	content := strings.Repeat("context ", 200_100)
	for _, tc := range []struct {
		name, actual, public string
		blocked              bool
	}{
		{"unknown account target", "unknown-target", "gpt-5.4", true},
		{"known target with unknown public alias", "gpt-5.4", "public-alias", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"input":%q}`, tc.actual, content))
			key := &APIKey{ID: 12, AllowLongContext: false}
			// At ingress a public alias can coexist with its known target.
			require.NoError(t, gateway.CheckAPIKeyLongContext(context.Background(), key, body, tc.public, "gpt-5.4"))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx := WithAPIKeyQueueRequestPermissions(context.Background(), APIKeyQueueRequestPermissions{Models: []string{tc.public}})
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			c.Set("api_key", key)
			SetAPIKeyLongContextForwardChecker(c, gateway.CheckAPIKeyLongContext)
			err := checkAPIKeyLongContextBeforeForward(ctx, c, billing, body, tc.actual)
			if !tc.blocked {
				require.NoError(t, err)
				return
			}
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, err, &policyErr)
			require.Equal(t, "context_length_exceeded", policyErr.Code)
			require.Equal(t, APIKeyDefaultContextLimit, policyErr.Limit)
		})
	}
}
