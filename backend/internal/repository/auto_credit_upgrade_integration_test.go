//go:build vipcreditintegration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestVipCreditUpgradeFromBaselineAndFirstDraftPreservesLedger(t *testing.T) {
	ctx := context.Background()
	host := creditDB(t)
	name := fmt.Sprintf("vip_credit_upgrade_%d", time.Now().UnixNano())
	_, err := host.ExecContext(ctx, `CREATE DATABASE `+pq.QuoteIdentifier(name)+` ENCODING 'UTF8' TEMPLATE template0`)
	require.NoError(t, err)
	dsn := os.Getenv("VIP_CREDIT_TEST_DSN")
	if strings.Contains(dsn, "://") {
		u, e := url.Parse(dsn)
		require.NoError(t, e)
		u.Path = "/" + name
		dsn = u.String()
	} else {
		dsn += " dbname=" + name
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
		_, e := host.ExecContext(context.Background(), `DROP DATABASE `+pq.QuoteIdentifier(name))
		require.NoError(t, e)
	})
	base := fstest.MapFS{}
	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") && e.Name() < "328" {
			raw, x := migrations.FS.ReadFile(e.Name())
			require.NoError(t, x)
			base[e.Name()] = &fstest.MapFile{Data: raw}
		}
	}
	require.NoError(t, applyMigrationsFS(ctx, db, base))
	uid := creditUser(t, db, "0")
	raw, err := migrations.FS.ReadFile("328_vip_auto_credit.sql")
	require.NoError(t, err)
	base["328_vip_auto_credit.sql"] = &fstest.MapFile{Data: raw}
	require.NoError(t, applyMigrationsFS(ctx, db, base))
	_, err = db.ExecContext(ctx, `INSERT INTO auto_credit_config(user_id,enabled,threshold,amount,cap,version) VALUES($1,true,0,2,4,1);`, uid)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO auto_credit_ledger(user_id,epoch,trigger_identity,amount,balance_before,balance_after) VALUES($1,0,'historical-draft',2,0,2)`, uid)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE users SET balance=2 WHERE id=$1`, uid)
	require.NoError(t, err)
	require.NoError(t, ApplyMigrations(ctx, db))
	require.NoError(t, ApplyMigrations(ctx, db))
	s, err := NewAutoCreditRepository(db).Get(ctx, uid, "all")
	require.NoError(t, err)
	require.False(t, s.Config.Enabled)
	require.Nil(t, s.Config.Threshold)
	require.EqualValues(t, 2, s.Config.Version)
	require.Equal(t, "2.00000000", s.Outstanding)
	require.Len(t, s.Entries, 1)
	var balance, total string
	var orders int
	require.NoError(t, db.QueryRow(`SELECT balance::text,total_recharged::text FROM users WHERE id=$1`, uid).Scan(&balance, &total))
	require.Equal(t, "2.00000000", balance)
	require.Equal(t, "0.00000000", total)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM payment_orders WHERE user_id=$1`, uid).Scan(&orders))
	require.Zero(t, orders)
	_, err = db.ExecContext(ctx, `UPDATE auto_credit_config SET threshold=0 WHERE user_id=$1`, uid)
	require.Error(t, err, "forward DB constraint rejects exact zero even on direct SQL")
}
