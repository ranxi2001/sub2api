//go:build integration

package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Account creation still traverses the real Create handler and provider PG
// transaction. The account sink forbids implicit network probes.
type budgetAccountSink struct {
	service.AdminService
	creates atomic.Int32
}

func (s *budgetAccountSink) CreateAccount(_ context.Context, in *service.CreateAccountInput) (*service.Account, error) {
	s.creates.Add(1)
	return &service.Account{ID: 1, Name: in.Name, Platform: in.Platform, Type: in.Type, Credentials: in.Credentials, Extra: in.Extra}, nil
}
func (*budgetAccountSink) ForceAntigravityPrivacy(context.Context, *service.Account) string {
	return ""
}
func (*budgetAccountSink) ForceOpenAIPrivacy(context.Context, *service.Account) string { return "" }

func TestMatureProviderImportRotationBudget(t *testing.T) {
	for _, auth := range []string{"refresh_only", "401_renewal"} {
		for _, scenario := range []string{"slow_pages", "lock_wait", "cancel", "failure", "depleted_lock_budget"} {
			t.Run(auth+"/"+scenario, func(t *testing.T) {
				t.Parallel()
				db := matureProviderDatabase(t)
				cfg := &config.Config{}
				cfg.Totp.EncryptionKey = strings.Repeat("73", 32)
				crypt, err := repository.NewAESEncryptor(cfg)
				require.NoError(t, err)
				var rotations, oldUses, pages atomic.Int32
				var healthy atomic.Bool
				rotated, entered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/auth/refresh" {
						var in struct {
							RefreshToken string `json:"refresh_token"`
						}
						_ = json.NewDecoder(r.Body).Decode(&in)
						if in.RefreshToken != "fixture-old-refresh" || rotations.Add(1) != 1 {
							oldUses.Add(1)
							w.WriteHeader(401)
							return
						}
						_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"fixture-new-access","refresh_token":"fixture-new-refresh","token_type":"Bearer"}}`))
						close(rotated)
						return
					}
					if r.Header.Get("Authorization") != "Bearer fixture-new-access" {
						w.WriteHeader(401)
						_, _ = w.Write([]byte(`{"message":"expired fixture"}`))
						return
					}
					if r.URL.Path == "/api/v1/groups/rates" {
						_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
						return
					}
					if r.URL.Path != "/api/v1/keys" || r.Method != "GET" {
						t.Errorf("unexpected metadata-only operation %s %s", r.Method, r.URL.Path)
						w.WriteHeader(500)
						return
					}
					if !healthy.Load() {
						if pages.Add(1) == 1 {
							close(entered)
						}
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
						if scenario == "slow_pages" || scenario == "lock_wait" {
							// Each page is below the 10s header limit; four pages exceed the
							// defective 35s transaction lifetime. Context cancellation ends work.
							timer := time.NewTimer(9 * time.Second)
							defer timer.Stop()
							select {
							case <-timer.C:
							case <-r.Context().Done():
								return
							}
						}
						if scenario == "cancel" {
							<-r.Context().Done()
							return
						}
						if scenario == "failure" {
							w.WriteHeader(503)
							_, _ = w.Write([]byte(`{"message":"fixture failure"}`))
							return
						}
					}
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					total := 4
					id := page
					if healthy.Load() {
						total = 1
						id = 4
					}
					_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[{"id":%d,"name":"Fixture key","status":"active","key":"sk-fixture-import-secret"}],"total":%d}}`, id, total)
				}))
				defer srv.Close()
				target, err := url.Parse(srv.URL)
				require.NoError(t, err)
				transport := srv.Client().Transport.(*http.Transport).Clone()
				transport.ResponseHeaderTimeout = 10 * time.Second
				defer transport.CloseIdleConnections()
				client := &http.Client{Timeout: 15 * time.Second, Transport: matureProviderTransport(func(r *http.Request) (*http.Response, error) {
					copy := r.Clone(r.Context())
					u := *r.URL
					u.Scheme, u.Host = target.Scheme, target.Host
					copy.URL = &u
					return transport.RoundTrip(copy)
				})}
				sink := &budgetAccountSink{}
				newRouter := func() *gin.Engine {
					ch := admin.NewChannelHandler(service.NewChannelService(repository.NewChannelRepository(db), nil, nil, nil, nil), nil, nil)
					ch.SetMatureProviderDependencies(crypt, client)
					ah := admin.NewAccountHandler(sink, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
					ah.SetMatureProviderCatalog(ch)
					r := gin.New()
					r.POST("/api/v1/admin/providers", ch.MutateMatureProvider)
					r.POST("/api/v1/admin/accounts", ah.Create)
					return r
				}
				request := func(ctx context.Context, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
					w := httptest.NewRecorder()
					req := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx)
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Idempotency-Key", "fixture-budget-0000001")
					r.ServeHTTP(w, req)
					return w
				}
				access := ""
				if auth == "401_renewal" {
					access = "fixture-expired-access"
				}
				r := newRouter()
				saved := request(context.Background(), r, "/api/v1/admin/providers", fmt.Sprintf(`{"name":"Budget fixture","site_type":"sub2api","base_url":"https://provider.example.invalid","auth_mode":"token","access_token":%q,"refresh_token":"fixture-old-refresh"}`, access))
				require.Equal(t, 200, saved.Code, saved.Body.String())
				var p struct{ Data struct{ ID int64 } }
				require.NoError(t, json.Unmarshal(saved.Body.Bytes(), &p))
				body := fmt.Sprintf(`{"name":"Import fixture","platform":"openai","type":"apikey","provider_id":%d,"provider_key_id":"4","credentials":{"openai_api_mode":"responses"}}`, p.Data.ID)
				var lock *sql.Tx
				var blockerPID int
				if scenario == "lock_wait" || scenario == "depleted_lock_budget" {
					lock, err = db.Begin()
					require.NoError(t, err)
					defer lock.Rollback()
					require.NoError(t, lock.QueryRow(`SELECT pg_backend_pid()`).Scan(&blockerPID))
					_, err = lock.Exec(`SELECT id FROM mature_providers WHERE id=$1 FOR UPDATE`, p.Data.ID)
					require.NoError(t, err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- request(ctx, r, "/api/v1/admin/accounts", body) }()
				if lock != nil {
					// Observe the real row-lock wait from another DB connection before
					// spending the budget. Time drives this deadline case, not the barrier.
					require.Eventually(t, func() bool {
						var waiting bool
						e := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%mature_providers%FOR UPDATE%' AND $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting)
						return e == nil && waiting
					}, 5*time.Second, 10*time.Millisecond)
					delay := 18 * time.Second
					if scenario == "depleted_lock_budget" {
						delay = 28 * time.Second
					}
					timer := time.NewTimer(delay)
					<-timer.C
					require.NoError(t, lock.Commit())
				}
				if scenario != "depleted_lock_budget" {
					select {
					case <-rotated:
					case <-time.After(10 * time.Second):
						t.Fatal("rotation did not enter")
					}
					select {
					case <-entered:
					case <-time.After(10 * time.Second):
						t.Fatal("page did not enter after rotation")
					}
					if scenario == "cancel" {
						cancel()
					}
					close(release)
				} else {
					close(release)
				}
				var result *httptest.ResponseRecorder
				select {
				case result = <-done:
				case <-time.After(65 * time.Second):
					t.Fatal("handler did not finish bounded metadata work")
				}
				if scenario == "depleted_lock_budget" {
					require.Zero(t, rotations.Load(), "budget guard must not consume a single-use credential after lock wait exhausted persistence reserve")
					require.NotEqual(t, 200, result.Code)
					return
				}
				var cipher string
				require.NoError(t, db.QueryRow(`SELECT credential_cipher FROM mature_providers WHERE id=$1`, p.Data.ID).Scan(&cipher))
				plain, err := crypt.Decrypt(cipher)
				require.NoError(t, err)
				var token struct {
					Access  string `json:"access_token"`
					Refresh string `json:"refresh_token"`
				}
				require.NoError(t, json.Unmarshal([]byte(plain), &token))
				require.Equal(t, "fixture-new-refresh", token.Refresh, "rotation must survive slow pagination, lock wait, cancellation and remote failure in durable PG")
				require.Equal(t, "fixture-new-access", token.Access)
				require.NotEqual(t, 200, result.Code, "incomplete or cancelled import must not create an account")
				require.Zero(t, sink.creates.Load())
				require.NotContains(t, result.Body.String(), "fixture-new-")
				require.NotContains(t, result.Body.String(), "fixture-old-")
				// A new handler/service decrypts the DB value; it has no access to the
				// first session. Its successful import must not reuse the consumed token.
				healthy.Store(true)
				second := request(context.Background(), newRouter(), "/api/v1/admin/accounts", body)
				require.Equal(t, 200, second.Code, second.Body.String())
				require.EqualValues(t, 1, sink.creates.Load())
				require.EqualValues(t, 1, rotations.Load())
				require.Zero(t, oldUses.Load())
			})
		}
	}
}

// Signed observations must traverse the real refresh handler, persistence and
// alert episode SQL. Unknown/failure are not zero and never mean recovery.
func TestMatureProviderNegativeBalanceEpisodes(t *testing.T) {
	db := matureProviderDatabase(t)
	cfg := &config.Config{}
	cfg.Totp.EncryptionKey = strings.Repeat("92", 32)
	crypt, err := repository.NewAESEncryptor(cfg)
	require.NoError(t, err)
	sample := "-2.5"
	client := &http.Client{Transport: matureProviderTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		raw := `{"code":0,"data":{}}`
		if r.URL.Path == "/api/v1/user/profile" {
			if sample == "failure" {
				status = 503
				raw = `{"message":"fixture unavailable"}`
			} else if sample != "unknown" {
				raw = fmt.Sprintf(`{"code":0,"data":{"balance":%s}}`, sample)
			}
		} else {
			require.Equal(t, "/api/v1/usage/dashboard/stats", r.URL.Path)
		}
		require.Equal(t, http.MethodGet, r.Method)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
	})}
	ch := admin.NewChannelHandler(service.NewChannelService(repository.NewChannelRepository(db), nil, nil, nil, nil), nil, nil)
	ch.SetMatureProviderDependencies(crypt, client)
	r := gin.New()
	registerChannelRoutes(r.Group("/api/v1/admin"), &handler.Handlers{Admin: &handler.AdminHandlers{Channel: ch}})
	request := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/admin/providers"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	saved := request("", `{"name":"Debt fixture","site_type":"sub2api","base_url":"https://provider.example.invalid","auth_mode":"token","access_token":"fixture-debt-token","low_balance_threshold":5}`)
	require.Equal(t, 200, saved.Code, saved.Body.String())
	var p struct{ Data struct{ ID int64 } }
	require.NoError(t, json.Unmarshal(saved.Body.Bytes(), &p))
	var firstEpisode int64
	for i, s := range []string{"-2.5", "-3", "failure", "unknown", "5", "-1"} {
		sample = s
		var revision int64
		require.NoError(t, db.QueryRow(`SELECT revision FROM mature_providers WHERE id=$1`, p.Data.ID).Scan(&revision))
		out := request(fmt.Sprintf("/%d/refresh", p.Data.ID), fmt.Sprintf(`{"revision":%d}`, revision))
		require.Equal(t, 200, out.Code, out.Body.String())
		var raw, status string
		require.NoError(t, db.QueryRow(`SELECT metrics::text,status FROM mature_providers WHERE id=$1`, p.Data.ID).Scan(&raw, &status))
		var metrics service.ProviderMetrics
		require.NoError(t, json.Unmarshal([]byte(raw), &metrics))
		var total, active int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*),COUNT(*) FILTER(WHERE resolved_at IS NULL) FROM mature_provider_alerts WHERE provider_id=$1`, p.Data.ID).Scan(&total, &active))
		wantTotal, wantActive := 1, 1
		if i == 4 {
			wantActive = 0
		}
		if i == 5 {
			wantTotal = 2
		}
		require.Equal(t, wantTotal, total, "negative samples dedupe within an episode and open another only after observed recovery: %s", s)
		require.Equal(t, wantActive, active, s)
		if i == 0 {
			require.NoError(t, db.QueryRow(`SELECT id FROM mature_provider_alerts WHERE provider_id=$1`, p.Data.ID).Scan(&firstEpisode))
		}
		if s == "failure" {
			require.Equal(t, "disconnected", status)
			require.NotNil(t, metrics.Balance)
			require.Equal(t, float64(-3), *metrics.Balance)
		} else if s == "unknown" {
			require.Nil(t, metrics.Balance, "unknown must not fabricate a zero balance")
		} else {
			require.NotNil(t, metrics.Balance, "known debt must persist as a signed observation")
			want := map[string]float64{"-2.5": -2.5, "-3": -3, "5": 5, "-1": -1}[s]
			require.Equal(t, want, *metrics.Balance)
		}
		if i == 2 || i == 3 {
			var resolved bool
			require.NoError(t, db.QueryRow(`SELECT resolved_at IS NOT NULL FROM mature_provider_alerts WHERE id=$1`, firstEpisode).Scan(&resolved))
			require.False(t, resolved, "failure/unknown must preserve the existing low episode")
		}
	}
	var resolved bool
	require.NoError(t, db.QueryRow(`SELECT resolved_at IS NOT NULL FROM mature_provider_alerts WHERE id=$1`, firstEpisode).Scan(&resolved))
	require.True(t, resolved)
}
