//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type requestProxyUpstreamMergeRepo struct {
	*upstreamBillingProbeAccountRepo
	atomicCreates   int
	ordinaryCreates int
	bindCalls       int
	initialGroupIDs []int64
	initialPolicy   *OAuthAPIKeyDispositionConfig
	atomicGroups    []AccountGroup
}

func (r *requestProxyUpstreamMergeRepo) Create(ctx context.Context, account *Account) error {
	r.ordinaryCreates++
	return r.upstreamBillingProbeAccountRepo.Create(ctx, account)
}

func (r *requestProxyUpstreamMergeRepo) BindGroups(_ context.Context, _ int64, _ []int64) error {
	r.bindCalls++
	return nil
}

func (r *requestProxyUpstreamMergeRepo) CreateWithAccountGroups(ctx context.Context, account *Account, groups []AccountGroup) error {
	r.atomicCreates++
	r.initialGroupIDs = append([]int64(nil), account.GroupIDs...)
	r.initialPolicy = account.InitialAPIKeyDisposition
	r.atomicGroups = append([]AccountGroup(nil), groups...)
	return r.upstreamBillingProbeAccountRepo.Create(ctx, account)
}

func TestAdminCreateRequestProxyPreservesAtomicAPIKeyDisposition(t *testing.T) {
	for _, tc := range []struct {
		name    string
		proxyID int64
		valid   bool
	}{
		{name: "valid proxy preserves initial groups and policy", proxyID: 11, valid: true},
		{name: "missing proxy rejects before any write", proxyID: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultOAuthAutoConfig()
			cfg.Enabled = true
			cfg.GroupIDs = []int64{5}
			cfg.APIKeyDisposition.Enabled = true
			cfg.APIKeyDisposition.AccountIDs = []int64{7}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			repo := &requestProxyUpstreamMergeRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{}}
			svc := &adminServiceImpl{
				accountRepo:          repo,
				accountDuplicateRepo: repo,
				settingService:       NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil),
				groupRepo:            autoConfigGroups{group: &Group{ID: 5, Platform: PlatformOpenAI, Status: StatusActive}},
				proxyRepo:            &bpsProxyConfigProxies{proxies: []Proxy{{ID: 11, Status: StatusActive}}},
			}

			created, err := svc.CreateAccount(t.Context(), &CreateAccountInput{
				Name: "request proxy account", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				SkipDefaultGroupBind: true, SkipMixedChannelCheck: true,
				Extra: map[string]any{OpenAIProxyModeKey: OpenAIProxyModeRequest, OpenAIProxyIDsKey: []int64{tc.proxyID}},
			})

			if !tc.valid {
				require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
				require.Zero(t, repo.atomicCreates)
				require.Zero(t, repo.ordinaryCreates)
				require.Zero(t, repo.bindCalls)
				return
			}
			require.NoError(t, err)
			require.True(t, created.OpenAIRequestProxyEnabled())
			require.Equal(t, []int64{5}, created.GroupIDs)
			require.Equal(t, 1, repo.atomicCreates)
			require.Zero(t, repo.ordinaryCreates)
			require.Zero(t, repo.bindCalls)
			require.Equal(t, []int64{5}, repo.initialGroupIDs, "initial account groups must be available at the atomic create boundary")
			require.NotNil(t, repo.initialPolicy)
			require.Equal(t, []int64{7}, repo.initialPolicy.AccountIDs)
			require.Equal(t, 180, repo.initialPolicy.PauseMinutes)
			require.Equal(t, []AccountGroup{{GroupID: 5, Priority: 50}}, repo.atomicGroups)
		})
	}
}
