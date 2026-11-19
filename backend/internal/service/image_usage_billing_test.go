//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
			details := applyImageMinimumCharge(c, tc.count, tc.retries, .01, nil)
			require.InDelta(t, tc.want, c.ActualCost, 1e-12)
			require.Equal(t, .1, c.TotalCost, "procurement statistics stay based on observed usage")
			require.Equal(t, .1, c.InputCost)
			if tc.count > 0 {
				require.InDelta(t, tc.want-tc.actual, details.ImageMinimumChargeAdjustment, 1e-12)
			}
		})
	}
	c := &CostBreakdown{BillingMode: "image", ActualCost: .005}
	require.Nil(t, applyImageMinimumCharge(c, 1, 0, .01, nil))
	require.Equal(t, .005, c.ActualCost)
}

func TestImageMinimumPreservesStandardTierSnapshot(t *testing.T) {
	for _, ultra := range []bool{false, true} {
		for _, floor := range []float64{.001, .01} {
			cost := &CostBreakdown{BillingMode: string(BillingModeToken), ActualCost: .002, TotalCost: .02}
			standard := &CustomerCostDetails{
				Version: 2, FreeFast: !ultra, FreeUltrafast: ultra, Multiplier: .5,
				CacheReadCost: .004, TotalCost: .004, ActualCost: .002,
				CacheCostDetails: []CustomerCacheCostDetail{{Kind: "read", Tokens: 40, UnitPrice: .0001}},
			}
			details := applyImageMinimumCharge(cost, 1, 0, floor, standard)
			want := *standard
			want.Version = 3
			want.ActualCost = math.Max(.002, floor)
			want.ImageMinimumCharge = floor
			want.ImageMinimumChargeAdjustment = QuantizeUsageBillingAmount(want.ActualCost - .002)
			require.Equal(t, &want, details)
			require.Equal(t, want.ActualCost, cost.ActualCost)
			require.Equal(t, .02, cost.TotalCost)
			require.Equal(t, 2, standard.Version)
			require.Equal(t, .002, standard.ActualCost)
		}
	}
}

func TestImageMinimumMappedEmptyResponse(t *testing.T) {
	for _, countBilling := range []bool{false, true} {
		for _, tc := range []struct {
			name, data string
			count      int
			cost       float64
		}{
			{"empty", "[]", 0, 0},
			{"two_completed", `[{"b64_json":"Zmlyc3Q="},{"b64_json":"c2Vjb25k"}]`, 2, .02},
		} {
			t.Run(fmt.Sprintf("%s/count_billing=%t", tc.name, countBilling), func(t *testing.T) {
				const publicModel, target = "gpt-image-public", "gpt-image-2"
				gid := int64(991)
				group := &Group{ID: gid, Platform: PlatformOpenAI, AllowImageGeneration: true, RateMultiplier: 1,
					ImageCountBilling: countBilling, ModelPricing: []ChannelModelPricing{{
						Models: []string{target}, Platform: PlatformOpenAI, BillingMode: BillingModeToken,
						InputPrice: f64p(.00001), OutputPrice: f64p(.00001), ImageMinimumCharge: f64p(.01),
					}}}
				key := &APIKey{ID: 2, GroupID: &gid, Group: group}
				requestBody := []byte(`{"model":"gpt-image-public","prompt":"fixture","n":2,"stream":false}`)
				c, _ := newOpenAIImagesTestContext(t, requestBody)
				c.Set("api_key", key)
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"mapped-image"}},
					Body: io.NopCloser(strings.NewReader(`{"data":` + tc.data + `,"usage":{"input_tokens":0,"output_tokens":0}}`)),
				}}
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				billingRepo := &openAIRecordUsageBillingRepoStub{}
				svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				resolver := newOpenAITokenImageChannelPricingResolverForTest(t, gid, target)
				svc.resolver, svc.billingService, svc.httpUpstream = resolver, resolver.billingService, upstream
				fields := (ChannelMappingResult{Mapped: true, MappedModel: target, BillingModelSource: BillingModelSourceChannelMapped}).ToUsageFields(publicModel, target)
				parsed := &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, Model: publicModel, N: 2, ContentType: "application/json"}
				account := newOpenAIImagesAPIKeyAccount()
				account.Status, account.Schedulable = StatusActive, true
				account.Extra = map[string]any{ExtraImageModelsKey: []string{publicModel}}
				account.GroupIDs = []int64{gid}
				svc.accountRepo = stubOpenAIAccountRepo{accounts: []Account{*account}}
				result, err := svc.ForwardImages(WithImageBillingModel(c.Request.Context(), fields), c, account, requestBody, parsed, target)
				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, target, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, tc.count, result.ImageCount)
				require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: result, APIKey: key, User: &User{ID: 1}, Account: account, ChannelUsageFields: fields,
				}))
				require.Equal(t, 1, billingRepo.calls)
				require.InDelta(t, tc.cost, billingRepo.lastCmd.BalanceCost, 1e-12)
				require.Equal(t, tc.count, usageRepo.lastLog.ImageCount)
			})
		}
	}
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
	created  bool
	stageErr error
	applyErr error
}

func (r *imageReceiptRepo) StageUsageBillingForReceipt(context.Context, *UsageBillingCommand, *UsageLog) (bool, error) {
	return r.created, r.stageErr
}
func (r *imageReceiptRepo) ApplyStagedUsageBilling(context.Context, UsageBillingEventKey) (*UsageBillingRecovery, error) {
	return r.recovery, r.applyErr
}

func TestImageReceiptComesFromDurableEventNotCurrentQuote(t *testing.T) {
	mode := "image"
	repo := &imageReceiptRepo{created: true, recovery: &UsageBillingRecovery{
		Event: UsageBillingEvent{Usage: UsageLog{ActualCost: .0201, BillingMode: &mode, ImageCount: 2}},
		Result: UsageBillingApplyResult{Applied: true}, Settled: true,
	}}
	receipt := &ImageBillingReceipt{}
	_, err := applyDurableUsageBilling(WithImageBillingReceipt(context.Background(), receipt), repo, &UsageBillingCommand{RequestID: "receipt", APIKeyID: 1}, &UsageLog{ActualCost: 99}, &postUsageBillingParams{Cost: &CostBreakdown{}}, &billingDeps{})
	require.NoError(t, err)
	require.True(t, receipt.Settled)
	require.Equal(t, .0201, receipt.ActualCost)
	require.Equal(t, 2, receipt.ImageCount)
	for _, tc := range []struct {
		name     string
		created  bool
		settled  bool
		stageErr error
		applyErr error
	}{
		{name: "legacy", created: true},
		{name: "older identical stage", settled: true},
		{name: "conflict", stageErr: ErrUsageBillingRequestConflict},
		{name: "unknown apply outcome", created: true, applyErr: ErrUsageBillingOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := &ImageBillingReceipt{}
			repo.created, repo.stageErr, repo.applyErr = tc.created, tc.stageErr, tc.applyErr
			repo.recovery.Result.Applied = false
			repo.recovery.Settled = tc.settled
			applied, err := applyDurableUsageBilling(WithImageBillingReceipt(context.Background(), receipt), repo, &UsageBillingCommand{RequestID: "receipt", APIKeyID: 1}, &UsageLog{ActualCost: 99}, &postUsageBillingParams{Cost: &CostBreakdown{}}, &billingDeps{})
			expected := tc.stageErr
			if expected == nil {
				expected = tc.applyErr
			}
			if expected != nil {
				require.ErrorIs(t, err, expected)
			} else {
				require.NoError(t, err)
			}
			require.False(t, applied)
			require.False(t, receipt.Settled)
		})
	}
}
