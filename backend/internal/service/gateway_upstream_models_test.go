//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayUpstreamModelsDiscoveryAndApply(t *testing.T) {
	for _, kind := range []string{"new_api", "sub2api"} {
		for _, apply := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/discovery", true: "/apply"}[apply], func(t *testing.T) {
				svc, repo, upstream := modelMappingSyncFixture(http.StatusOK, `{"data":[{"id":"new"},{"id":"new"}]}`)
				readOnlyRepo := &batchWhitelistReadOnlyRepo{upstreamModelMappingRepoStub: repo}
				svc.accountRepo = readOnlyRepo
				a := repo.account
				a.Platform, a.Type = PlatformGateway, AccountTypeGateway
				a.Credentials["upstream_kind"] = kind
				a.Credentials["unrelated"] = "keep"
				proxyID := int64(7)
				a.ProxyID = &proxyID
				a.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example", Port: 8080}
				before := cloneTestCredentials(a.Credentials)
				// Record metadata/health writes separately from explicit mapping
				// application. Discovery must only issue the authenticated listing.
				catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), a, UpstreamModelSyncOptions{Apply: apply})
				require.NoError(t, err)
				require.Equal(t, []string{"new"}, catalog.Models)
				require.Equal(t, "upstream", catalog.DiscoverySource)
				require.Len(t, upstream.requests, 1, "no provider registry request")
				require.Equal(t, "https://provider.example/v1/models", upstream.lastReq.URL.String())
				require.Equal(t, http.MethodGet, upstream.lastReq.Method)
				require.Equal(t, "Bearer test-key", upstream.lastReq.Header.Get("Authorization"))
				require.Equal(t, "http://proxy.example:8080", upstream.lastProxyURL)
				require.Equal(t, before, a.Credentials)
				require.Zero(t, repo.claimCalls)
				require.Zero(t, readOnlyRepo.extraWrites.Load())
				require.Zero(t, readOnlyRepo.credentialWrites.Load())
				require.Zero(t, readOnlyRepo.authErrors.Load())
				require.Zero(t, readOnlyRepo.tempUnsched.Load())
				if apply {
					require.True(t, catalog.Applied)
					require.Equal(t, map[string]string{"new": "new", "public-new": "new"}, repo.mapping)
					require.Equal(t, 1, repo.applyCalls)
				} else {
					require.Zero(t, repo.applyCalls)
				}
			})
		}
	}
}

func cloneTestCredentials(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func TestGatewayUpstreamModelsRejectInvalidConnection(t *testing.T) {
	for _, scenario := range []string{"kind", "key", "http", "userinfo", "query", "fragment", "allowlist", "private", "proxy", "type"} {
		t.Run(scenario, func(t *testing.T) {
			a := gatewayManagementFixture()
			upstream := &httpUpstreamRecorder{}
			svc := &AccountTestService{httpUpstream: upstream, cfg: upstreamModelSyncTestConfig()}
			switch scenario {
			case "kind":
				a.Credentials["upstream_kind"] = "other"
			case "key":
				a.Credentials["api_key"] = ""
			case "http":
				a.Credentials["base_url"] = "http://gateway.example"
			case "userinfo":
				a.Credentials["base_url"] = "https://user:secret@gateway.example"
			case "query":
				a.Credentials["base_url"] = "https://gateway.example?key=secret"
			case "fragment":
				a.Credentials["base_url"] = "https://gateway.example/#secret"
			case "allowlist":
				svc.cfg.Security.URLAllowlist.Enabled = true
				svc.cfg.Security.URLAllowlist.UpstreamHosts = []string{"allowed.example"}
			case "private":
				a.Credentials["base_url"] = "https://127.0.0.1"
				svc.cfg.Security.URLAllowlist.Enabled = true
				svc.cfg.Security.URLAllowlist.UpstreamHosts = []string{"127.0.0.1"}
			case "proxy":
				id := int64(7)
				a.ProxyID = &id
			case "type":
				a.Type = AccountTypeAPIKey
			}
			_, err := svc.SyncUpstreamModelCatalog(context.Background(), a)
			require.Error(t, err)
			require.Empty(t, upstream.requests)
		})
	}
}

func TestGatewayUpstreamModelsFailureKeepsMapping(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway} {
		svc, repo, _ := modelMappingSyncFixture(status, `{"error":"private-detail"}`)
		repo.account.Platform, repo.account.Type = PlatformGateway, AccountTypeGateway
		repo.account.Credentials["upstream_kind"] = "sub2api"
		_, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, UpstreamModelSyncOptions{Apply: true})
		require.Error(t, err)
		require.Zero(t, repo.applyCalls)
	}
	for _, body := range []string{`not-json`, `{"data":[]}`, `{"data":[{"id":"new"}],"has_more":true}`} {
		svc, repo, upstream := modelMappingSyncFixture(http.StatusOK, body)
		repo.account.Platform, repo.account.Type = PlatformGateway, AccountTypeGateway
		repo.account.Credentials["upstream_kind"] = "new_api"
		upstream.resp.Body = io.NopCloser(strings.NewReader(body))
		catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, UpstreamModelSyncOptions{Apply: true})
		require.True(t, err != nil || catalog.Skipped == "unverified_catalog")
		require.Zero(t, repo.applyCalls)
	}
}
