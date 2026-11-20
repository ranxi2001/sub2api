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
		err := svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: &APIKey{ID: 501, Quota: 100}, User: &User{ID: 601}, Account: account})
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
