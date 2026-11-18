//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type batchWhitelistReadOnlyRepo struct {
	*upstreamModelMappingRepoStub
	authErrors       atomic.Int32
	tempUnsched      atomic.Int32
	credentialWrites atomic.Int32
	extraWrites      atomic.Int32
}

func (r *batchWhitelistReadOnlyRepo) SetError(context.Context, int64, string) error {
	r.authErrors.Add(1)
	return nil
}

func (r *batchWhitelistReadOnlyRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempUnsched.Add(1)
	return nil
}

func (r *batchWhitelistReadOnlyRepo) UpdateCredentials(context.Context, int64, map[string]any) error {
	r.credentialWrites.Add(1)
	return nil
}

func (r *batchWhitelistReadOnlyRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	r.extraWrites.Add(1)
	return nil
}

type batchWhitelistTokenInvalidator struct{ calls atomic.Int32 }

func (i *batchWhitelistTokenInvalidator) InvalidateToken(context.Context, *Account) error {
	i.calls.Add(1)
	return nil
}

// Only persistence and BPS HTTP are replaced. Native discovery traverses the
// real Codex request builder, HTTP client, envelope parser and error handling.
type batchWhitelistReadFixture struct {
	service      *AccountTestService
	gateway      *OpenAIGatewayService
	repo         *batchWhitelistReadOnlyRepo
	invalidator  *batchWhitelistTokenInvalidator
	nativeCalls  atomic.Int32
	bpsCalls     atomic.Int32
	nativeStatus atomic.Int32
	bpsStatus    atomic.Int32
	nativeBody   atomic.Value
	bpsBody      atomic.Value
}

func newBatchWhitelistReadFixture(t *testing.T) *batchWhitelistReadFixture {
	t.Helper()
	f := &batchWhitelistReadFixture{}
	f.nativeStatus.Store(http.StatusOK)
	f.bpsStatus.Store(http.StatusOK)
	f.nativeBody.Store(`{"models":[{"slug":"gpt-native-a"},{"slug":"gpt-native-b"}]}`)
	f.bpsBody.Store(`{"allowed":true,"model_catalog":{"models":[{"id":"gpt-7-new"}]}}`)
	account := excelAccount()
	account.Credentials["refresh_token"] = "synthetic-refresh-token"
	account.Credentials["model_mapping"] = map[string]any{
		"gpt-native-b": "gpt-native-b", "native-alias": "gpt-native-b", "obsolete": "obsolete",
	}
	account.Extra["openai_excel_bps_models"] = []string{"gpt-7-new", "gpt-7-cached"}
	account.Extra["keep"] = "unchanged"
	f.repo = &batchWhitelistReadOnlyRepo{upstreamModelMappingRepoStub: &upstreamModelMappingRepoStub{account: account}}
	f.invalidator = &batchWhitelistTokenInvalidator{}
	f.gateway = newCodexModels401TestService(f.repo)
	f.gateway.accountRepo = f.repo
	f.gateway.rateLimitService.SetTokenCacheInvalidator(f.invalidator)
	f.gateway.httpUpstream = &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
		f.bpsCalls.Add(1)
		assert.Equal(t, http.MethodGet, req.Method)
		assert.Equal(t, "/basispoints/api/responses/access", req.URL.Path)
		assert.Equal(t, account.ID, id)
		response := ordinaryModelsUpstreamResponse(f.bpsBody.Load().(string))
		response.StatusCode = int(f.bpsStatus.Load())
		return response, nil
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		f.nativeCalls.Add(1)
		assert.Equal(t, http.MethodGet, req.Method)
		assert.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(f.nativeStatus.Load()))
		_, _ = w.Write([]byte(f.nativeBody.Load().(string)))
	}))
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() { chatgptCodexModelsURL = original; server.Close() })
	f.service = &AccountTestService{accountRepo: f.repo, openaiGatewayService: f.gateway}
	return f
}

func (f *batchWhitelistReadFixture) assertNoAccountSideEffects(t *testing.T, before []byte) {
	t.Helper()
	after, err := json.Marshal(f.repo.account)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after), "discovery must preserve credentials, mapping, status, cooldowns and Extra")
	require.Zero(t, f.repo.authErrors.Load(), "whitelist discovery must not disable an account")
	require.Zero(t, f.repo.tempUnsched.Load(), "whitelist discovery must not park an account")
	require.Zero(t, f.repo.credentialWrites.Load(), "whitelist discovery must not rotate credentials or recover tasks")
	require.Zero(t, f.repo.extraWrites.Load(), "whitelist discovery must not write health or catalog state")
	require.Zero(t, f.invalidator.calls.Load(), "whitelist discovery must not invalidate token caches")
	require.Zero(t, f.repo.claimCalls)
	require.False(t, f.gateway.isOpenAIAccountRuntimeBlocked(f.repo.account))
	require.Zero(t, f.gateway.openaiAccountRuntimeBlockSequence.Load(), "discovery must not mutate runtime health generations")
}

func TestBatchWhitelistBPSNativeStructured401IsReadOnly(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"token_revoked","message":"synthetic revoked token"}}`,
		`{"error":{"code":"token_invalidated"}}`,
		`{"detail":{"message":"invalid token"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := newBatchWhitelistReadFixture(t)
			f.nativeStatus.Store(http.StatusUnauthorized)
			f.nativeBody.Store(body)
			before, err := json.Marshal(f.repo.account)
			require.NoError(t, err)
			_, err = f.service.SyncUpstreamModelCatalogWithOptions(context.Background(), f.repo.account, whitelistOnlyOptions(t))
			require.Error(t, err)
			require.EqualValues(t, 1, f.bpsCalls.Load())
			require.EqualValues(t, 1, f.nativeCalls.Load())
			require.Zero(t, f.repo.applyCalls)
			f.assertNoAccountSideEffects(t, before)
		})
	}
}

func TestBatchWhitelistBPSNativeStaleFailureKeepsMapping(t *testing.T) {
	for _, admissionOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "admin", true: "admission owner"}[admissionOwner], func(t *testing.T) {
			f := newBatchWhitelistReadFixture(t)
			f.nativeBody.Store(`{"models":[{"slug":"gpt-native-a"}]}`)
			_, err := f.gateway.fetchNativeOpenAIModelsList(context.Background(), f.repo.account)
			require.NoError(t, err)
			expireCodexModelsManifestCache(f.gateway, 2*time.Minute)
			f.nativeStatus.Store(http.StatusServiceUnavailable)
			f.nativeBody.Store(`{"error":{"code":"unavailable"}}`)
			before, err := json.Marshal(f.repo.account)
			require.NoError(t, err)
			ctx := context.Background()
			if admissionOwner {
				var cancel context.CancelFunc
				ctx, cancel = WithAPIKeyAdmissionOwner(ctx)
				defer cancel()
			}
			_, err = f.service.SyncUpstreamModelCatalogWithOptions(ctx, f.repo.account, whitelistOnlyOptions(t))
			require.Error(t, err, "stale native cache cannot authorize a replacement whitelist")
			require.EqualValues(t, 2, f.nativeCalls.Load(), "this request must wait for its own native response")
			require.Zero(t, f.repo.applyCalls, "failed live discovery must keep native-b and its alias")
			f.assertNoAccountSideEffects(t, before)
		})
	}
}

func TestBatchWhitelistBPSNativeFreshCompleteSuccess(t *testing.T) {
	f := newBatchWhitelistReadFixture(t)
	// Both caches contain old but still fresh display catalogs. A destructive
	// sync must observe this round's successful responses from both sources.
	f.nativeBody.Store(`{"models":[{"slug":"gpt-native-a"}]}`)
	f.bpsBody.Store(`{"allowed":true,"model_catalog":{"models":[{"id":"gpt-7-cached"}]}}`)
	_, err := f.gateway.fetchNativeOpenAIModelsList(context.Background(), f.repo.account)
	require.NoError(t, err)
	_, err = f.gateway.FetchExcelBPSModelsList(context.Background(), f.repo.account)
	require.NoError(t, err)
	f.nativeBody.Store(`{"models":[{"slug":"gpt-native-a"},{"slug":"gpt-native-b"}]}`)
	f.bpsBody.Store(`{"allowed":true,"model_catalog":{"models":[{"id":"gpt-7-new"}]}}`)
	before, err := json.Marshal(f.repo.account)
	require.NoError(t, err)
	catalog, err := f.service.SyncUpstreamModelCatalogWithOptions(context.Background(), f.repo.account, whitelistOnlyOptions(t))
	require.NoError(t, err)
	require.True(t, catalog.Applied)
	assert.EqualValues(t, 2, f.nativeCalls.Load(), "native fresh cache must also be bypassed")
	assert.EqualValues(t, 2, f.bpsCalls.Load(), "BPS fresh cache must also be bypassed")
	require.Equal(t, map[string]string{
		"gpt-native-a": "gpt-native-a", "gpt-native-b": "gpt-native-b",
		"native-alias": "gpt-native-b", "gpt-7-new": "gpt-7-new",
	}, f.repo.mapping)
	require.Equal(t, 1, f.repo.applyCalls)
	f.assertNoAccountSideEffects(t, before)
}

func TestBatchWhitelistBPSFreshCacheCannotHideFailure(t *testing.T) {
	f := newBatchWhitelistReadFixture(t)
	_, err := f.gateway.FetchExcelBPSModelsList(context.Background(), f.repo.account)
	require.NoError(t, err)
	f.bpsStatus.Store(http.StatusUnauthorized)
	f.bpsBody.Store(`{"error":{"code":"token_revoked"}}`)
	before, err := json.Marshal(f.repo.account)
	require.NoError(t, err)
	_, err = f.service.SyncUpstreamModelCatalogWithOptions(context.Background(), f.repo.account, whitelistOnlyOptions(t))
	require.Error(t, err, "fresh BPS display cache must not mask this round's failure")
	require.EqualValues(t, 2, f.bpsCalls.Load())
	require.Zero(t, f.nativeCalls.Load())
	require.Zero(t, f.repo.applyCalls)
	f.assertNoAccountSideEffects(t, before)
}

func TestBatchWhitelistBPSExpiredGrantDoesNotRefresh(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "rejected"}[rejected], func(t *testing.T) {
			reauth, reader, grantRepo, updater, blocker := newExcelReauthTestService(t)
			reader.account.Status = StatusActive
			reader.account.Schedulable = true
			reader.account.Extra = map[string]any{"openai_excel_bps": true, "openai_excel_bps_models": []string{"gpt-7-new"}}
			expiry := time.Now().Add(-time.Hour)
			if rejected {
				expiry = time.Now().Add(time.Hour)
			}
			credentials := excelTestCredentials("read-only", expiry)
			credentials[excelAccessTokenRejectedKey] = rejected
			storeExcelTestCredentials(t, reauth, grantRepo, credentials)
			refreshClient := &excelRefreshTestClient{credentials: excelTestCredentials("fresh", time.Now().Add(time.Hour))}
			reauth.oauth.oauthClient = refreshClient
			ciphertext := grantRepo.ciphertext
			repo := &batchWhitelistReadOnlyRepo{upstreamModelMappingRepoStub: &upstreamModelMappingRepoStub{account: reader.account}}
			gateway := newCodexModels401TestService(repo)
			gateway.excelOAuthReauth = reauth
			gateway.httpUpstream = &codexModelsHTTPUpstreamStub{do: func(*http.Request, string, int64, int) (*http.Response, error) {
				t.Error("unready grant must not reach a catalog endpoint")
				return nil, errors.New("synthetic catalog must not be called")
			}}
			svc := &AccountTestService{accountRepo: repo, openaiGatewayService: gateway}
			_, err := svc.SyncUpstreamModelCatalogWithOptions(context.Background(), reader.account, whitelistOnlyOptions(t))
			require.Error(t, err)
			require.Zero(t, refreshClient.calls, "whitelist reads must not refresh expired/rejected grants")
			require.Equal(t, ciphertext, grantRepo.ciphertext)
			require.False(t, grantRepo.applied)
			require.False(t, updater.applied)
			require.Zero(t, blocker.clearedAccountID)
			require.Zero(t, repo.applyCalls)
		})
	}
}

func TestBatchWhitelistBPSExpiredLegacyTokenDoesNotDisable(t *testing.T) {
	f := newBatchWhitelistReadFixture(t)
	f.repo.account.Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	delete(f.repo.account.Credentials, "refresh_token")
	f.gateway.openAITokenProvider = NewOpenAITokenProvider(f.repo, nil, nil)
	f.gateway.openAITokenProvider.SetAccountRuntimeBlocker(f.gateway)
	before, err := json.Marshal(f.repo.account)
	require.NoError(t, err)
	_, err = f.service.SyncUpstreamModelCatalogWithOptions(context.Background(), f.repo.account, whitelistOnlyOptions(t))
	require.Error(t, err)
	require.Zero(t, f.bpsCalls.Load())
	require.Zero(t, f.nativeCalls.Load())
	require.Zero(t, f.repo.applyCalls)
	f.assertNoAccountSideEffects(t, before)
}
