//go:build integration

package routes

// This test always starts its own PostgreSQL and Redis containers and ignores
// the main integration harness's external endpoint environment variables.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	ratelimit "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/tidwall/gjson"
)

const (
	usageRecordsIntegrationPostgresImage = "postgres:18.1-alpine3.23"
	usageRecordsIntegrationRedisImage    = "redis:8.4-alpine"
)

type usageRecordsCrossInstanceFixture struct {
	db      *sql.DB
	client  *dbent.Client
	options redis.Options
	config  *config.Config
	setting service.SettingRepository
	keys    service.APIKeyRepository
	usage   service.UsageLogRepository
}

type countedAPIKeyRepository struct {
	service.APIKeyRepository
	authReads atomic.Int64
}

func (r *countedAPIKeyRepository) GetByKeyForAuth(ctx context.Context, key string) (*service.APIKey, error) {
	r.authReads.Add(1)
	return r.APIKeyRepository.GetByKeyForAuth(ctx, key)
}

type usageRecordsFixtureCase struct {
	key           *service.APIKey
	replacementID int64
	record        *service.UsageLog
}

type countedUsageRecordsAuthCache struct {
	service.APIKeyCache
	reads atomic.Int64
}

func (c *countedUsageRecordsAuthCache) GetAuthCache(ctx context.Context, key string) (*service.APIKeyAuthCacheEntry, error) {
	c.reads.Add(1)
	return c.APIKeyCache.GetAuthCache(ctx, key)
}

func TestUsageRecordsCrossInstanceRealAdapterRevocation(t *testing.T) {
	fixture := newUsageRecordsCrossInstanceFixture(t)
	ctx := context.Background()

	const setting = service.SettingKeyAllowUngroupedKeyScheduling
	previous, err := fixture.setting.GetValue(ctx, setting)
	missing := errors.Is(err, service.ErrSettingNotFound)
	if !missing {
		require.NoError(t, err)
	}
	require.NoError(t, fixture.setting.Set(ctx, setting, "false"))
	t.Cleanup(func() {
		if missing {
			require.NoError(t, fixture.setting.Delete(ctx, setting))
			return
		}
		require.NoError(t, fixture.setting.Set(ctx, setting, previous))
	})

	redisA := newUsageRecordsRedisClient(t, fixture.options)
	redisB := newUsageRecordsRedisClient(t, fixture.options)
	countedRepo := &countedAPIKeyRepository{APIKeyRepository: fixture.keys}
	cacheA := repository.NewAPIKeyCache(redisA)
	cacheB := &countedUsageRecordsAuthCache{APIKeyCache: repository.NewAPIKeyCache(redisB)}
	users := repository.NewUserRepository(fixture.client, fixture.db)
	groups := repository.NewGroupRepository(fixture.client, fixture.db)
	instanceA := service.NewAPIKeyService(fixture.keys, users, groups, nil, nil, cacheA, fixture.config)
	instanceB := service.NewAPIKeyService(countedRepo, users, groups, nil, nil, cacheB, fixture.config)
	instanceA.StartAuthCacheInvalidationSubscriber(ctx)
	t.Cleanup(instanceA.StopAuthCacheInvalidationSubscriber)
	instanceB.StartAuthCacheInvalidationSubscriber(ctx)
	t.Cleanup(instanceB.StopAuthCacheInvalidationSubscriber)
	require.Eventually(t, func() bool {
		return instanceA.AuthCacheInvalidationSubscriberHealth().Connected &&
			instanceB.AuthCacheInvalidationSubscriberHealth().Connected
	}, 5*time.Second, 10*time.Millisecond, "both subscribers must receive their own Redis subscription acknowledgement")

	// All stale grants must still refer to the original key, owner and group.
	type staleGrant struct {
		status       string
		codexCLIOnly bool
	}
	groupedActive := &staleGrant{status: service.StatusActive}
	codexOnlyActive := &staleGrant{status: service.StatusActive, codexCLIOnly: true}
	type rejection struct {
		field, code string
		reason      servermiddleware.IngressRejectReason
	}
	invalidKey := rejection{"code", "INVALID_API_KEY", servermiddleware.IngressRejectInvalidAPIKey}
	deleteThroughA := func(t *testing.T, f *usageRecordsFixtureCase) {
		t.Helper()
		require.NoError(t, instanceA.Delete(ctx, f.key.ID, f.key.UserID))
	}
	update := func(t *testing.T, query string, args ...any) {
		t.Helper()
		result, err := fixture.db.ExecContext(ctx, query, args...)
		require.NoError(t, err)
		rows, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
	}
	tests := []struct {
		name       string
		stopB      bool
		wantStale  *staleGrant
		mutate     func(*testing.T, *usageRecordsFixtureCase)
		wantStatus int
		denial     rejection
	}{
		{
			name: "delete through instance A", mutate: deleteThroughA,
			wantStatus: http.StatusUnauthorized, denial: invalidKey,
		},
		{
			name: "status disabled by direct database write", wantStale: groupedActive,
			mutate: func(t *testing.T, f *usageRecordsFixtureCase) {
				update(t, "UPDATE api_keys SET status = $1 WHERE id = $2", service.StatusAPIKeyDisabled, f.key.ID)
			},
			wantStatus: http.StatusUnauthorized, denial: rejection{"code", "API_KEY_DISABLED", servermiddleware.IngressRejectAPIKeyDisabled},
		},
		{
			name: "owner replaced by direct database write", wantStale: groupedActive,
			mutate: func(t *testing.T, f *usageRecordsFixtureCase) {
				update(t, "UPDATE api_keys SET user_id = $1 WHERE id = $2", f.replacementID, f.key.ID)
				rebound, err := fixture.keys.GetByKeyForAuth(ctx, f.key.Key)
				require.NoError(t, err)
				require.NotNil(t, rebound)
				require.Equal(t, f.key.ID, rebound.ID)
				require.Equal(t, f.replacementID, rebound.UserID)
				require.NotNil(t, rebound.User)
				require.Equal(t, f.replacementID, rebound.User.ID)
			},
			wantStatus: http.StatusUnauthorized, denial: invalidKey,
		},
		{
			name: "group removed while ungrouped keys are forbidden", wantStale: groupedActive,
			mutate: func(t *testing.T, f *usageRecordsFixtureCase) {
				update(t, "UPDATE api_keys SET group_id = NULL WHERE id = $1", f.key.ID)
			},
			wantStatus: http.StatusForbidden, denial: rejection{"error.type", "permission_error", servermiddleware.IngressRejectGroupUnassigned},
		},
		{
			name: "Codex restriction enabled by direct database write", wantStale: groupedActive,
			mutate: func(t *testing.T, f *usageRecordsFixtureCase) {
				update(t, "UPDATE groups SET codex_cli_only = TRUE WHERE id = $1", *f.key.GroupID)
			},
			wantStatus: http.StatusForbidden, denial: rejection{"error.code", "codex_client_required", servermiddleware.IngressRejectCodexClientRequired},
		},
		{
			name: "Codex restriction lifted by direct database write", wantStale: codexOnlyActive,
			mutate: func(t *testing.T, f *usageRecordsFixtureCase) {
				update(t, "UPDATE groups SET codex_cli_only = FALSE WHERE id = $1", *f.key.GroupID)
			},
			wantStatus: http.StatusOK,
		},
		{ // Stopping the shared B subscriber is final for this test run.
			name: "delete through instance A after B subscriber stops", stopB: true,
			wantStale: groupedActive, mutate: deleteThroughA,
			wantStatus: http.StatusUnauthorized, denial: invalidKey,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			initialCodexOnly := tc.wantStale != nil && tc.wantStale.codexCLIOnly
			f := createUsageRecordsFixtureCase(t, fixture, initialCodexOnly)
			cacheKey := usageRecordsAuthCacheKey(f.key.Key)
			t.Cleanup(func() {
				require.NoError(t, cacheA.DeleteAuthCache(ctx, cacheKey))
				bucket := fmt.Sprintf("rate_limit:usage-records:{u:%d}:", f.key.UserID)
				require.NoError(t, redisB.Del(ctx, bucket+"user", fmt.Sprintf("%skey:%d", bucket, f.key.ID)).Err())
			})

			var rejectReason servermiddleware.IngressRejectReason
			router := newUsageRecordsCrossInstanceRouter(t, instanceB, fixture, redisB, &rejectReason)
			var warmHeaders map[string]string
			if initialCodexOnly {
				warmHeaders = map[string]string{
					"User-Agent":        "codex-tui/0.163.0 (Ubuntu 22.4.0; x86_64) xterm-256color",
					"Originator":        "codex-tui",
					"X-Codex-Window-Id": "usage-records-integration",
				}
			}
			l2BeforeWarm, pgBeforeWarm := cacheB.reads.Load(), countedRepo.authReads.Load()
			warm := serveUsageRecordsRequest(t, router, f.key.Key, warmHeaders, f.record.CreatedAt)
			require.Equal(t, http.StatusOK, warm.Code, warm.Body.String())
			assertUsageRecordsResponse(t, warm, f.record.RequestID, f.record.Model)
			require.Positive(t, countedRepo.authReads.Load()-pgBeforeWarm)
			require.Positive(t, cacheB.reads.Load()-l2BeforeWarm)

			// Inspect shared L2 independently of B's lookup counters.
			entry, err := cacheA.GetAuthCache(ctx, cacheKey)
			require.NoError(t, err)
			require.NotNil(t, entry)
			require.NotNil(t, entry.Snapshot)
			require.Equal(t, f.key.UserID, entry.Snapshot.UserID)
			require.Equal(t, f.key.ID, entry.Snapshot.APIKeyID)
			require.Equal(t, service.StatusActive, entry.Snapshot.Status)

			// Ristretto admits writes asynchronously. Wait for an observed L1 hit.
			waitForUsageRecordsLookup(t, "B's ordinary lookup must hit L1 (zero L2 and PG reads)", func(pollCtx context.Context) bool {
				l2, pg := cacheB.reads.Load(), countedRepo.authReads.Load()
				hot, err := instanceB.GetByKey(pollCtx, f.key.Key)
				return err == nil && hot != nil && hot.ID == f.key.ID && hot.UserID == f.key.UserID &&
					cacheB.reads.Load() == l2 && countedRepo.authReads.Load() == pg
			})

			if tc.stopB {
				instanceB.StopAuthCacheInvalidationSubscriber() // Joins the subscriber goroutine.
				require.False(t, instanceB.AuthCacheInvalidationSubscriberHealth().Connected)
			}
			tc.mutate(t, f)

			if tc.wantStale == nil {
				// With B online, wait for A's broadcast to evict the hot grant.
				waitForUsageRecordsLookup(t, "B must observe the deletion broadcast from A", func(pollCtx context.Context) bool {
					_, err := instanceB.GetByKey(pollCtx, f.key.Key)
					return errors.Is(err, service.ErrAPIKeyNotFound)
				})
			} else {
				l2, pg := cacheB.reads.Load(), countedRepo.authReads.Load()
				stale, err := instanceB.GetByKey(ctx, f.key.Key)
				require.NoError(t, err)
				l2Delta, pgDelta := cacheB.reads.Load()-l2, countedRepo.authReads.Load()-pg
				require.Zero(t, l2Delta, "stale lookup read Redis L2")
				require.Zero(t, pgDelta, "stale lookup read PostgreSQL")
				require.NotNil(t, stale)
				require.Equal(t, f.key.ID, stale.ID)
				require.Equal(t, f.key.UserID, stale.UserID)
				require.Equal(t, f.key.GroupID, stale.GroupID)
				require.Equal(t, tc.wantStale.status, stale.Status)
				require.NotNil(t, stale.Group)
				require.Equal(t, *f.key.GroupID, stale.Group.ID)
				require.Equal(t, tc.wantStale.codexCLIOnly, stale.Group.CodexCLIOnly)
				t.Logf("ordinary lookup status=%s, B GetAuthCache delta=%d, B GetByKeyForAuth delta=%d", stale.Status, l2Delta, pgDelta)
			}

			l2, pg := cacheB.reads.Load(), countedRepo.authReads.Load()
			result := serveUsageRecordsRequest(t, router, f.key.Key, nil, f.record.CreatedAt)
			l2Reads, pgReads := cacheB.reads.Load()-l2, countedRepo.authReads.Load()-pg
			t.Logf("records HTTP=%d, B GetAuthCache delta=%d, B GetByKeyForAuth delta=%d", result.Code, l2Reads, pgReads)
			require.Equal(t, tc.wantStatus, result.Code, result.Body.String())
			require.EqualValues(t, 1, pgReads, "one authoritative auth read per post-change request")
			if tc.wantStale != nil {
				require.Zero(t, l2Reads, "initial authentication must still hit B's hot L1")
			}
			if tc.wantStatus == http.StatusOK {
				assertUsageRecordsResponse(t, result, f.record.RequestID, f.record.Model)
				return
			}
			require.True(t, json.Valid(result.Body.Bytes()))
			require.Equal(t, tc.denial.code, gjson.GetBytes(result.Body.Bytes(), tc.denial.field).String(), tc.denial.field)
			if tc.denial.reason == servermiddleware.IngressRejectGroupUnassigned {
				message := gjson.GetBytes(result.Body.Bytes(), "error.message")
				require.Equal(t, gjson.String, message.Type, "error.message")
				require.NotEmpty(t, strings.TrimSpace(message.String()), "error.message")
			}
			require.Equal(t, tc.denial.reason, rejectReason)
			require.NotContains(t, result.Body.String(), f.record.RequestID)
		})
	}
}

// Run each lookup inline so its Redis and PostgreSQL reads belong to this subtest.
func waitForUsageRecordsLookup(t *testing.T, message string, lookup func(context.Context) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if lookup(ctx) {
			break
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	require.NoError(t, ctx.Err(), message)
}

func newUsageRecordsCrossInstanceFixture(t *testing.T) *usageRecordsCrossInstanceFixture {
	t.Helper()
	ctx := t.Context()
	const database = "usage_records"
	dsn, redisOptions := startUsageRecordsIntegrationServices(t, ctx, database)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var connectedDatabase string
	require.NoError(t, db.QueryRowContext(readyCtx, "SELECT current_database()").Scan(&connectedDatabase))
	require.Equal(t, database, connectedDatabase, "migrations and fixture writes must target this run's database")
	t.Logf("verified current_database()=%s before migrations", connectedDatabase)
	require.NoError(t, repository.ApplyMigrations(ctx, db))

	client := dbent.NewClient(dbent.Driver(entsql.OpenDB("postgres", db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	cfg := &config.Config{
		RunMode: config.RunModeStandard,
		JWT:     config.JWTConfig{Secret: "usage-records-integration-secret"},
		Gateway: config.GatewayConfig{MaxBodySize: 1 << 20, TextMaxBodySize: 1 << 20},
		APIKeyAuth: config.APIKeyAuthCacheConfig{
			L1Size: 128, L1TTLSeconds: 300, L2TTLSeconds: 300, JitterPercent: 0, Singleflight: true,
		},
	}
	return &usageRecordsCrossInstanceFixture{
		db: db, client: client, options: redisOptions, config: cfg,
		setting: repository.NewSettingRepository(client),
		keys:    repository.NewAPIKeyRepository(client, db),
		usage:   repository.NewUsageLogRepository(client, db),
	}
}

func startUsageRecordsIntegrationServices(t *testing.T, ctx context.Context, database string) (string, redis.Options) {
	t.Helper()
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := exec.CommandContext(probeCtx, "docker", "info").Run()
	cancel()
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("Docker unavailable in CI: %v", err)
		}
		t.Skipf("Docker unavailable: %v", err)
	}
	postgresImage := strings.TrimSpace(os.Getenv("SUB2API_TEST_POSTGRES_IMAGE"))
	if postgresImage == "" {
		postgresImage = usageRecordsIntegrationPostgresImage
	}
	redisImage := strings.TrimSpace(os.Getenv("SUB2API_TEST_REDIS_IMAGE"))
	if redisImage == "" {
		redisImage = usageRecordsIntegrationRedisImage
	}
	postgresContainer, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase(database),
		tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, postgresContainer)
	require.NoError(t, err)
	redisContainer, err := tcredis.Run(ctx, redisImage)
	testcontainers.CleanupContainer(t, redisContainer)
	require.NoError(t, err)

	dsn, err := postgresContainer.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC", "connect_timeout=5")
	require.NoError(t, err)
	addr, err := redisContainer.Endpoint(ctx, "")
	require.NoError(t, err)
	return dsn, redis.Options{Addr: addr, DialTimeout: 5 * time.Second, ContextTimeoutEnabled: true}
}

func newUsageRecordsRedisClient(t *testing.T, options redis.Options) *redis.Client {
	t.Helper()
	client := redis.NewClient(&options)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(ctx).Err())
	return client
}

func createUsageRecordsFixtureCase(t *testing.T, fixture *usageRecordsCrossInstanceFixture, codexCLIOnly bool) *usageRecordsFixtureCase {
	t.Helper()
	ctx := context.Background()
	stamp := uuid.NewString()
	// Commit the two usage rows with their keys in one transaction. Passing
	// the Ent transaction to UsageLogRepository.Create selects its single insert.
	tx, err := fixture.client.Tx(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	createUser := func(role string) int64 {
		t.Helper()
		user, err := client.User.Create().SetEmail("usage-records-" + role + "-" + stamp + "@example.invalid").
			SetPasswordHash("fixture").SetRole(service.RoleUser).SetStatus(service.StatusActive).
			SetBalance(100).SetConcurrency(5).Save(ctx)
		require.NoError(t, err)
		return user.ID
	}
	ownerID := createUser("owner")
	replacementID := createUser("replacement")
	group, err := client.Group.Create().SetName("usage-records-group-" + stamp).SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).SetSubscriptionType(service.SubscriptionTypeStandard).SetRateMultiplier(1).
		SetCodexCliOnly(codexCLIOnly).Save(ctx)
	require.NoError(t, err)
	createKey := func(name string) *service.APIKey {
		t.Helper()
		key := &service.APIKey{UserID: ownerID, GroupID: &group.ID, Key: "usage-records-" + name + "-" + stamp, Name: name, Status: service.StatusActive}
		require.NoError(t, fixture.keys.Create(txCtx, key))
		return key
	}
	target := createKey("target")
	control := createKey("control")
	account, err := client.Account.Create().SetName("usage-records-account-" + stamp).SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).SetCredentials(map[string]any{}).SetExtra(map[string]any{}).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	createRecord := func(key *service.APIKey, at time.Time) *service.UsageLog {
		t.Helper()
		model := "fixture-" + key.Name
		record := &service.UsageLog{
			UserID: ownerID, APIKeyID: key.ID, AccountID: account.ID, GroupID: &group.ID,
			RequestID: key.Name + "-" + stamp, Model: model, RequestedModel: model,
			InputTokens: 11, OutputTokens: 7, TotalCost: 1.25, ActualCost: 1.25, RateMultiplier: 1, CreatedAt: at,
		}
		inserted, err := fixture.usage.Create(txCtx, record)
		require.NoError(t, err)
		require.True(t, inserted)
		return record
	}
	at := time.Now().UTC().Add(-2 * time.Second)
	// Both keys are in the HTTP time window. The control would sort first
	// under limit=1 if the records query filtered only by owner.
	createRecord(control, at.Add(-time.Second))
	record := createRecord(target, at)
	require.NoError(t, tx.Commit())
	return &usageRecordsFixtureCase{key: target, replacementID: replacementID, record: record}
}

func newUsageRecordsCrossInstanceRouter(t *testing.T, keys *service.APIKeyService, fixture *usageRecordsCrossInstanceFixture, rdb *redis.Client, reason *servermiddleware.IngressRejectReason) *gin.Engine {
	t.Helper()
	cfg := fixture.config
	settingService := service.NewSettingService(fixture.setting, cfg)
	usageService := service.NewUsageService(fixture.usage, nil, nil, nil)
	usageHandler := handler.NewUsageHandler(usageService, keys, nil, settingService)
	handlers := &handler.Handlers{
		Gateway:       &handler.GatewayHandler{},
		OpenAIGateway: &handler.OpenAIGatewayHandler{},
		UsageRecords:  handler.NewUsageRecordsHandler(usageHandler, ratelimit.NewRateLimiter(rdb), cfg),
		AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
		BatchImage:    handler.NewBatchImageHandler(nil, nil, nil),
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	// The Anthropic group-denial envelope exposes only the permission error
	// type. Read the specific business reason after the real route finishes.
	router.Use(func(c *gin.Context) {
		c.Next()
		*reason, _ = servermiddleware.GetIngressRejectReason(c)
	})
	RegisterGatewayRoutes(router, handlers, servermiddleware.NewAPIKeyAuthMiddleware(keys, nil, cfg), keys, nil, nil, settingService, nil, cfg)
	return router
}

func serveUsageRecordsRequest(t *testing.T, router http.Handler, key string, headers map[string]string, createdAt time.Time) *httptest.ResponseRecorder {
	t.Helper()
	start := createdAt.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	end := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	req := httptest.NewRequest(http.MethodGet, "/v1/usage/records?start_time="+start+"&end_time="+end+"&limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func assertUsageRecordsResponse(t *testing.T, response *httptest.ResponseRecorder, requestID, model string) {
	t.Helper()
	var payload struct {
		Object  string           `json:"object"`
		Data    []map[string]any `json:"data"`
		HasMore *bool            `json:"has_more"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "list", payload.Object)
	require.NotNil(t, payload.HasMore)
	require.False(t, *payload.HasMore)
	require.Len(t, payload.Data, 1)
	require.Equal(t, requestID, payload.Data[0]["request_id"])
	require.Equal(t, model, payload.Data[0]["model"])
	require.NotContains(t, payload.Data[0], "user_id")
	require.NotContains(t, payload.Data[0], "api_key_id")
}

func usageRecordsAuthCacheKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}
