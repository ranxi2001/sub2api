package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func upstreamBalanceProbeWalletBody() io.ReadCloser {
	return io.NopCloser(strings.NewReader(`{
		"mode":"unrestricted",
		"isValid":true,
		"planName":"钱包余额",
		"remaining":12.34,
		"unit":"USD",
		"balance":12.34,
		"usage":{"today":{"requests":3,"cost":0.5},"total":{"requests":9,"cost":7.5}},
		"daily_usage":[{"date":"2026-07-13","requests":3}],
		"model_stats":[{"model":"gpt-5","requests":3}]
	}`))
}

func upstreamBalanceProbeJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestParseUpstreamBalanceResponseModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "wallet keeps only balance fields",
			body: `{"mode":"unrestricted","isValid":true,"planName":"钱包余额","remaining":12.34,"unit":"USD","balance":12.34,
				"usage":{"today":{"cost":1}},"daily_usage":[],"model_stats":[{"model":"gpt-5"}],"secret":"must-not-persist"}`,
			want: map[string]any{
				"mode": "unrestricted", "is_valid": true, "plan_name": "钱包余额", "unit": "USD",
				"remaining": 12.34, "wallet_balance": 12.34,
			},
		},
		{
			name: "negative wallet balance stays a balance",
			body: `{"mode":"unrestricted","isValid":true,"remaining":-1,"balance":-1,"unit":"USD"}`,
			want: map[string]any{
				"mode": "unrestricted", "is_valid": true, "unit": "USD", "remaining": -1.0, "wallet_balance": -1.0,
			},
		},
		{
			name: "subscription without limits is unlimited",
			body: `{"mode":"unrestricted","isValid":true,"planName":"Claude Max","unit":"USD","remaining":-1,
				"subscription":{"daily_usage_usd":3,"weekly_usage_usd":4,"monthly_usage_usd":5,
				"daily_limit_usd":null,"weekly_limit_usd":0,"monthly_limit_usd":null,
				"expires_at":"2026-08-01T08:00:00+08:00"}}`,
			want: map[string]any{
				"mode": "unrestricted", "is_valid": true, "plan_name": "Claude Max", "unit": "USD",
				"unlimited": true, "expires_at": "2026-08-01T00:00:00Z",
			},
		},
		{
			name: "subscription reports configured windows",
			body: `{"mode":"unrestricted","isValid":true,"planName":"Pro","unit":"USD","remaining":7.5,
				"subscription":{"daily_usage_usd":2.5,"weekly_usage_usd":4,"monthly_usage_usd":30,
				"daily_limit_usd":10,"weekly_limit_usd":null,"monthly_limit_usd":100,
				"expires_at":"2026-08-01T00:00:00Z"}}`,
			want: map[string]any{
				"mode": "unrestricted", "is_valid": true, "plan_name": "Pro", "unit": "USD", "remaining": 7.5,
				"windows": []map[string]any{
					{"window": "daily", "limit": 10.0, "used": 2.5},
					{"window": "monthly", "limit": 100.0, "used": 30.0},
				},
				"expires_at": "2026-08-01T00:00:00Z",
			},
		},
		{
			name: "subscription without an active subscription has no remaining",
			body: `{"mode":"unrestricted","isValid":true,"planName":"Pro","unit":"USD"}`,
			want: map[string]any{"mode": "unrestricted", "is_valid": true, "plan_name": "Pro", "unit": "USD"},
		},
		{
			name: "quota limited key",
			body: `{"mode":"quota_limited","isValid":true,"status":"quota_exhausted",
				"quota":{"limit":100,"used":100,"remaining":0,"unit":"USD"},"remaining":0,"unit":"USD",
				"rate_limits":[
					{"window":"5h","limit":5,"used":1.25,"remaining":3.75,"window_start":"2026-07-13T00:00:00Z","reset_at":"2026-07-13T05:00:00Z"},
					{"window":"7d","limit":20,"used":2,"remaining":18}
				],
				"expires_at":"2026-09-01T00:00:00Z","days_until_expiry":50}`,
			want: map[string]any{
				"mode": "quota_limited", "is_valid": true, "key_status": "quota_exhausted", "unit": "USD",
				"remaining": 0.0, "quota_limit": 100.0, "quota_used": 100.0,
				"windows": []map[string]any{
					{"window": "5h", "limit": 5.0, "used": 1.25, "reset_at": "2026-07-13T05:00:00Z"},
					{"window": "7d", "limit": 20.0, "used": 2.0},
				},
				"expires_at": "2026-09-01T00:00:00Z",
			},
		},
		{
			name: "quota remaining falls back to the quota object",
			body: `{"mode":"quota_limited","isValid":true,"status":"active","quota":{"limit":10,"used":4,"remaining":6}}`,
			want: map[string]any{
				"mode": "quota_limited", "is_valid": true, "key_status": "active",
				"remaining": 6.0, "quota_limit": 10.0, "quota_used": 4.0,
			},
		},
		{
			name: "legacy response without mode",
			body: `{"isValid":true,"planName":"钱包余额","remaining":3,"unit":"USD"}`,
			want: map[string]any{"is_valid": true, "plan_name": "钱包余额", "unit": "USD", "remaining": 3.0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := parseUpstreamBalanceResponse([]byte(tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.want, data)
		})
	}
}

func TestParseUpstreamBalanceResponseSanitizesUpstreamText(t *testing.T) {
	longName := strings.Repeat("名", upstreamBalanceProbeMaxPlanNameRunes+10)
	data, err := parseUpstreamBalanceResponse([]byte(`{
		"mode":"something_new",
		"isValid":false,
		"status":"not active",
		"unit":"US$",
		"planName":"  Pro\u0000\u202e plan\n  ",
		"remaining":1,
		"rate_limits":[
			{"window":"../5h","limit":5,"used":1},
			{"window":"1d","limit":0,"used":1},
			{"window":"1d","limit":3,"used":1,"reset_at":"not-a-time"}
		]
	}`))
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"is_valid":  false,
		"plan_name": "Pro plan",
		"remaining": 1.0,
		"windows":   []map[string]any{{"window": "1d", "limit": 3.0, "used": 1.0}},
	}, data)

	data, err = parseUpstreamBalanceResponse([]byte(`{"isValid":true,"planName":"` + longName + `"}`))
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("名", upstreamBalanceProbeMaxPlanNameRunes), data["plan_name"])

	var rateLimits []string
	for range upstreamBalanceProbeMaxWindows + 4 {
		rateLimits = append(rateLimits, `{"window":"5h","limit":1,"used":0}`)
	}
	data, err = parseUpstreamBalanceResponse([]byte(`{"isValid":true,"rate_limits":[` + strings.Join(rateLimits, ",") + `]}`))
	require.NoError(t, err)
	require.Len(t, data["windows"], upstreamBalanceProbeMaxWindows)
}

func TestParseUpstreamBalanceResponseRejectsForeignPayloads(t *testing.T) {
	for _, body := range []string{
		``,
		`<html>not found</html>`,
		`[]`,
		`{"remaining":12.34,"unit":"USD"}`,
		`{"isValid":"true","remaining":1}`,
		`{"isValid":true,"remaining":"12.34"}`,
		`{"isValid":true,"quota":{"limit":"100"}}`,
		`{"error":{"message":"Invalid URL (GET /v1/usage)","type":"invalid_request_error"}}`,
	} {
		_, err := parseUpstreamBalanceResponse([]byte(body))
		require.Error(t, err, body)
	}
}

func TestUpstreamBalanceProbeAllowed(t *testing.T) {
	const relay = "https://relay.example/v1"
	for _, tc := range []struct {
		name    string
		baseURL string
		status  int
		want    bool
	}{
		{"rate ok", relay, http.StatusOK, true},
		{"rate endpoint missing", relay, http.StatusNotFound, true},
		{"rate forbidden", relay, http.StatusForbidden, true},
		{"rate server error", relay, http.StatusBadGateway, true},
		{"no response", relay, 0, false},
		{"key rejected", relay, http.StatusUnauthorized, false},
		{"rate limited", relay, http.StatusTooManyRequests, false},
		{"official api", "https://api.openai.com", http.StatusNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := upstreamBalanceProbeAllowed(tc.baseURL, upstreamBillingProbeHTTPResult{statusCode: tc.status})
			require.Equal(t, tc.want, got)
		})
	}
}

func TestUpstreamBalanceProbeURLAsksForSmallestStatisticsWindow(t *testing.T) {
	now := time.Date(2026, time.July, 13, 23, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	require.Equal(t,
		"https://relay.example/v1/usage?days=1&end_date=2026-07-13&start_date=2026-07-13",
		upstreamBalanceProbeURL("https://relay.example/v1", now))
	require.Equal(t,
		"https://relay.example/prefix/v1/usage?days=1&end_date=2026-07-13&start_date=2026-07-13&tenant=a",
		upstreamBalanceProbeURL("https://relay.example/prefix?tenant=a", now))
}

func newUpstreamBalanceProbeTestAccount(id int64, extra map[string]any) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-sensitive", "base_url": "https://relay.example/v1"},
		Extra:       extra,
	}
}

// previousUpstreamBalanceExtra mirrors a snapshot as it is read back from
// accounts.extra: a successful rate plus a successful balance.
func previousUpstreamBalanceExtra() map[string]any {
	return map[string]any{
		UpstreamBillingProbeExtraKey: map[string]any{
			"status": "ok",
			"data": map[string]any{
				"object": "sub2api.key_billing", "schema_version": 1.0, "billing_scope": "token",
				"group_rate_multiplier": 0.5, "resolved_rate_multiplier": 0.5, "peak_rate_enabled": false,
				"effective_rate_multiplier": 0.5, "observed_at": "2026-07-13T01:00:00Z",
			},
			"received_at":     "2026-07-13T01:00:00Z",
			"fresh_until":     "2026-07-13T02:00:00Z",
			"last_attempt_at": "2026-07-13T01:00:00Z",
			"next_probe_at":   "2026-07-13T01:30:00Z",
			"balance": map[string]any{
				"status":          "ok",
				"data":            map[string]any{"is_valid": true, "remaining": 5.0},
				"received_at":     "2026-07-13T01:00:00Z",
				"fresh_until":     "2026-07-13T02:00:00Z",
				"last_attempt_at": "2026-07-13T01:00:00Z",
			},
		},
	}
}

func runUpstreamBalanceProbe(t *testing.T, account *Account, upstream *httpUpstreamRecorder) *UpstreamBillingProbeSnapshot {
	t.Helper()
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})
	svc.now = func() time.Time { return time.Date(2026, time.July, 13, 1, 30, 0, 0, time.UTC) }
	snapshot, err := svc.ProbeAccount(context.Background(), account.ID)
	require.NoError(t, err)
	persisted := decodeUpstreamBillingProbeSnapshot(account.Extra)
	require.NotNil(t, persisted)
	require.Equal(t, snapshot.Status, persisted.Status)
	return snapshot
}

// The usage endpoint aggregates statistics before returning the key quota.
type slowKeyUsageUpstream struct {
	httpUpstreamRecorder
	rateDelay time.Duration
}

func (u *slowKeyUsageUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	delay := u.rateDelay
	if req.URL.Path == upstreamBalanceProbePath {
		delay = 12 * time.Second
	}
	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-time.After(delay):
	}
	if req.URL.Path == upstreamBalanceProbePath {
		return upstreamBalanceProbeJSONResponse(http.StatusOK, `{"isValid":true,"mode":"quota_limited","quota":{"remaining":42},"unit":"USD"}`), nil
	}
	return u.httpUpstreamRecorder.DoWithTLS(req, proxy, id, concurrency, profile)
}

func TestUpstreamBalanceProbeSlowBusinessKeyQuota(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rateDelay  time.Duration
		rateStatus string
	}{
		{"rate available", 0, UpstreamBillingProbeStatusOK},
		{"rate times out", 15 * time.Second, UpstreamBillingProbeStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				account := newUpstreamBalanceProbeTestAccount(61, nil)
				repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
				upstream := &slowKeyUsageUpstream{rateDelay: tc.rateDelay}
				upstream.resp = &http.Response{StatusCode: http.StatusOK, Body: upstreamBillingProbeValidBody()}
				svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})
				snapshot, err := svc.ProbeAccount(context.Background(), account.ID)
				require.NoError(t, err)
				require.Equal(t, tc.rateStatus, snapshot.Status)
				persisted := decodeUpstreamBillingProbeSnapshot(account.Extra)
				require.NotNil(t, persisted)
				require.NotNil(t, persisted.Balance)
				require.Equal(t, UpstreamBillingProbeStatusOK, persisted.Balance.Status)
				require.Equal(t, 42.0, persisted.Balance.Data["remaining"])
				require.Empty(t, persisted.Balance.LastError)
			})
		})
	}
}

func TestUpstreamBalanceProbeReadsBalanceWhenRateIsUnsupported(t *testing.T) {
	now := time.Date(2026, time.July, 13, 1, 30, 0, 0, time.UTC)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		upstreamBalanceProbeJSONResponse(http.StatusNotFound, `{"error":"not found"}`),
		upstreamBalanceProbeJSONResponse(http.StatusOK, `{"isValid":true,"planName":"钱包余额","remaining":42,"unit":"USD","balance":42}`),
	}}

	snapshot := runUpstreamBalanceProbe(t, newUpstreamBalanceProbeTestAccount(61, nil), upstream)

	require.Equal(t, UpstreamBillingProbeStatusUnsupported, snapshot.Status)
	require.NotNil(t, snapshot.Balance)
	require.Equal(t, UpstreamBillingProbeStatusOK, snapshot.Balance.Status)
	require.Equal(t, 42.0, snapshot.Balance.Data["remaining"])
	// A readable balance keeps the normal cadence instead of the stretched
	// unsupported delay, which would leave it stale for most of every cycle.
	require.False(t, snapshot.NextProbeAt.After(now.Add(35*time.Minute)))
	require.False(t, snapshot.NextProbeAt.Before(now.Add(25*time.Minute)))
}

func TestUpstreamBalanceProbeKeepsStretchedDelayWhenNothingIsSupported(t *testing.T) {
	now := time.Date(2026, time.July, 13, 1, 30, 0, 0, time.UTC)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		upstreamBalanceProbeJSONResponse(http.StatusNotFound, `{}`),
		upstreamBalanceProbeJSONResponse(http.StatusNotFound, `{}`),
	}}

	snapshot := runUpstreamBalanceProbe(t, newUpstreamBalanceProbeTestAccount(62, nil), upstream)

	require.Equal(t, UpstreamBillingProbeStatusUnsupported, snapshot.Status)
	require.NotNil(t, snapshot.Balance)
	require.Equal(t, UpstreamBillingProbeStatusUnsupported, snapshot.Balance.Status)
	require.Equal(t, "unsupported", snapshot.Balance.LastError)
	require.Equal(t, http.StatusNotFound, snapshot.Balance.HTTPStatus)
	require.True(t, snapshot.NextProbeAt.After(now.Add(3*time.Hour)))
}

func TestUpstreamBalanceProbeSkipsBalanceAfterRejectedOrThrottledRate(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			account := newUpstreamBalanceProbeTestAccount(63, previousUpstreamBalanceExtra())
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				upstreamBalanceProbeJSONResponse(status, `{"error":"no"}`),
			}}

			snapshot := runUpstreamBalanceProbe(t, account, upstream)

			require.Len(t, upstream.requests, 1)
			require.Equal(t, UpstreamBillingProbeStatusFailed, snapshot.Status)
			require.NotNil(t, snapshot.Balance)
			require.Equal(t, UpstreamBillingProbeStatusOK, snapshot.Balance.Status)
			require.Equal(t, 5.0, snapshot.Balance.Data["remaining"])
			require.Equal(t, time.Date(2026, time.July, 13, 1, 0, 0, 0, time.UTC), *snapshot.Balance.ReceivedAt)
		})
	}
}

func TestUpstreamBalanceProbeSkipsBalanceWithoutUpstreamResponse(t *testing.T) {
	account := newUpstreamBalanceProbeTestAccount(64, previousUpstreamBalanceExtra())
	upstream := &httpUpstreamRecorder{err: errors.New("dial tcp: connection refused")}

	snapshot := runUpstreamBalanceProbe(t, account, upstream)

	require.Len(t, upstream.requests, 1)
	require.Equal(t, "request_failed", snapshot.LastError)
	require.NotNil(t, snapshot.Balance)
	require.Equal(t, 5.0, snapshot.Balance.Data["remaining"])

	// Failures before any request, such as a missing key, carry the balance too.
	account = newUpstreamBalanceProbeTestAccount(65, previousUpstreamBalanceExtra())
	account.Credentials = map[string]any{"base_url": "https://relay.example/v1"}
	upstream = &httpUpstreamRecorder{}

	snapshot = runUpstreamBalanceProbe(t, account, upstream)

	require.Empty(t, upstream.requests)
	require.Equal(t, "missing_api_key", snapshot.LastError)
	require.NotNil(t, snapshot.Balance)
	require.Equal(t, 5.0, snapshot.Balance.Data["remaining"])
}

func TestUpstreamBalanceProbeFailureKeepsLastBalance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		response   *http.Response
		wantReason string
		wantStatus int
	}{
		{"http error", upstreamBalanceProbeJSONResponse(http.StatusBadGateway, `{"error":"bad gateway"}`), "http_error", http.StatusBadGateway},
		{"invalid response", upstreamBalanceProbeJSONResponse(http.StatusOK, `<html></html>`), "invalid_response", http.StatusOK},
		{"too large", upstreamBalanceProbeJSONResponse(http.StatusOK, `{"isValid":true,"pad":"`+strings.Repeat("x", upstreamBalanceProbeMaxBodyBytes)+`"}`), "response_too_large", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := newUpstreamBalanceProbeTestAccount(66, previousUpstreamBalanceExtra())
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: http.StatusOK, Header: http.Header{}, Body: upstreamBillingProbeValidBody()},
				tc.response,
			}}

			snapshot := runUpstreamBalanceProbe(t, account, upstream)

			require.Equal(t, UpstreamBillingProbeStatusOK, snapshot.Status)
			require.NotNil(t, snapshot.Balance)
			require.Equal(t, UpstreamBillingProbeStatusFailed, snapshot.Balance.Status)
			require.Equal(t, tc.wantReason, snapshot.Balance.LastError)
			require.Equal(t, tc.wantStatus, snapshot.Balance.HTTPStatus)
			require.Equal(t, time.Date(2026, time.July, 13, 1, 30, 0, 0, time.UTC), snapshot.Balance.LastAttemptAt)
			require.Equal(t, 5.0, snapshot.Balance.Data["remaining"])
			require.Equal(t, time.Date(2026, time.July, 13, 1, 0, 0, 0, time.UTC), *snapshot.Balance.ReceivedAt)
			require.Equal(t, time.Date(2026, time.July, 13, 2, 0, 0, 0, time.UTC), *snapshot.Balance.FreshUntil)
		})
	}
}

func TestUpstreamBalanceProbeNeverQueriesOfficialOpenAI(t *testing.T) {
	account := newUpstreamBalanceProbeTestAccount(67, nil)
	account.Credentials = map[string]any{"api_key": "sk-official"}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		upstreamBalanceProbeJSONResponse(http.StatusNotFound, `{}`),
	}}

	snapshot := runUpstreamBalanceProbe(t, account, upstream)

	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://api.openai.com/v1/sub2api/billing", upstream.requests[0].URL.String())
	require.Equal(t, UpstreamBillingProbeStatusUnsupported, snapshot.Status)
	require.Nil(t, snapshot.Balance)
}

func TestDecodeUpstreamBillingProbeSnapshotDropsInvalidBalance(t *testing.T) {
	extra := previousUpstreamBalanceExtra()
	probe, ok := extra[UpstreamBillingProbeExtraKey].(map[string]any)
	require.True(t, ok)
	balance, ok := probe["balance"].(map[string]any)
	require.True(t, ok)
	balance["status"] = "maybe"

	snapshot := decodeUpstreamBillingProbeSnapshot(extra)

	require.NotNil(t, snapshot)
	require.Equal(t, UpstreamBillingProbeStatusOK, snapshot.Status)
	require.Nil(t, snapshot.Balance)
}

func TestBuildUpstreamBillingRateSnapshotItemsIncludesBalance(t *testing.T) {
	items := BuildUpstreamBillingRateSnapshotItems([]Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: previousUpstreamBalanceExtra()},
	})

	require.Len(t, items, 1)
	require.NotNil(t, items[0].Snapshot)
	require.NotNil(t, items[0].Snapshot.Balance)
	require.Equal(t, 5.0, items[0].Snapshot.Balance.Data["remaining"])
}
