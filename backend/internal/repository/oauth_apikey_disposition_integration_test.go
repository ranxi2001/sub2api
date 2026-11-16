//go:build integration

package repository

import (
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthAPIKeyDispositionAtomicCreate(t *testing.T) {
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	key := &service.Account{Name: fmt.Sprintf("disposition-key-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1}
	require.NoError(t, repo.Create(t.Context(), key))
	p := service.DefaultOAuthAPIKeyDispositionConfig()
	p.Enabled = true
	p.AccountIDs = []int64{key.ID}
	source := &service.Account{Name: fmt.Sprintf("disposition-oauth-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, InitialAPIKeyDisposition: &p}
	t.Cleanup(func() {
		for _, id := range []int64{source.ID, key.ID} {
			_, _ = integrationDB.Exec("DELETE FROM scheduler_outbox WHERE account_id=$1", id)
			_, _ = integrationDB.Exec("DELETE FROM account_schedulable_timers WHERE account_id=$1", id)
			_, _ = integrationDB.Exec("DELETE FROM account_schedulable_timer_audit WHERE account_id=$1", id)
			_, _ = integrationDB.Exec("DELETE FROM accounts WHERE id=$1", id)
		}
	})
	// Both account and timer changes roll back with the caller-owned import transaction.
	tx, err := integrationEntClient.Tx(t.Context())
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithAccountGroups(dbent.NewTxContext(t.Context(), tx), source, nil))
	require.NoError(t, tx.Rollback())
	var count int
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM account_schedulable_timers WHERE account_id=$1", key.ID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM accounts WHERE id=$1", source.ID).Scan(&count))
	require.Zero(t, count)
	source.ID = 0
	require.NoError(t, repo.Create(t.Context(), source))
	require.NoError(t, integrationDB.QueryRow("SELECT count(*) FROM account_schedulable_timers WHERE account_id=$1 AND status='pending'", key.ID).Scan(&count))
	require.Equal(t, 2, count)
	loaded, err := repo.GetByID(t.Context(), source.ID)
	require.NoError(t, err)
	require.Nil(t, loaded.InitialAPIKeyDisposition)
	require.NoError(t, repo.Update(t.Context(), loaded))
	require.NoError(t, integrationDB.QueryRow("SELECT max(version) FROM account_schedulable_timers WHERE account_id=$1", key.ID).Scan(&count))
	require.Equal(t, 1, count)
}
