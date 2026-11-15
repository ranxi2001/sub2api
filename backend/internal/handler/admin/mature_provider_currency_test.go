package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatureProviderCurrencyDoesNotRelabelQuota(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":1000,"used_quota":500}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":100,"quota_display_type":"CNY","usd_exchange_rate":7}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		}
	}))
	defer upstream.Close()
	secret, err := encodeProviderToken("nap_synthetic", "", "Bearer")
	require.NoError(t, err)
	result, err := probeProviderAccount(context.Background(), providerRecord{SiteType: "newapi", AuthMode: "token", BaseURL: upstream.URL}, secret, upstream.Client())
	require.NoError(t, err)
	// quota_per_unit defines source quota units. A display-currency preference
	// cannot silently label those units USD; either convert or preserve units.
	require.NotEqual(t, "USD", result.metrics.Currency, "display currency must not relabel unconverted quota as USD")
	require.Equal(t, "CNY", result.metrics.Currency)
	require.InDelta(t, 70, *result.metrics.Balance, 1e-9)
	require.Nil(t, result.metrics.TotalRecharge)
}
