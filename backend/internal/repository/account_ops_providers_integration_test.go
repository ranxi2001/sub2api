//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountOpsProviderSnapshotReadsExistingCatalogWithoutCredentials(t *testing.T) {
	db, client := repository.UpstreamChannelsTestDatabase(t)
	var id int64
	require.NoError(t, db.QueryRow(`INSERT INTO mature_providers(name,site_type,base_url,status,metrics,observed_at,credential_cipher,refresh_interval_seconds) VALUES('Snapshot fixture','sub2api','https://fixture.invalid','connected','{"balance":-2.5,"currency":"USD"}',NOW(),'synthetic-cipher',300) RETURNING id`).Scan(&id))
	accounts := repository.NewAdminAccountRepository(client, db, nil)
	reader, ok := accounts.(interface {
		ReadAccountOpsProviders(context.Context, []int64) ([]service.MatureProvider, error)
	})
	require.True(t, ok)
	rows, err := reader.ReadAccountOpsProviders(context.Background(), []int64{id})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, -2.5, *rows[0].Metrics.Balance)
	require.Equal(t, "USD", rows[0].Metrics.Currency)
	require.NotNil(t, rows[0].ObservedAt)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "synthetic-cipher")
	require.Empty(t, rows[0].Account)
	require.False(t, rows[0].HasCredential)
	rows, err = reader.ReadAccountOpsProviders(context.Background(), []int64{})
	require.NoError(t, err)
	require.Empty(t, rows)
}
