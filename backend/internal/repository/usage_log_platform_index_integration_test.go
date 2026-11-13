//go:build integration

package repository

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestUsagePlatformIndexRetryRebuildsInvalidRemnant(t *testing.T) {
	ctx := context.Background()
	_ = testEntClient(t)
	var originalOID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT indexrelid FROM pg_index WHERE indexrelid=$1::regclass", usageLogsPlatformIndex).Scan(&originalOID))
	// Reproduce the catalog state left by an interrupted concurrent build in
	// this disposable test database. The real runner must drop and rebuild it.
	_, err := integrationDB.ExecContext(ctx, "UPDATE pg_index SET indisvalid=false WHERE indexrelid=$1::oid", originalOID)
	require.NoError(t, err)
	invalid, err := indexIsInvalid(ctx, integrationDB, usageLogsPlatformIndex)
	require.NoError(t, err)
	require.True(t, invalid)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM schema_migrations WHERE filename=$1", usageLogsPlatformIndexMigration)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile(usageLogsPlatformIndexMigration)
	require.NoError(t, err)
	require.NoError(t, applyMigrationsFS(ctx, integrationDB, fstest.MapFS{
		usageLogsPlatformIndexMigration: &fstest.MapFile{Data: migration},
	}))
	var rebuiltOID int64
	var valid bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT indexrelid,indisvalid FROM pg_index WHERE indexrelid=$1::regclass", usageLogsPlatformIndex).Scan(&rebuiltOID, &valid))
	require.True(t, valid, "retry must leave a usable index")
	require.NotEqual(t, originalOID, rebuiltOID, "IF NOT EXISTS must not retain the invalid remnant")
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE filename=$1", usageLogsPlatformIndexMigration).Scan(&count))
	require.Equal(t, 1, count)
}
