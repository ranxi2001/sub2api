//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration380QQBotSidebarDefaultHidden(t *testing.T) {
	migration, err := dbmigrations.FS.ReadFile("380_qq_bot_sidebar_default_hidden.sql")
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"show all", `[]`, `["/admin/qqbot/overview"]`},
		{"custom selection", `["/admin/token-guard","/admin/future-entry"]`, `["/admin/token-guard","/admin/future-entry","/admin/qqbot/overview"]`},
		{"already hidden", `["/admin/qqbot/overview"]`, `["/admin/qqbot/overview"]`},
		{"invalid JSON", `invalid`, `invalid`},
		{"blank", ``, ``},
		{"null", `null`, `null`},
		{"non-array", `{}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()
			_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('smart_ops_hidden_menu_items', $1)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, tc.raw)
			require.NoError(t, err)
			for range 2 {
				_, err = tx.ExecContext(ctx, string(migration))
				require.NoError(t, err)
				var actual string
				require.NoError(t, tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'smart_ops_hidden_menu_items'`).Scan(&actual))
				if tc.want == "invalid" || tc.want == "" {
					require.Equal(t, tc.want, actual)
				} else {
					require.JSONEq(t, tc.want, actual)
				}
			}
		})
	}
	t.Run("missing preferences", func(t *testing.T) {
		tx := testTx(t)
		ctx := context.Background()
		_, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = 'smart_ops_hidden_menu_items'`)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
		var count int
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM settings WHERE key = 'smart_ops_hidden_menu_items'`).Scan(&count))
		require.Zero(t, count, "unconfigured sites keep using application defaults")
	})
}
