//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type gatewayMessagesCandidateRepo struct {
	*gatewayProtocolAdmissionRepo
	queries int
	err     error
}

func (r *gatewayMessagesCandidateRepo) ListGatewayRequestCandidates(context.Context, int64, string) ([]Account, *Group, error) {
	r.queries++
	return []Account{*r.account}, r.group, r.err
}

func TestGatewayProtocolMessagesCandidatesStayRequestScoped(t *testing.T) {
	for _, scenario := range []string{"eligible", "unbound", "oauth-only", "other-target", "composite", "disabled", "paused", "cooling", "429", "401", "low-balance", "wrong-model", "unscoped"} {
		t.Run(scenario, func(t *testing.T) {
			base, ctx := gatewayProtocolFixture(PlatformAnthropic)
			repo := &gatewayMessagesCandidateRepo{gatewayProtocolAdmissionRepo: base}
			ctx = WithGatewayMessagesSelection(ctx, "public-model")
			switch scenario {
			case "unbound":
				repo.account.GroupIDs, repo.account.AccountGroups = nil, nil
			case "oauth-only":
				repo.group.RequireOAuthOnly = true
			case "other-target":
				ctx = WithResolvedTargetPlatform(ctx, PlatformGemini)
			case "composite":
				repo.group.Platform = PlatformComposite
				ctx = WithResolvedTargetPlatform(ctx, PlatformAnthropic)
			case "disabled":
				repo.account.Status = StatusDisabled
			case "paused":
				repo.account.PerformancePaused = true
			case "cooling":
				until := time.Now().Add(time.Hour)
				repo.account.TempUnschedulableUntil = &until
			case "429":
				until := time.Now().Add(time.Hour)
				repo.account.RateLimitResetAt = &until
			case "401", "low-balance":
				repo.account.Status = StatusError
			case "wrong-model":
				repo.account.Credentials["model_mapping"] = map[string]any{"other": "other"}
			case "unscoped":
				ctx = context.WithValue(context.Background(), ctxkey.Group, repo.group)
			}
			svc := &GatewayService{accountRepo: repo}
			accounts, err := svc.appendGatewayMessagesCandidates(ctx, &repo.group.ID, PlatformAnthropic, nil)
			require.NoError(t, err)
			if scenario != "eligible" {
				require.Empty(t, accounts)
				return
			}
			require.Len(t, accounts, 1)
			require.Equal(t, repo.account.ID, accounts[0].ID)
			require.True(t, svc.isAccountSchedulableForSelection(&accounts[0]))
			require.True(t, svc.isAccountAllowedForPlatform(&accounts[0], PlatformAnthropic, true))
			require.False(t, accounts[0].IsSchedulable(), "neutral identity never becomes globally schedulable")
			require.Nil(t, repo.account.gatewayMessagesView, "shared repository snapshot stays untouched")
			repo.account.Status = StatusDisabled
			_, err = svc.hydrateSelectedAccount(ctx, &accounts[0])
			require.Error(t, err, "post-selection hydration must consult fresh authority")
		})
	}
}

func TestGatewayProtocolMessagesCandidateReadFailurePreservesLegacy(t *testing.T) {
	base, ctx := gatewayProtocolFixture(PlatformAnthropic)
	repo := &gatewayMessagesCandidateRepo{gatewayProtocolAdmissionRepo: base, err: errors.New("primary unavailable")}
	ctx = WithGatewayMessagesSelection(ctx, "public-model")
	legacy := Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	svc := &GatewayService{accountRepo: repo}
	accounts, err := svc.appendGatewayMessagesCandidates(ctx, &repo.group.ID, PlatformAnthropic, []Account{legacy})
	require.NoError(t, err)
	require.Equal(t, []Account{legacy}, accounts)
	require.Equal(t, 1, repo.queries)
}

func TestGatewayProtocolMessagesCandidateSourceRefreshesAfterWarmSnapshot(t *testing.T) {
	base, ctx := gatewayProtocolFixture(PlatformAnthropic)
	repo := &gatewayMessagesCandidateRepo{gatewayProtocolAdmissionRepo: base}
	ctx = WithGatewayMessagesSelection(ctx, "public-model")
	snapshot := NewSchedulerSnapshotService(&snapshotReaderCache{hit: true}, nil, nil, nil, nil)
	svc := &GatewayService{accountRepo: repo, schedulerSnapshot: snapshot}
	first, _, err := svc.listSchedulableAccounts(ctx, &repo.group.ID, PlatformAnthropic, false)
	require.NoError(t, err)
	require.Len(t, first, 1)
	repo.account.GroupIDs, repo.account.AccountGroups = nil, nil
	second, _, err := svc.listSchedulableAccounts(ctx, &repo.group.ID, PlatformAnthropic, false)
	require.NoError(t, err)
	require.Empty(t, second, "outbox lag cannot retain a revoked candidate")
	require.Equal(t, 2, repo.queries)
}
