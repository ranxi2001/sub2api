//go:build integration

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

type oauth429HandlerLoopback struct{ target *url.URL }

func (u oauth429HandlerLoopback) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	if err := service.ValidateOAuth429Upstream(req, id); err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	target := *u.target
	target.Path = req.URL.Path
	clone.URL = &target
	clone.Host = ""
	return (&http.Client{Timeout: 3 * time.Second}).Do(clone)
}
func (u oauth429HandlerLoopback) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}

func TestOAuth429HandlerPostgresWorkerLoopback(t *testing.T) {
	ctx := t.Context()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("oauth429"), tcpostgres.WithUsername("fixture"), tcpostgres.WithPassword("fixture"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	defer pg.Terminate(context.Background())
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	redisContainer, err := tcredis.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	defer redisContainer.Terminate(context.Background())
	redisURL, err := redisContainer.ConnectionString(ctx)
	require.NoError(t, err)
	opts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	accounts := repository.NewAccountRepository(client, db, nil)
	runtime := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	ops := service.ProvideAccountOpsService(repository.NewSettingRepository(client), repository.NewAccountOpsRepository(db), nil, accounts, repository.NewGroupRepository(client, db), runtime, nil, nil, nil, nil, nil)
	defer ops.Stop()
	handler := NewAccountOpsHandler(ops, nil)
	router := gin.New()
	router.PUT("/config", handler.SaveAutoConfig)
	router.GET("/config", handler.GetAutoConfig)
	cfg := service.DefaultOAuthAutoConfig()
	off := false
	cfg.DegradationAction.Enabled = true
	cfg.DegradationAction.ProbeEnabled = &off
	cfg.DegradationAction.Independent429 = service.OAuth429TestConfig{Enabled: true, MinSamples: 20}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/config", nil))
	require.Equal(t, 200, read.Code)
	require.Contains(t, read.Body.String(), `"min_samples":20`)
	require.NotContains(t, read.Body.String(), "daily_test_budget")
	a := &service.Account{Name: "fixture-only", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 50, Credentials: map[string]any{"access_token": "loopback-fixture", "model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra", "gpt-6-luna": "gpt-6-luna"}}, Extra: map[string]any{}}
	require.NoError(t, accounts.Create(ctx, a))
	_, err = db.ExecContext(ctx, `UPDATE accounts SET rate_limit_reset_at=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer provider.Close()
	target, err := url.Parse(provider.URL)
	require.NoError(t, err)
	tests := service.NewAccountTestService(accounts, nil, nil, nil, nil, oauth429HandlerLoopback{target: target}, runtime, nil)
	runner := service.ProvideScheduledTestRunnerService(nil, nil, tests, nil, runtime, &service.QualityJudgeService{}, nil, nil, &service.ChannelMonitorV2Service{}, service.NewConcurrencyService(repository.NewConcurrencyCache(rdb, 15, 60)))
	defer runner.Stop()
	runner.StartOAuth429Worker()
	for i := 0; i < 20; i++ {
		sampleCtx := context.WithValue(ctx, ctxkey.ClientRequestID, fmt.Sprintf("business-%d", i))
		sample, err := http.NewRequestWithContext(sampleCtx, http.MethodPost, "http://127.0.0.1/fixture", strings.NewReader(`{"model":"gpt-6-astra"}`))
		require.NoError(t, err)
		service.ObserveOAuth429HTTP(sample, a.ID, 429)
	}
	require.Eventually(t, func() bool {
		got, err := accounts.GetByID(ctx, a.ID)
		return err == nil && got.HasOAuthDegradationAction()
	}, 8*time.Second, 20*time.Millisecond, "threshold must drive five tests without a quality plan, cron or ordinary 429 cooldown")
	require.EqualValues(t, 5, calls.Load())
	got, err := accounts.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, cfg.DegradationAction.TargetConcurrency, got.Concurrency)
	require.True(t, got.IsModelSupported("gpt-6-luna"))
	require.NotNil(t, got.RateLimitResetAt)
	var plans int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_test_plans WHERE account_id=$1`, a.ID).Scan(&plans))
	require.Zero(t, plans)
}
