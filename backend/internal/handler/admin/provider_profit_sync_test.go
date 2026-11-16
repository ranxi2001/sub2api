package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	return map[string]any{"page": page, "total": total, "pages": max(1, (total+99)/100), "page_size": 100, "items": rows}
}
func profitProof(id, key int, req string) map[string]any {
	return map[string]any{"usage_id": id, "api_key_id": key, "user_id": 7, "request_id": req, "currency": "USD", "amount": "0.00007813"}
}

func profitHandlerCall(t *testing.T, site, body string, remote func(*http.Request) (int, any)) (*profitSyncStore, *httptest.ResponseRecorder) {
	t.Helper()
	s := &profitSyncStore{p: service.MatureProvider{ID: 5, Revision: 7, BaseURL: "https://wallet.invalid", SiteType: site, AuthMode: "token"}}
	h := NewChannelHandler(service.NewChannelService(s, nil, nil, nil, nil), nil, nil)
	h.SetMatureProviderDependencies(profitFakeCrypt{}, &http.Client{Transport: profitTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "wallet.invalid", r.URL.Host)
		require.Equal(t, "Bearer fixture-access", r.Header.Get("Authorization"))
		status, data := remote(r)
		env := map[string]any{"code": 0, "data": data}
		if status != 200 {
			env = map[string]any{"code": status, "message": "private-remote-fixture"}
		}
		raw, err := json.Marshal(env)
		require.NoError(t, err)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
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

func TestProviderProfitSyncExactUsageAndProof(t *testing.T) {
	for _, status := range []int{200, 404, 405} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			rows := make([]map[string]any, 0, 103)
			for i := 1; i <= 103; i++ {
				rows = append(rows, profitRow(i, 4, fmt.Sprintf("request-%d", i)))
				rows[i-1]["actual_cost"] = 0
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
			rows[100] = profitRow(101, 4, "same")
			rows[100]["billing_type"] = 1
			var paths []string
			s, w := profitHandlerCall(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any) {
				paths = append(paths, r.URL.Path)
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
					require.Equal(t, "100", q.Get("page_size"))
					if q.Get("page") == "1" {
						return 200, profitPage(1, len(rows), rows[:100])
					}
					require.Equal(t, "2", q.Get("page"))
					return 200, profitPage(2, len(rows), rows[100:])
				case "/api/v1/usage/settled-debits":
					require.Equal(t, "POST", r.Method)
					raw, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.JSONEq(t, `{"usage_ids":[2,3]}`, string(raw))
					return status, map[string]any{"items": []any{profitProof(2, 6, "same")}}
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
					return 0, nil
				}
			})
			require.Equal(t, 200, w.Code, w.Body.String())
			require.JSONEq(t, `{"code":0,"message":"success","data":{"matched":2,"unmatched":3}}`, w.Body.String())
			require.Equal(t, 1, s.writes)
			require.Len(t, paths, 4)
			require.Len(t, s.charges, 2)
			require.Equal(t, "same", s.charges[0].RequestID)
			require.EqualValues(t, 6, s.charges[0].KeyID)
			require.Equal(t, "0.123456789012345678901", s.charges[0].Amount)
			if status == 200 {
				require.NotNil(t, s.charges[0].SettledAmount)
				require.Equal(t, "0.00007813", *s.charges[0].SettledAmount)
			} else {
				require.Nil(t, s.charges[0].SettledAmount)
			}
			require.Nil(t, s.charges[1].SettledAmount)
		})
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
	for _, name := range []string{"simple", "late-http", "truncated", "wrong-page", "changed-total", "wrong-pages", "oversized", "repeated-id", "foreign-user", "bad-time", "proof-http", "proof-absent-midscan", "proof-key", "proof-user", "proof-request", "proof-currency", "proof-id", "proof-not-decimal-text", "proof-duplicate"} {
		t.Run(name, func(t *testing.T) {
			batches := 0
			s, w := profitHandlerCall(t, "sub2api", profitWindowBody, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/api/v1/auth/me":
					mode := "standard"
					if name == "simple" {
						mode = "simple"
					}
					return 200, map[string]any{"id": 7, "run_mode": mode}
				case "/api/v1/usage":
					rows := make([]map[string]any, 100)
					for i := range rows {
						rows[i] = profitRow(i+1, 4, fmt.Sprintf("req-%d", i+1))
					}
					if r.URL.Query().Get("page") == "1" {
						if name == "oversized" {
							return 200, profitPage(1, 5001, rows)
						}
						return 200, profitPage(1, 101, rows)
					}
					row := profitRow(101, 4, "req-101")
					page, total := 2, 101
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
					case "changed-total":
						total++
					case "repeated-id":
						row["id"] = 100
					case "foreign-user":
						row["user_id"] = 9
					case "bad-time":
						row["created_at"] = "not an instant"
					}
					return 200, profitPage(page, total, []map[string]any{row})
				case "/api/v1/usage/settled-debits":
					batches++
					p := profitProof(1, 4, "req-1")
					switch name {
					case "proof-http":
						return 403, nil
					case "proof-absent-midscan":
						if batches == 2 {
							return 404, nil
						}
					case "proof-key":
						p["api_key_id"] = 5
					case "proof-user":
						p["user_id"] = 8
					case "proof-request":
						p["request_id"] = "other"
					case "proof-id":
						p["usage_id"] = 101
					case "proof-currency":
						p["currency"] = "EUR"
					case "proof-not-decimal-text":
						p["amount"] = 1
					case "proof-duplicate":
						return 200, map[string]any{"items": []any{p, p}}
					default:
						t.Fatal("lookup on incomplete usage")
					}
					return 200, map[string]any{"items": []any{p}}
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
					return 0, nil
				}
			})
			want := 502
			if name == "simple" || name == "oversized" {
				want = 400
			}
			require.Equal(t, want, w.Code, w.Body.String())
			require.Zero(t, s.writes)
			if name == "proof-absent-midscan" {
				require.Equal(t, 2, batches)
			}
		})
	}
}
