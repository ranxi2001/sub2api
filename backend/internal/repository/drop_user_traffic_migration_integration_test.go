//go:build integration

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestDropUserTrafficMigration(t *testing.T) {
	tx := testTx(t)
	// Use only temporary relations so a regression cannot change shared fixtures.
	_, err := tx.Exec("SET LOCAL search_path TO pg_temp")
	require.NoError(t, err)
	for _, name := range []string{"user_traffic_events", "user_traffic_hourly", "usage_logs"} {
		_, err = tx.Exec("CREATE TEMP TABLE " + name + " AS SELECT 1 AS id")
		require.NoError(t, err)
	}
	sql, err := migrations.FS.ReadFile("398_drop_user_traffic.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.Exec(string(sql))
		require.NoError(t, err)
	}
	var removed bool
	require.NoError(t, tx.QueryRow("SELECT to_regclass('user_traffic_events') IS NULL AND to_regclass('user_traffic_hourly') IS NULL").Scan(&removed))
	require.True(t, removed)
	var id int
	require.NoError(t, tx.QueryRow("SELECT id FROM usage_logs").Scan(&id))
	require.Equal(t, 1, id)
}
