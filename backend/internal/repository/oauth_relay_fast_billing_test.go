//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthRelayFastBillingSurvivesSchedulerProjection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		projected := filterSchedulerExtra(map[string]any{service.OpenAIOAuthRelayFastBillingKey: enabled})
		account := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: projected}
		want := "default"
		if enabled {
			want = "priority"
		}
		require.Equal(t, want, service.ResolveOpenAIServiceTierBilling(account, "priority", "default").Billing)
	}
}
