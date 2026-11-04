//go:build vipcreditintegration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
)

func creditDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("VIP_CREDIT_TEST_DSN")
	require.NotEmpty(t, dsn, "explicit disposable PostgreSQL DSN is required; tests must never silently skip")
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, ApplyMigrations(context.Background(), db))
	return db
}

func creditUser(t *testing.T, db *sql.DB, balance string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash,balance) VALUES($1,'test-only',$2::numeric) RETURNING id`, fmt.Sprintf("credit-%d@example.invalid", time.Now().UnixNano()), balance).Scan(&id))
	return id
}

func TestVipCreditFirstVertical(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	actor := creditUser(t, db, "0")
	repo := NewAutoCreditRepository(db)
	state, err := repo.Get(ctx, uid, "all")
	require.NoError(t, err)
	require.False(t, state.Config.Enabled)
	require.EqualValues(t, 0, state.Config.Version)
	patch := service.AutoCreditPatch{Version: 0, Enabled: boolPtr(true), Threshold: creditJSON(`"1.00000001"`), Amount: creditJSON(`"10.12345678"`), Cap: creditJSON(`"20.24691356"`)}
	_, err = repo.Save(ctx, uid, actor, patch)
	require.NoError(t, err)
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, "0.00000000", balance, "save must not issue")
	result, err := repo.Evaluate(ctx, uid)
	require.NoError(t, err)
	require.True(t, result.Issued)
	state, err = repo.Get(ctx, uid, "pending")
	require.NoError(t, err)
	require.Equal(t, "10.12345678", state.Outstanding)
	require.Len(t, state.Entries, 1)
	entry := state.Entries[0]
	require.Equal(t, "auto_credit", entry.Source)
	require.Equal(t, "自动充值需索取费用", entry.Note)
	require.Equal(t, "0.00000000", entry.BalanceBefore)
	require.Equal(t, "10.12345678", entry.BalanceAfter)
	result, err = repo.Evaluate(ctx, uid)
	require.NoError(t, err)
	require.False(t, result.Issued)
	_, err = repo.Settle(ctx, uid, entry.ID, actor)
	require.NoError(t, err)
	_, err = repo.Settle(ctx, uid, entry.ID, actor)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, "10.12345678", balance, "settlement must never add balance")
	state, err = repo.Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Equal(t, "0.00000000", state.Outstanding)
	require.Equal(t, "settled", state.Entries[0].Status)
	var audits int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auto_credit_audit WHERE user_id=$1`, uid).Scan(&audits))
	require.Equal(t, 3, audits)
}

func boolPtr(v bool) *bool       { return &v }
func creditJSON(s string) []byte { return []byte(s) }

func configureCredit(t *testing.T, db *sql.DB, uid int64, threshold, amount, cap string) {
	t.Helper()
	_, err := NewAutoCreditRepository(db).Save(context.Background(), uid, uid, service.AutoCreditPatch{Enabled: boolPtr(true), Threshold: creditJSON(`"` + threshold + `"`), Amount: creditJSON(`"` + amount + `"`), Cap: creditJSON(`"` + cap + `"`)})
	require.NoError(t, err)
}
func TestVipCreditRealDebitEpochAndConcurrency(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	configureCredit(t, db, uid, "100", "2.00000001", "4.00000002")
	// Each worker owns an independent repository/transaction. Database row locks
	// and uniqueness, not a shared Go instance, must serialize the operation.
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := NewAutoCreditRepository(db).Evaluate(ctx, uid); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	state, err := NewAutoCreditRepository(db).Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Len(t, state.Entries, 1)
	cmd := &service.UsageBillingCommand{RequestID: fmt.Sprintf("vip-debit-%d", uid), APIKeyID: uid, UserID: uid, BalanceCost: 0.00000001}
	applied, err := NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, applied.Applied)
	applied, err = NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, applied.Applied)
	state, err = NewAutoCreditRepository(db).Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Len(t, state.Entries, 2, "a genuine debit must open exactly one new credit epoch")
	require.Equal(t, "4.00000002", state.Outstanding)
	cmd.RequestID += "-next"
	_, err = NewUsageBillingRepository(nil, db).Apply(ctx, cmd)
	require.NoError(t, err)
	state, err = NewAutoCreditRepository(db).Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Len(t, state.Entries, 2)
	require.True(t, state.CapBlocked)
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, "4.00000000", balance)
}

func TestVipCreditValidationVersionClearAndIsolation(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0.00000001")
	other := creditUser(t, db, "0")
	repo := NewAutoCreditRepository(db)
	for _, raw := range []string{`0`, `"-1"`, `"0"`, `"0.000000001"`, `"1000000.00000001"`, `"NaN"`, `"1e2"`, `"01"`} {
		_, err := repo.Save(ctx, uid, uid, service.AutoCreditPatch{Amount: creditJSON(raw)})
		require.ErrorIs(t, err, service.ErrAutoCreditInvalid, raw)
	}
	configureCredit(t, db, uid, "0.00000001", "1", "1")
	result, err := repo.Evaluate(ctx, uid)
	require.NoError(t, err)
	require.False(t, result.Issued, "balance equals threshold; boundary is strict")
	_, err = repo.Save(ctx, uid, uid, service.AutoCreditPatch{Version: 0, Enabled: boolPtr(false)})
	require.ErrorIs(t, err, service.ErrAutoCreditConflict)
	state, err := repo.Save(ctx, uid, uid, service.AutoCreditPatch{Version: 1, Threshold: creditJSON(`"0.00000002"`)})
	require.NoError(t, err)
	require.Equal(t, "1.00000000", *state.Config.Amount)
	_, err = repo.Evaluate(ctx, uid)
	require.NoError(t, err)
	state, err = repo.Save(ctx, uid, uid, service.AutoCreditPatch{Version: 2, Enabled: boolPtr(false), Threshold: creditJSON(`null`), Amount: creditJSON(`null`), Cap: creditJSON(`null`)})
	require.NoError(t, err)
	require.Nil(t, state.Config.Amount)
	require.Equal(t, "1.00000000", state.Outstanding)
	require.Len(t, state.Entries, 1)
	_, err = repo.Settle(ctx, other, state.Entries[0].ID, other)
	require.ErrorIs(t, err, service.ErrAutoCreditInvalid)
	isolated, err := repo.Get(ctx, other, "all")
	require.NoError(t, err)
	require.Empty(t, isolated.Entries)
	require.Equal(t, "0.00000000", isolated.Outstanding)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := NewAutoCreditRepository(db).Settle(ctx, uid, state.Entries[0].ID, uid)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auto_credit_audit WHERE user_id=$1 AND action='settle'`, uid).Scan(&n))
	require.Equal(t, 1, n)
}

func TestVipCreditActualBalanceAdmission(t *testing.T) {
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	configureCredit(t, db, uid, "1", "5", "10")
	var keyid int64
	credential := fmt.Sprintf("sk-vip-admission-test-%d", uid)
	require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'credit-fixture') RETURNING id`, uid, credential).Scan(&keyid))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	cfg := &config.Config{}
	keys := service.NewAPIKeyService(NewAPIKeyRepository(client, db), NewUserRepository(client, db), NewGroupRepository(client, db), nil, NewUserGroupRateRepository(db), nil, cfg)
	keys.SetAutoCredit(NewAutoCreditRepository(db), nil)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)))
	engine.POST("/v1/messages", func(c *gin.Context) { c.Status(200) })
	request := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.Header.Set("Authorization", "Bearer "+credential)
		engine.ServeHTTP(w, r)
		return w.Code
	}
	require.Equal(t, 200, request(), "real middleware+repository must recover zero balance")
	state, err := NewAutoCreditRepository(db).Get(context.Background(), uid, "all")
	require.NoError(t, err)
	require.Len(t, state.Entries, 1)
	_, err = db.Exec(`UPDATE users SET status='disabled',balance=0 WHERE id=$1`, uid)
	require.NoError(t, err)
	require.Equal(t, 401, request())
	state, err = NewAutoCreditRepository(db).Get(context.Background(), uid, "all")
	require.NoError(t, err)
	require.Len(t, state.Entries, 1)
}

func TestVipCreditRollbackAndCacheOutbox(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	configureCredit(t, db, uid, "10", "2", "4")
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	b, s, err := lockAutoCreditUser(ctx, tx, uid)
	require.NoError(t, err)
	result, err := evaluateAutoCredit(ctx, tx, uid, b, s)
	require.NoError(t, err)
	require.True(t, result.Issued)
	require.NoError(t, tx.Rollback())
	state, err := NewAutoCreditRepository(db).Get(ctx, uid, "all")
	require.NoError(t, err)
	require.Empty(t, state.Entries)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auto_credit_cache_outbox WHERE user_id=$1`, uid).Scan(&n))
	require.Zero(t, n)
	_, err = NewAutoCreditRepository(db).Evaluate(ctx, uid)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM auto_credit_cache_outbox WHERE user_id=$1`, uid).Scan(&n))
	require.Equal(t, 1, n)
}

func TestVipCreditZeroThresholdInvalid(t *testing.T) {
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	repo := NewAutoCreditRepository(db)
	for _, v := range []string{`"0"`, `"0.00000000"`} {
		_, err := repo.Save(context.Background(), uid, uid, service.AutoCreditPatch{Threshold: creditJSON(v)})
		require.ErrorIs(t, err, service.ErrAutoCreditInvalid, "threshold must be strictly positive, including exact zero: %s", v)
	}
}

func TestVipCreditConcurrentAdmissionAndUsage(t *testing.T) {
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	configureCredit(t, db, uid, "100", "1", "100")
	var key int64
	require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'concurrent') RETURNING id`, uid, fmt.Sprintf("sk-concurrent-credit-%d", uid)).Scan(&key))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, e := NewAutoCreditRepository(db).EvaluateAdmission(ctx, uid, key)
			errs <- e
		}()
		go func(i int) {
			defer wg.Done()
			_, e := NewUsageBillingRepository(nil, db).Apply(ctx, &service.UsageBillingCommand{RequestID: fmt.Sprintf("concurrent:%d:%d", uid, i), UserID: uid, APIKeyID: key, BalanceCost: 0.01, APIKeyQuotaCost: 0.01})
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var count int
	var balance, owed string
	require.NoError(t, db.QueryRow(`SELECT count(*),sum(amount)::text FROM auto_credit_ledger WHERE user_id=$1`, uid).Scan(&count, &owed))
	require.Contains(t, []int{20, 21}, count)
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, decimal.RequireFromString(owed).Sub(decimal.RequireFromString("0.2")).StringFixed(8), balance)
}
func TestVipCreditHistoryPagination(t *testing.T) {
	db := creditDB(t)
	uid := creditUser(t, db, "0")
	repo := NewAutoCreditRepository(db)
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO auto_credit_ledger(user_id,epoch,trigger_identity,amount,balance_before,balance_after) SELECT $1,n,'pagination-fixture',1,n-1,n FROM generate_series(1,205)n`, uid)
	require.NoError(t, err)
	seen := map[int64]bool{}
	before := int64(0)
	for {
		s, e := repo.GetPage(ctx, uid, "all", before)
		require.NoError(t, e)
		require.LessOrEqual(t, len(s.Entries), 50)
		require.Equal(t, "205.00000000", s.Outstanding)
		for _, entry := range s.Entries {
			require.False(t, seen[entry.ID])
			seen[entry.ID] = true
		}
		if !s.HasMore {
			break
		}
		require.Positive(t, s.NextBeforeID)
		before = s.NextBeforeID
	}
	require.Len(t, seen, 205)
}

func TestVipCreditRecoveryMustReachMinimumReserve(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	repo := NewAutoCreditRepository(db)
	uid := creditUser(t, db, "0.005")
	yes, err := repo.CanRecover(ctx, uid, "0.01")
	require.NoError(t, err)
	require.False(t, yes, "a positive non-VIP balance below reserve is unusable")
	configureCredit(t, db, uid, "1", "0.00499999", "1")
	yes, err = repo.CanRecover(ctx, uid, "0.01")
	require.NoError(t, err)
	require.False(t, yes, "credit that remains below reserve cannot shadow subscription")
	_, err = repo.Save(ctx, uid, uid, service.AutoCreditPatch{Version: 1, Amount: creditJSON(`"0.005"`)})
	require.NoError(t, err)
	yes, err = repo.CanRecover(ctx, uid, "0.01")
	require.NoError(t, err)
	require.True(t, yes, "exact reserve boundary is usable")
}

func TestVipCreditGoogleRecoveryAndRegionalNoAuthority(t *testing.T) {
	ctx := context.Background()
	db := creditDB(t)
	for _, regional := range []bool{false, true} {
		uid := creditUser(t, db, "0")
		configureCredit(t, db, uid, "1", "2", "4")
		credential := fmt.Sprintf("sk-vip-google-%d", uid)
		var key int64
		require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'google') RETURNING id`, uid, credential).Scan(&key))
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		cfg := &config.Config{}
		if regional {
			cfg.Runtime.Role = config.RuntimeRoleGateway
			cfg.Runtime.RegionalAuthFile = "replica-fixture-no-authority"
		}
		keys := service.NewAPIKeyService(NewAPIKeyRepository(client, db), NewUserRepository(client, db), NewGroupRepository(client, db), nil, NewUserGroupRateRepository(db), nil, cfg)
		keys.SetAutoCredit(NewAutoCreditRepository(db), nil)
		if regional {
			ok, err := keys.RecoverAutoCreditAdmission(ctx, &service.APIKey{ID: key, UserID: uid, Status: service.StatusActive, User: &service.User{ID: uid, Status: service.StatusActive}})
			require.NoError(t, err)
			require.False(t, ok)
		} else {
			r := gin.New()
			r.Use(middleware.APIKeyAuthGoogle(keys, cfg))
			r.POST("/v1beta/models/gemini:generateContent", func(c *gin.Context) { c.Status(200) })
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1beta/models/gemini:generateContent", nil)
			req.Header.Set("x-goog-api-key", credential)
			r.ServeHTTP(w, req)
			require.Equal(t, 200, w.Code, w.Body.String())
		}
		state, err := NewAutoCreditRepository(db).Get(ctx, uid, "all")
		require.NoError(t, err)
		if regional {
			require.Empty(t, state.Entries)
			var balance string
			require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
			require.Equal(t, "0.00000000", balance)
		} else {
			require.Len(t, state.Entries, 1)
		}
	}
}
