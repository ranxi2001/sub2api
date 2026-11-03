//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUltrafastResponseBillingSurvivesSchedulerProjection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		original := map[string]any{service.OpenAIResponseTierBillingEnabledKey: enabled, "irrelevant_large_field": "drop"}
		projected := filterSchedulerExtra(original)
		require.Equal(t, enabled, projected[service.OpenAIResponseTierBillingEnabledKey])
		require.NotContains(t, projected, "irrelevant_large_field")
		account := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: projected}
		resolution := service.ResolveOpenAIServiceTierBilling(account, "", "ultrafast")
		if enabled {
			require.Equal(t, "ultrafast", resolution.Billing)
		} else {
			require.Empty(t, resolution.Billing)
		}
		require.Equal(t, enabled, original[service.OpenAIResponseTierBillingEnabledKey])
	}
}
