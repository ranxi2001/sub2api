package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDataBPSRequestProxyPortableRoundTrip(t *testing.T) {
	router, svc := setupAccountDataRouter()
	svc.proxies = []service.Proxy{
		{ID: 11, Name: "exit-one", Protocol: "http", Host: "192.0.2.11", Port: 8080, Status: service.StatusActive},
		{ID: 22, Name: "exit-two", Protocol: "http", Host: "192.0.2.22", Port: 8080, Status: service.StatusActive},
	}
	svc.accounts = []service.Account{{ID: 1, Name: "bps", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{"openai_excel_bps": true, "openai_excel_bps_proxy_mode": "request", "openai_excel_bps_proxy_ids": []int64{11, 22}}}}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	proxies, ok := envelope.Data["proxies"].([]any)
	require.True(t, ok)
	require.Len(t, proxies, 2)
	accounts, ok := envelope.Data["accounts"].([]any)
	require.True(t, ok)
	require.Len(t, accounts, 1)
	account, ok := accounts[0].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, account["extra"], "openai_excel_bps_proxy_ids")
	require.Equal(t, []any{buildBPSProxyKey("http", "192.0.2.11", 8080, "", ""), buildBPSProxyKey("http", "192.0.2.22", 8080, "", "")}, account["bps_proxy_keys"])
	// The same exits have different IDs in the destination database.
	svc.proxies[0].ID = 101
	svc.proxies[1].ID = 202
	body, err := json.Marshal(map[string]any{"data": envelope.Data, "skip_default_group_bind": true})
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, svc.createdAccounts, 1)
	restored := &service.Account{Extra: svc.createdAccounts[0].Extra}
	require.Equal(t, []int64{101, 202}, restored.ExcelBPSRequestProxyIDs())
}

func TestAccountDataBPSRequestProxyRoundTripPreservesExactCredentialIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, firstUsername, firstPassword, secondUsername, secondPassword string
	}{
		{name: "delimiter", firstUsername: "a|b", firstPassword: "c", secondUsername: "a", secondPassword: "b|c"},
		{name: "username whitespace", firstUsername: " user ", firstPassword: "pass", secondUsername: "user", secondPassword: "pass"},
		{name: "password whitespace", firstUsername: "user", firstPassword: " pass ", secondUsername: "user", secondPassword: "pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, svc := setupAccountDataRouter()
			svc.proxies = []service.Proxy{
				{ID: 11, Name: "first", Protocol: "http", Host: "192.0.2.11", Port: 8080, Username: tc.firstUsername, Password: tc.firstPassword, Status: service.StatusActive},
				{ID: 22, Name: "second", Protocol: "http", Host: "192.0.2.11", Port: 8080, Username: tc.secondUsername, Password: tc.secondPassword, Status: service.StatusActive},
			}
			for i, id := range []int64{11, 22} {
				svc.accounts = append(svc.accounts, service.Account{ID: int64(i + 1), Name: svc.proxies[i].Name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{"openai_excel_bps": true, service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: []int64{id}}})
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var exported struct {
				Data DataPayload `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
			svc.proxies[0].ID = 101
			svc.proxies[1].ID = 202
			body, err := json.Marshal(DataImportRequest{Data: exported.Data})
			require.NoError(t, err)
			rec = httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, svc.createdAccounts, 2)
			first := &service.Account{Extra: svc.createdAccounts[0].Extra}
			second := &service.Account{Extra: svc.createdAccounts[1].Extra}
			require.Equal(t, []int64{101}, first.ExcelBPSRequestProxyIDs(), "first credential identity must survive actual export/import")
			require.Equal(t, []int64{202}, second.ExcelBPSRequestProxyIDs())
			require.NotEqual(t, exported.Data.Accounts[0].BPSProxyKeys, exported.Data.Accounts[1].BPSProxyKeys)
		})
	}
}

func TestAccountDataBPSRequestProxyRejectsSourceNumericIDs(t *testing.T) {
	router, svc := setupAccountDataRouter()
	svc.proxies = []service.Proxy{{ID: 11, Protocol: "http", Host: "192.0.2.99", Port: 8080}}
	body := []byte(`{"data":{"proxies":[],"accounts":[{"name":"bps","platform":"openai","type":"oauth","credentials":{"access_token":"fixture"},"extra":{"openai_excel_bps_proxy_mode":"request","openai_excel_bps_proxy_ids":[11]}}]},"skip_default_group_bind":true}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, svc.createdAccounts)
	var envelope struct {
		Data DataImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, 1, envelope.Data.AccountFailed)
	require.Contains(t, envelope.Data.Errors[0].Message, "bps_proxy_keys")
}

func TestAccountDataBPSRequestProxyRejectsLegacyCollisionAndPayloadAlias(t *testing.T) {
	wanted := service.Proxy{ID: 11, Name: "wanted", Protocol: "http", Host: "192.0.2.11", Port: 8080, Username: "a|b", Password: "c", Status: service.StatusActive}
	other := wanted
	other.ID, other.Name, other.Username, other.Password = 202, "other", "a", "b|c"
	identity := buildBPSProxyKey(wanted.Protocol, wanted.Host, wanted.Port, wanted.Username, wanted.Password)
	legacy := buildProxyKey(wanted.Protocol, wanted.Host, wanted.Port, wanted.Username, wanted.Password)
	for _, tc := range []struct {
		name     string
		key      string
		proxyKey string
	}{
		{name: "colliding destination only", key: identity, proxyKey: legacy},
		{name: "payload alias matching BPS identity", key: identity, proxyKey: identity},
		{name: "legacy ambiguous BPS identity", key: legacy, proxyKey: legacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, svc := setupAccountDataRouter()
			svc.proxies = []service.Proxy{other}
			payloadProxy := wanted
			if tc.proxyKey == identity {
				payloadProxy = other
			}
			data := DataPayload{
				Proxies:  []DataProxy{{ProxyKey: tc.proxyKey, Name: payloadProxy.Name, Protocol: payloadProxy.Protocol, Host: payloadProxy.Host, Port: payloadProxy.Port, Username: payloadProxy.Username, Password: payloadProxy.Password, Status: payloadProxy.Status}},
				Accounts: []DataAccount{{Name: "bps", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest}, BPSProxyKeys: []string{tc.key}}},
			}
			body, err := json.Marshal(DataImportRequest{Data: data})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Empty(t, svc.createdAccounts, "BPS import must not trust a legacy collision or payload alias")
			var imported struct {
				Data DataImportResult `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &imported))
			require.Equal(t, 1, imported.Data.AccountFailed)
			require.Contains(t, imported.Data.Errors[len(imported.Data.Errors)-1].Message, "not found")
		})
	}
}

// Return the actual created endpoint, rather than the generic stub's ID/name-only
// response, so this seam also exercises newly persisted destination identities.
type bpsProxyDataCreateService struct {
	*stubAdminService
}

func (s *bpsProxyDataCreateService) CreateProxy(_ context.Context, input *service.CreateProxyInput) (*service.Proxy, error) {
	id := int64(400 + len(s.createdProxies))
	s.createdProxies = append(s.createdProxies, input)
	p := service.Proxy{ID: id, Name: input.Name, Protocol: input.Protocol, Host: input.Host, Port: input.Port, Username: input.Username, Password: input.Password, Status: service.StatusActive}
	s.proxies = append(s.proxies, p)
	return &p, nil
}

func TestAccountDataBPSRequestProxyBatchCollisionRejectsUncreatedIdentity(t *testing.T) {
	_, stub := setupAccountDataRouter()
	svc := &bpsProxyDataCreateService{stubAdminService: stub}
	h := &AccountHandler{adminService: svc}
	first := DataProxy{Name: "first", Protocol: "http", Host: "192.0.2.11", Port: 8080, Username: "a|b", Password: "c", Status: service.StatusActive}
	second := first
	second.Name, second.Username, second.Password = "second", "a", "b|c"
	data := DataPayload{Proxies: []DataProxy{first, second}}
	for _, p := range data.Proxies {
		data.Accounts = append(data.Accounts, DataAccount{Name: p.Name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest}, BPSProxyKeys: []string{buildBPSProxyKey(p.Protocol, p.Host, p.Port, p.Username, p.Password)}})
	}
	result, err := h.importData(context.Background(), DataImportRequest{Data: data})
	require.NoError(t, err)
	require.Equal(t, 1, result.ProxyCreated)
	require.Equal(t, 1, result.ProxyReused, "ordinary legacy proxy import semantics stay unchanged")
	require.Equal(t, 1, result.AccountCreated)
	require.Equal(t, 1, result.AccountFailed)
	require.Len(t, svc.createdAccounts, 1)
	restored := &service.Account{Extra: svc.createdAccounts[0].Extra}
	require.Equal(t, []int64{400}, restored.ExcelBPSRequestProxyIDs())
	require.Equal(t, "second", result.Errors[0].Name)
	require.Contains(t, result.Errors[0].Message, "not found")
}

func TestAccountDataBPSRequestProxyExportWithoutProxiesCannotImportAsDirect(t *testing.T) {
	router, svc := setupAccountDataRouter()
	svc.accounts = []service.Account{{ID: 1, Name: "bps", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{"openai_excel_bps": true, service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: []int64{11}}}}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data?include_proxies=false", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var exported struct {
		Data DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
	require.Empty(t, exported.Data.Proxies)
	require.Empty(t, exported.Data.Accounts[0].BPSProxyKeys)
	require.NotContains(t, exported.Data.Accounts[0].Extra, service.ExcelBPSProxyIDsKey)
	require.Equal(t, service.ExcelBPSProxyModeRequest, exported.Data.Accounts[0].Extra[service.ExcelBPSProxyModeKey])
	body, err := json.Marshal(DataImportRequest{Data: exported.Data})
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, svc.createdAccounts)
	var imported struct {
		Data DataImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &imported))
	require.Equal(t, 1, imported.Data.AccountFailed)
	require.Contains(t, imported.Data.Errors[0].Message, "bps_proxy_keys")
}

func TestAccountDataBPSRequestProxyRejectsMissingKey(t *testing.T) {
	router, svc := setupAccountDataRouter()
	svc.proxies = []service.Proxy{{ID: 11, Protocol: "http", Host: "192.0.2.11", Port: 8080}}
	body := []byte(`{"data":{"proxies":[],"accounts":[{"name":"bps","platform":"openai","type":"oauth","credentials":{"access_token":"fixture"},"bps_proxy_keys":["http|192.0.2.11|8080||","http|192.0.2.22|8080||"],"extra":{"openai_excel_bps_proxy_mode":"request"}}]}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, svc.createdAccounts)
	var envelope struct {
		Data DataImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, 1, envelope.Data.AccountFailed)
	require.Contains(t, envelope.Data.Errors[0].Message, "not found")
}

func TestAccountDataBPSRequestProxyObserverCannotImportProxyKeys(t *testing.T) {
	router, svc := setupAccountDataRouter()
	body := []byte(`{"group_ids":[1],"data":{"proxies":[],"accounts":[{"name":"bps","platform":"openai","type":"oauth","credentials":{"access_token":"fixture"},"bps_proxy_keys":["http|192.0.2.11|8080||"],"extra":{"openai_excel_bps_proxy_mode":"request"}}]}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(service.WithObserverScope(req.Context(), []int64{1}))
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, svc.createdAccounts)
}

func TestAccountDataBPSRequestProxyRejectsNonPortableMihomoExit(t *testing.T) {
	t.Run("export source node", func(t *testing.T) {
		router, svc := setupAccountDataRouter()
		svc.proxies = []service.Proxy{{ID: 11, Name: "node-source", Protocol: "http", Host: "127.0.0.1", Port: 32000, MihomoNodeID: "source-node"}}
		svc.accounts = []service.Account{{ID: 1, Name: "bps", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: []int64{11}}}}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "Mihomo")
	})
	t.Run("same port different destination node", func(t *testing.T) {
		router, svc := setupAccountDataRouter()
		svc.proxies = []service.Proxy{{ID: 101, Name: "node-destination", Protocol: "http", Host: "127.0.0.1", Port: 32000, MihomoNodeID: "different-destination-node"}}
		body, err := json.Marshal(DataImportRequest{Data: DataPayload{Proxies: []DataProxy{}, Accounts: []DataAccount{{Name: "bps", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, BPSProxyKeys: []string{buildBPSProxyKey("http", "127.0.0.1", 32000, "", "")}, Extra: map[string]any{service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest}}}}})
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Empty(t, svc.createdAccounts)
		var envelope struct {
			Data DataImportResult `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		require.Equal(t, 1, envelope.Data.AccountFailed)
		require.Contains(t, envelope.Data.Errors[0].Message, "Mihomo")
	})
}
