//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type batchWhitelistOAuthCache struct {
	token  string
	reads  int
	writes int
	locks  int
}

func (c *batchWhitelistOAuthCache) GetAccessToken(context.Context, string) (string, error) {
	c.reads++
	return c.token, nil
}

func (c *batchWhitelistOAuthCache) SetAccessToken(context.Context, string, string, time.Duration) error {
	c.writes++
	return nil
}

func (c *batchWhitelistOAuthCache) DeleteAccessToken(context.Context, string) error {
	c.writes++
	return nil
}

func (c *batchWhitelistOAuthCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	c.locks++
	return true, nil
}

func (c *batchWhitelistOAuthCache) ReleaseRefreshLock(context.Context, string) error {
	c.locks++
	return nil
}

type batchWhitelistOAuthCooldown struct {
	TempUnschedCache
	writes int
}

func (c *batchWhitelistOAuthCooldown) SetTempUnsched(context.Context, int64, *TempUnschedState) error {
	c.writes++
	return nil
}

type batchWhitelistOAuthFixture struct {
	service       *AccountTestService
	repo          *batchWhitelistReadOnlyRepo
	cache         *batchWhitelistOAuthCache
	cooldown      *batchWhitelistOAuthCooldown
	executor      *refreshAPIExecutorStub
	upstream      *httpUpstreamRecorder
	ws            *agentIdentityWSInvalidationRecorder
	httpCalls     atomic.Int32
	backfillCalls int
}

// Keep the real token providers and refresh API wired: removing the whitelist
// guard must expose refresh success/failure and project backfill side effects.
// Every HTTP destination is synthetic, including Antigravity's direct client.
func newBatchWhitelistOAuthFixture(t *testing.T, platform, state string) *batchWhitelistOAuthFixture {
	t.Helper()
	svc, baseRepo, upstream := modelMappingSyncFixture(http.StatusServiceUnavailable, `{"error":"synthetic catalog failure"}`)
	account := baseRepo.account
	account.Platform, account.Type = platform, AccountTypeOAuth
	account.Credentials["access_token"] = "synthetic-access-token"
	account.Credentials["refresh_token"] = "synthetic-refresh-token"
	account.Credentials["expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	account.Credentials["tier_id"] = "synthetic-existing-tier"
	account.Extra = map[string]any{"keep": "unchanged"}
	cooldownUntil := time.Now().Add(2 * time.Minute)
	account.TempUnschedulableUntil = &cooldownUntil
	account.TempUnschedulableReason = "synthetic-existing-cooldown"
	if platform == PlatformAntigravity {
		account.Credentials["project_id"] = "synthetic-existing-project"
	}
	f := &batchWhitelistOAuthFixture{
		service: svc, upstream: upstream,
		repo:     &batchWhitelistReadOnlyRepo{upstreamModelMappingRepoStub: baseRepo},
		cache:    &batchWhitelistOAuthCache{},
		cooldown: &batchWhitelistOAuthCooldown{},
		executor: &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{
			"access_token": "synthetic-new-access-token", "refresh_token": "synthetic-new-refresh-token",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}},
		ws: &agentIdentityWSInvalidationRecorder{},
	}
	switch state {
	case "expired_refresh_success", "expired_refresh_error":
		account.Credentials["expires_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	case "missing_expiry":
		delete(account.Credentials, "expires_at")
	case "missing_project":
		delete(account.Credentials, "project_id")
		account.Credentials["auto_detect_project_id"] = "true"
	case "cached_token":
		f.cache.token = "synthetic-cached-token"
	}
	if state == "expired_refresh_error" {
		f.executor.err = errors.New("synthetic refresh failure")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		f.httpCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = io.WriteString(w, `{"cloudaicompanionProject":"synthetic-detected-project"}`)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"synthetic catalog failure"}`)
		}
	}))
	t.Cleanup(server.Close)
	withAntigravityModelSyncEndpoints(t, server.URL, server.URL)
	svc.accountRepo, svc.agentIdentityWS = f.repo, f.ws
	refreshAPI := NewOAuthRefreshAPI(f.repo, f.cache)
	if platform == PlatformAntigravity {
		provider := NewAntigravityTokenProvider(f.repo, f.cache, NewAntigravityOAuthService(nil))
		provider.SetRefreshAPI(refreshAPI, f.executor)
		provider.SetTempUnschedCache(f.cooldown)
		svc.antigravityGatewayService = &AntigravityGatewayService{tokenProvider: provider}
	} else {
		codeAssist := &mockGeminiCodeAssistClient{loadCodeAssistFunc: func(context.Context, string, string, *geminicli.LoadCodeAssistRequest) (*geminicli.LoadCodeAssistResponse, error) {
			f.backfillCalls++
			return &geminicli.LoadCodeAssistResponse{CloudAICompanionProject: "synthetic-detected-project"}, nil
		}}
		provider := NewGeminiTokenProvider(f.repo, f.cache, &GeminiOAuthService{codeAssist: codeAssist})
		provider.SetRefreshAPI(refreshAPI, f.executor)
		svc.geminiTokenProvider = provider
	}
	return f
}

func TestBatchWhitelistOAuthCredentialLifecycleRejected(t *testing.T) {
	for _, platform := range []string{PlatformAntigravity, PlatformGemini} {
		t.Run(platform, func(t *testing.T) {
			for _, state := range []string{"expired_refresh_success", "expired_refresh_error", "missing_expiry", "missing_project", "ready", "cached_token"} {
				t.Run(state, func(t *testing.T) {
					f := newBatchWhitelistOAuthFixture(t, platform, state)
					before, err := json.Marshal(f.repo.account)
					require.NoError(t, err)
					catalog, err := f.service.SyncUpstreamModelCatalogWithOptions(context.Background(), f.repo.account, whitelistOnlyOptions(t))
					var syncErr *UpstreamModelSyncError
					if assert.ErrorAs(t, err, &syncErr, "whitelist-only OAuth discovery must reject before entering token providers") {
						assert.Equal(t, UpstreamModelSyncErrorUnsupported, syncErr.Kind)
					}
					assert.Nil(t, catalog)
					assert.Zero(t, f.cache.reads, "whitelist-only OAuth discovery must not enter token providers")
					assert.Zero(t, f.cache.writes)
					assert.Zero(t, f.cache.locks)
					assert.Zero(t, f.executor.refreshCalls, "whitelist-only OAuth discovery must not refresh credentials")
					assert.Zero(t, f.backfillCalls, "whitelist-only OAuth discovery must not discover a project")
					assert.Zero(t, f.httpCalls.Load(), "whitelist-only OAuth discovery must not call catalog, backfill or task endpoints")
					assert.Empty(t, f.upstream.requests)
					assert.Zero(t, f.repo.credentialWrites.Load())
					assert.Zero(t, f.repo.authErrors.Load())
					assert.Zero(t, f.repo.tempUnsched.Load())
					assert.Zero(t, f.cooldown.writes)
					assert.Zero(t, f.repo.extraWrites.Load())
					assert.Zero(t, f.repo.claimCalls)
					assert.Zero(t, f.repo.applyCalls)
					assert.Nil(t, f.repo.mapping, "rejection must preserve the old mapping without CAS")
					assert.Empty(t, f.ws.accountIDs, "whitelist-only discovery must not create or recover a task")
					after, err := json.Marshal(f.repo.account)
					require.NoError(t, err)
					assert.JSONEq(t, string(before), string(after), "tokens, project/tier IDs, mapping, task, status and cooldown must remain unchanged")
				})
			}
		})
	}
}

func TestBatchWhitelistOAuthLegacySyncStillUsesTokenProvider(t *testing.T) {
	for _, platform := range []string{PlatformAntigravity, PlatformGemini} {
		t.Run(platform, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				options UpstreamModelSyncOptions
			}{
				{"discovery", UpstreamModelSyncOptions{}},
				{"manual_apply", UpstreamModelSyncOptions{Apply: true}},
				{"automatic_apply", UpstreamModelSyncOptions{Apply: true, Automatic: true}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f := newBatchWhitelistOAuthFixture(t, platform, "expired_refresh_error")
					_, err := f.service.SyncUpstreamModelCatalogWithOptions(context.Background(), f.repo.account, tc.options)
					require.Error(t, err)
					require.ErrorContains(t, err, "synthetic refresh failure")
					require.Equal(t, 1, f.executor.refreshCalls, "legacy sync retains request-path refresh behavior")
					require.Positive(t, f.cache.reads)
					expectedClaims := 0
					if tc.options.Automatic {
						expectedClaims = 1
					}
					require.Equal(t, expectedClaims, f.repo.claimCalls)
					expectedCooldownWrites := int32(0)
					if platform == PlatformAntigravity {
						expectedCooldownWrites = 1
					}
					require.Equal(t, expectedCooldownWrites, f.repo.tempUnsched.Load())
					require.EqualValues(t, expectedCooldownWrites, f.cooldown.writes)
					require.Zero(t, f.repo.applyCalls)
				})
			}
		})
	}
}

func TestBatchWhitelistOAuthPlatformAPIKeyStillApplies(t *testing.T) {
	for _, platform := range []string{PlatformAntigravity, PlatformGemini} {
		t.Run(platform, func(t *testing.T) {
			svc, repo, upstream := modelMappingSyncFixture(http.StatusOK, `{"models":[{"name":"models/new"}]}`)
			repo.account.Platform = platform
			if platform == PlatformAntigravity {
				repo.account.Credentials["base_url"] = "https://provider.example/antigravity"
				upstream.resp = ordinaryModelsUpstreamResponse(`{"data":[{"id":"new"}]}`)
			}
			before, err := json.Marshal(repo.account)
			require.NoError(t, err)
			catalog, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), repo.account, whitelistOnlyOptions(t))
			require.NoError(t, err)
			require.True(t, catalog.Applied)
			require.Equal(t, map[string]string{"new": "new", "public-new": "new"}, repo.mapping)
			require.Equal(t, 1, repo.applyCalls)
			require.Zero(t, repo.claimCalls)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, http.MethodGet, upstream.requests[0].Method)
			after, err := json.Marshal(repo.account)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
}
