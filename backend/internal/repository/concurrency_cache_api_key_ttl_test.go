package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// newDedicatedAPIKeyTTLCache builds a cache whose shared slot TTL is 15 minutes
// while ordinary API key slots use the dedicated short TTL, mirroring the
// intended 120-second deployment on a smaller, test-friendly scale. The virtual
// clock starts at a fixed point so deadlines and scores stay deterministic.
func newDedicatedAPIKeyTTLCache(t *testing.T, apiKeyTTLSeconds int) (*concurrencyCache, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	server.SetTime(time.Unix(1_800_000_000, 0))
	cache, ok := NewConcurrencyCacheWithAPIKeyTTL(client, 15, 900, apiKeyTTLSeconds).(*concurrencyCache)
	require.True(t, ok, "expected Redis concurrency cache")
	return cache, server
}

func TestConcurrencyCacheAPIKeyTTLOverrideAndInherit(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// The legacy three-argument constructor keeps inheriting the shared TTL.
	legacy, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok, "expected Redis concurrency cache")
	require.Equal(t, 15*time.Minute, legacy.APIKeySlotTTL())
	require.Equal(t, 5*time.Minute, legacy.APIKeySlotRefreshInterval())

	// A dedicated positive value overrides only the API key TTL.
	dedicated, ok := NewConcurrencyCacheWithAPIKeyTTL(client, 15, 900, 120).(*concurrencyCache)
	require.True(t, ok, "expected Redis concurrency cache")
	require.Equal(t, 120*time.Second, dedicated.APIKeySlotTTL())
	require.Equal(t, 40*time.Second, dedicated.APIKeySlotRefreshInterval())
	require.Equal(t, 900, dedicated.slotTTLSeconds, "shared slot TTL must stay untouched")
	require.Equal(t, 900, dedicated.waitQueueTTLSeconds)

	// Non-positive dedicated values inherit, including the zero-value struct
	// literal shape used by existing tests and callers.
	negative, ok := NewConcurrencyCacheWithAPIKeyTTL(client, 15, 900, -5).(*concurrencyCache)
	require.True(t, ok, "expected Redis concurrency cache")
	require.Equal(t, 15*time.Minute, negative.APIKeySlotTTL(), "negative dedicated TTL must inherit")
	literal := &concurrencyCache{rdb: client, slotTTLSeconds: 900}
	require.Equal(t, 15*time.Minute, literal.APIKeySlotTTL(), "zero-value struct literal must inherit")
}

func TestAPIKeySlotDedicatedTTLAcquireRefreshAndCrashRecovery(t *testing.T) {
	cache, server := newDedicatedAPIKeyTTLCache(t, 30)
	ctx := context.Background()

	acquired, _, err := cache.AcquireAPIKeySlotWithTime(ctx, 7, 1, "holder")
	require.NoError(t, err)
	require.True(t, acquired)
	// The key EXPIRE itself must use the dedicated TTL, never the 15-minute
	// shared slot TTL and never 0.
	require.Equal(t, 30*time.Second, server.TTL(apiKeySlotKey(7)).Round(time.Second))

	// An actively renewed member survives far beyond one dedicated TTL window.
	for i := 0; i < 7; i++ {
		server.FastForward(10 * time.Second)
		ok, err := cache.RefreshAPIKeySlot(ctx, 7, "holder")
		require.NoError(t, err)
		require.True(t, ok)
	}
	counts, err := cache.GetAPIKeyConcurrencyBatch(ctx, []int64{7})
	require.NoError(t, err)
	require.Equal(t, 1, counts[7], "renewed member survives multiple dedicated TTL windows")

	// A process crash stops renewal; the next stats access reclaims the dead
	// member after the dedicated TTL, and a late refresh cannot recreate it.
	require.NoError(t, cache.TrackAPIKeySlot(ctx, 7, "crashed"))
	server.FastForward(31 * time.Second)
	ok, err := cache.RefreshAPIKeySlot(ctx, 7, "crashed")
	require.NoError(t, err)
	require.False(t, ok, "expired member must not be recreated by a late refresh")
	counts, err = cache.GetAPIKeyConcurrencyBatch(ctx, []int64{7})
	require.NoError(t, err)
	require.Zero(t, counts[7], "dead members are reclaimed by the next stats access")

	// Admission counts freed capacity after the dedicated TTL elapsed.
	acquired, _, err = cache.AcquireAPIKeySlotWithTime(ctx, 7, 1, "holder2")
	require.NoError(t, err)
	require.True(t, acquired)

	active, waiting, err := cache.GetAPIKeyQueueStats(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, 1, active)
	require.Zero(t, waiting)
}

func TestAPIKeySlotDedicatedTTLSharedClientsKeepLivePeer(t *testing.T) {
	server := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: server.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})
	now := time.Unix(1_800_000_000, 0)
	server.SetTime(now)
	cacheA, ok := NewConcurrencyCacheWithAPIKeyTTL(clientA, 15, 900, 30).(*concurrencyCache)
	require.True(t, ok, "expected Redis concurrency cache")
	cacheB, ok := NewConcurrencyCacheWithAPIKeyTTL(clientB, 15, 900, 30).(*concurrencyCache)
	require.True(t, ok, "expected Redis concurrency cache")
	ctx := context.Background()

	require.NoError(t, cacheA.TrackAPIKeySlot(ctx, 9, "live"))
	require.NoError(t, cacheA.TrackAPIKeySlot(ctx, 9, "dead"))

	// A second Redis client renews the live peer after the dead member's score
	// has aged out; only the dead member should be pruned. miniredis
	// FastForward only decrements TTLs, so advance its TIME separately.
	now = now.Add(15 * time.Second)
	server.FastForward(15 * time.Second)
	server.SetTime(now)
	ok, err := cacheB.RefreshAPIKeySlot(ctx, 9, "live")
	require.NoError(t, err)
	require.True(t, ok)
	now = now.Add(16 * time.Second)
	server.FastForward(16 * time.Second)
	server.SetTime(now)

	counts, err := cacheA.GetAPIKeyConcurrencyBatch(ctx, []int64{9})
	require.NoError(t, err)
	require.Equal(t, 1, counts[9], "live peer survives while the dead peer expires")

	ok, err = cacheB.RefreshAPIKeySlot(ctx, 9, "dead")
	require.NoError(t, err)
	require.False(t, ok, "late refresh from another client must not recreate the dead member")

	// A joining Pod's startup cleanup must not delete a live API key peer.
	require.NoError(t, cacheB.CleanupStaleProcessSlots(ctx, "joining-pod-"))
	counts, err = cacheA.GetAPIKeyConcurrencyBatch(ctx, []int64{9})
	require.NoError(t, err)
	require.Equal(t, 1, counts[9])
}

func TestAPIKeySlotDedicatedTTLSparesAccountAndUserSlots(t *testing.T) {
	cache, server := newDedicatedAPIKeyTTLCache(t, 30)
	ctx := context.Background()

	acquired, err := cache.AcquireAccountSlot(ctx, 11, 1, "acct")
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = cache.AcquireUserSlot(ctx, 12, 1, "usr")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, cache.TrackAPIKeySlot(ctx, 13, "key"))

	// Older than the dedicated API key TTL, still far inside the 15-minute
	// shared slot TTL that account/user members keep.
	server.FastForward(31 * time.Second)

	accountCount, err := cache.GetAccountConcurrency(ctx, 11)
	require.NoError(t, err)
	require.Equal(t, 1, accountCount, "account slot keeps the shared TTL")
	userCount, err := cache.GetUserConcurrency(ctx, 12)
	require.NoError(t, err)
	require.Equal(t, 1, userCount, "user slot keeps the shared TTL")

	counts, err := cache.GetAPIKeyConcurrencyBatch(ctx, []int64{13})
	require.NoError(t, err)
	require.Zero(t, counts[13], "API key stats-only member expires with the dedicated TTL")
}

func TestAPIKeyQueueDedicatedTTLAdmissionAndHandover(t *testing.T) {
	cache, server := newDedicatedAPIKeyTTLCache(t, 30)
	ctx := context.Background()

	// Deadlines come from the Redis virtual clock, not wall time.
	nowSeconds, err := cache.redisUnixSeconds(ctx)
	require.NoError(t, err)
	deadlineMs := (nowSeconds + 120) * 1000

	outcome, err := cache.AdvanceAPIKeyQueue(ctx, 5, "first", service.APIKeyQueueModeEnter, deadlineMs, 1, 20)
	require.NoError(t, err)
	require.Equal(t, service.APIKeyQueueOutcomeAcquired, outcome)
	require.Equal(t, 30*time.Second, server.TTL(apiKeySlotKey(5)).Round(time.Second),
		"queue admission EXPIRE must use the dedicated TTL")

	active, waiting, err := cache.GetAPIKeyQueueStats(ctx, 5)
	require.NoError(t, err)
	require.Equal(t, 1, active)
	require.Zero(t, waiting)

	// The dead active member is pruned with the dedicated TTL, so the next
	// queued attempt inherits the freed capacity.
	server.FastForward(31 * time.Second)
	outcome, err = cache.AdvanceAPIKeyQueue(ctx, 5, "second", service.APIKeyQueueModeEnter, deadlineMs, 1, 20)
	require.NoError(t, err)
	require.Equal(t, service.APIKeyQueueOutcomeAcquired, outcome, "expired holder frees capacity for the next attempt")

	stats, err := cache.GetAPIKeyQueueStatsBatch(ctx, []int64{5})
	require.NoError(t, err)
	require.Equal(t, 1, stats[5].Active)
	require.Zero(t, stats[5].Waiting)

	// A waiting attempt has its own deadline-based key TTL; it must survive
	// expiry of the short active-slot TTL and still be admitted afterwards.
	acquired, _, err := cache.AcquireAPIKeySlotWithTime(ctx, 6, 1, "active")
	require.NoError(t, err)
	require.True(t, acquired)
	outcome, err = cache.AdvanceAPIKeyQueue(ctx, 6, "waiter", service.APIKeyQueueModeEnter, deadlineMs, 1, 20)
	require.NoError(t, err)
	require.Equal(t, service.APIKeyQueueOutcomeWaiting, outcome)
	server.FastForward(31 * time.Second)
	outcome, err = cache.AdvanceAPIKeyQueue(ctx, 6, "waiter", service.APIKeyQueueModePoll, deadlineMs, 1, 20)
	require.NoError(t, err)
	require.Equal(t, service.APIKeyQueueOutcomeAcquired, outcome,
		"waiting attempt must not expire with the active-slot TTL")
}

func TestLiveLeaseHandoverPrunesRegularAPIKeySlotsWithDedicatedTTL(t *testing.T) {
	cache, server := newDedicatedAPIKeyTTLCache(t, 30)
	ctx := context.Background()
	nowSeconds, err := cache.redisUnixSeconds(ctx)
	require.NoError(t, err)
	now := time.Unix(nowSeconds, 0)

	seed := func(key, member string, age time.Duration) {
		require.NoError(t, cache.rdb.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(-age).Unix()), Member: member}).Err())
	}
	seed(accountSlotKey(21), "acct-src", 0)
	seed(accountSlotKey(21), "acct-dead", 31*time.Second)
	seed(userSlotKey(22), "user-src", 0)
	seed(apiKeySlotKey(23), "key-src", 0)
	seed(apiKeySlotKey(23), "key-dead", 31*time.Second)

	ok, err := cache.AcquireLiveLeaseTransferring(ctx, service.LiveLeaseTransferRequest{
		AccountID: 21,
		// Account/user max 2: the surviving "acct-dead" member legitimately
		// occupies one unit because the transfer script never prunes regular
		// account/user members.
		AccountMax:       2,
		AccountRequestID: "acct-src",
		UserID:           22,
		UserMax:          2,
		UserRequestID:    "user-src",
		APIKeyID:         23,
		APIKeyMax:        1,
		KeyRequestID:     "key-src",
		LeaseID:          "lease",
	})
	require.NoError(t, err)
	require.True(t, ok)

	require.ErrorIs(t, cache.rdb.ZScore(ctx, apiKeySlotKey(23), "key-dead").Err(), redis.Nil,
		"stale regular API key member is pruned with the dedicated TTL during the Live handoff")
	require.ErrorIs(t, cache.rdb.ZScore(ctx, apiKeySlotKey(23), "key-src").Err(), redis.Nil,
		"the transferred source member is moved into the Live lease")
	require.NoError(t, cache.rdb.ZScore(ctx, liveAPIKeySlotKey(23), "lease").Err())
	require.Equal(t, time.Duration(liveLeaseTTLSeconds)*time.Second, server.TTL(liveAPIKeySlotKey(23)).Round(time.Second),
		"Live lease keeps its own TTL")
	require.NoError(t, cache.rdb.ZScore(ctx, accountSlotKey(21), "acct-dead").Err(),
		"account regular members are not pruned with the API key dedicated TTL")

	// Direct Live admission prunes the regular API key ZSET with the dedicated
	// TTL too. Fresh account/user IDs avoid the capacity held by the lease above.
	seed(apiKeySlotKey(24), "key-dead", 31*time.Second)
	live, err := cache.AcquireLiveLease(ctx, 25, 1, 26, 1, 24, 1, "lease2", false)
	require.NoError(t, err)
	require.True(t, live)
	require.ErrorIs(t, cache.rdb.ZScore(ctx, apiKeySlotKey(24), "key-dead").Err(), redis.Nil,
		"stale regular API key member is pruned with the dedicated TTL during direct Live admission")
}

func TestProvideConcurrencyCacheAppliesDedicatedAPIKeyTTLForSharedRedis(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Gateway.ConcurrencySlotTTLMinutes = 15
	cfg.Runtime.RegionalConcurrencyFile = ""

	cfg.Gateway.APIKeySlotTTLSeconds = 0
	shared, err := ProvideConcurrencyCache(client, cfg, nil)
	require.NoError(t, err)
	sharedCache, ok := shared.(*concurrencyCache)
	require.True(t, ok, "expected Redis concurrency cache")
	require.Equal(t, 15*time.Minute, sharedCache.APIKeySlotTTL(), "default 0 inherits the shared slot TTL")

	cfg.Gateway.APIKeySlotTTLSeconds = 120
	dedicated, err := ProvideConcurrencyCache(client, cfg, nil)
	require.NoError(t, err)
	cache, ok := dedicated.(*concurrencyCache)
	require.True(t, ok, "expected Redis concurrency cache")
	require.Equal(t, 120*time.Second, cache.APIKeySlotTTL())
	require.Equal(t, 900, cache.slotTTLSeconds, "account/user slots keep the shared TTL")
	acquired, _, err := cache.AcquireAPIKeySlotWithTime(ctx, 31, 1, "wire-holder")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, 120*time.Second, server.TTL(apiKeySlotKey(31)).Round(time.Second))
}

func TestProvideConcurrencyCacheRegionalKeepsInheritedAPIKeyTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	configPath := filepath.Join(t.TempDir(), "regional-concurrency.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"Mode": "primary",
		"Instance": "instance-1",
		"Epoch": "epoch-1",
		"PrimaryInstances": ["instance-1"],
		"LegacyRedisUser": "legacy"
	}`), 0o600))

	cfg := &config.Config{}
	cfg.Gateway.ConcurrencySlotTTLMinutes = 15
	cfg.Gateway.APIKeySlotTTLSeconds = 120
	cfg.Runtime.RegionalConcurrencyFile = configPath

	mock.ExpectExec("INSERT INTO regional_runtime_members").
		WithArgs("epoch-1", "instance-1", "primary").
		WillReturnResult(sqlmock.NewResult(0, 1))

	value, err := ProvideConcurrencyCache(client, cfg, db)
	require.NoError(t, err)
	regional, ok := value.(*regionalConcurrencyCache)
	require.True(t, ok, "expected regional concurrency cache")
	base, ok := regional.ConcurrencyCache.(*concurrencyCache)
	require.True(t, ok, "expected Redis base cache")
	require.Equal(t, 15*time.Minute, base.APIKeySlotTTL(),
		"regional authority must keep the inherited base TTL, not the shared-Redis override")
	require.Equal(t, 900, base.slotTTLSeconds)
	require.NoError(t, mock.ExpectationsWereMet())
}
