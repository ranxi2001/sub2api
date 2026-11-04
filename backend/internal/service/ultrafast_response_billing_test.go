//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUltrafastResponseBillingAudit(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	account := &Account{ID: 1001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{OpenAIResponseTierBillingEnabledKey: true}}
	for _, observed := range []string{"ultrafast", ""} {
		output.Reset()
		resolution := ResolveOpenAIServiceTierBilling(account, "priority", observed)
		logServiceTierBillingDowngrade("test", account, "fixture-request", resolution)
		var entry map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
		require.Equal(t, "priority", entry["requested_tier"])
		require.Equal(t, observed, entry["response_tier"])
		require.Equal(t, resolution.Billing, entry["billed_tier"])
		if observed == "" {
			require.Equal(t, "WARN", entry["level"])
			require.Equal(t, "outbound_request", entry["tier_source_fallback"])
		} else {
			require.Equal(t, true, entry["upgraded"])
		}
	}
}

func TestUltrafastResponseObservation(t *testing.T) {
	for _, tc := range []struct{ name, body, event, want string }{
		{"stream", `{"response":{"model":"gpt-6.1-sol","service_tier":"ultrafast"}}`, "response.completed", "ultrafast"},
		{"nonstream", `{"model":"gpt-6-astra","service_tier":" UltraFast "}`, "", "ultrafast"},
		{"terminal without model", `{"response":{"service_tier":"ultrafast"}}`, "response.completed", "ultrafast"},
		{"created is only an echo", `{"response":{"model":"gpt-6.1-sol","service_tier":"ultrafast"}}`, "response.created", ""},
		{"unknown ignored", `{"model":"gpt-6.1-sol","service_tier":"turbo"}`, "", ""},
		{"malformed ignored", `{"model":"gpt-6.1-sol","service_tier":"ultrafast"`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &upstreamResponseModelObserver{}
			o.ObserveOpenAI([]byte(tc.body), tc.event)
			require.Equal(t, tc.want, o.ServiceTier())
		})
	}
	o := &upstreamResponseModelObserver{}
	o.ObserveOpenAI([]byte(`{"model":"gpt-6.1-sol","service_tier":"ultrafast"}`), "")
	o.ObserveOpenAI([]byte(`{"response":{"model":"gpt-6.1-sol","service_tier":"default"}}`), "response.completed")
	require.Equal(t, "default", o.ServiceTier(), "actual terminal downgrade wins")
}

func TestUltrafastResponseBillingOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind, requested, observed, want string
		enabled                                         any
	}{
		{"enabled without request", PlatformOpenAI, AccountTypeAPIKey, "", "ultrafast", "ultrafast", true},
		{"enabled upgrades priority", PlatformOpenAI, AccountTypeAPIKey, "priority", "ultrafast", "ultrafast", true},
		{"enabled accepts downgrade", PlatformOpenAI, AccountTypeAPIKey, "ultrafast", "default", "default", true},
		{"enabled accepts priority", PlatformOpenAI, AccountTypeAPIKey, "", "priority", "priority", true},
		{"enabled accepts flex", PlatformOpenAI, AccountTypeAPIKey, "priority", "flex", "flex", true},
		{"missing keeps request", PlatformOpenAI, AccountTypeAPIKey, "priority", "", "priority", true},
		{"unknown keeps request", PlatformOpenAI, AccountTypeAPIKey, "priority", "turbo", "priority", true},
		{"no tier does not guess", PlatformOpenAI, AccountTypeAPIKey, "", "", "", true},
		{"disabled keeps request", PlatformOpenAI, AccountTypeAPIKey, "priority", "ultrafast", "priority", false},
		{"unset keeps request", PlatformOpenAI, AccountTypeAPIKey, "", "ultrafast", "", nil},
		{"string true is not enabled", PlatformOpenAI, AccountTypeAPIKey, "", "ultrafast", "", "true"},
		{"oauth cannot opt in", PlatformOpenAI, AccountTypeOAuth, "", "ultrafast", "", true},
		{"other platform cannot opt in", PlatformGrok, AccountTypeAPIKey, "", "ultrafast", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{Platform: tc.platform, Type: tc.kind, Extra: map[string]any{"openai_response_tier_billing_enabled": tc.enabled}}
			r := &OpenAIForwardResult{UpstreamResponseServiceTier: tc.observed}
			if tc.requested != "" {
				v := tc.requested
				r.ServiceTier = &v
			}
			got := ApplyOpenAIServiceTierBillingResolution(a, r)
			require.Equal(t, tc.want, got.Billing)
			require.Equal(t, tc.requested, got.Requested, "retain outbound tier for audit")
			require.Equal(t, tc.want, optionalStringValue(r.ServiceTier))
		})
	}
}

func TestUltrafastResponseToCost(t *testing.T) {
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(data)
	require.NoError(t, err)
	for source, svc := range map[string]*BillingService{
		"fallback": NewBillingService(&config.Config{}, nil),
		"catalog":  NewBillingService(&config.Config{}, catalog),
	} {
		for _, model := range []struct {
			name                        string
			input, output, cache, write float64
			ultrafastMultiplier         float64
			fallbackLongContext         bool
			catalogLongContext          bool
		}{
			{"gpt-5.6-sol", 5e-6, 30e-6, .5e-6, 6.25e-6, 6, false, true},
			{"gpt-5.6-terra", 2e-6, 12e-6, .2e-6, 2.5e-6, 6, false, true},
			{"gpt-6-astra", 10e-6, 50e-6, 1e-6, 12.5e-6, 6, true, true},
			{"gpt-6-sol", 2e-6, 10e-6, .2e-6, 2.5e-6, 6, true, true},
			{"gpt-6.1-sol", 2e-6, 10e-6, .1e-6, 2.5e-6, 6, true, true},
			{"gpt-6.1-terra", 1e-6, 5e-6, .05e-6, 1.25e-6, 6, true, true},
		} {
			longContext := model.fallbackLongContext
			if source == "catalog" {
				longContext = model.catalogLongContext
			}
			for _, total := range []int{272000, 272001} {
				for _, tier := range []struct {
					name   string
					factor float64
				}{{"default", 1}, {"priority", 2}, {"ultrafast", model.ultrafastMultiplier}} {
					t.Run(fmt.Sprintf("%s/%s/%d/%s", source, model.name, total, tier.name), func(t *testing.T) {
						o := &upstreamResponseModelObserver{}
						o.ObserveOpenAI([]byte(fmt.Sprintf(`{"response":{"model":%q,"service_tier":%q}}`, model.name, tier.name)), "response.completed")
						r := &OpenAIForwardResult{UpstreamResponseServiceTier: o.ServiceTier()}
						ApplyOpenAIServiceTierBillingResolution(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_response_tier_billing_enabled": true}}, r)
						tokens := UsageTokens{InputTokens: total - 3000, CacheReadTokens: 2000, CacheCreationTokens: 1000, OutputTokens: 500}
						cost, err := svc.CalculateCostWithServiceTier(model.name, tokens, .25, optionalStringValue(r.ServiceTier))
						require.NoError(t, err)
						im, om := 1.0, 1.0
						if total > 272000 && longContext {
							im, om = 2, 1.5
						}
						wantInput := float64(tokens.InputTokens) * model.input * im * tier.factor
						wantOutput := 500 * model.output * om * tier.factor
						wantCache := 2000 * model.cache * im * tier.factor
						wantWrite := 1000 * model.write * im * tier.factor
						require.InDelta(t, wantInput, cost.InputCost, 1e-10)
						require.InDelta(t, wantOutput, cost.OutputCost, 1e-10)
						require.InDelta(t, wantCache, cost.CacheReadCost, 1e-10)
						require.InDelta(t, wantWrite, cost.CacheCreationCost, 1e-10)
						require.InDelta(t, (wantInput+wantOutput+wantCache+wantWrite)*.25, cost.ActualCost, 1e-10)
					})
				}
			}
		}
	}
}
