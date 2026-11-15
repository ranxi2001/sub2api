//go:build integration

package routes

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type matureProviderTransport func(*http.Request) (*http.Response, error)

func (f matureProviderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMatureProvidersCredentialsAndMetadata(t *testing.T) {
	db := matureProviderDatabase(t)
	cfg := &config.Config{}
	cfg.Totp.EncryptionKey = strings.Repeat("12", 32)
	crypt, err := repository.NewAESEncryptor(cfg)
	require.NoError(t, err)
	var calls atomic.Int32
	client := &http.Client{Transport: matureProviderTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, "provider.example.invalid", r.URL.Host)
		require.Equal(t, "GET", r.Method, "metadata refresh must not generate content")
		require.Equal(t, "Bearer synthetic-dashboard-secret", r.Header.Get("Authorization"))
		data := ""
		switch r.URL.Path {
		case "/api/v1/user/profile":
			data = `{"code":0,"data":{"balance":12.5}}`
		case "/api/v1/usage/dashboard/stats":
			data = `{"code":0,"data":{"today_actual_cost":0,"total_actual_cost":3.5}}`
		default:
			t.Errorf("unexpected remote operation %s", r.URL.Path)
			data = `{"code":1}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}, nil
	})}
	ch := admin.NewChannelHandler(service.NewChannelService(repository.NewChannelRepository(db), nil, nil, nil, nil), nil, nil)
	// Baseline has no credential support. Its real HTTP decoder rejects the
	// new fields; the RED remains behavioral and compiles without new symbols.
	if configurable, ok := any(ch).(interface {
		SetMatureProviderDependencies(service.SecretEncryptor, *http.Client)
	}); ok {
		configurable.SetMatureProviderDependencies(crypt, client)
	}
	r := gin.New()
	registerChannelRoutes(r.Group("/api/v1/admin"), &handler.Handlers{Admin: &handler.AdminHandlers{Channel: ch}})
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/admin/providers"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		require.Equal(t, 200, w.Code, "encrypted dashboard credentials must support metadata-only refresh: %s", w.Body.String())
		require.NotContains(t, w.Body.String(), "synthetic-dashboard-secret")
		var out struct{ Data map[string]any }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out.Data
	}
	body := `{"name":"With login","site_type":"sub2api","base_url":"https://provider.example.invalid","auth_mode":"token","access_token":"synthetic-dashboard-secret"}`
	p := decode(request("POST", "", body))
	require.Equal(t, true, p["has_credential"])
	require.Zero(t, calls.Load(), "saving credentials must not contact provider")
	path := fmt.Sprintf("/%.0f", p["id"])
	var stored string
	require.NoError(t, db.QueryRow(`SELECT credential_cipher FROM mature_providers WHERE id=$1`, p["id"]).Scan(&stored))
	require.NotContains(t, stored, "synthetic-dashboard-secret")
	plain, err := crypt.Decrypt(stored)
	require.NoError(t, err)
	require.Contains(t, plain, "synthetic-dashboard-secret")
	p = decode(request("POST", path+"/refresh", fmt.Sprintf(`{"revision":%.0f}`, p["revision"])))
	require.EqualValues(t, 2, calls.Load())
	require.Equal(t, "connected", p["status"])
	metrics := p["metrics"].(map[string]any)
	require.EqualValues(t, 12.5, metrics["balance"])
	require.EqualValues(t, 0, metrics["today_spend"])
	require.EqualValues(t, 3.5, metrics["total_spend"])
	require.Nil(t, metrics["total_recharge"], "balance plus spend must not invent recharge")
	require.Equal(t, "USD", metrics["currency"])
	decode(request("GET", path, ""))
	changed := fmt.Sprintf(`{"name":"New destination","site_type":"sub2api","base_url":"https://other.example.invalid","auth_mode":"token","revision":%.0f}`, p["revision"])
	p = decode(request("PUT", path, changed))
	require.Equal(t, false, p["has_credential"], "a different origin cannot inherit dashboard credentials")
	require.Equal(t, "unknown", p["status"])
	w := request("POST", path+"/refresh", fmt.Sprintf(`{"revision":%.0f}`, p["revision"]))
	require.Equal(t, 400, w.Code)
	require.EqualValues(t, 2, calls.Load(), "missing login must fail before networking")
}
