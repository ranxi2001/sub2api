package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// No listener, resolver, external credential, or default HTTP transport.
// Use the production session, envelope decoder, and admin error translation.
type profitTransport func(*http.Request) (*http.Response, error)

func (f profitTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type profitFakeCrypt struct{}

func (profitFakeCrypt) Encrypt(string) (string, error) { panic("unexpected rotation") }
func (profitFakeCrypt) Decrypt(string) (string, error) {
	return encodeProviderToken("fixture-access", "", "Bearer")
}

type profitSyncStore struct {
	service.ChannelRepository
	service.MatureProviderRepository
	p                service.MatureProvider
	sessions, writes int
	charges          []service.ProviderProfitRemoteCharge
}

func (s *profitSyncStore) GetMatureProvider(context.Context, int64) (*service.MatureProvider, error) {
	return &s.p, nil
}
func (s *profitSyncStore) WithMatureProviderSession(ctx context.Context, id, revision int64, run func(context.Context, *service.MatureProvider, string) (service.ProviderSessionUpdate, error)) (*service.MatureProvider, error) {
	if id != s.p.ID || revision != s.p.Revision {
		return nil, service.ErrMatureProviderConflict
	}
	s.sessions++
	_, err := run(ctx, &s.p, "fixture-cipher")
	return &s.p, err
}
func (s *profitSyncStore) ProviderProfitIdentityFrom(context.Context, int64) (time.Time, error) {
	return profitInstant.Add(-time.Hour), nil
}
func (s *profitSyncStore) ReconcileProviderProfitCharges(_ context.Context, _ int64, _ time.Time, _ string, _ service.ProviderProfitWindow, charges []service.ProviderProfitRemoteCharge) (service.ProviderProfitSyncResult, error) {
	s.writes++
	s.charges = charges
	return service.ProviderProfitSyncResult{Matched: len(charges), Unmatched: 3}, nil
}

var profitInstant = time.Date(2026, 1, 10, 23, 50, 0, 0, time.UTC)

const profitWindowBody = `{"from":"2026-01-11T07:50:00+08:00","until":"2026-01-11T00:05:00Z"}`

func profitRow(id, key int, req string) map[string]any {
	return map[string]any{"id": id, "api_key_id": key, "user_id": 7, "request_id": req, "created_at": profitInstant, "billing_type": 0, "actual_cost": json.Number("0.123456789012345678901"), "api_key": map[string]any{"key": "private-key-fixture"}}
}
func profitPage(page, total int, rows []map[string]any) map[string]any {
	return profitPageSized(page, profitPageSize, total, rows)
}

func profitPageSized(page, size, total int, rows []map[string]any) map[string]any {
	return map[string]any{"page": page, "total": total, "pages": max(1, (total+size-1)/size), "page_size": size, "items": rows}
}

func profitHandlerCall(t *testing.T, site, body string, remote func(*http.Request) (int, any)) (*profitSyncStore, *httptest.ResponseRecorder) {
	t.Helper()
	return profitHandlerCallHeaders(t, site, body, func(r *http.Request) (int, any, http.Header) {
		status, data := remote(r)
		return status, data, nil
	})
}

func profitHandlerCallHeaders(t *testing.T, site, body string, remote func(*http.Request) (int, any, http.Header)) (*profitSyncStore, *httptest.ResponseRecorder) {
	t.Helper()
	s := &profitSyncStore{p: service.MatureProvider{ID: 5, Revision: 7, BaseURL: "https://wallet.invalid", SiteType: site, AuthMode: "token"}}
	h := NewChannelHandler(service.NewChannelService(s, nil, nil, nil, nil), nil, nil)
	t.Cleanup(h.StopMatureProviderRefresh)
	h.SetMatureProviderDependencies(profitFakeCrypt{}, &http.Client{Transport: profitTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "wallet.invalid", r.URL.Host)
		require.Equal(t, "Bearer fixture-access", r.Header.Get("Authorization"))
		status, data, header := remote(r)
		env := map[string]any{"code": 0, "data": data}
		if status != 200 {
			env = map[string]any{"code": status, "message": "private-remote-fixture"}
		}
		raw, err := json.Marshal(env)
		require.NoError(t, err)
		if header == nil {
			header = make(http.Header)
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})})
	r := gin.New()
	r.POST("/admin/providers/:id/profit-charges/sync", h.SyncProviderProfitCharges)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/providers/5/profit-charges/sync", strings.NewReader(body)))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	for _, secret := range []string{"private-remote-fixture", "fixture-access", "fixture-cipher", "private-key-fixture"} {
		require.NotContains(t, w.Body.String(), secret)
	}
	return s, w
}

func stubProfitBackoff(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	prevSleep, prevJitter := profitSleep, profitJitter
	profitJitter = func(time.Duration) time.Duration { return 0 }
	profitSleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { profitSleep, profitJitter = prevSleep, prevJitter })
	return &waits
}

func TestProviderProfitSyncExactUsageWithoutWalletProof(t *testing.T) {
	rows := make([]map[string]any, profitPageSize+3)
	for i := range rows {
		rows[i] = profitRow(i+1, 4, fmt.Sprintf("request-%d", i+1))
		rows[i]["actual_cost"] = nil
	}
	rows[0] = profitRow(1, 4, "same") // disqualified by page 2's subscription row on this key
	rows[1] = profitRow(2, 6, "same") // this key is distinct
	rows[2] = profitRow(3, 4, "reported-only")
	rows[3] = profitRow(4, 4, "subscription")
	rows[3]["subscription_id"] = 99
	rows[4] = profitRow(5, 4, "")
	rows[5] = profitRow(6, 4, "missing-actual")
	delete(rows[5], "actual_cost")
	rows[5]["total_cost"] = 18
	rows[5]["cost_details"] = map[string]any{"customer_actual_cost": 22}
	rows[6] = profitRow(7, 4, "negative")
	rows[6]["actual_cost"] = -1
	rows[7] = profitRow(8, 4, "missing-billing")
	delete(rows[7], "billing_type")
	rows[8] = profitRow(9, 4, strings.Repeat("x", 128))
	rows[9] = profitRow(10, 4, "nano")
	rows[9]["created_at"] = profitInstant.Add(time.Nanosecond)
	rows[10] = profitRow(11, 4, "until")
	rows[10]["created_at"] = "2026-01-11T00:05:00Z"
	rows[11] = profitRow(12, 4, strings.Repeat("x", 125))
	rows[profitPageSize] = profitRow(profitPageSize+1, 4, "same")
	rows[profitPageSize]["billing_type"] = 1
	rows[12] = profitRow(13, 4, "free")
	rows[12]["actual_cost"] = 0
	rows[13] = profitRow(14, 4, "subscription-bill")
	rows[13]["subscription_id"], rows[13]["billing_type"] = 9, 1
	// Outside the window, on a different key, not even priced: can
	// still conflict when the local key number was never recorded.
	rows[profitPageSize+1] = profitRow(profitPageSize+2, 8, "reported-only")
	rows[profitPageSize+1]["created_at"], rows[profitPageSize+1]["actual_cost"] = "2026-01-11T00:05:00Z", nil
	dataPages := 0
	s, w := profitHandlerCall(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			return 200, map[string]any{"id": 7, "run_mode": "standard"}
		case "/api/v1/usage":
			require.Equal(t, "GET", r.Method)
			q := r.URL.Query()
			require.Equal(t, "2026-01-10", q.Get("start_date"))
			require.Equal(t, "2026-01-11", q.Get("end_date"))
			require.Equal(t, "UTC", q.Get("timezone"))
			require.Equal(t, "id", q.Get("sort_by"))
			require.Equal(t, "asc", q.Get("sort_order"))
			if q.Get("page_size") == "1" {
				require.Equal(t, strconv.Itoa(profitPageSize), q.Get("page"))
				return 200, profitPageSized(profitPageSize, 1, len(rows), rows[profitPageSize-1:profitPageSize])
			}
			dataPages++
			require.Equal(t, strconv.Itoa(profitPageSize), q.Get("page_size"))
			if q.Get("page") == "1" {
				return 200, profitPage(1, len(rows), rows[:profitPageSize])
			}
			require.Equal(t, "2", q.Get("page"))
			return 200, profitPage(2, len(rows), rows[profitPageSize:])
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
			return 0, nil
		}
	})
	require.Equal(t, 200, w.Code, w.Body.String())
	require.JSONEq(t, `{"code":0,"message":"success","data":{"matched":4,"unmatched":3}}`, w.Body.String())
	require.Equal(t, 1, s.writes)
	require.Equal(t, 2, dataPages)
	require.Len(t, s.charges, 4)
	require.Equal(t, "same", s.charges[0].RequestID)
	require.EqualValues(t, 6, s.charges[0].KeyID)
	require.Equal(t, "0.123456789012345678901", s.charges[0].Amount)
	require.False(t, s.charges[0].UniqueAtSite)
	require.False(t, s.charges[1].UniqueAtSite)
	require.Equal(t, "free", s.charges[2].RequestID)
	require.Equal(t, "0", s.charges[2].Amount)
	require.True(t, s.charges[2].UniqueAtSite)
	require.Equal(t, "subscription-bill", s.charges[3].RequestID)
	for _, charge := range s.charges {
		require.Nil(t, charge.SettledAmount)
	}
}

func TestProviderProfitSyncRejectsBeforeAuthentication(t *testing.T) {
	for _, tt := range []struct{ site, body string }{
		{"newapi", profitWindowBody}, {"sub2api", `{}`}, {"sub2api", `{"from":"2026-01-01","until":"2026-02-01T00:00:00Z"}`}, {"sub2api", `{"from":"2026-01-01T00:00:00Z","until":"2026-02-02T00:00:00.000000001Z"}`}, {"sub2api", profitWindowBody + `{}`},
	} {
		s, w := profitHandlerCall(t, tt.site, tt.body, func(*http.Request) (int, any) { t.Fatal("unexpected remote request"); return 0, nil })
		require.Equal(t, 400, w.Code)
		require.Zero(t, s.sessions)
		require.Zero(t, s.writes)
	}
}

func TestProviderProfitSyncRejectsPartialAndInvalidResponses(t *testing.T) {
	for _, name := range []string{"simple", "late-http", "truncated", "wrong-page", "inconsistent-growth", "shrinking-total", "wrong-pages", "wrong-size", "repeated-id", "foreign-user", "bad-time"} {
		t.Run(name, func(t *testing.T) {
			s, w := profitHandlerCall(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/api/v1/auth/me":
					mode := "standard"
					if name == "simple" {
						mode = "simple"
					}
					return 200, map[string]any{"id": 7, "run_mode": mode}
				case "/api/v1/usage":
					rows := make([]map[string]any, profitPageSize)
					for i := range rows {
						rows[i] = profitRow(i+1, 4, fmt.Sprintf("req-%d", i+1))
					}
					initial := profitPageSize + 1
					if name == "shrinking-total" {
						initial++
					}
					if r.URL.Query().Get("page_size") == "1" {
						return 200, profitPageSized(profitPageSize, 1, initial, rows[profitPageSize-1:])
					}
					if r.URL.Query().Get("page") == "1" {
						return 200, profitPage(1, initial, rows)
					}
					row := profitRow(profitPageSize+1, 4, "last-request")
					page, total := 2, profitPageSize+1
					switch name {
					case "late-http":
						return 502, nil
					case "truncated":
						return 200, profitPage(2, total, []map[string]any{})
					case "wrong-page":
						page = 1
					case "wrong-pages":
						bad := profitPage(page, total, []map[string]any{row})
						bad["pages"] = 3
						return 200, bad
					case "inconsistent-growth":
						total++ // advertised more than the final page contains
					case "wrong-size":
						return 200, profitPageSized(page, profitPageSize+1, total, []map[string]any{row})
					case "repeated-id":
						row["id"] = profitPageSize
					case "foreign-user":
						row["user_id"] = 9
					case "bad-time":
						row["created_at"] = "not an instant"
					}
					return 200, profitPage(page, total, []map[string]any{row})
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
					return 0, nil
				}
			})
			want := 502
			if name == "simple" {
				want = 400
			}
			require.Equal(t, want, w.Code, w.Body.String())
			require.Zero(t, s.writes)
		})
	}
}

// This upstream accepts the real Sub2API page & page_size parameters. It
// appends two observations after the initial tail was advertised, including
// a duplicate outside the selected window. Neither can be ignored.
const profitInitialBill = 5250

type profitLiveBill struct {
	failLate, replaceUser, moveAnchor bool
	sessions, anchors, dataPages      int
}

func (b *profitLiveBill) serve(t *testing.T, r *http.Request) (int, any) {
	t.Helper()
	if r.URL.Path == "/api/v1/auth/me" {
		b.sessions++
		if b.replaceUser && b.sessions > 1 {
			return 200, map[string]any{"id": 8, "run_mode": "standard"}
		}
		return 200, map[string]any{"id": 7, "run_mode": "standard"}
	}
	require.Equal(t, "/api/v1/usage", r.URL.Path)
	q := r.URL.Query()
	size, err := strconv.Atoi(q.Get("page_size"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, size, 1)
	require.LessOrEqual(t, size, 1000) // upstream ParsePagination rejects larger values
	page, err := strconv.Atoi(q.Get("page"))
	require.NoError(t, err)
	offset := (page - 1) * size
	total := profitInitialBill
	if size == 1 {
		b.anchors++
		require.Greater(t, offset, 0)
	} else {
		b.dataPages++
	}
	if offset+size >= profitInitialBill {
		total += 2
	}
	if b.failLate && offset >= profitInitialBill {
		return 502, nil
	}
	rows := make([]map[string]any, 0, size)
	for i := offset + 1; i <= min(total, offset+size); i++ {
		row := profitRow(i, 4, fmt.Sprintf("billed-%d", i))
		switch i {
		case profitInitialBill + 1:
			row["request_id"], row["actual_cost"] = "billed-1", nil
			row["created_at"] = "2026-01-11T00:05:00Z"
		case profitInitialBill + 2:
			row["api_key_id"], row["request_id"] = 7, "billed-2"
			delete(row, "actual_cost")
		}
		if b.moveAnchor && size == 1 {
			row["id"] = i + 1
		}
		rows = append(rows, row)
	}
	return 200, profitPageSized(page, size, total, rows)
}

func TestProviderProfitSyncLargeBillWithLateDuplicates(t *testing.T) {
	var bill profitLiveBill
	s, w := profitHandlerCall(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any) { return bill.serve(t, r) })
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, s.writes)
	require.Len(t, s.charges, profitInitialBill-1)
	// First row is ambiguous on its known key; second only for unknown-key
	// attribution. Every earlier page still contributes exactly once.
	for i, r := range s.charges {
		require.EqualValues(t, i+2, r.UsageID)
		require.Equal(t, i != 0, r.UniqueAtSite)
	}
	require.Greater(t, s.sessions, 1)
	require.Equal(t, s.sessions, bill.sessions)
	require.Equal(t, bill.sessions-1, bill.anchors)
	require.Equal(t, (profitInitialBill+2+profitPageSize-1)/profitPageSize, bill.dataPages)
	require.LessOrEqual(t, bill.dataPages+bill.anchors, s.sessions*profitPagesPerBatch)
}

func TestProviderProfitSyncLargeBillLateFailure(t *testing.T) {
	bill := profitLiveBill{failLate: true}
	s, w := profitHandlerCall(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any) { return bill.serve(t, r) })
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	require.Greater(t, (bill.dataPages-1)*profitPageSize, 5000)
	require.Zero(t, s.writes)
	require.Nil(t, s.charges)
}

func TestProviderProfitSyncRejectsChangedSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		bill profitLiveBill
	}{{"user", profitLiveBill{replaceUser: true}}, {"anchor", profitLiveBill{moveAnchor: true}}} {
		t.Run(tc.name, func(t *testing.T) {
			s, w := profitHandlerCall(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any) { return tc.bill.serve(t, r) })
			require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
			require.GreaterOrEqual(t, tc.bill.sessions, 2)
			require.Zero(t, s.writes)
		})
	}
}

func TestProviderProfitSyncEvidenceBudget(t *testing.T) {
	// Start at the protective budget rather than building a huge fixture.
	// Another occurrence of a known pair costs no additional index space;
	// retaining an entirely new pair fails rather than reporting partial data.
	s := profitUsageScan{user: 7, retained: profitEvidenceBudget, first: profitInstant, end: profitInstant.Add(time.Hour),
		byKey: map[profitIdentity]bool{{4, "old"}: false}, bySite: map[string]bool{"old": false}}
	row := map[string]any{"user_id": json.Number("7"), "api_key_id": json.Number("4"), "created_at": profitInstant.Format(time.RFC3339Nano), "id": json.Number("21"), "request_id": "old"}
	require.NoError(t, s.observe(row))
	require.Equal(t, profitEvidenceBudget, s.retained)
	require.True(t, s.byKey[profitIdentity{4, "old"}])
	row["id"], row["request_id"] = json.Number("22"), "new"
	require.ErrorIs(t, s.observe(row), errProviderProfitEvidenceBudget)
}

func TestProfitRateLimitBackoffAndRetryAfter(t *testing.T) {
	prev := profitJitter
	profitJitter = func(time.Duration) time.Duration { return 0 }
	t.Cleanup(func() { profitJitter = prev })
	require.Equal(t, 2*time.Second, profitBackoff(1, 0))
	require.Equal(t, 4*time.Second, profitBackoff(2, 0))
	require.Equal(t, 8*time.Second, profitBackoff(3, 0))
	require.Equal(t, profitRateLimitCap, profitBackoff(10, 0))
	require.Equal(t, 7*time.Second, profitBackoff(4, 7*time.Second))

	retryAfter := func(status int, value string) time.Duration {
		t.Helper()
		return providerRetryAfter(&http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{value}}})
	}
	require.Equal(t, 5*time.Second, retryAfter(http.StatusTooManyRequests, "5"))
	require.Zero(t, retryAfter(http.StatusTooManyRequests, "0"))
	require.Zero(t, retryAfter(http.StatusTooManyRequests, "soon"))
	require.Zero(t, retryAfter(http.StatusBadGateway, "5"))
	when := time.Now().Add(9 * time.Second).UTC()
	delay := retryAfter(http.StatusTooManyRequests, when.Format(http.TimeFormat))
	require.InDelta(t, float64(9*time.Second), float64(delay), float64(time.Second))
}

func TestProviderProfitSyncRetries429ThenReconciles(t *testing.T) {
	waits := stubProfitBackoff(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	auth := 0
	s, w := profitHandlerCallHeaders(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any, http.Header) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			auth++
			if auth == 1 {
				return http.StatusTooManyRequests, nil, http.Header{"Retry-After": []string{"5"}}
			}
			return 200, map[string]any{"id": 7, "run_mode": "standard"}, nil
		case "/api/v1/usage":
			require.Equal(t, "1", r.URL.Query().Get("page"))
			return 200, profitPage(1, 1, []map[string]any{profitRow(1, 4, "billed-once")}), nil
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
			return 0, nil, nil
		}
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []time.Duration{5 * time.Second}, *waits)
	require.Equal(t, 1, s.writes)
	require.Len(t, s.charges, 1)
	require.Equal(t, "billed-once", s.charges[0].RequestID)
	text := logs.String()
	require.Contains(t, text, "provider profit sync rate limited")
	require.Contains(t, text, "operation=auth_me")
	require.Contains(t, text, "attempt=1")
	require.Contains(t, text, "wait_ms=5000")
	require.Contains(t, text, "retry_after_ms=5000")
	for _, secret := range []string{"private-remote-fixture", "fixture-access", "fixture-cipher", "private-key-fixture"} {
		require.NotContains(t, text, secret)
	}
}

func TestProviderProfitSyncRetriesUsageAnchorAndLaterSession(t *testing.T) {
	waits := stubProfitBackoff(t)
	var bill profitLiveBill
	authLeft, pageLeft, anchorLeft := 1, 1, 1
	s, w := profitHandlerCallHeaders(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any, http.Header) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			if authLeft > 0 {
				authLeft--
				return http.StatusTooManyRequests, nil, nil
			}
		case "/api/v1/usage":
			if r.URL.Query().Get("page_size") == "1" && anchorLeft > 0 {
				anchorLeft--
				return http.StatusTooManyRequests, nil, nil
			}
			if r.URL.Query().Get("page_size") != "1" && pageLeft > 0 {
				pageLeft--
				return http.StatusTooManyRequests, nil, nil
			}
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
		status, data := bill.serve(t, r)
		return status, data, nil
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []time.Duration{2 * time.Second, 2 * time.Second, 2 * time.Second}, *waits)
	require.Equal(t, 1, s.writes)
	require.Len(t, s.charges, profitInitialBill-1)
	require.Zero(t, authLeft)
	require.Zero(t, pageLeft)
	require.Zero(t, anchorLeft)
}

func TestProviderProfitSyncDoesNotBackoffOnOtherHTTPErrors(t *testing.T) {
	prev := profitSleep
	profitSleep = func(context.Context, time.Duration) error { t.Fatal("non-429 response was retried"); return nil }
	t.Cleanup(func() { profitSleep = prev })
	s, w := profitHandlerCall(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any) {
		if r.URL.Path == "/api/v1/auth/me" {
			return 200, map[string]any{"id": 7, "run_mode": "standard"}
		}
		return http.StatusBadGateway, nil
	})
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	require.Zero(t, s.writes)
}
