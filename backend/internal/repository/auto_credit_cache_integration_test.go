//go:build vipcreditintegration

package repository

import (
	"context"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"fmt"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
)

func TestVipCreditRedisFailureRecoveryAndLatePublisher(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	configureCredit(t, db, uid, "1", "2", "4")
	addr := os.Getenv("VIP_CREDIT_TEST_REDIS_ADDR")
	require.NotEmpty(t, addr, "explicit disposable Redis required")
	admin := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = admin.Close() })
	require.NoError(t, admin.Ping(ctx).Err())
	username := fmt.Sprintf("credit-fixture-%d", uid)
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "on", ">fixture-only", "~*", "+@all", "-del").Err())
	t.Cleanup(func() { _ = admin.Do(context.Background(), "ACL", "DELUSER", username).Err() })
	rdb := redis.NewClient(&redis.Options{Addr: addr, Username: username, Password: "fixture-only", MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewBillingCache(rdb)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	svc := service.NewBillingCacheService(cache, NewUserRepository(client, db), nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)
	keys := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})
	keys.SetAutoCredit(NewAutoCreditRepository(db), svc)
	issued, err := NewAutoCreditRepository(db).Evaluate(ctx, uid)
	require.NoError(t, err)
	require.True(t, issued.Issued)
	require.NoError(t, cache.SetUserBalance(ctx, uid, -3))
	err = svc.RecoverAutoCreditInvalidations(ctx)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "NOPERM"), err)
	var stage int
	require.NoError(t, db.QueryRow(`SELECT stage FROM auto_credit_cache_outbox WHERE user_id=$1`, uid).Scan(&stage))
	require.Zero(t, stage, "failed Redis invalidation cannot ack durable receipt")
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "+del").Err())
	require.NoError(t, svc.RecoverAutoCreditInvalidations(ctx))
	require.NoError(t, cache.SetUserBalance(ctx, uid, -5)) // delayed precommit publisher between passes
	_, err = db.Exec(`UPDATE auto_credit_cache_outbox SET available_at=NOW() WHERE user_id=$1`, uid)
	require.NoError(t, err)
	require.NoError(t, svc.RecoverAutoCreditInvalidations(ctx))
	_, err = cache.GetUserBalance(ctx, uid)
	require.ErrorIs(t, err, redis.Nil)
	require.NoError(t, cache.SetUserBalance(ctx, uid, -9)) // arbitrarily late publisher after the second pass
	balance, err := svc.GetUserBalance(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, float64(2), balance, "VIP admission must use authoritative DB even after a late stale cache publisher")
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auto_credit_ledger WHERE user_id=$1`, uid).Scan(&count))
	require.Equal(t, 1, count, "cache retries must not issue money")
}
