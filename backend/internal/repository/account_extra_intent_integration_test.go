//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Return A's real, stale service snapshot only after B has committed its edit.
// No outer test transaction or sleep can hide the read-before-write ordering.
type accountExtraIntentReadRepo struct {
	service.AdminAccountRepository
	afterRead func(context.Context, *service.Account) error
}

func (r *accountExtraIntentReadRepo) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	account, err := r.AdminAccountRepository.GetByID(ctx, id)
	if err == nil && r.afterRead != nil {
		hook := r.afterRead
		r.afterRead = nil
		err = hook(ctx, account)
	}
	return account, err
}

func accountExtraIntentAdmin(repo service.AdminAccountRepository) service.AdminService {
	return service.NewAdminService(&config.Config{}, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func accountExtraIntentFixture(t *testing.T) (service.AdminAccountRepository, *service.Account) {
	t.Helper()
	repo := NewAdminAccountRepository(integrationEntClient, integrationDB, nil)
	account := &service.Account{
		Name: "extra-intent-synthetic", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 2, Priority: 5,
		Credentials: map[string]any{"api_key": "synthetic-only"},
		Extra: map[string]any{
			service.AccountProbePenaltyExtraKey:   map[string]any{"enabled": true, "minimum_charge": .001},
			service.AccountCostMultiplierExtraKey: .1,
			service.AccountCostAutoSyncExtraKey:   true,
		},
	}
	require.NoError(t, repo.Create(context.Background(), account))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, account.ID)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, account.ID)
	})
	return repo, account
}

func assertAccountExtraIntent(t *testing.T, account *service.Account, want map[string]any) {
	t.Helper()
	for _, key := range []string{service.AccountProbePenaltyExtraKey, service.AccountCostMultiplierExtraKey, service.AccountCostAutoSyncExtraKey} {
		if value, exists := want[key]; exists {
			require.Equal(t, value, account.Extra[key], key)
		} else {
			require.NotContains(t, account.Extra, key)
		}
	}
}

func TestAccountExtraIntentServiceReadBeforeConcurrentCommit(t *testing.T) {
	for _, route := range []string{"admin", "account-service"} {
		for _, extraMode := range []string{"nil-extra", "missing-keys"} {
			for _, change := range []string{"disable", "change-amount", "delete"} {
				t.Run(route+"/"+extraMode+"/"+change, func(t *testing.T) {
					repo, account := accountExtraIntentFixture(t)
					latest := map[string]any{
						service.AccountProbePenaltyExtraKey:   map[string]any{"enabled": change != "disable", "minimum_charge": .002},
						service.AccountCostMultiplierExtraKey: .2,
						service.AccountCostAutoSyncExtraKey:   false,
					}
					if change == "delete" {
						latest = map[string]any{}
					}
					read := false
					gate := &accountExtraIntentReadRepo{AdminAccountRepository: repo}
					gate.afterRead = func(ctx context.Context, stale *service.Account) error {
						read = true
						require.Equal(t, .001, stale.ProbePenaltyMinimum(), "A must read the old enabled setting")
						if change == "delete" {
							_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra-'probe_penalty'-'cost_multiplier'-'cost_multiplier_auto_sync' WHERE id=$1`, account.ID)
							return err
						}
						return repo.UpdateExtra(ctx, account.ID, latest)
					}
					var extra map[string]any
					if extraMode == "missing-keys" {
						extra = map[string]any{"form_setting": "saved"}
					}
					ctx := context.Background()
					name := "extra-intent-renamed"
					var saved *service.Account
					var err error
					if route == "admin" {
						// The failing HTTP DTO is only {name: ...}: no extra or platform.
						saved, err = accountExtraIntentAdmin(gate).UpdateAccount(ctx, account.ID, &service.UpdateAccountInput{Name: name, Extra: extra})
					} else {
						input := service.UpdateAccountRequest{Name: &name}
						if extra != nil {
							input.Extra = &extra
						}
						saved, err = service.NewAccountService(gate, nil).Update(ctx, account.ID, input)
					}
					require.NoError(t, err)
					require.True(t, read)
					got, err := repo.GetByID(ctx, account.ID)
					require.NoError(t, err)
					require.Equal(t, name, got.Name)
					assertAccountExtraIntent(t, got, latest)
					assertAccountExtraIntent(t, saved, latest)
					if extra != nil {
						require.Equal(t, "saved", got.Extra["form_setting"])
					}
				})
			}
		}
	}
}

func TestAccountExtraIntentExplicitServiceUpdates(t *testing.T) {
	for _, route := range []string{"admin", "account-service"} {
		t.Run(route, func(t *testing.T) {
			repo, account := accountExtraIntentFixture(t)
			ctx := context.Background()
			for _, extra := range []map[string]any{
				{
					service.AccountProbePenaltyExtraKey:   map[string]any{"enabled": false, "minimum_charge": .002},
					service.AccountCostMultiplierExtraKey: .2,
					service.AccountCostAutoSyncExtraKey:   false,
				},
				{
					service.AccountProbePenaltyExtraKey:   map[string]any{"enabled": true, "minimum_charge": .003},
					service.AccountCostMultiplierExtraKey: 0.0,
					service.AccountCostAutoSyncExtraKey:   true,
				},
				{
					service.AccountProbePenaltyExtraKey:   nil,
					service.AccountCostMultiplierExtraKey: nil,
					service.AccountCostAutoSyncExtraKey:   nil,
				},
			} {
				var err error
				if route == "admin" {
					_, err = accountExtraIntentAdmin(repo).UpdateAccount(ctx, account.ID, &service.UpdateAccountInput{Extra: extra})
				} else {
					_, err = service.NewAccountService(repo, nil).Update(ctx, account.ID, service.UpdateAccountRequest{Extra: &extra})
				}
				require.NoError(t, err)
				got, err := repo.GetByID(ctx, account.ID)
				require.NoError(t, err)
				assertAccountExtraIntent(t, got, extra)
			}
		})
	}
}
