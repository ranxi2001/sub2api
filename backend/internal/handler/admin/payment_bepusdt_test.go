//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/bepusdt"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBEpusdtCollectionSecretsAreMaskedAndBlankEditsPreserveKeys(t *testing.T) {
	cleanup, err := bepusdt.InitializeForTest(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, bepusdt.SaveSettings(context.Background(), map[string]string{"rpc_endpoint_tron_grid_api_key": "tron-private-key", "rate_sync_coingecko_api_key": "price-private-key"}))
	token, err := bepusdt.Token()
	require.NoError(t, err)
	router := gin.New()
	handler := &PaymentHandler{}
	router.GET("/collection", handler.GetBEpusdt)
	router.PUT("/collection", handler.SaveBEpusdtSettings)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/collection", nil))
	require.Equal(t, http.StatusOK, res.Code)
	require.NotContains(t, res.Body.String(), "tron-private-key")
	require.NotContains(t, res.Body.String(), "price-private-key")
	require.NotContains(t, res.Body.String(), token)
	require.Contains(t, res.Body.String(), `"secret_configured":{"rate_sync_coingecko_api_key":true,"rpc_endpoint_tron_grid_api_key":true}`)
	res = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/collection", strings.NewReader(`{"settings":{"rpc_endpoint_tron_grid_api_key":"","rate_sync_coingecko_api_key":"  ","rate_sync_interval":"120"}}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(res, req)
	require.Equal(t, http.StatusOK, res.Code)
	settings, err := bepusdt.Settings()
	require.NoError(t, err)
	require.Equal(t, "tron-private-key", settings["rpc_endpoint_tron_grid_api_key"])
	require.Equal(t, "price-private-key", settings["rate_sync_coingecko_api_key"])
	require.Equal(t, "120", settings["rate_sync_interval"])
}
