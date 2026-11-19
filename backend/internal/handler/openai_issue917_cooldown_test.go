//go:build unit && issue917pg

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

const issue917BareError = "event: error\ndata: " + `{"type":"error","sequence_number":0,"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"No credits remaining (test fixture)."}}` + "\n\n"
const issue917Delta = "event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","delta":"fixture-output"}` + "\n\n"
const issue917Failed = "event: response.failed\ndata: " + `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"No credits remaining (test fixture)."}}}` + "\n\n"

// Count calls at the repository boundary, while retaining the real SQL write,
// outbox and reload. Runtime blocking alone cannot satisfy these assertions.
type issue917AccountRepo struct {
	service.AccountRepository
	mu          sync.Mutex
	tempWrites  int
	modelWrites int
	errorWrites int
}

func (r *issue917AccountRepo) GetOpenAITurnAdmission(ctx context.Context, id int64) (*service.Account, *service.Account, error) {
	reader, ok := r.AccountRepository.(service.OpenAITurnAdmissionReader)
	if !ok {
		return nil, nil, service.ErrAccountNotFound
	}
	return reader.GetOpenAITurnAdmission(ctx, id)
}

func (r *issue917AccountRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.mu.Lock()
	r.tempWrites++
	r.mu.Unlock()
	return r.AccountRepository.SetTempUnschedulable(ctx, id, until, reason)
}

func (r *issue917AccountRepo) SetModelRateLimit(ctx context.Context, id int64, model string, until time.Time, reason ...string) error {
	r.mu.Lock()
	r.modelWrites++
	r.mu.Unlock()
	return r.AccountRepository.SetModelRateLimit(ctx, id, model, until, reason...)
}

func (r *issue917AccountRepo) SetError(ctx context.Context, id int64, message string) error {
	r.mu.Lock()
	r.errorWrites++
	r.mu.Unlock()
	return r.AccountRepository.SetError(ctx, id, message)
}

func newIssue917Handler(t *testing.T, repo service.AccountRepository, upstream service.HTTPUpstream) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxAccountSwitches = 2
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), service.NewRateLimitService(repo, nil, cfg, nil, nil),
		billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	return NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
}

func issue917Request(h *OpenAIGatewayHandler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.1","input":"ping","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 917, UserID: 918, AllowLongContext: true,
		User:  &service.User{ID: 918, Status: service.StatusActive},
		Group: &service.Group{Platform: service.PlatformOpenAI, Status: service.StatusActive},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 918, Concurrency: 0})
	h.Responses(c)
	return rec
}

// This strict, CI-only fixture exercises the normal Responses handler, account
// selection, Forward, a real HTTP 200 SSE reader and the PostgreSQL repository.
// Missing infrastructure fails instead of silently skipping the regression.
func TestIssue917ResponsesCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := os.Getenv("ISSUE917_TEST_POSTGRES_DSN")
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost"}, u.Hostname())
	require.Equal(t, "/issue917_fixture", u.Path)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	require.NoError(t, db.PingContext(ctx))
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	newRepo := func() service.AccountRepository {
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		return repository.NewAccountRepository(client, db, nil)
	}

	for _, route := range []struct {
		name, accountType string
		passthrough       bool
	}{
		{name: "normal", accountType: service.AccountTypeAPIKey},
		{name: "passthrough", accountType: service.AccountTypeAPIKey, passthrough: true},
		{name: "oauth", accountType: service.AccountTypeOAuth},
	} {
		t.Run(route.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, timing, keyword, body string
				disabled                    bool
				threshold                   int
				cooldown                    bool
			}{
				{name: "initial", timing: "after_stream", body: issue917BareError, cooldown: true},
				{name: "after_output", timing: "after_stream", body: issue917Delta + issue917BareError, cooldown: true},
				{name: "disabled", timing: "after_stream", body: issue917BareError, disabled: true},
				{name: "nonmatching", timing: "after_stream", body: issue917BareError, keyword: "unrelated fixture"},
				{name: "legacy_timing", body: issue917BareError},
				{name: "before_stream", timing: "before_stream", body: issue917BareError},
				{name: "threshold_eof", timing: "after_stream", body: issue917BareError, threshold: 2, cooldown: true},
				{name: "threshold_pair_before_output", timing: "after_stream", body: issue917BareError + strings.Replace(issue917Failed, "insufficient_quota", "invalid_request_error", 1), threshold: 2, cooldown: true},
				{name: "threshold_pair_after_output", timing: "after_stream", body: issue917Delta + issue917BareError + issue917Failed, threshold: 2, cooldown: true},
				{name: "context_window", timing: "after_stream", keyword: "context", body: "event: error\ndata: " + `{"type":"error","error":{"code":"context_length_exceeded","message":"maximum context length exceeded"}}` + "\n\n"},
				{name: "encrypted_request", timing: "after_stream", keyword: "encrypted", body: "event: error\ndata: " + `{"type":"error","error":{"code":"invalid_encrypted_content","message":"Invalid encrypted content"}}` + "\n\n"},
				{name: "cyber_policy", timing: "after_stream", keyword: "fixture", body: "event: error\ndata: " + `{"type":"error","error":{"code":"cyber_policy","message":"fixture policy rejection"}}` + "\n\n"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if tc.threshold == 0 {
						tc.threshold = 1
					}
					if tc.keyword == "" {
						tc.keyword = "no credits remaining"
					}
					repo := &issue917AccountRepo{AccountRepository: newRepo()}
					provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, readErr := io.ReadAll(r.Body)
						if readErr != nil || !strings.Contains(string(body), `"stream":true`) {
							t.Errorf("expected a streaming Responses request: body=%s err=%v", body, readErr)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(http.StatusOK)
						if r.Header.Get("Authorization") == "Bearer fixture-failing" {
							_, _ = io.WriteString(w, tc.body)
						} else {
							_, _ = io.WriteString(w, issue917Delta+"event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"resp_healthy","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"fixture-output"}]}]}}`+"\n\n")
						}
					}))
					t.Cleanup(provider.Close)
					target, parseErr := url.Parse(provider.URL)
					require.NoError(t, parseErr)
					upstream := &anthropic413HTTPUpstream{client: provider.Client(), target: target}
					accounts := make([]*service.Account, 0, 2)
					for i, credential := range []string{"fixture-failing", "fixture-healthy"} {
						account := &service.Account{
							Name: "issue917-fixture", Platform: service.PlatformOpenAI, Type: route.accountType,
							Status: service.StatusActive, Schedulable: true, Priority: i + 1,
							Credentials: map[string]any{
								"api_key": credential, "access_token": credential, "base_url": provider.URL,
								"temp_unschedulable_enabled": !tc.disabled,
								"temp_unschedulable_rules": []any{map[string]any{
									"error_code": 502, "keywords": []string{tc.keyword}, "scope": "account",
									"stream_timing": tc.timing, "trigger_count": tc.threshold,
									"trigger_window_minutes": 5, "duration_minutes": 5,
								}},
							},
							Extra: map[string]any{"openai_passthrough": route.passthrough},
						}
						require.NoError(t, repo.Create(ctx, account))
						t.Cleanup(func() { require.NoError(t, repo.Delete(ctx, account.ID)) })
						accounts = append(accounts, account)
					}
					failingID := accounts[0].ID
					h := newIssue917Handler(t, repo, upstream)
					started := time.Now()
					for attempt := 1; attempt <= tc.threshold; attempt++ {
						rec := issue917Request(h)
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						require.Contains(t, rec.Body.String(), "event:", "keep the existing SSE terminal response")
						if strings.HasPrefix(tc.body, issue917Delta) {
							require.Contains(t, rec.Body.String(), "fixture-output")
						}
						upstream.mu.Lock()
						calls := append([]int64(nil), upstream.accounts...)
						upstream.mu.Unlock()
						require.Len(t, calls, attempt, "a bare error or committed output must not grant replay")
						for _, id := range calls {
							require.Equal(t, failingID, id)
						}
						loaded, loadErr := newRepo().GetByID(ctx, failingID)
						require.NoError(t, loadErr)
						wantCooldown := tc.cooldown && attempt == tc.threshold
						if !wantCooldown {
							require.Nil(t, loaded.TempUnschedulableUntil, "pending/disabled/nonmatching rules must not cool")
							require.Empty(t, loaded.TempUnschedulableReason)
							require.True(t, loaded.IsSchedulable())
						} else {
							require.NotNil(t, loaded.TempUnschedulableUntil, "matched initial SSE error must persist account cooldown")
							require.False(t, loaded.IsSchedulable(), "reloaded account must be excluded during cooldown")
							require.WithinRange(t, *loaded.TempUnschedulableUntil, started.Add(5*time.Minute), time.Now().Add(5*time.Minute))
							var reason service.TempUnschedState
							require.NoError(t, json.Unmarshal([]byte(loaded.TempUnschedulableReason), &reason))
							require.Equal(t, 502, reason.StatusCode)
							require.Equal(t, "account", reason.Scope)
							require.Equal(t, tc.keyword, reason.MatchedKeyword)
							require.Equal(t, loaded.TempUnschedulableUntil.Unix(), reason.UntilUnix)
							if tc.threshold > 1 {
								require.EqualValues(t, 2, reason.TriggerCount)
								require.Equal(t, 2, reason.TriggerThreshold)
							}
						}
						require.Equal(t, service.StatusActive, loaded.Status)
						require.True(t, loaded.Schedulable)
						require.Empty(t, loaded.ErrorMessage)
						require.Empty(t, loaded.Extra["model_rate_limits"])
						repo.mu.Lock()
						writes, models, permanent := repo.tempWrites, repo.modelWrites, repo.errorWrites
						repo.mu.Unlock()
						wantWrites := 0
						if wantCooldown {
							wantWrites = 1
						}
						require.Equal(t, wantWrites, writes, "one upstream failure must count once")
						require.Zero(t, models)
						require.Zero(t, permanent)
					}
					healthy, loadErr := newRepo().GetByID(ctx, accounts[1].ID)
					require.NoError(t, loadErr)
					require.Nil(t, healthy.TempUnschedulableUntil)
					require.Empty(t, healthy.TempUnschedulableReason)
					require.True(t, healthy.IsSchedulable())
					if tc.cooldown {
						// A fresh gateway cannot use the original runtime block/cache.
						fresh := newIssue917Handler(t, newRepo(), upstream)
						rec := issue917Request(fresh)
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						require.Contains(t, rec.Body.String(), "response.completed")
						upstream.mu.Lock()
						calls := append([]int64(nil), upstream.accounts...)
						upstream.mu.Unlock()
						require.Len(t, calls, tc.threshold+1)
						require.Equal(t, healthy.ID, calls[len(calls)-1], "fresh scheduler must select the healthy alternative")
					}
				})
			}
		})
	}
}
