package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSumSnapshotActualCostPrefersTrendAndIgnoresListPrice(t *testing.T) {
	sum, err := sumSnapshotActualCost(map[string]any{
		"trend": []any{
			map[string]any{"actual_cost": 1.25, "cost": 9.0},
			map[string]any{"actual_cost": 0.75, "cost": 4.0},
		},
		"models": []any{map[string]any{"actual_cost": 100.0, "cost": 100.0}},
	})
	require.NoError(t, err)
	require.InDelta(t, 2.0, *sum, 1e-9)

	sum, err = sumSnapshotActualCost(map[string]any{
		"models": []any{map[string]any{"actual_cost": 3.5, "cost": 8}},
	})
	require.NoError(t, err)
	require.InDelta(t, 3.5, *sum, 1e-9)

	empty, err := sumSnapshotActualCost(map[string]any{"trend": []any{}})
	require.NoError(t, err)
	require.Equal(t, 0.0, *empty)

	_, err = sumSnapshotActualCost(map[string]any{
		"trend": []any{map[string]any{"cost": 4}},
	})
	require.ErrorIs(t, err, errProviderResponse)
}

func TestSub2APICustomWindowUsesSnapshotActualCostAndLeavesTodaySpendUnset(t *testing.T) {
	var sawStats atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user/profile":
			require.Equal(t, "Bearer window-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":4}}`))
		case "/api/v1/usage/dashboard/stats":
			sawStats.Store(true)
			http.NotFound(w, r)
		case "/api/v1/usage/dashboard/snapshot-v2":
			require.Equal(t, "2026-11-16", r.URL.Query().Get("start_date"))
			require.Equal(t, "2026-11-16", r.URL.Query().Get("end_date"))
			require.Equal(t, "Asia/Shanghai", r.URL.Query().Get("timezone"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"trend":[{"date":"2026-11-16","actual_cost":2.5,"cost":9}],"models":[{"model":"fixture","actual_cost":2.5,"cost":9}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	loc := service.ProfitConsumptionLocation()
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, loc)
	until := time.Date(2026, 11, 17, 0, 0, 0, 0, loc)
	secret, err := encodeProviderToken("window-token", "", "Bearer")
	require.NoError(t, err)
	probe, err := probeProviderSpend(context.Background(), providerRecord{ID: "7", SiteType: "sub2api", AuthMode: "token", BaseURL: server.URL}, secret, server.Client(), from, until)
	require.NoError(t, err)
	require.False(t, sawStats.Load(), "a historical window must not read today's dashboard stats")
	require.Nil(t, probe.metrics.TodaySpend)
	require.InDelta(t, 2.5, *probe.spend, 1e-9)
	require.Equal(t, "USD", probe.spendCurrency)
	require.Equal(t, "sub2api_snapshot_v2", probe.spendSource)
	require.Empty(t, probe.secret)
}

func TestSub2APITodayWindowStillUsesDashboardStats(t *testing.T) {
	var snapshotCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user/profile":
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":4,"total_recharged":9}}`))
		case "/api/v1/usage/dashboard/stats":
			_, _ = w.Write([]byte(`{"code":0,"data":{"today_actual_cost":1.5,"total_actual_cost":8}}`))
		case "/api/v1/usage/dashboard/snapshot-v2":
			snapshotCalls.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	loc := service.ProfitConsumptionLocation()
	now := time.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	secret, err := encodeProviderToken("window-token", "", "Bearer")
	require.NoError(t, err)
	probe, err := probeProviderSpend(context.Background(), providerRecord{ID: "7", SiteType: "sub2api", AuthMode: "token", BaseURL: server.URL}, secret, server.Client(), from, now)
	require.NoError(t, err)
	require.Zero(t, snapshotCalls.Load())
	require.NotNil(t, probe.metrics.TodaySpend)
	require.InDelta(t, 1.5, *probe.metrics.TodaySpend, 1e-9)
	require.InDelta(t, 1.5, *probe.spend, 1e-9)
	require.Equal(t, "sub2api_dashboard_stats", probe.spendSource)
}

func TestNewAPIWindowPassesRequestedTimestamps(t *testing.T) {
	loc := service.ProfitConsumptionLocation()
	from := time.Date(2026, 11, 10, 0, 0, 0, 0, loc)
	until := time.Date(2026, 11, 12, 0, 0, 0, 0, loc)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000,"used_quota":0}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"USD"}}`))
		case "/api/log/self/stat":
			require.Equal(t, "2", r.URL.Query().Get("type"))
			require.Equal(t, strconv.FormatInt(from.Unix(), 10), r.URL.Query().Get("start_timestamp"))
			require.Equal(t, strconv.FormatInt(until.Unix(), 10), r.URL.Query().Get("end_timestamp"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":250000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	secret, err := encodeProviderToken("system-token", "", "Bearer")
	require.NoError(t, err)
	probe, err := probeProviderSpend(context.Background(), providerRecord{ID: "9", SiteType: "newapi", AuthMode: "token", BaseURL: server.URL, UserID: "user-1"}, secret, server.Client(), from, until)
	require.NoError(t, err)
	require.Nil(t, probe.metrics.TodaySpend, "a historical range must not be stored as today's spend")
	require.InDelta(t, 0.5, *probe.spend, 1e-9)
	require.Equal(t, "USD", probe.spendCurrency)
	require.Equal(t, "newapi_log_stat", probe.spendSource)
}

func TestWalletSpendRetries429WithoutLoggingTheBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/log/self/stat" && r.URL.Path != "/api/user/self" && r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/api/user/self" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
			return
		}
		if r.URL.Path == "/api/status" {
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"USD"}}`))
			return
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"secret-token-body"}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"quota":500000}}`))
	}))
	defer server.Close()
	previous := profitSleep
	profitSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { profitSleep = previous })
	loc := service.ProfitConsumptionLocation()
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, loc)
	secret, err := encodeProviderToken("system-token", "", "Bearer")
	require.NoError(t, err)
	probe, err := probeProviderSpend(context.Background(), providerRecord{ID: "3", SiteType: "newapi", AuthMode: "token", BaseURL: server.URL}, secret, server.Client(), from, from.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
	require.InDelta(t, 1.0, *probe.spend, 1e-9)
	require.NotContains(t, providerPublicError(err), "secret-token")
}
