//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountProbePenaltyValidation(t *testing.T) {
	for _, minimum := range []any{nil, "0.001", -1., math.NaN(), math.Inf(1), 1001., .000000001, 0.} {
		require.Error(t, ValidateAccountProbePenaltyExtra(map[string]any{AccountProbePenaltyExtraKey: map[string]any{"enabled": true, "minimum_charge": minimum}}))
	}
	for _, enabled := range []bool{true, false} {
		a := &Account{Platform: PlatformAnthropic, Extra: map[string]any{AccountProbePenaltyExtraKey: map[string]any{"enabled": enabled, "minimum_charge": .001}}}
		require.NoError(t, ValidateAccountProbePenaltyExtra(a.Extra))
		if enabled {
			require.Equal(t, .001, a.ProbePenaltyMinimum())
		} else {
			require.Zero(t, a.ProbePenaltyMinimum())
		}
	}
	require.Zero(t, (*Account)(nil).ProbePenaltyMinimum())
	require.NoError(t, ValidateAccountProbePenaltyExtra(nil))
	require.Error(t, ValidateAccountProbePenaltyExtra(map[string]any{AccountProbePenaltyExtraKey: true}))
}

func TestAccountProbePenaltyEquivalentUsage(t *testing.T) {
	for _, actual := range []float64{.000312, .000315, .000630, .001, .308745} {
		cost := &CostBreakdown{ActualCost: actual, TotalCost: actual / 1.3, BillingMode: "token"}
		p, err := calculateProbePenalty(cost, .001, .0000195, 13)
		require.NoError(t, err)
		require.Equal(t, actual, cost.ActualCost, "quoting cannot mutate cost")
		if actual >= .001 {
			require.Nil(t, p)
			continue
		}
		require.Equal(t, .001, p.ActualCost)
		equivalent := actual + float64(p.BillingOutputTokens-p.OriginalOutputTokens)*p.OutputTokenPrice
		require.GreaterOrEqual(t, equivalent, .001)
		require.Less(t, equivalent, .001+p.OutputTokenPrice)
	}
	_, err := calculateProbePenalty(&CostBreakdown{}, .001, 0, 0)
	require.Error(t, err, "cannot invent token prices for a free/unknown model")
}

func TestAccountProbePenaltyCanonicalBilling(t *testing.T) {
	for _, eligible := range []bool{true, false} {
		usageRepo := &openAIRecordUsageLogRepoStub{}
		billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
		svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		result := &ForwardResult{RequestID: "probe-test", Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 13}, ProbePenaltyEligible: eligible}
		account := &Account{ID: 701, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{AccountProbePenaltyExtraKey: map[string]any{"enabled": true, "minimum_charge": .001}, "quota_limit": 100.}}
		err := svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: &APIKey{ID: 501, Quota: 100}, User: &User{ID: 601}, Account: account, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{}})
		require.NoError(t, err)
		require.Equal(t, 13, usageRepo.lastLog.OutputTokens)
		require.Equal(t, usageRepo.lastLog.ActualCost, billingRepo.lastCmd.BalanceCost)
		require.Equal(t, usageRepo.lastLog.TotalCost, billingRepo.lastCmd.AccountQuotaCost)
		if eligible {
			require.Equal(t, .001, billingRepo.lastCmd.BalanceCost)
			require.Equal(t, .001, billingRepo.lastCmd.APIKeyQuotaCost)
			require.NotNil(t, usageRepo.lastLog.CustomerCostDetails.ProbePenalty)
			require.Greater(t, result.ProbePenalty.BillingOutputTokens, 13)
		} else {
			require.Less(t, billingRepo.lastCmd.BalanceCost, .001)
			require.Nil(t, result.ProbePenalty)
		}
	}
}

func TestAccountProbePenaltySubscriptionAfterDiscount(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	gid := int64(9)
	rate := .5
	result := &ForwardResult{RequestID: "subscription-probe", Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 13}, ProbePenaltyEligible: true}
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: &APIKey{ID: 501, GroupID: &gid, Group: &Group{ID: gid, RateMultiplier: .01, SubscriptionType: SubscriptionTypeSubscription}}, User: &User{ID: 601}, Subscription: &UserSubscription{ID: 17}, Account: &Account{ID: 701, Platform: PlatformAnthropic, GroupRateMultiplier: &rate, Extra: map[string]any{AccountProbePenaltyExtraKey: map[string]any{"enabled": true, "minimum_charge": .001}}}})
	require.NoError(t, err)
	require.Equal(t, .001, billingRepo.lastCmd.SubscriptionCost)
	require.Zero(t, billingRepo.lastCmd.BalanceCost)
	require.Equal(t, .001, usageRepo.lastLog.ActualCost)
}

type probeReceiptReplayRepo struct{ imageReceiptRepo }

func (*probeReceiptReplayRepo) StageUsageBilling(context.Context, *UsageBillingCommand, *UsageLog) error {
	return nil
}
func TestAccountProbePenaltyReceiptUsesCanonicalReplay(t *testing.T) {
	canonical := &ProbePenaltyReceipt{MinimumCharge: .002, ActualCost: .002, Adjustment: .0015, OriginalOutputTokens: 13, BillingOutputTokens: 90, OutputTokenPrice: .00002}
	repo := &probeReceiptReplayRepo{imageReceiptRepo{recovery: &UsageBillingRecovery{Settled: true, Event: UsageBillingEvent{Usage: UsageLog{OutputTokens: 13, InputTokens: 10, CustomerCostDetails: &CustomerCostDetails{Version: 4, ProbePenalty: canonical}}}}}}
	current := &ForwardResult{Usage: ClaudeUsage{OutputTokens: 1}, ProbePenalty: &ProbePenaltyReceipt{ActualCost: .001, BillingOutputTokens: 50}}
	_, err := applyDurableUsageBilling(WithProbePenaltyResult(context.Background(), current), repo, &UsageBillingCommand{RequestID: "replay", APIKeyID: 1}, &UsageLog{}, &postUsageBillingParams{}, &billingDeps{})
	require.NoError(t, err)
	require.Equal(t, canonical, current.ProbePenalty)
	require.Equal(t, 13, current.Usage.OutputTokens)
	repo.recovery.Settled = false
	_, err = applyDurableUsageBilling(WithProbePenaltyResult(context.Background(), current), repo, &UsageBillingCommand{RequestID: "unknown", APIKeyID: 1}, &UsageLog{}, &postUsageBillingParams{}, &billingDeps{})
	require.ErrorIs(t, err, ErrUsageBillingOutcomeUnknown)
}
