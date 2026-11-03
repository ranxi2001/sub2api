//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRecordUsageAPIKeyModelPolicyBilling(t *testing.T) {
	for _, tc := range []struct {
		name, source, applied, want string
		mapping                     map[string]string
	}{
		{"key target", BillingModelSourceRequested, "gpt-5.6-sol", "gpt-5.6-sol", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}},
		{"one hop only", BillingModelSourceRequested, "gpt-5.6-sol", "gpt-5.6-sol", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol", "gpt-5.6-sol": "gpt-5.1"}},
		{"configuration changed after forwarding", BillingModelSourceRequested, "gpt-5.6-sol", "gpt-5.6-sol", map[string]string{"gpt-6.1-sol": "gpt-5.1"}},
		{"configured without applied rewrite", BillingModelSourceRequested, "", "gpt-6.1-sol", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}},
		{"other key unchanged", BillingModelSourceRequested, "", "gpt-6.1-sol", nil},
		{"channel policy retained", BillingModelSourceChannelMapped, "gpt-5.6-sol", "gpt-5.1", map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			expected, err := svc.billingService.CalculateCost(tc.want, UsageTokens{InputTokens: 100, OutputTokens: 20}, 1.1)
			require.NoError(t, err)
			err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "key-policy-billing", Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol",
					Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 20}, Duration: time.Second,
				},
				APIKey: &APIKey{ID: 10, ModelMapping: tc.mapping}, User: &User{ID: 20}, Account: &Account{ID: 30},
				ChannelUsageFields: ChannelUsageFields{
					OriginalModel: "gpt-6.1-sol", ChannelMappedModel: "gpt-5.1", BillingModelSource: tc.source,
					APIKeyMappedModel: tc.applied, ChannelMappingApplied: tc.source == BillingModelSourceChannelMapped,
				},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, "gpt-6.1-sol", usageRepo.lastLog.RequestedModel)
			// Model records forwarding identity; cost follows the selected billing source.
			require.Equal(t, "gpt-5.6-sol", usageRepo.lastLog.Model)
			require.Equal(t, "gpt-5.6-sol", *usageRepo.lastLog.UpstreamModel)
			require.Equal(t, QuantizeUsageBillingAmount(expected.ActualCost), usageRepo.lastLog.ActualCost)
		})
	}
}

func TestGatewayRecordUsageAPIKeyModelPolicyBilling(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	key := &APIKey{ID: 10, ModelMapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol", "gpt-5.6-sol": "gpt-5.1"}}
	fields := ChannelUsageFields{OriginalModel: "gpt-6.1-sol", APIKeyMappedModel: "gpt-5.6-sol", BillingModelSource: BillingModelSourceRequested}
	result := &ForwardResult{
		RequestID: "key-policy-generic-billing", Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol",
		Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 20}, Duration: time.Second,
	}
	require.Equal(t, "gpt-5.6-sol", svc.recordUsageBillingModel(context.Background(), key, result, fields))
	expected, err := svc.billingService.CalculateCost("gpt-5.6-sol", UsageTokens{InputTokens: 100, OutputTokens: 20}, 1.1)
	require.NoError(t, err)
	err = svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: result, APIKey: key, User: &User{ID: 20}, Account: &Account{ID: 30}, ChannelUsageFields: fields,
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "gpt-6.1-sol", usageRepo.lastLog.RequestedModel)
	require.Equal(t, "gpt-5.6-sol", usageRepo.lastLog.Model)
	require.Equal(t, "gpt-5.6-sol", *usageRepo.lastLog.UpstreamModel)
	require.Equal(t, QuantizeUsageBillingAmount(expected.ActualCost), usageRepo.lastLog.ActualCost)
}

// An unpriced key target may fall back to the actual account model, but never
// to the old client alias carried in an unchanged channel audit field.
func TestOpenAIRecordUsageAPIKeyModelPolicyUnmappedChannelFallback(t *testing.T) {
	for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped} {
		t.Run(source, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			const target = "not-priceable-alias"
			expected, err := svc.billingService.CalculateCost("gpt-5.4", UsageTokens{InputTokens: 100, OutputTokens: 20}, 1.1)
			require.NoError(t, err)
			err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "key-policy-channel-fallback", Model: target, BillingModel: target, UpstreamModel: "gpt-5.4",
					Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 20}, Duration: time.Second,
				},
				APIKey: &APIKey{ID: 10, ModelMapping: map[string]string{"gpt-6.1-sol": target}},
				User:   &User{ID: 20}, Account: &Account{ID: 30},
				ChannelUsageFields: ChannelUsageFields{
					OriginalModel: "gpt-6.1-sol", ChannelMappedModel: "gpt-6.1-sol", BillingModelSource: source,
					APIKeyMappedModel: target,
				},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, "gpt-6.1-sol", usageRepo.lastLog.RequestedModel)
			require.Equal(t, QuantizeUsageBillingAmount(expected.ActualCost), usageRepo.lastLog.ActualCost)
		})
	}
}

func TestGatewayRecordUsageAPIKeyModelPolicyUnmappedChannel(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	key := &APIKey{ID: 10, ModelMapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}}
	fields := ChannelUsageFields{
		OriginalModel: "gpt-6.1-sol", ChannelMappedModel: "gpt-6.1-sol", BillingModelSource: BillingModelSourceChannelMapped,
		APIKeyMappedModel: "gpt-5.6-sol",
	}
	result := &ForwardResult{
		RequestID: "key-policy-unmapped-channel", Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol",
		Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 20}, Duration: time.Second,
	}
	require.Equal(t, "gpt-5.6-sol", svc.recordUsageBillingModel(context.Background(), key, result, fields))
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: result, APIKey: key, User: &User{ID: 20}, Account: &Account{ID: 30}, ChannelUsageFields: fields,
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "gpt-5.6-sol", usageRepo.lastLog.Model)
	require.Equal(t, "gpt-6.1-sol", usageRepo.lastLog.RequestedModel)
}

func TestOpenAIRecordUsageAPIKeyModelPolicySkippedEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, model, configuredTarget string
		image                                   bool
	}{
		{"embeddings", "/v1/embeddings", "text-embedding-3-small", "gpt-5.6-sol", false},
		{"images", "/v1/images/generations", "grok-imagine-image-quality", "grok-imagine-image", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			result := &OpenAIForwardResult{
				RequestID: "key-policy-skipped-" + tc.name, Model: tc.model, BillingModel: tc.model, UpstreamModel: tc.model,
				Usage: OpenAIUsage{InputTokens: 100}, Duration: time.Second,
			}
			var expected, wrong *CostBreakdown
			if tc.image {
				result.ImageCount, result.ImageSize, result.ImageEndpoint = 1, "2K", true
				expected = svc.billingService.CalculateImageCost(tc.model, "2K", 1, nil, 1.1)
				wrong = svc.billingService.CalculateImageCost(tc.configuredTarget, "2K", 1, nil, 1.1)
			} else {
				// Isolate the embedding price from optional downloaded price tables.
				svc.billingService = NewBillingService(svc.cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
					tc.model:            {Mode: "embedding", InputCostPerToken: 2e-8},
					tc.configuredTarget: {Mode: "chat", InputCostPerToken: 5e-6, OutputCostPerToken: 30e-6},
				}})
				var err error
				expected, err = svc.billingService.CalculateCost(tc.model, UsageTokens{InputTokens: 100}, 1.1)
				require.NoError(t, err)
				wrong, err = svc.billingService.CalculateCost(tc.configuredTarget, UsageTokens{InputTokens: 100}, 1.1)
				require.NoError(t, err)
			}
			require.NotEqual(t, QuantizeUsageBillingAmount(expected.ActualCost), QuantizeUsageBillingAmount(wrong.ActualCost))
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: result, InboundEndpoint: tc.endpoint,
				APIKey: &APIKey{ID: 10, ModelMapping: map[string]string{tc.model: tc.configuredTarget}},
				User:   &User{ID: 20}, Account: &Account{ID: 30},
				ChannelUsageFields: ChannelUsageFields{
					OriginalModel: tc.model, ChannelMappedModel: tc.model, BillingModelSource: BillingModelSourceRequested,
					// No APIKeyMappedModel: forwarding deliberately skipped key policy.
				},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, tc.model, usageRepo.lastLog.RequestedModel)
			require.Equal(t, QuantizeUsageBillingAmount(expected.ActualCost), usageRepo.lastLog.ActualCost)
		})
	}
}

func TestAPIKeyModelPolicyExplicitChannelBackMappingBilling(t *testing.T) {
	const original, keyTarget, upstream = "gpt-6.1-sol", "gpt-5.6-sol", "gpt-5.1"
	fields := (ChannelMappingResult{
		Mapped: true, MappedModel: original, BillingModelSource: BillingModelSourceChannelMapped,
	}).ToUsageFields(original, upstream)
	fields.APIKeyMappedModel = keyTarget
	require.True(t, fields.ChannelMappingApplied)
	require.Equal(t, fields.OriginalModel, fields.ChannelMappedModel)
	key := &APIKey{ID: 10, ModelMapping: map[string]string{original: keyTarget}}
	t.Run("openai", func(t *testing.T) {
		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
		expected, err := svc.billingService.CalculateCost(original, UsageTokens{InputTokens: 100, OutputTokens: 20}, 1.1)
		require.NoError(t, err)
		err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result: &OpenAIForwardResult{
				RequestID: "key-policy-back-mapping-openai", Model: original, BillingModel: upstream, UpstreamModel: upstream,
				Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 20}, Duration: time.Second,
			},
			APIKey: key, User: &User{ID: 20}, Account: &Account{ID: 30}, ChannelUsageFields: fields,
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		require.Equal(t, original, usageRepo.lastLog.RequestedModel)
		require.Equal(t, QuantizeUsageBillingAmount(expected.ActualCost), usageRepo.lastLog.ActualCost)
	})
	t.Run("generic", func(t *testing.T) {
		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		result := &ForwardResult{
			RequestID: "key-policy-back-mapping-generic", Model: original, UpstreamModel: upstream,
			Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 20}, Duration: time.Second,
		}
		require.Equal(t, original, svc.recordUsageBillingModel(context.Background(), key, result, fields))
		expected, err := svc.billingService.CalculateCost(original, UsageTokens{InputTokens: 100, OutputTokens: 20}, 1.1)
		require.NoError(t, err)
		err = svc.RecordUsage(context.Background(), &RecordUsageInput{
			Result: result, APIKey: key, User: &User{ID: 20}, Account: &Account{ID: 30}, ChannelUsageFields: fields,
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		require.Equal(t, original, usageRepo.lastLog.RequestedModel)
		require.Equal(t, QuantizeUsageBillingAmount(expected.ActualCost), usageRepo.lastLog.ActualCost)
	})
}

func TestOpenAIRecordUsageAPIKeyModelPolicyNoMarkerSelfMappingPreservesBilling(t *testing.T) {
	const original, upstream = "gpt-6.1-sol", "gpt-5.1"
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	fields := (ChannelMappingResult{
		Mapped: true, MappedModel: original, BillingModelSource: BillingModelSourceChannelMapped,
	}).ToUsageFields(original, upstream)
	require.True(t, fields.ChannelMappingApplied)
	require.Empty(t, fields.APIKeyMappedModel)
	// An unchanged channel model historically preserves the forwarder's billing
	// model. Explicit key provenance is required to alter that precedence.
	expected, err := svc.billingService.CalculateCost(upstream, UsageTokens{InputTokens: 100, OutputTokens: 20}, 1.1)
	require.NoError(t, err)
	aliasCost, err := svc.billingService.CalculateCost(original, UsageTokens{InputTokens: 100, OutputTokens: 20}, 1.1)
	require.NoError(t, err)
	require.NotEqual(t, QuantizeUsageBillingAmount(expected.ActualCost), QuantizeUsageBillingAmount(aliasCost.ActualCost))
	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "key-policy-no-marker-self-mapping", Model: original, BillingModel: upstream, UpstreamModel: upstream,
			Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 20}, Duration: time.Second,
		},
		APIKey: &APIKey{ID: 10, ModelMapping: map[string]string{original: "gpt-5.6-sol"}},
		User:   &User{ID: 20}, Account: &Account{ID: 30}, ChannelUsageFields: fields,
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, original, usageRepo.lastLog.RequestedModel)
	require.Equal(t, QuantizeUsageBillingAmount(expected.ActualCost), usageRepo.lastLog.ActualCost)
}
