//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func quality152Admin(repo service.AdminAccountRepository) service.AdminService {
	return service.NewAdminService(&config.Config{}, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}
func quality152Marker(stamp string) map[string]any {
	return map[string]any{"sample-model": map[string]any{"reason": "quality_rule:7", "isolated_at": stamp}}
}
func quality152Account(t *testing.T, isolation any) (service.AdminAccountRepository, *service.Account) {
	t.Helper()
	repo := NewAdminAccountRepository(integrationEntClient, integrationDB, nil)
	extra := map[string]any{"form_setting": "original"}
	if isolation != nil {
		extra[service.QualityModelIsolationKey] = isolation
	}
	account := &service.Account{Name: "quality152-synthetic", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 20, Priority: 5, Credentials: map[string]any{"api_key": "synthetic-only"}, Extra: extra}
	require.NoError(t, repo.Create(context.Background(), account))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, account.ID)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, account.ID)
	})
	return repo, account
}
func quality152Assert(t *testing.T, repo service.AccountRepository, id int64, want any) *service.Account {
	t.Helper()
	got, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	if want == nil {
		require.NotContains(t, got.Extra, service.QualityModelIsolationKey)
	} else {
		expected, _ := json.Marshal(want)
		actual, _ := json.Marshal(got.Extra[service.QualityModelIsolationKey])
		require.JSONEq(t, string(expected), string(actual))
	}
	require.Equal(t, service.StatusActive, got.Status)
	require.True(t, got.Schedulable)
	require.Equal(t, 20, got.Concurrency)
	return got
}

func TestQuality152AdminUpdatePreservesLockedIsolation(t *testing.T) {
	latest := quality152Marker("2026-01-01T00:00:02.222222222Z")
	old := quality152Marker("2026-01-01T00:00:01.111111111Z")
	for _, tc := range []struct {
		name           string
		server, client any
		omit           bool
	}{
		{"same-reason-different-timestamp", latest, old, false},
		{"client-omits-current-isolation", latest, nil, true},
		{"client-null-cannot-erase", latest, nil, false},
		{"client-old-value-cannot-resurrect", nil, old, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, a := quality152Account(t, tc.server)
			extra := map[string]any{"form_setting": "saved"}
			if !tc.omit {
				extra[service.QualityModelIsolationKey] = tc.client
			}
			saved, err := quality152Admin(repo).UpdateAccount(context.Background(), a.ID, &service.UpdateAccountInput{Name: "quality152-edited", Extra: extra})
			require.NoError(t, err)
			got := quality152Assert(t, repo, a.ID, tc.server)
			require.Equal(t, "quality152-edited", got.Name)
			require.Equal(t, "saved", got.Extra["form_setting"])
			require.Equal(t, got.Extra[service.QualityModelIsolationKey], saved.Extra[service.QualityModelIsolationKey])
		})
	}
}

// Gate the real billing-settings repository call after the service has read and
// normalized its snapshot, before the production repository acquires its lock.
type quality152GatedRepo struct {
	service.AdminAccountRepository
	entered chan struct{}
	resume  chan struct{}
}

func (r *quality152GatedRepo) UpdateWithAccountBillingSettings(ctx context.Context, a *service.Account, settings service.AccountBillingSettingsUpdate) error {
	close(r.entered)
	select {
	case <-r.resume:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.AdminAccountRepository.UpdateWithAccountBillingSettings(ctx, a, settings)
}
func TestQuality152AdminUpdateServiceReadBeforeLock(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("runtime-remove=%v", remove), func(t *testing.T) {
			old := quality152Marker("2026-01-01T00:00:01.111111111Z")
			latest := quality152Marker("2026-01-01T00:00:02.222222222Z")
			repo, a := quality152Account(t, old)
			gate := &quality152GatedRepo{AdminAccountRepository: repo, entered: make(chan struct{}), resume: make(chan struct{})}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := quality152Admin(gate).UpdateAccount(ctx, a.ID, &service.UpdateAccountInput{Name: "quality152-racing-edit", Extra: map[string]any{"form_setting": "saved", service.QualityModelIsolationKey: old}})
				done <- err
			}()
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("service did not reach pre-lock repository boundary")
			}
			if remove {
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=extra-'quality_model_isolation',updated_at=clock_timestamp() WHERE id=$1`, a.ID)
				require.NoError(t, err)
			} else {
				require.NoError(t, repo.UpdateExtra(ctx, a.ID, map[string]any{service.QualityModelIsolationKey: latest}))
			}
			close(gate.resume)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("account edit did not finish")
			}
			var want any = latest
			if remove {
				want = nil
			}
			got := quality152Assert(t, repo, a.ID, want)
			require.Equal(t, "quality152-racing-edit", got.Name)
		})
	}
}

func TestQuality152OrdinaryBulkAndExtraSavesPreserveIsolation(t *testing.T) {
	for _, route := range []string{"repository-bulk", "admin-bulk", "admin-extra"} {
		for _, present := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/present=%v", route, present), func(t *testing.T) {
				var current any
				if present {
					current = quality152Marker("2026-01-01T00:00:02.222222222Z")
				}
				repo, a := quality152Account(t, current)
				input := map[string]any{"form_setting": "bulk-saved", service.QualityModelIsolationKey: quality152Marker("2026-01-01T00:00:01.111111111Z")}
				switch route {
				case "repository-bulk":
					_, err := repo.BulkUpdate(context.Background(), []int64{a.ID}, service.AccountBulkUpdate{Extra: input})
					require.NoError(t, err)
				case "admin-bulk":
					_, err := quality152Admin(repo).BulkUpdateAccounts(context.Background(), &service.BulkUpdateAccountsInput{AccountIDs: []int64{a.ID}, Extra: input})
					require.NoError(t, err)
				case "admin-extra":
					require.NoError(t, quality152Admin(repo).UpdateAccountExtra(context.Background(), a.ID, input))
				}
				got := quality152Assert(t, repo, a.ID, current)
				require.Equal(t, "bulk-saved", got.Extra["form_setting"])
			})
		}
	}
}

func TestQuality152OwnershipAndExplicitRestoreRemainIntact(t *testing.T) {
	ctx := context.Background()
	f, _, _ := newQualityIsolationFixture(t)
	require.Equal(t, "groups_removed", isolationRound(f, "failed", "failed"))
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
	repo := NewAdminAccountRepository(integrationEntClient, integrationDB, nil)
	before, err := repo.GetByID(ctx, f.account)
	require.NoError(t, err)
	var ownership string
	require.NoError(t, integrationDB.QueryRow(`SELECT state::text FROM account_quality_states WHERE plan_id=$1`, f.plan.ID).Scan(&ownership))
	stale := quality152Marker("2026-01-01T00:00:01.111111111Z")
	_, err = quality152Admin(repo).UpdateAccount(ctx, f.account, &service.UpdateAccountInput{Extra: map[string]any{service.QualityModelIsolationKey: stale}})
	require.NoError(t, err)
	after, err := repo.GetByID(ctx, f.account)
	require.NoError(t, err)
	require.Equal(t, before.Extra[service.QualityModelIsolationKey], after.Extra[service.QualityModelIsolationKey])
	var retained string
	require.NoError(t, integrationDB.QueryRow(`SELECT state::text FROM account_quality_states WHERE plan_id=$1`, f.plan.ID).Scan(&retained))
	require.JSONEq(t, ownership, retained)
	// Same owner reason does not relax byte/field equality of the entry.
	raw, _ := json.Marshal(map[string]any{"reason": "quality_rule:7", "isolated_at": "2026-01-01T00:00:01Z"})
	require.False(t, cooldownEntryEqual(map[string]any{"reason": "quality_rule:7", "isolated_at": "2026-01-01T00:00:02Z"}, raw))
	owner := f.plans.(service.QualityOwnershipRepository)
	preview, err := owner.QualityOwnership(ctx, f.plan.ID, nil)
	require.NoError(t, err)
	require.True(t, preview.RestoreReady)
	_, err = owner.QualityOwnership(ctx, f.plan.ID, &service.QualityOwnershipRequest{Confirm: true, Fence: preview.Fence, Restore: true})
	require.NoError(t, err)
	_, err = quality152Admin(repo).UpdateAccount(ctx, f.account, &service.UpdateAccountInput{Extra: before.Extra})
	require.NoError(t, err)
	restored, err := repo.GetByID(ctx, f.account)
	require.NoError(t, err)
	require.NotContains(t, restored.Extra, service.QualityModelIsolationKey)
	require.Equal(t, before.Status, restored.Status)
	require.Equal(t, before.Schedulable, restored.Schedulable)
	require.Equal(t, before.Concurrency, restored.Concurrency)
}

// Quality152AdminFixture exposes only the existing isolated test fixture to the
// external test package so it can use the real handler without an import cycle.
func Quality152AdminFixture(t *testing.T, isolation any) (service.AdminService, service.AdminAccountRepository, *service.Account) {
	repo, account := quality152Account(t, isolation)
	return quality152Admin(repo), repo, account
}
