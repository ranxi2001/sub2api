package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupUpstreamBillingProbeRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetUpstreamBillingProbeService(service.NewUpstreamBillingProbeService(nil, nil, nil))

	router := gin.New()
	router.GET("/admin/accounts/upstream-billing-probe/settings", handler.GetUpstreamBillingProbeSettings)
	router.POST("/admin/accounts/upstream-billing-probe/batch", handler.ProbeUpstreamBillingBatch)
	router.POST("/admin/accounts/upstream-billing-probe/group-preview", handler.PreviewUpstreamGroup)
	router.PUT("/admin/accounts/:id/upstream-billing-probe", handler.SetUpstreamBillingProbeEnabled)
	return router
}

func TestAccountHandlerUpstreamBillingGroupPreviewKeepsStoredCredentialsBound(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	for _, body := range []string{
		`{"account_id":7,"base_url":"https://different.example"}`,
		`{"account_id":7,"proxy_id":8}`,
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/admin/accounts/upstream-billing-probe/group-preview", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	}
}

func TestAccountHandlerGetUpstreamBillingProbeSettingsReturnsDefaults(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-probe/settings", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data service.UpstreamBillingProbeSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Data.Enabled)
	require.Equal(t, 30, response.Data.IntervalMinutes)
}

func TestAccountHandlerProbeUpstreamBillingBatchValidatesIDs(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()

	for _, body := range []string{`{"account_ids":[]}`, `{"account_ids":[0]}`} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/admin/accounts/upstream-billing-probe/batch", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}

func TestAccountHandlerSetUpstreamBillingProbeEnabledRejectsInvalidID(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/accounts/not-an-id/upstream-billing-probe", bytes.NewBufferString(`{"enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestAccountHandlerSetUpstreamBillingProbeEnabledRequiresValue(t *testing.T) {
	router := setupUpstreamBillingProbeRouter()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/accounts/1/upstream-billing-probe", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

type groupPreviewAdmin struct {
	service.AdminService
	account *service.Account
}

func (s *groupPreviewAdmin) GetAccount(context.Context, int64) (*service.Account, error) {
	return s.account, nil
}

type groupPreviewHTTP struct{ service.HTTPUpstream }

func (*groupPreviewHTTP) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_name":"Remote group"}`))}, nil
}

func TestAccountHandlerUpstreamGroupPreviewBindsGuardAndUpdatePassesIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notes := "Original notes"
	account := &service.Account{ID: 7, Name: "Original name", Notes: &notes, Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-private-key", "base_url": "https://relay.example/v1"}}
	admin := &groupPreviewAdmin{account: account}
	handler := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	transport := service.NewAccountTestService(nil, nil, nil, nil, nil, &groupPreviewHTTP{}, &config.Config{}, nil)
	handler.SetUpstreamBillingProbeService(service.NewUpstreamBillingProbeService(nil, transport, nil))
	router := gin.New()
	router.POST("/preview", handler.PreviewUpstreamGroup)
	// Register parameterized routes before the first pooled context is allocated.
	stub := newStubAdminService()
	updater := NewAccountHandler(stub, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.PUT("/accounts/:id", updater.Update)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(`{"account_id":7}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "synthetic-private-key")
	var result struct {
		Data struct {
			Name  string                          `json:"group_name"`
			Guard *service.UpstreamGroupSyncGuard `json:"sync_guard"`
		}
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	want, err := service.NewUpstreamGroupSyncGuard(account)
	require.NoError(t, err)
	require.Equal(t, want, result.Data.Guard)
	require.Equal(t, "Remote group", result.Data.Name)

	body, err := json.Marshal(map[string]any{"name": result.Data.Name, "upstream_group_guard": result.Data.Guard})
	require.NoError(t, err)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/accounts/7", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, want, stub.lastUpdateAccountInput.UpstreamGroupGuard)
}
