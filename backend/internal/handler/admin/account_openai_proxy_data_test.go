package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDataOpenAIProxyPortableRoundTrip(t *testing.T) {
	for _, mode := range []string{service.OpenAIProxyModeRequest, service.OpenAIProxyModeSingle} {
		t.Run(mode, func(t *testing.T) {
			router, svc := setupAccountDataRouter()
			svc.proxies = []service.Proxy{{ID: 11, Name: "first", Protocol: "http", Host: "192.0.2.11", Port: 8080, Status: service.StatusActive}, {ID: 22, Name: "second", Protocol: "http", Host: "192.0.2.22", Port: 8080, Status: service.StatusActive}}
			svc.accounts = []service.Account{{ID: 1, Name: "ordinary", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{service.OpenAIProxyModeKey: mode, service.OpenAIProxyIDsKey: []int64{11, 22}, service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: []int64{99}}}}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var exported struct {
				Data DataPayload `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
			require.NotContains(t, exported.Data.Accounts[0].Extra, service.OpenAIProxyIDsKey)
			require.NotContains(t, exported.Data.Accounts[0].Extra, service.ExcelBPSProxyIDsKey)
			require.Contains(t, rec.Body.String(), "openai_proxy_keys")
			svc.proxies[0].ID, svc.proxies[1].ID = 101, 202
			body, err := json.Marshal(DataImportRequest{Data: exported.Data})
			require.NoError(t, err)
			rec = httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, svc.createdAccounts, 1)
			restored := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: svc.createdAccounts[0].Extra}
			require.Equal(t, []int64{101, 202}, restored.OpenAIRequestProxyIDs())
			require.Equal(t, mode == service.OpenAIProxyModeRequest, restored.OpenAIRequestProxyEnabled())
		})
	}
}

func TestAccountDataOpenAIProxyRejectsNumericIDsAndMissingKeys(t *testing.T) {
	for _, extra := range []map[string]any{
		{service.OpenAIProxyModeKey: service.OpenAIProxyModeRequest, service.OpenAIProxyIDsKey: []int64{11}},
		{service.OpenAIProxyModeKey: service.OpenAIProxyModeRequest},
		{service.OpenAIProxyModeKey: service.OpenAIProxyModeSingle, service.OpenAIProxyIDsKey: []int64{11}},
	} {
		router, svc := setupAccountDataRouter()
		body, err := json.Marshal(DataImportRequest{Data: DataPayload{Proxies: []DataProxy{}, Accounts: []DataAccount{{Name: "ordinary", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: extra}}}})
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Empty(t, svc.createdAccounts)
		require.Contains(t, rec.Body.String(), "openai_proxy_keys")
	}
}

func TestAccountDataOpenAIProxyExportWithoutProxiesCannotImportAsDirect(t *testing.T) {
	router, svc := setupAccountDataRouter()
	svc.accounts = []service.Account{{ID: 1, Name: "ordinary", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{service.OpenAIProxyModeKey: service.OpenAIProxyModeRequest, service.OpenAIProxyIDsKey: []int64{11}}}}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data?include_proxies=false", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var exported struct {
		Data DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
	require.NotContains(t, exported.Data.Accounts[0].Extra, service.OpenAIProxyIDsKey)
	require.Empty(t, exported.Data.Accounts[0].OpenAIProxyKeys)
	body, err := json.Marshal(DataImportRequest{Data: exported.Data})
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, svc.createdAccounts)
	require.Contains(t, rec.Body.String(), "openai_proxy_keys")
}

func TestAccountDataOpenAIProxyObserverCannotImportProxyKeys(t *testing.T) {
	router, svc := setupAccountDataRouter()
	body := []byte(`{"group_ids":[1],"data":{"proxies":[],"accounts":[{"name":"ordinary","platform":"openai","type":"oauth","credentials":{"access_token":"fixture"},"openai_proxy_keys":["fixture"],"extra":{"openai_proxy_mode":"request"}}]}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(service.WithObserverScope(req.Context(), []int64{1}))
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, svc.createdAccounts)
}
