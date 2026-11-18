//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func whitelistOnlyOptions(t *testing.T) UpstreamModelSyncOptions {
	t.Helper()
	var options UpstreamModelSyncOptions
	require.NoError(t, json.Unmarshal([]byte(`{"apply":true,"whitelist_only":true}`), &options))
	return options
}

func TestBatchWhitelistSafety(t *testing.T) {
	for _, tc := range []struct {
		name, body            string
		platform, accountType string
		expected              map[string]string
		skipped               string
	}{
		{"normalize and live alias", `{"data":[{"id":" new "},{"id":"new"},{"id":"*"},{"id":""}]}`, PlatformOpenAI, AccountTypeAPIKey, map[string]string{"new": "new", "public-new": "new"}, ""},
		{"unconsumed page", `{"data":[{"id":"new"}],"has_more":true}`, PlatformOpenAI, AccountTypeAPIKey, nil, "unverified_catalog"},
		{"unconsumed token", `{"data":[{"id":"new"}],"nextPageToken":"next"}`, PlatformOpenAI, AccountTypeAPIKey, nil, "unverified_catalog"},
		{"wildcards", `{"data":[{"id":"*"}]}`, PlatformOpenAI, AccountTypeAPIKey, nil, "empty"},
		{"subscription exclusions", `{"models":[{"slug":"gpt-5.5"},{"slug":"gpt-5.4-mini"},{"slug":"gpt-image-1.5"},{"slug":"new"}]}`, PlatformOpenAI, AccountTypeOAuth, map[string]string{"new": "new", "public-new": "new"}, ""},
		{"grok normalization", `{"data":[{"id":"new","name":"New label"}]}`, PlatformGrok, AccountTypeAPIKey, map[string]string{"new": "new", "public-new": "new"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, up := modelMappingSyncFixture(http.StatusOK, tc.body)
			repo.account.Platform, repo.account.Type = tc.platform, tc.accountType
			repo.account.Credentials["access_token"] = "synthetic-oauth"
			catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, whitelistOnlyOptions(t))
			require.NoError(t, err)
			require.Equal(t, tc.skipped, catalog.Skipped)
			require.Equal(t, tc.expected, repo.mapping)
			require.Len(t, up.requests, 1, "only one catalog read, no registry or generation requests")
			require.Equal(t, http.MethodGet, up.requests[0].Method)
			require.Zero(t, repo.claimCalls)
		})
	}
	t.Run("account policy before discovery", func(t *testing.T) {
		svc, repo, up := modelMappingSyncFixture(200, `{"data":[{"id":"new"}]}`)
		repo.account.Type = AccountTypeOAuth
		repo.account.Credentials["model_mapping_mode"] = "aliases"
		catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, whitelistOnlyOptions(t))
		require.NoError(t, err)
		require.Equal(t, "account_policy", catalog.Skipped)
		require.Empty(t, up.requests)
		require.Zero(t, repo.applyCalls)
	})
	t.Run("concurrent credentials leave original mapping", func(t *testing.T) {
		svc, repo, _ := modelMappingSyncFixture(200, `{"data":[{"id":"new"}]}`)
		repo.changed = true
		catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, whitelistOnlyOptions(t))
		require.NoError(t, err)
		require.Equal(t, "account_changed", catalog.Skipped)
		require.Nil(t, repo.mapping)
	})
}

func TestBatchWhitelistVertexPagination(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "all pages", true: "partial failure"}[fail], func(t *testing.T) {
			second := vertexModelsJSONResponse(200, `{"publisherModels":[{"name":"publishers/google/models/gemini-b"}]}`)
			if fail {
				second = vertexModelsJSONResponse(500, `{"error":{}}`)
			}
			up := &httpUpstreamRecorder{responses: []*http.Response{
				vertexModelsJSONResponse(200, `{"publisherModels":[{"name":"publishers/google/models/gemini-a"},{"name":"publishers/google/models/gemma-hidden"}],"nextPageToken":"next"}`),
				second,
			}}
			svc := vertexModelSyncTestService(up)
			account := vertexModelSyncTestAccount(map[string]any{"model_mapping": map[string]any{"old": "old"}})
			repo := &upstreamModelMappingRepoStub{account: account}
			svc.accountRepo = repo
			catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), account, whitelistOnlyOptions(t))
			if fail {
				require.Error(t, err)
				require.Nil(t, repo.mapping)
			} else {
				require.NoError(t, err)
				require.True(t, catalog.Applied)
				require.Equal(t, map[string]string{"gemini-a": "gemini-a", "gemini-b": "gemini-b"}, repo.mapping)
			}
			require.Len(t, up.requests, 2)
			require.Equal(t, "next", up.requests[1].URL.Query().Get("pageToken"))
		})
	}
}

func TestBatchWhitelistBPSPartialCatalog(t *testing.T) {
	for _, failedSource := range []string{"bps", "native"} {
		t.Run(failedSource, func(t *testing.T) {
			account := excelAccount()
			account.Extra["openai_excel_bps_models"] = []string{"gpt-7-new"}
			account.Credentials["model_mapping"] = map[string]any{"old": "old"}
			gateway := openAIClientToolsTestService(nil)
			gateway.httpUpstream = &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				require.Equal(t, http.MethodGet, req.Method)
				isBPS := strings.Contains(req.URL.Path, "/access")
				if (isBPS && failedSource == "bps") || (!isBPS && failedSource == "native") {
					return nil, errors.New("synthetic unavailable source")
				}
				if isBPS {
					return ordinaryModelsUpstreamResponse(bpsAccessFixture), nil
				}
				return ordinaryModelsUpstreamResponse(`{"models":[{"slug":"gpt-native"}]}`), nil
			}}
			repo := &upstreamModelMappingRepoStub{account: account}
			svc := &AccountTestService{accountRepo: repo, openaiGatewayService: gateway}
			_, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), account, whitelistOnlyOptions(t))
			require.Error(t, err, "partial BPS/native catalogs cannot replace the entire whitelist")
			require.Zero(t, repo.applyCalls)
		})
	}
}
