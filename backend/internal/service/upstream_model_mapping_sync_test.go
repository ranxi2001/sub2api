package service

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type upstreamModelMappingRepoStub struct {
	AccountRepository
	account    *Account
	next       time.Time
	claimCalls int
	applyCalls int
	mapping    map[string]string
	changed    bool
}

func (r *upstreamModelMappingRepoStub) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func (r *upstreamModelMappingRepoStub) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

func (r *upstreamModelMappingRepoStub) ClaimAutomaticUpstreamModelSync(_ context.Context, _ int64, now, next time.Time) (bool, error) {
	r.claimCalls++
	if now.Before(r.next) {
		return false, nil
	}
	r.next = next
	r.account.Extra = map[string]any{UpstreamModelSyncExtraKey: map[string]any{"next_attempt_unix": next.Unix()}}
	return true, nil
}

func (r *upstreamModelMappingRepoStub) UpdateUpstreamModelMappingIfUnchanged(_ context.Context, expected *Account, mapping map[string]string) (bool, error) {
	r.applyCalls++
	if r.changed || !reflect.DeepEqual(expected.Credentials, r.account.Credentials) {
		return false, nil
	}
	r.mapping = mapping
	return true, nil
}

func modelMappingSyncFixture(status int, body string) (*AccountTestService, *upstreamModelMappingRepoStub, *httpUpstreamRecorder) {
	account := &Account{
		ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"api_key": "test-key", "base_url": "https://provider.example/v1",
			"model_mapping": map[string]any{"old": "old", "public-new": "new", "public-old": "old"},
		},
	}
	repo := &upstreamModelMappingRepoStub{account: account}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)),
	}}
	svc := &AccountTestService{
		accountRepo: repo, httpUpstream: upstream, cfg: upstreamModelSyncTestConfig(),
		modelMetadataRegistry:   map[string]modelsDevProvider{"test": {API: "https://provider.example/v1"}},
		modelMetadataRegistryAt: time.Now(),
	}
	return svc, repo, upstream
}

func TestApplyUpstreamModelsReplacesStaleModelsAndPreservesLiveRedirects(t *testing.T) {
	svc, repo, upstream := modelMappingSyncFixture(http.StatusOK, `{"data":[{"id":"new"}]}`)
	catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, UpstreamModelSyncOptions{Apply: true})
	require.NoError(t, err)
	require.True(t, catalog.Applied)
	require.True(t, catalog.Changed)
	require.Equal(t, "upstream", catalog.DiscoverySource)
	require.Equal(t, map[string]string{"new": "new", "public-new": "new"}, repo.mapping)
	require.Equal(t, 0, repo.claimCalls, "explicit apply is not subject to the automatic interval")
	require.Len(t, upstream.requests, 1, "model sync must not send a generation/probe request")
	require.Equal(t, "/v1/models", upstream.requests[0].URL.Path)
}

func TestAutomaticUpstreamModelsFailureConsumesRefreshWindow(t *testing.T) {
	svc, repo, upstream := modelMappingSyncFixture(http.StatusBadGateway, `{"error":"upstream failed"}`)
	options := UpstreamModelSyncOptions{Apply: true, Automatic: true}
	_, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, options)
	require.Error(t, err)
	catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, options)
	require.NoError(t, err)
	require.Equal(t, "throttled", catalog.Skipped)
	require.NotNil(t, catalog.NextSyncAt)
	require.WithinDuration(t, repo.next, *catalog.NextSyncAt, time.Second)
	require.Len(t, upstream.requests, 1, "the next page poll must not retry a failed upstream")
	require.Zero(t, repo.applyCalls)
}

func TestApplyUpstreamModelsDoesNotTrustConfiguredFallbackOrEmptyList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unsupported_list", http.StatusNotFound, `{"error":"missing list endpoint"}`},
		{"empty_list", http.StatusOK, `{"data":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _ := modelMappingSyncFixture(tc.status, tc.body)
			catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, UpstreamModelSyncOptions{Apply: true})
			if tc.status == http.StatusNotFound {
				require.NoError(t, err)
				require.Equal(t, "configured_fallback", catalog.DiscoverySource)
				require.Equal(t, "unverified_catalog", catalog.Skipped)
			} else {
				require.Error(t, err)
			}
			require.Zero(t, repo.applyCalls)
			require.Nil(t, repo.mapping)
		})
	}
}

func TestApplyUpstreamModelsSkipsConcurrentAccountChange(t *testing.T) {
	svc, repo, _ := modelMappingSyncFixture(http.StatusOK, `{"data":[{"id":"new"}]}`)
	repo.changed = true
	catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, UpstreamModelSyncOptions{Apply: true})
	require.NoError(t, err)
	require.False(t, catalog.Applied)
	require.False(t, catalog.Changed)
	require.Equal(t, "account_changed", catalog.Skipped)
	require.Nil(t, repo.mapping)
}

func TestApplyUpstreamModelsDoesNotApplyWildcardOnlyCatalog(t *testing.T) {
	svc, repo, _ := modelMappingSyncFixture(http.StatusOK, `{"data":[{"id":"*"}]}`)
	catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, UpstreamModelSyncOptions{Apply: true})
	require.NoError(t, err)
	require.False(t, catalog.Applied)
	require.Equal(t, "empty", catalog.Skipped)
	require.Zero(t, repo.applyCalls)
}

func TestAutomaticUpstreamModelsPreservesShadowAndAliasPolicies(t *testing.T) {
	for _, policy := range []string{"shadow", "aliases", "inactive", "observer"} {
		t.Run(policy, func(t *testing.T) {
			svc, repo, upstream := modelMappingSyncFixture(http.StatusOK, `{"data":[{"id":"new"}]}`)
			ctx := context.Background()
			switch policy {
			case "shadow":
				parent := int64(10)
				repo.account.ParentAccountID = &parent
			case "aliases":
				repo.account.Type = AccountTypeOAuth
				repo.account.Credentials[OpenAIModelMappingModeKey] = "aliases"
			case "inactive":
				repo.account.Schedulable = false
			case "observer":
				ctx = WithObserverScope(ctx, []int64{1})
			}
			catalog, err := svc.SyncUpstreamModelCatalogWithOptions(ctx, repo.account, UpstreamModelSyncOptions{Apply: true, Automatic: true})
			if policy == "observer" {
				require.ErrorIs(t, err, ErrObserverScope)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, catalog.Skipped)
			}
			require.Empty(t, upstream.requests)
			require.Zero(t, repo.claimCalls)
		})
	}
}

func TestReconciledUpstreamModelsDoesNotRetainChainedOrWildcardIdentityTargets(t *testing.T) {
	existing := map[string]string{
		"new": "new", "old": "old", "gpt-*": "gpt-*",
		"alias": "new", "chain": "alias", "alias-*": "new", "stale": "old",
	}
	for i := 0; i < 20; i++ {
		mapping := reconciledUpstreamModelMapping(existing, []string{"new", "next"})
		require.Equal(t, map[string]string{"new": "new", "next": "next", "alias": "new", "alias-*": "new"}, mapping)
	}
}
