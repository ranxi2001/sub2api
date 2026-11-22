//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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
			if scenario != "eligible" && scenario != "composite" {
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

func TestGatewayProtocolCandidatesByGroupAndProtocol(t *testing.T) {
	for _, target := range []string{PlatformOpenAI, PlatformKimi, PlatformGrok, PlatformGemini, PlatformAntigravity} {
		for _, composite := range []bool{false, true} {
			t.Run(target+map[bool]string{false: "/concrete", true: "/composite"}[composite], func(t *testing.T) {
				base, ctx := gatewayProtocolFixture(target)
				repo := &gatewayMessagesCandidateRepo{gatewayProtocolAdmissionRepo: base}
				if composite {
					base.group.Platform = PlatformComposite
					ctx = WithResolvedTargetPlatform(ctx, target)
				}
				if target == PlatformGemini || target == PlatformAntigravity {
					ctx = WithGatewayGeminiSelection(ctx, "public-model")
				} else {
					ctx = WithGatewayOpenAISelection(ctx, "public-model", "public-model")
				}
				accounts := appendGatewayCandidates(ctx, repo, nil, &base.group.ID, target, nil)
				require.Len(t, accounts, 1)
				require.Equal(t, base.account.ID, accounts[0].ID)
				require.Equal(t, base.account.Concurrency, accounts[0].Concurrency)
				require.Empty(t, appendGatewayCandidates(ctx, repo, nil, nil, target, nil))
				if accounts[0].IsGateway() {
					svc := &GatewayService{accountRepo: repo}
					require.True(t, svc.isAccountAllowedForPlatform(&accounts[0], target, false))
					_, err := svc.hydrateSelectedAccount(ctx, &accounts[0])
					require.NoError(t, err)
				} else {
					require.Equal(t, target, accounts[0].Platform)
					svc := &OpenAIGatewayService{accountRepo: repo}
					fresh, err := svc.openAIGatewaySelectionView(ctx, &accounts[0])
					require.NoError(t, err)
					require.True(t, fresh.gatewayProtocolSource.IsGateway())
					base.account.GroupIDs, base.account.AccountGroups = nil, nil
					_, err = svc.openAIGatewaySelectionView(ctx, &accounts[0])
					require.Error(t, err)
				}
			})
		}
	}
}

func TestGatewayProtocolRegionalAssignmentAndRemoval(t *testing.T) {
	base, ctx := gatewayProtocolFixture(PlatformOpenAI)
	base.account.ID = 1
	ctx = WithGatewayOpenAISelection(ctx, "public-model", "public-model")
	bucket := SchedulerBucket{GroupID: base.group.ID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	doc := RegionalSchedulerDocument{Version: 1, Groups: []*Group{base.group},
		Buckets: []RegionalSchedulerBucket{{Bucket: bucket, Accounts: []*Account{base.account}}}}
	payload, err := json.Marshal(doc)
	require.NoError(t, err)
	require.Error(t, RegionalSchedulerValidator([]int64{2})(payload))
	replica := regionalTestReplica(t, filepath.Join(t.TempDir(), "state"), true)
	require.NoError(t, replica.PublishNext(ctx, "test", time.Minute, payload))
	reader, err := NewRegionalSchedulerReader(replica)
	require.NoError(t, err)
	snapshot := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	snapshot.SetLocalSnapshotReader(reader)
	accounts := appendGatewayCandidates(ctx, nil, snapshot, &base.group.ID, PlatformOpenAI, nil)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(1), accounts[0].ID)
	ordinary, _, err := reader.GetLocalSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.Empty(t, ordinary)
	doc.Buckets[0].Accounts = []*Account{}
	payload, err = json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, replica.PublishNext(ctx, "test", time.Minute, payload))
	_, err = (&OpenAIGatewayService{schedulerSnapshot: snapshot}).openAIGatewaySelectionView(ctx, &accounts[0])
	require.Error(t, err)
}

func (r *gatewayMessagesCandidateRepo) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return nil, nil
}

func TestGatewayProtocolCatalogFollowsLiveBindings(t *testing.T) {
	base, ctx := gatewayProtocolFixture(PlatformOpenAI)
	base.account.Credentials["model_mapping"] = map[string]any{"public-alias": "upstream", "other": "hidden"}
	base.account.AccountGroups[0].AllowedModels = []string{"public-alias"}
	svc := &GatewayService{accountRepo: &gatewayMessagesCandidateRepo{gatewayProtocolAdmissionRepo: base}}
	want := []string{"public-alias"}
	require.Equal(t, want, svc.GetAvailableModels(ctx, &base.group.ID, PlatformOpenAI))
	models, err := svc.GetGroupModelCatalog(ctx, base.group.ID, PlatformOpenAI)
	require.NoError(t, err)
	require.Equal(t, want, models)
	base.account.GroupIDs, base.account.AccountGroups = nil, nil
	require.Empty(t, svc.GetAvailableModels(ctx, &base.group.ID, PlatformOpenAI))
}

func TestGatewayProtocolCatalogSavedModelsAndGroupPrecedence(t *testing.T) {
	base, ctx := gatewayProtocolFixture(PlatformOpenAI)
	base.account.Credentials["model_mapping"] = map[string]any{"saved": "wire", "other": "other"}
	svc := &GatewayService{accountRepo: &gatewayMessagesCandidateRepo{gatewayProtocolAdmissionRepo: base}}
	models, err := svc.GetGroupModelCatalog(ctx, base.group.ID, PlatformOpenAI)
	require.NoError(t, err)
	require.Equal(t, []string{"other", "saved"}, models, "saved IDs replace platform defaults")
	base.group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"saved"}}
	models, err = svc.GetGroupModelCatalog(ctx, base.group.ID, PlatformOpenAI)
	require.NoError(t, err)
	require.Equal(t, []string{"saved"}, models)
	base.account.Credentials["model_mapping"] = map[string]any{"*": "wire"}
	models, err = svc.GetGroupModelCatalog(ctx, base.group.ID, PlatformOpenAI)
	require.NoError(t, err)
	require.Equal(t, []string{"saved"}, models, "explicit group IDs seed wildcard account mappings")
	base.account.AccountGroups[0].AllowedModels = []string{"other"}
	models, err = svc.GetGroupModelCatalog(ctx, base.group.ID, PlatformOpenAI)
	require.NoError(t, err)
	require.Empty(t, models, "a fetched catalog must not bypass binding admission")
}

func TestGatewayProtocolOpenAIChannelModelKeepsBinding(t *testing.T) {
	base, ctx := gatewayProtocolFixture(PlatformOpenAI)
	ctx = WithGatewayOpenAISelection(ctx, "public", "public")
	base.account.AccountGroups[0].AllowedModels = []string{"public"}
	base.account.Credentials["model_mapping"] = map[string]any{"public": "mapped"}
	view, err := prepareGatewayProtocolAccount(ctx, base.account, base, "openai", "channel-model")
	require.NoError(t, err)
	require.True(t, view.IsModelAllowedInGroup(&base.group.ID, "channel-model"))
	require.True(t, supportsOpenAIRequestModel(view, "channel-model", false))
	base.account.AccountGroups[0].AllowedModels = []string{"channel-model"}
	_, err = prepareGatewayProtocolAccount(ctx, base.account, base, "openai", "channel-model")
	require.Error(t, err)
}

func TestGatewayProtocolCatalogCompositePublicAlias(t *testing.T) {
	base, ctx := gatewayProtocolFixture(PlatformComposite)
	base.group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"public"}}
	base.account.AccountGroups[0].AllowedModels = []string{"upstream"}
	base.account.Credentials["model_mapping"] = map[string]any{"upstream": "wire"}
	svc := &GatewayService{accountRepo: &gatewayMessagesCandidateRepo{gatewayProtocolAdmissionRepo: base},
		compositeResolver: NewCompositeRouteResolver(compositeRouteRepoStub{routes: []CompositeModelRoute{
			{GroupID: base.group.ID, PublicModel: "public", UpstreamModel: "upstream", TargetPlatform: PlatformOpenAI,
				Endpoint: CompositeRouteEndpointAny, MatchType: CompositeRouteMatchExact, Enabled: true},
		}})}
	models, err := svc.GetGroupModelCatalog(ctx, base.group.ID, PlatformComposite)
	require.NoError(t, err)
	require.Equal(t, []string{"public"}, models)
	base.account.Credentials["model_mapping"] = map[string]any{"public": "wire"}
	base.account.AccountGroups[0].AllowedModels = []string{"public"}
	models, err = svc.GetGroupModelCatalog(ctx, base.group.ID, PlatformComposite)
	require.NoError(t, err)
	require.Empty(t, models, "an explicit route must authorize its actual upstream model")

}
