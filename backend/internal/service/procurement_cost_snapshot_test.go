//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProcurementSnapshotMixedTargetsThroughUsageWriters(t *testing.T) {
	for _, writer := range []string{"gateway", "openai"} {
		t.Run(writer, func(t *testing.T) {
			a := &Account{ID: 3, Platform: PlatformGateway, Type: AccountTypeGateway,
				Credentials: map[string]any{GatewayUpstreamKindCredentialKey: "new_api"}, Extra: map[string]any{UpstreamBillingProviderExtraKey: "new_api", AccountCostMultiplierExtraKey: 99.0}}
			a.Extra[UpstreamBillingProbeExtraKey] = newAPISnapshot(a, NewAPIToken{Group: "fixed"}, &newAPIData{wallet: 12, ratios: map[string]float64{"fixed": .1}}, 30, time.Now())
			var recorded []*UsageLog
			for _, target := range []string{PlatformOpenAI, PlatformKimi} {
				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				users, subs := &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}
				key := &APIKey{ID: 1, Group: &Group{Platform: PlatformComposite, RateMultiplier: .3}}
				billing := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
				if writer == "openai" {
					svc := newOpenAIRecordUsageServiceForTest(logs, users, subs, nil)
					svc.usageBillingRepo = billing
					require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: &OpenAIForwardResult{RequestID: "procurement-" + target, Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 100}},
						APIKey: key, User: &User{ID: 2}, Account: a, QuotaPlatform: target}))
				} else {
					svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, billing, users, subs)
					require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
						Result: &ForwardResult{RequestID: "procurement-" + target, Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 100}},
						APIKey: key, User: &User{ID: 2}, Account: a, QuotaPlatform: target}))
				}
				require.NotNil(t, logs.lastLog)
				require.NotNil(t, logs.lastLog.ProcurementCostSnapshot)
				require.NotNil(t, logs.lastLog.ProcurementCostSnapshot.CostMultiplier)
				want := .7
				if target == PlatformKimi {
					want = .1
				}
				require.Equal(t, want, *logs.lastLog.ProcurementCostSnapshot.CostMultiplier)
				require.Equal(t, target, logs.lastLog.ProcurementCostSnapshot.TargetPlatform)
				recorded = append(recorded, logs.lastLog)
			}
			// Already-built asynchronous usage/events cannot be repriced by edits.
			a.Extra[NewAPIApplyUSDRateExtraKey] = false
			a.Extra[AccountCostMultiplierExtraKey] = 1.0
			a.Extra[UpstreamBillingProbeExtraKey] = nil
			for i, want := range []float64{.7, .1} {
				raw, err := json.Marshal(&UsageBillingEvent{Version: 1, Usage: *recorded[i]})
				require.NoError(t, err)
				var event UsageBillingEvent
				require.NoError(t, json.Unmarshal(raw, &event))
				require.Equal(t, want, *event.Usage.ProcurementCostSnapshot.CostMultiplier)
				require.Equal(t, recorded[i].TotalCost, event.Usage.TotalCost)
				require.Equal(t, recorded[i].ActualCost, event.Usage.ActualCost)
			}
		})
	}
}

func TestProcurementSnapshotUnknownAndLegacySemantics(t *testing.T) {
	a := &Account{Platform: PlatformGateway, Type: AccountTypeGateway, Credentials: map[string]any{GatewayUpstreamKindCredentialKey: "new_api"}}
	log := &UsageLog{Platform: PlatformOpenAI, TotalCost: 10, ActualCost: 3}
	captureGatewayProcurementCost(log, a)
	require.NotNil(t, log.ProcurementCostSnapshot, "record unknown request-time evidence, never use the gateway's current account cost")
	require.Nil(t, log.ProcurementCostSnapshot.CostMultiplier)
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeUpstream, AccountTypeOAuth} {
		legacy := &UsageLog{Platform: PlatformOpenAI}
		captureGatewayProcurementCost(legacy, &Account{Type: kind})
		require.Nil(t, legacy.ProcurementCostSnapshot, "legacy single-platform reporting must retain its existing fallback")
	}
	require.Equal(t, 10.0, log.TotalCost)
	require.Equal(t, 3.0, log.ActualCost)
}

func TestProcurementSnapshotZeroDisabledConversionAndUnresolvedTarget(t *testing.T) {
	a := &Account{Platform: PlatformGateway, Type: AccountTypeGateway,
		Credentials: map[string]any{GatewayUpstreamKindCredentialKey: "new_api"},
		Extra:       map[string]any{UpstreamBillingProviderExtraKey: "new_api", NewAPIApplyUSDRateExtraKey: false}}
	for _, rate := range []float64{0, .1} {
		a.Extra[UpstreamBillingProbeExtraKey] = newAPISnapshot(a, NewAPIToken{Group: "fixed"}, &newAPIData{wallet: 12, ratios: map[string]float64{"fixed": rate}}, 30, time.Now())
		log := &UsageLog{Platform: PlatformOpenAI}
		captureGatewayProcurementCost(log, a)
		require.NotNil(t, log.ProcurementCostSnapshot.CostMultiplier)
		require.Equal(t, rate, *log.ProcurementCostSnapshot.CostMultiplier)
		log.Platform = PlatformComposite
		captureGatewayProcurementCost(log, a)
		require.Nil(t, log.ProcurementCostSnapshot.CostMultiplier, "composite cannot decide the currency scale")
	}
}

func TestProcurementSnapshotSub2APIDeclaredRate(t *testing.T) {
	account := &Account{Type: AccountTypeGateway, Credentials: map[string]any{GatewayUpstreamKindCredentialKey: "sub2api"},
		Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{
			Status: UpstreamBillingProbeStatusOK, LastAttemptAt: time.Now(),
			Data: map[string]any{"billing_scope": "token", "resolved_rate_multiplier": .14, "peak_rate_enabled": false},
		}}}
	for _, target := range []string{PlatformKimi, PlatformOpenAI} {
		log := &UsageLog{Platform: target}
		captureGatewayProcurementCost(log, account)
		require.NotNil(t, log.ProcurementCostSnapshot.CostMultiplier)
		require.Equal(t, .14, *log.ProcurementCostSnapshot.CostMultiplier)
	}
}

func TestProcurementSnapshotSub2APIManualRate(t *testing.T) {
	account := &Account{Type: AccountTypeGateway, Credentials: map[string]any{GatewayUpstreamKindCredentialKey: "sub2api"},
		Extra: map[string]any{AccountCostAutoSyncExtraKey: false}}
	log := &UsageLog{Platform: PlatformOpenAI}
	captureGatewayProcurementCost(log, account)
	require.Nil(t, log.ProcurementCostSnapshot.CostMultiplier)
	for _, factor := range []float64{0, .25} {
		account.Extra[AccountCostMultiplierExtraKey] = factor
		captureGatewayProcurementCost(log, account)
		require.NotNil(t, log.ProcurementCostSnapshot.CostMultiplier)
		require.Equal(t, factor, *log.ProcurementCostSnapshot.CostMultiplier)
	}
}
