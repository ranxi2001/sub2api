//go:build unit && integration

package handler

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The sole HTTP adapter targets an ephemeral loopback synthetic provider.
type lifecycleChainHTTP struct {
	target string
	writes atomic.Int32
}

func (u *lifecycleChainHTTP) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.writes.Add(1)
	copy := r.Clone(r.Context())
	url := *r.URL
	url.Scheme = "http"
	url.Host = u.target
	copy.URL = &url
	copy.Host = ""
	return http.DefaultClient.Do(copy)
}
func (u *lifecycleChainHTTP) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}

func TestPriorityLifecycleRealHandlerUsagePolicyAndState(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("lifecycle_chain"), tcpostgres.WithUsername("synthetic"), tcpostgres.WithPassword("synthetic"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	accounts := repository.NewAccountRepository(client, db, nil)
	usage := repository.NewUsageLogRepository(client, db)
	settings := service.NewSettingService(repository.NewSettingRepository(client), nil)
	priority := service.DefaultPrioritySchedulingConfig()
	priority.Enabled = true
	priority.MinSamples = 1
	priority.TargetTTFTMs = 100
	require.NoError(t, settings.SavePrioritySchedulingConfig(ctx, priority))
	var userID, keyID, groupID int64
	require.NoError(t, db.QueryRow("INSERT INTO users(email,password_hash,balance,status,concurrency) VALUES('lifecycle@test.invalid','synthetic',100,'active',0) RETURNING id").Scan(&userID))
	require.NoError(t, db.QueryRow("INSERT INTO groups(name,platform) VALUES('synthetic lifecycle','openai') RETURNING id").Scan(&groupID))
	require.NoError(t, db.QueryRow("INSERT INTO api_keys(user_id,key,name,group_id,status) VALUES($1,'synthetic-lifecycle','synthetic',$2,'active') RETURNING id", userID, groupID).Scan(&keyID))
	account := &service.Account{Name: "synthetic account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "synthetic-only"}}
	require.NoError(t, accounts.Create(ctx, account))
	require.NoError(t, accounts.BindGroups(ctx, account.ID, []int64{groupID}))
	// An existing offline fallback price avoids any external price lookup. The
	// fixture model is forwarded over the Responses protocol to loopback only.
	model := "claude-sonnet-4"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n")
		w.(http.Flusher).Flush()
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic_chain\",\"model\":%q,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", model)
	}))
	defer provider.Close()
	upstream := &lifecycleChainHTTP{target: strings.TrimPrefix(provider.URL, "http://")}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	slots := service.NewConcurrencyService(&penaltySlotCache{})
	gateway := service.NewOpenAIGatewayService(accounts, nil, usage, nil, nil, nil, nil, nil, cfg, nil, slots, service.NewBillingService(cfg, nil), nil, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, settings, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	t.Cleanup(gateway.StopOpenAICodexTicketHarvester)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gateway, slots, billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	collector := requesttiming.New(time.Now(), -1)
	requestCtx := requesttiming.With(ctx, collector)
	c, rec := newExcelBPSFailoverTestContext(requestCtx, true)
	body := fmt.Sprintf("{\"model\":%q,\"input\":\"synthetic\",\"stream\":true}", model)
	c.Request.Body = io.NopCloser(strings.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: keyID, UserID: userID, Status: service.StatusActive, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1}, User: &service.User{ID: userID, Balance: 100, Status: service.StatusActive}})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID, Concurrency: 0})
	h.Responses(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "synthetic")
	collector.Finish(rec.Code, false)
	require.Equal(t, int32(1), upstream.writes.Load(), "one submitted generation, no replay")
	var usageID int64
	require.Eventually(t, func() bool {
		return db.QueryRow("SELECT id FROM usage_logs WHERE api_key_id=$1 AND input_tokens+output_tokens>0", keyID).Scan(&usageID) == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		var count int
		_ = db.QueryRow("SELECT count(*) FROM request_timing_details WHERE usage_log_id=$1 AND trace_id=$2", usageID, collector.Identity()).Scan(&count)
		return count == 1
	}, 5*time.Second, 10*time.Millisecond)
	durable := usage.(service.PriorityLifecycleRepository)
	measurement, spec, _ := service.PriorityLifecycleVersions(priority)
	scope := service.PriorityLifecycleScope{GroupID: &groupID, Model: model, Endpoint: "/v1/responses", RequestType: service.RequestTypeStream, MeasurementSchema: measurement, StatisticsSpec: spec}
	require.NoError(t, durable.AdvancePriorityLifecycle(ctx, priority))
	stats, policies, err := durable.ReadPriorityLifecycle(ctx, scope)
	require.NoError(t, err)
	require.NotNil(t, stats)
	require.Len(t, policies, 1)
	require.Equal(t, 1, stats.Signals[account.ID].Samples)
	require.NoError(t, accounts.SetSchedulable(ctx, account.ID, false))
	_, err = db.ExecContext(ctx, "UPDATE accounts SET status='error' WHERE id=$1", account.ID)
	require.NoError(t, err)
	ah := adminhandler.NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ah.SetOpenAIGatewayService(gateway)
	router := gin.New()
	router.GET("/state", ah.PriorityLifecycleState)
	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest("GET", fmt.Sprintf("/state?group_id=%d&model=%s&endpoint=/v1/responses&request_type=2", groupID, model), nil))
	require.Equal(t, 200, read.Code, read.Body.String())
	require.Contains(t, read.Body.String(), "ADMIN_DISABLED")
	require.Contains(t, read.Body.String(), policies[0].EvidenceID)
	require.NotContains(t, read.Body.String(), "synthetic-only")
	require.Equal(t, int32(1), upstream.writes.Load(), "state GET must never probe a provider")
	decisions, err := durable.ReadPriorityLifecycleDecisions(ctx, scope, 0)
	require.NoError(t, err)
	require.NotEmpty(t, decisions)
	require.Equal(t, collector.Identity(), decisions[0].TraceID, "handler -> transport -> usage and decision share an actual trace")
}
