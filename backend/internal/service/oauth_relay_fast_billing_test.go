//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthRelayFastBillingContract(t *testing.T) {
	for _, tc := range []struct {
		name, requested, observed, want string
		enabled                         any
		responseBilling, compatible     bool
	}{
		{"opt in", "priority", "default", "priority", true, false, true},
		{"fast alias", "fast", " DEFAULT ", "fast", true, false, true},
		{"missing flag", "priority", "default", "default", nil, false, false},
		{"disabled", "priority", "default", "default", false, false, false},
		{"wrong flag type", "priority", "default", "default", "true", false, false},
		{"response contract wins", "priority", "default", "default", true, true, false},
		{"explicit flex downgrade", "priority", "flex", "flex", true, false, false},
		{"explicit standard downgrade", "priority", "standard", "standard", true, false, false},
		{"no outbound tier", "", "default", "", true, false, false},
		{"filtered outbound tier", "default", "default", "default", true, false, false},
		{"flex outbound", "flex", "default", "flex", true, false, false},
		{"ultrafast is outside contract", "ultrafast", "default", "default", true, false, false},
		{"response cannot upgrade", "priority", "ultrafast", "priority", true, false, false},
		{"missing response", "priority", "", "priority", true, false, false},
		{"unknown response", "priority", "turbo", "priority", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
				OpenAIOAuthRelayFastBillingKey: tc.enabled, OpenAIResponseTierBillingEnabledKey: tc.responseBilling,
			}}
			result := &OpenAIForwardResult{UpstreamResponseServiceTier: tc.observed}
			if tc.requested != "" {
				result.ServiceTier = &tc.requested
			}
			got := ApplyOpenAIServiceTierBillingResolution(account, result)
			require.Equal(t, tc.want, got.Billing)
			require.Equal(t, tc.want, optionalStringValue(result.ServiceTier))
			require.Equal(t, tc.compatible, got.OAuthRelayFast)
			require.Equal(t, tc.observed, result.UpstreamResponseServiceTier, "retain the upstream observation")
		})
	}
	for _, account := range []*Account{nil, {Platform: PlatformGrok, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIOAuthRelayFastBillingKey: true}}} {
		require.Equal(t, "default", ResolveOpenAIServiceTierBilling(account, "priority", "default").Billing)
	}
}

func TestOAuthRelayFastBillingAudit(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	account := &Account{ID: 1001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{OpenAIOAuthRelayFastBillingKey: true}}
	logServiceTierBillingDowngrade("test", account, "fixture-request", ResolveOpenAIServiceTierBilling(account, "priority", "default"))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "billing.service_tier_resolved", entry["msg"])
	require.Equal(t, "trusted_oauth_relay_request", entry["tier_source"])
	require.Equal(t, "priority", entry["requested_tier"])
	require.Equal(t, "default", entry["response_tier"])
	require.Equal(t, "priority", entry["billed_tier"])
}

// Exercise the same terminal-response observation, pricing and durable charge
// staging path used by HTTP/SSE and WS results.
func TestOAuthRelayFastRecordUsage(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		enabled, freeFast        bool
		wantTier                 string
		costFactor, chargeFactor float64
	}{
		{"ordinary API retains downgrade", false, false, "default", 1, 1},
		{"trusted relay charges fast", true, false, "priority", 2, 2},
		{"free fast still applies", true, true, "priority", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := ultrafastUsageGroup()
			group.RateMultiplier, group.FreeOpenAIFast = .128, tc.freeFast
			group.ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-6.1-sol"}, BillingMode: BillingModeToken,
				FastMultiplier: ultrafastUsageFloat(2)}}
			svc, input, repo, _ := ultrafastUsageHarness(t, group, false)
			input.Account.Extra[OpenAIOAuthRelayFastBillingKey] = tc.enabled
			input.Result = ultrafastUsageResult(t, "gpt-6.1-sol", "priority", "default", 10000, 8000, 1000, 500)
			require.NoError(t, svc.RecordUsage(context.Background(), input))
			event := ultrafastUsageEvent(t, repo)
			standard := 1000*2e-6 + 500*10e-6 + 8000*.1e-6 + 1000*2.5e-6
			wantCharge := QuantizeUsageBillingAmount(standard * tc.chargeFactor * .128)
			require.Equal(t, tc.wantTier, optionalStringValue(event.Usage.ServiceTier))
			require.InDelta(t, standard*tc.costFactor, event.Usage.TotalCost, 1e-10)
			require.InDelta(t, wantCharge, event.Usage.ActualCost, 1e-10)
			require.InDelta(t, wantCharge, event.Command.BalanceCost, 1e-10)
			require.InDelta(t, wantCharge, event.Command.APIKeyQuotaCost, 1e-10)
		})
	}
}

// The compatibility flag must not broaden the existing OAuth-only shadow
// credential contract or stage a charge for an invalid API-key parent.
func TestOAuthRelayFastRejectsAPIKeyShadowParent(t *testing.T) {
	svc, input, repo, _ := ultrafastUsageHarness(t, ultrafastUsageGroup(), false)
	parentID := int64(2001)
	input.Account.ParentAccountID = &parentID
	svc.accountRepo = &openAIRecordUsageAccountRepoStub{account: &Account{
		ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{OpenAIOAuthRelayFastBillingKey: true},
	}}
	input.Result = ultrafastUsageResult(t, "gpt-6.1-sol", "priority", "default", 10000, 8000, 1000, 500)
	require.ErrorContains(t, svc.RecordUsage(context.Background(), input), "is not OpenAI OAuth")
	require.Empty(t, repo.events)
}
