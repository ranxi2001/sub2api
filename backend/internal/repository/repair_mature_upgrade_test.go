package repository

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// The production migration set is supplied from the pinned merged Git tree,
// never from application configuration. Both instances use the real runner.
func TestRepairMatureProductionUpgradeAndRollbackBoundary(t *testing.T) {
	dsn, baselineDir := os.Getenv("MATURE_UPGRADE_DSN"), os.Getenv("MATURE_PRODUCTION_MIGRATIONS")
	if dsn == "" || baselineDir == "" {
		t.Skip("isolated upgrade DSN and pinned production migration directory required")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	productionFiles := os.DirFS(baselineDir)
	baseline := fstest.MapFS{}
	files, err := fs.Glob(productionFiles, "*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, name := range files {
		before, err := fs.ReadFile(productionFiles, name)
		require.NoError(t, err)
		after, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		require.Equal(t, before, after, "applied production migration must remain immutable: %s", name)
	}
	// Enumerate executable names/data from embed.FS only. The production archive
	// selects the previously applied subset after the equality proof above.
	embeddedFiles, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range embeddedFiles {
		if _, err := fs.Stat(productionFiles, name); err != nil {
			require.True(t, os.IsNotExist(err))
			continue
		}
		data, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		baseline[name] = &fstest.MapFile{Data: data}
	}
	require.Len(t, baseline, len(files))
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh_%t", fresh), func(t *testing.T) {
			name := fmt.Sprintf("repair_upgrade_%d", time.Now().UnixNano())
			_, err := admin.ExecContext(ctx, "CREATE DATABASE "+name)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := admin.ExecContext(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
				require.NoError(t, err)
			})
			u, err := url.Parse(dsn)
			require.NoError(t, err)
			u.Path = "/" + name
			db, err := sql.Open("postgres", u.String())
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			if !fresh {
				require.NoError(t, applyMigrationsFS(ctx, db, baseline))
			} else {
				require.NoError(t, ApplyMigrations(ctx, db))
			}
			var userID int64
			require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES('upgrade@example.invalid','synthetic',17.25) RETURNING id`).Scan(&userID))
			_, err = db.ExecContext(ctx, `INSERT INTO channels(name,description) VALUES('retained-canonical','original'); INSERT INTO accounts(name,platform,type,credentials,extra) VALUES('retained-account','openai','apikey','{"api_key":"synthetic-old"}','{"cost_multiplier":0.8}'); INSERT INTO settings(key,value) VALUES('account_ops_notifications_v1','{"enabled":false,"marker":"retained"}');`)
			require.NoError(t, err)
			// The old schema has no probe timestamp. Treat it as NULL so the
			// snapshot still detects a timestamp written to a retained account.
			snapshot := func() string {
				var raw string
				require.NoError(t, db.QueryRowContext(ctx, `SELECT jsonb_build_object('user',(SELECT to_jsonb(u) FROM users u WHERE id=$1),'channels',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM channels c),'accounts',(SELECT jsonb_agg(jsonb_build_object('domestic_route_probe_at', NULL) || to_jsonb(a) ORDER BY id) FROM accounts a),'ops',(SELECT value FROM settings WHERE key='account_ops_notifications_v1'))::text`, userID).Scan(&raw))
				return raw
			}
			before := snapshot()
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for range 2 {
				wg.Add(1)
				go func() { defer wg.Done(); errs <- ApplyMigrations(ctx, db) }()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			require.JSONEq(t, before, snapshot(), "upgrade cannot alter canonical/account_ops state")
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('mature_providers','qq_bot_jobs','user_recharge_coupons')`).Scan(&count))
			require.Equal(t, 3, count)
			// The old runner ignores additive feature tables/checksums on rollback;
			// it does not remove or reinterpret pending feature financial state.
			_, err = db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('qq_bot_config','{"enabled":false,"app_id":"synthetic"}')`)
			require.NoError(t, err)
			require.NoError(t, applyMigrationsFS(ctx, db, baseline))
			require.JSONEq(t, before, snapshot())
			var config string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='qq_bot_config'`).Scan(&config))
			require.Contains(t, config, "synthetic")
			// Old canonical writes remain possible while the feature is disabled.
			_, err = db.ExecContext(ctx, `UPDATE users SET balance=balance+1 WHERE id=$1`, userID)
			require.NoError(t, err)
			require.NoError(t, ApplyMigrations(ctx, db))
			var balance float64
			require.NoError(t, db.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, userID).Scan(&balance))
			require.Equal(t, 18.25, balance)
			// Verify an applied feature migration still rejects checksum mutation.
			mutated := fstest.MapFS{}
			all, err := fs.Glob(migrations.FS, "*.sql")
			require.NoError(t, err)
			for _, file := range all {
				raw, err := migrations.FS.ReadFile(file)
				require.NoError(t, err)
				if strings.HasPrefix(file, "345_") {
					raw = append(raw, []byte("\n-- synthetic forbidden edit "+strconv.FormatInt(userID, 10))...)
				}
				mutated[file] = &fstest.MapFile{Data: raw}
			}
			require.ErrorContains(t, applyMigrationsFS(ctx, db, mutated), "checksum mismatch")
		})
	}
}
