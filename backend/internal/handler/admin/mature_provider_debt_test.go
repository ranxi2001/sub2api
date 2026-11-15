package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatureProviderNegativeBalanceRemainsObserved(t *testing.T) {
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/user/profile" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":-2.5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"total_actual_cost":3}}`))
	}))
	defer remote.Close()
	got, err := probeProviderAccount(context.Background(), providerRecord{SiteType: "sub2api", AuthMode: "token", BaseURL: remote.URL}, "synthetic-token", remote.Client())
	require.NoError(t, err)
	require.NotNil(t, got.metrics.Balance, "an observed negative balance must remain a known debt for low-balance alerts")
	require.Equal(t, -2.5, *got.metrics.Balance)
	require.Nil(t, got.metrics.TotalRecharge)
}
