//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The barriers are outside the production implementation. They exercise real
// service calls and independent PostgreSQL transactions without timing sleeps.
type gatewayAtomicBarrierRepo struct {
	service.AdminAccountRepository
	atomic      service.GatewayAccountTransactionRepository
	beforeLock  bool
	failBinding bool
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	staged      *service.Account
}

func (r *gatewayAtomicBarrierRepo) wait(ctx context.Context) error {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *gatewayAtomicBarrierRepo) WithinGatewayAccountTransaction(ctx context.Context, id int64, groups []int64, run func(context.Context) error) error {
	if r.beforeLock {
		if err := r.wait(ctx); err != nil {
			return err
		}
	}
	return r.atomic.WithinGatewayAccountTransaction(ctx, id, groups, run)
}

func (r *gatewayAtomicBarrierRepo) BindGroups(ctx context.Context, id int64, groups []int64) error {
	if r.failBinding {
		var err error
		r.staged, err = r.AdminAccountRepository.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if err := r.wait(ctx); err != nil {
			return err
		}
		return service.ErrGatewayAccountGroup
	}
	return r.AdminAccountRepository.BindGroups(ctx, id, groups)
}

type gatewayAtomicResult struct {
	err  error
	bulk *service.BulkUpdateAccountsResult
}

func gatewayAtomicFixture(t *testing.T) (service.AdminAccountRepository, service.AdminGroupRepository, *service.Account, int64) {
	t.Helper()
	ctx := t.Context()
	repo := NewAdminAccountRepository(integrationEntClient, integrationDB, nil)
	groups := NewAdminGroupRepository(integrationEntClient, integrationDB)
	stamp := time.Now().UnixNano()
	oldGroup, err := integrationEntClient.Group.Create().SetName(fmt.Sprintf("gateway-atomic-old-%d", stamp)).SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	target, err := integrationEntClient.Group.Create().SetName(fmt.Sprintf("gateway-atomic-target-%d", stamp)).SetPlatform(service.PlatformAnthropic).Save(ctx)
	require.NoError(t, err)
	a := &service.Account{Name: fmt.Sprintf("gateway-atomic-%d", stamp), Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
		Status: service.StatusActive, Concurrency: 2, Priority: 10,
		Credentials: map[string]any{"upstream_kind": "new_api", "base_url": "https://gateway.example", "api_key": "original-synthetic-key"},
		Extra:       map[string]any{service.AccountCostMultiplierExtraKey: 0.3, "quota_used": 12.0}}
	require.NoError(t, repo.CreateWithAccountGroups(ctx, a, []service.AccountGroup{{GroupID: oldGroup.ID, Priority: 37, AllowedModels: []string{"fixture-model"}}}))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1 OR group_id IN ($2,$3)`, a.ID, oldGroup.ID, target.ID)
		_, _ = integrationDB.Exec(`DELETE FROM account_groups WHERE account_id=$1`, a.ID)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, a.ID)
		_, _ = integrationDB.Exec(`DELETE FROM groups WHERE id IN ($1,$2)`, oldGroup.ID, target.ID)
	})
	return repo, groups, a, target.ID
}

func gatewayAtomicWrite(ctx context.Context, route string, repo service.AdminAccountRepository, groups service.AdminGroupRepository, id, target int64) gatewayAtomicResult {
	ids := []int64{target}
	credentials := map[string]any{"api_key": "replacement-synthetic-key"}
	name := "gateway-atomic-edited"
	if route == "account-service" {
		_, err := service.NewAccountService(repo, groups).Update(ctx, id, service.UpdateAccountRequest{Name: &name, Credentials: &credentials, GroupIDs: &ids})
		return gatewayAtomicResult{err: err}
	}
	admin := service.NewAdminService(nil, nil, groups, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if route == "bulk" {
		result, err := admin.BulkUpdateAccounts(ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{id}, Name: name, Credentials: credentials, GroupIDs: &ids, SkipMixedChannelCheck: true})
		return gatewayAtomicResult{err: err, bulk: result}
	}
	// Platform is intentionally omitted: this was the reviewed partial-write path.
	_, err := admin.UpdateAccount(ctx, id, &service.UpdateAccountInput{Name: name, Credentials: credentials, GroupIDs: &ids, SkipMixedChannelCheck: true})
	return gatewayAtomicResult{err: err}
}

func gatewayAtomicAssertUnchanged(t *testing.T, repo service.AccountRepository, before *service.Account) {
	t.Helper()
	after, err := repo.GetByID(t.Context(), before.ID)
	require.NoError(t, err)
	require.Equal(t, before.Name, after.Name)
	require.Equal(t, before.Credentials, after.Credentials)
	require.Equal(t, before.RateMultiplier, after.RateMultiplier)
	require.Equal(t, before.Extra, after.Extra)
	require.Equal(t, before.Status, after.Status)
	require.Equal(t, before.Schedulable, after.Schedulable)
	require.Equal(t, before.GroupIDs, after.GroupIDs)
	for i := range before.AccountGroups {
		require.Equal(t, before.AccountGroups[i].Priority, after.AccountGroups[i].Priority)
		require.Equal(t, before.AccountGroups[i].AllowedModels, after.AccountGroups[i].AllowedModels)
	}
}

func TestGatewayAtomicUpdateOAuthInterleaving(t *testing.T) {
	for _, route := range []string{"admin", "account-service", "bulk"} {
		t.Run(route, func(t *testing.T) {
			repo, groups, a, target := gatewayAtomicFixture(t)
			barrier := &gatewayAtomicBarrierRepo{AdminAccountRepository: repo, atomic: repo.(service.GatewayAccountTransactionRepository), beforeLock: true,
				entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			done := make(chan gatewayAtomicResult, 1)
			go func() { done <- gatewayAtomicWrite(ctx, route, barrier, groups, a.ID, target) }()
			select {
			case <-barrier.entered:
			case <-ctx.Done():
				t.Fatal("update did not reach the post-precheck transaction barrier")
			}
			group, err := groups.GetByID(ctx, target)
			require.NoError(t, err)
			group.RequireOAuthOnly = true
			require.NoError(t, groups.Update(ctx, group), "independent group transaction wins after the service precheck")
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET rate_multiplier=2.5,extra=extra||'{"quota_used":19,"cost_multiplier":0.7}'::jsonb WHERE id=$1`, a.ID)
			require.NoError(t, err)
			before, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			close(barrier.release)
			select {
			case result := <-done:
				if route == "bulk" {
					require.NoError(t, result.err)
					require.NotNil(t, result.bulk)
					require.Equal(t, []int64{a.ID}, result.bulk.FailedIDs)
					require.Empty(t, result.bulk.SuccessIDs)
				} else {
					require.ErrorIs(t, result.err, service.ErrGatewayAccountGroup)
				}
			case <-ctx.Done():
				t.Fatal("update did not finish")
			}
			gatewayAtomicAssertUnchanged(t, repo, before)
		})
	}
}

func TestGatewayAtomicUpdateRollbackAfterStagedFields(t *testing.T) {
	for _, route := range []string{"admin", "account-service", "bulk"} {
		t.Run(route, func(t *testing.T) {
			repo, groups, a, target := gatewayAtomicFixture(t)
			before, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			var events int
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&events))
			barrier := &gatewayAtomicBarrierRepo{AdminAccountRepository: repo, atomic: repo.(service.GatewayAccountTransactionRepository), failBinding: true,
				entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			done := make(chan gatewayAtomicResult, 1)
			go func() { done <- gatewayAtomicWrite(ctx, route, barrier, groups, a.ID, target) }()
			select {
			case <-barrier.entered:
			case <-ctx.Done():
				t.Fatal("update did not reach binding after staging fields")
			}
			require.Equal(t, "gateway-atomic-edited", barrier.staged.Name)
			require.Equal(t, "replacement-synthetic-key", barrier.staged.Credentials["api_key"])
			gatewayAtomicAssertUnchanged(t, repo, before) // independent connection cannot see staged writes
			close(barrier.release)
			select {
			case result := <-done:
				if route == "bulk" {
					require.NoError(t, result.err)
					require.NotNil(t, result.bulk)
					require.Equal(t, []int64{a.ID}, result.bulk.FailedIDs)
				} else {
					require.ErrorIs(t, result.err, service.ErrGatewayAccountGroup)
				}
			case <-ctx.Done():
				t.Fatal("rollback did not finish")
			}
			gatewayAtomicAssertUnchanged(t, repo, before)
			var afterEvents int
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, a.ID).Scan(&afterEvents))
			require.Equal(t, events, afterEvents, "failed writes must not publish outbox events")
		})
	}
}

func TestGatewayAtomicUpdateSuccessfulCommit(t *testing.T) {
	for _, route := range []string{"admin", "account-service", "bulk"} {
		t.Run(route, func(t *testing.T) {
			repo, groups, a, target := gatewayAtomicFixture(t)
			barrier := &gatewayAtomicBarrierRepo{AdminAccountRepository: repo, atomic: repo.(service.GatewayAccountTransactionRepository), beforeLock: true,
				entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			done := make(chan gatewayAtomicResult, 1)
			go func() { done <- gatewayAtomicWrite(ctx, route, barrier, groups, a.ID, target) }()
			select {
			case <-barrier.entered:
			case <-ctx.Done():
				t.Fatal("update did not reach the pre-lock billing-state barrier")
			}
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET rate_multiplier=2.5,extra=extra||'{"quota_used":19,"cost_multiplier":0.7}'::jsonb WHERE id=$1`, a.ID)
			require.NoError(t, err)
			close(barrier.release)
			var result gatewayAtomicResult
			select {
			case result = <-done:
			case <-ctx.Done():
				t.Fatal("atomic commit did not finish")
			}
			require.NoError(t, result.err)
			if route == "bulk" {
				require.NotNil(t, result.bulk)
				require.Equal(t, []int64{a.ID}, result.bulk.SuccessIDs)
			}
			after, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.Equal(t, "gateway-atomic-edited", after.Name)
			require.Equal(t, "replacement-synthetic-key", after.Credentials["api_key"])
			require.Equal(t, []int64{target}, after.GroupIDs)
			require.NotNil(t, after.RateMultiplier)
			require.Equal(t, 2.5, *after.RateMultiplier, "omitted request rate must retain the latest committed value")
			require.Equal(t, 19.0, after.Extra["quota_used"])
			require.Equal(t, 0.7, after.CostMultiplier())
			require.False(t, after.IsSchedulable())
		})
	}
}

func TestGatewayAtomicUpdateCallerTransactionRollback(t *testing.T) {
	for _, route := range []string{"admin", "account-service", "bulk"} {
		t.Run(route, func(t *testing.T) {
			repo, groups, a, target := gatewayAtomicFixture(t)
			before, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			barrier := &gatewayAtomicBarrierRepo{AdminAccountRepository: repo, atomic: repo.(service.GatewayAccountTransactionRepository), failBinding: true,
				entered: make(chan struct{}), release: make(chan struct{})}
			close(barrier.release)
			tx, err := integrationEntClient.Tx(t.Context())
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			result := gatewayAtomicWrite(dbent.NewTxContext(t.Context(), tx), route, barrier, groups, a.ID, target)
			if route == "bulk" {
				require.NoError(t, result.err)
				require.NotNil(t, result.bulk)
				require.Equal(t, []int64{a.ID}, result.bulk.FailedIDs)
			} else {
				require.ErrorIs(t, result.err, service.ErrGatewayAccountGroup)
			}
			// A caller can commit other successful work without committing this
			// failed item's fields or outbox. The savepoint owns item rollback.
			require.NoError(t, tx.Commit())
			gatewayAtomicAssertUnchanged(t, repo, before)
		})
	}
}
