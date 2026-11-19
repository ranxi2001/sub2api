//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestImageMinimumChargeSurvivesDiscountsAndZeroUsage(t *testing.T) {
	for _, tc := range []struct {
		name           string
		actual         float64
		count, retries int
		want           float64
	}{
		{"zero usage", 0, 1, 0, .01}, {"personal discount", .0002, 2, 0, .02},
		{"token cost above floor", .3, 2, 0, .3}, {"no completed image", 0, 0, 0, 0},
		{"known billed header retry", .001, 2, 1, .04},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &CostBreakdown{BillingMode: "token", TotalCost: .1, InputCost: .1, ActualCost: tc.actual}
			details := applyImageMinimumCharge(c, tc.count, tc.retries, .01)
			require.InDelta(t, tc.want, c.ActualCost, 1e-12)
			require.Equal(t, .1, c.TotalCost, "procurement statistics stay based on observed usage")
			require.Equal(t, .1, c.InputCost)
			if tc.count > 0 {
				require.InDelta(t, tc.want-tc.actual, details.ImageMinimumChargeAdjustment, 1e-12)
			}
		})
	}
	c := &CostBreakdown{BillingMode: "image", ActualCost: .005}
	require.Nil(t, applyImageMinimumCharge(c, 1, 0, .01))
	require.Equal(t, .005, c.ActualCost)
}

func TestImageMinimumChargeValidation(t *testing.T) {
	for _, v := range []float64{-1, math.NaN(), math.Inf(1), 1e-9, 1001} {
		require.Error(t, validateImageMinimumCharge(PlatformOpenAI, ChannelModelPricing{BillingMode: BillingModeToken, ImageMinimumCharge: &v}))
	}
	v := .01
	require.NoError(t, validateImageMinimumCharge(PlatformOpenAI, ChannelModelPricing{BillingMode: BillingModeToken, ImageMinimumCharge: &v}))
	require.Error(t, validateImageMinimumCharge(PlatformOpenAI, ChannelModelPricing{BillingMode: BillingModeImage, ImageMinimumCharge: &v}))
}

func TestImageBillingEquivalentUsesSettledLedgerQuantum(t *testing.T) {
	for _, cost := range []float64{0, .00000001, .01000001, .0201, .03015, .0402, .12345678} {
		r := ImageBillingReceipt{Settled: true, ActualCost: cost}
		tokens, err := r.EquivalentTokens()
		require.NoError(t, err)
		require.InDelta(t, cost, float64(tokens)*ImageEquivalentOutputPricePerMillion/1e6, 1e-12)
	}
	_, err := (ImageBillingReceipt{ActualCost: .01}).EquivalentTokens()
	require.Error(t, err)
}

func TestRecordImageUsageFloorChargesWalletOnceAndPreservesTokens(t *testing.T) {
	for _, tokens := range []int{0, 100, 5000} {
		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		billingRepo := &openAIRecordUsageBillingRepoStub{}
		svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
		gid := int64(991)
		model := "gpt-image-2"
		resolver := newOpenAITokenImageChannelPricingResolverForTest(t, gid, model)
		svc.resolver = resolver
		svc.billingService = resolver.billingService
		group := &Group{ID: gid, Platform: PlatformOpenAI, RateMultiplier: .1, ModelPricing: []ChannelModelPricing{{Models: []string{model}, Platform: PlatformOpenAI, BillingMode: BillingModeToken, OutputPrice: f64p(.00001), ImageOutputPrice: f64p(.00001), ImageMinimumCharge: f64p(.01)}}}
		result := &OpenAIForwardResult{RequestID: "image-floor", Model: model, ImageEndpoint: true, ImageCount: 2, Usage: OpenAIUsage{OutputTokens: tokens, ImageOutputTokens: tokens}}
		require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 2, GroupID: &gid, Group: group}, User: &User{ID: 1}, Account: &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, ChannelUsageFields: ChannelUsageFields{OriginalModel: model}}))
		require.Equal(t, 1, billingRepo.calls)
		require.InDelta(t, .02, billingRepo.lastCmd.BalanceCost, 1e-12)
		require.Equal(t, tokens, usageRepo.lastLog.OutputTokens)
		require.Equal(t, tokens, usageRepo.lastLog.ImageOutputTokens)
		require.InDelta(t, float64(tokens)*.00001, usageRepo.lastLog.TotalCost, 1e-12)
		require.InDelta(t, .02, usageRepo.lastLog.CustomerCostDetails.ImageMinimumCharge, 1e-12)
	}
}

type imageReceiptRepo struct {
	DurableUsageBillingRepository
	recovery *UsageBillingRecovery
}

func (r *imageReceiptRepo) StageUsageBilling(context.Context, *UsageBillingCommand, *UsageLog) error {
	return nil
}
func (r *imageReceiptRepo) ApplyStagedUsageBilling(context.Context, UsageBillingEventKey) (*UsageBillingRecovery, error) {
	return r.recovery, nil
}

func TestImageReceiptComesFromDurableEventNotCurrentQuote(t *testing.T) {
	mode := "image"
	repo := &imageReceiptRepo{recovery: &UsageBillingRecovery{Event: UsageBillingEvent{Usage: UsageLog{ActualCost: .0201, BillingMode: &mode, ImageCount: 2}}, Result: UsageBillingApplyResult{Applied: true}}}
	receipt := &ImageBillingReceipt{}
	_, err := applyDurableUsageBilling(WithImageBillingReceipt(context.Background(), receipt), repo, &UsageBillingCommand{RequestID: "receipt", APIKeyID: 1}, &UsageLog{ActualCost: 99}, &postUsageBillingParams{Cost: &CostBreakdown{}}, &billingDeps{})
	require.NoError(t, err)
	require.True(t, receipt.Settled)
	require.Equal(t, .0201, receipt.ActualCost)
	require.Equal(t, 2, receipt.ImageCount)
	receipt = &ImageBillingReceipt{}
	repo.recovery.Result.Applied = false
	_, err = applyDurableUsageBilling(WithImageBillingReceipt(context.Background(), receipt), repo, &UsageBillingCommand{RequestID: "receipt", APIKeyID: 1}, &UsageLog{ActualCost: 99}, &postUsageBillingParams{Cost: &CostBreakdown{}}, &billingDeps{})
	require.NoError(t, err)
	require.False(t, receipt.Settled)
}
