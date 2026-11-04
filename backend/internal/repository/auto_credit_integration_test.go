//go:build vipcreditintegration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
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
