// Adapted from mizaawa/zayuapi a71aa9615ccb55a19973908c9f261b5ba25a48c6 (LGPL v3).
// See mature_provider_NOTICE.md and docs/mature-provider-provenance.json.
package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatureProtocolProviderURLRejectsUnsafeOrigins(t *testing.T) {
	for _, raw := range []string{
		"http://example.com", "https://127.0.0.1", "https://localhost",
		"https://example.com/private", "https://user:pass@example.com", "https://example.com?q=1",
	} {
		_, err := normalizeProviderURL(raw)
		require.Error(t, err, raw)
	}
	base, err := normalizeProviderURL("https://provider.example.invalid/")
	require.NoError(t, err)
	require.Equal(t, "https://provider.example.invalid", base)
}

func TestMatureProtocolSub2APITokenRefreshAndMetrics(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user/profile":
			if r.Header.Get("Authorization") == "Bearer expired" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"expired"}`))
				return
			}
			require.Equal(t, "Bearer renewed", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":8}}`))
		case "/api/v1/auth/refresh":
			require.Empty(t, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"renewed","refresh_token":"rotated","token_type":"Bearer"}}`))
		case "/api/v1/usage/dashboard/stats":
			require.Equal(t, "Bearer renewed", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"today_actual_cost":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	secret, err := encodeProviderToken("expired", "refresh", "Bearer")
	require.NoError(t, err)
	probe, err := probeProviderAccount(context.Background(), providerRecord{SiteType: "sub2api", AuthMode: "token", BaseURL: server.URL}, secret, server.Client())
	require.NoError(t, err)
	require.Equal(t, 8.0, *probe.metrics.Balance)
	require.Contains(t, probe.secret, "renewed")
	require.Contains(t, probe.secret, "rotated")
}

func TestMatureProtocolSub2APIRefreshOnlyExchangesBeforeProfileAndStoresRotatedToken(t *testing.T) {
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			require.Empty(t, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"renewed","refresh_token":"rotated","token_type":"Bearer"}}`))
		case "/api/v1/user/profile":
			require.Equal(t, "Bearer renewed", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":8}}`))
		case "/api/v1/usage/dashboard/stats":
			require.Equal(t, "Bearer renewed", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"today_actual_cost":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	secret, err := encodeProviderToken("", "refresh-only", "Bearer")
	require.NoError(t, err)
	probe, err := probeProviderAccount(context.Background(), providerRecord{SiteType: "sub2api", AuthMode: "token", BaseURL: server.URL}, secret, server.Client())
	require.NoError(t, err)
	require.Equal(t, []string{"/api/v1/auth/refresh", "/api/v1/user/profile", "/api/v1/usage/dashboard/stats"}, paths)
	require.Equal(t, 8.0, *probe.metrics.Balance)
	require.Contains(t, probe.secret, "renewed")
	require.Contains(t, probe.secret, "rotated")
}

func TestMatureProtocolNewAPITokenUsesUserIDAndRejectsModelKey(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/user/self" {
			require.Equal(t, "Bearer system-token", r.Header.Get("Authorization"))
			require.Equal(t, "user-abc_123", r.Header.Get("New-Api-User"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
			return
		}
		if r.URL.Path == "/api/status" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
			return
		}
		if r.URL.Path == "/api/log/self/stat" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":0}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	secret, err := encodeProviderToken("system-token", "", "Bearer")
	require.NoError(t, err)
	metrics, err := probeProviderWithClient(context.Background(), providerRecord{SiteType: "newapi", AuthMode: "token", BaseURL: server.URL, UserID: "user-abc_123"}, secret, server.Client())
	require.NoError(t, err)
	require.Equal(t, 1.0, *metrics.Balance)
}

func TestMatureProtocolProviderErrorShowsTurnstileWithoutLeakingUpstreamMessage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":400,"reason":"TURNSTILE_VERIFICATION_FAILED","message":"secret-password"}`))
	}))
	defer server.Close()
	r := providerRemote{client: server.Client(), base: server.URL}
	_, err := r.request(context.Background(), http.MethodPost, "/api/v1/auth/login", map[string]string{"password": "secret-password"})
	require.True(t, errors.Is(err, errProviderTurnstile))
	require.Contains(t, providerPublicError(err), "Turnstile")
	require.NotContains(t, providerPublicError(err), "secret-password")
}

func TestMatureProtocolSub2APIProviderLoginAndMetrics(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			require.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"test-token"}}`))
		case "/api/v1/user/profile":
			require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":15.79,"total_recharged":20}}`))
		case "/api/v1/usage/dashboard/stats":
			require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"today_actual_cost":0.70,"total_actual_cost":9.21}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	metrics, err := probeProviderWithClient(context.Background(), providerRecord{SiteType: "sub2api", AuthMode: "password", BaseURL: server.URL, Account: "demo@example.com"}, "password", server.Client())
	require.NoError(t, err)
	require.Equal(t, 15.79, *metrics.Balance)
	require.Equal(t, 0.70, *metrics.TodaySpend)
	require.Equal(t, 20.0, *metrics.TotalRecharge)
	require.Equal(t, 9.21, *metrics.TotalSpend)
}

func TestMatureProtocolNewAPIProviderUsesSessionAndQuotaPerUnit(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "demo", Path: "/", Secure: true})
			_, _ = w.Write([]byte(`{"success":true,"data":{"username":"demo"}}`))
		case "/api/user/self":
			require.True(t, strings.Contains(r.Header.Get("Cookie"), "session=demo"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000,"used_quota":250000}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
		case "/api/log/self/stat":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":100000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := server.Client()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client.Jar = jar
	metrics, err := probeProviderWithClient(context.Background(), providerRecord{SiteType: "newapi", AuthMode: "password", BaseURL: server.URL, Account: "demo"}, "password", client)
	require.NoError(t, err)
	require.Equal(t, 1.0, *metrics.Balance)
	require.Equal(t, 0.5, *metrics.TotalSpend)
	require.Equal(t, 0.2, *metrics.TodaySpend)
	require.Nil(t, metrics.TotalRecharge) // Unknown is not zero.
}

func TestMatureProtocolNewAPIPasswordUsesDashboardAccessTokenWhenReturned(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			_, _ = w.Write([]byte(`{"success":true,"data":{"access_token":"dashboard-jwt"}}`))
		case "/api/user/self":
			require.Equal(t, "Bearer dashboard-jwt", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
		case "/api/log/self/stat":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	metrics, err := probeProviderWithClient(context.Background(), providerRecord{SiteType: "newapi", AuthMode: "password", BaseURL: server.URL, Account: "demo"}, "password", server.Client())
	require.NoError(t, err)
	require.Equal(t, 1.0, *metrics.Balance)
}
