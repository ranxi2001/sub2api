package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type credentialProbeHTTPFixture struct {
	status  atomic.Int32
	calls   atomic.Int32
	headers chan http.Header
}

func newCredentialProbeHTTPFixture(t *testing.T) *credentialProbeHTTPFixture {
	t.Helper()
	f := &credentialProbeHTTPFixture{headers: make(chan http.Header, 16)}
	f.status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		f.calls.Add(1)
		f.headers <- request.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"synthetic-catalog"`)
		status := int(f.status.Load())
		w.WriteHeader(status)
		switch status {
		case http.StatusOK:
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6-sol"}]}`))
		case http.StatusUnauthorized:
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_token","message":"synthetic-private-provider-response"}}`))
		case http.StatusForbidden:
			_, _ = w.Write([]byte(`{"error":{"code":"session_blocked_by_cyber_policy","message":"synthetic-private-provider-response"}}`))
		}
	}))
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() {
		chatgptCodexModelsURL = original
		server.Close()
	})
	return f
}

func credentialProbeCacheSize(gateway *OpenAIGatewayService) int {
	gateway.openAIModelsCache.mu.Lock()
	defer gateway.openAIModelsCache.mu.Unlock()
	return len(gateway.openAIModelsCache.entries)
}

func TestAccountTokenGuardV2FreshProbeBypassesWarmAndStaleSuccess(t *testing.T) {
	for _, test := range []struct {
		name string
		age  time.Duration
	}{{"warm", 0}, {"stale", 2 * time.Minute}} {
		t.Run(test.name, func(t *testing.T) {
			upstream := newCredentialProbeHTTPFixture(t)
			gateway := &OpenAIGatewayService{}
			account := newCodexModelsTestAccount()
			_, err := gateway.FetchOpenAIModelsList(context.Background(), account)
			require.NoError(t, err)
			require.EqualValues(t, 1, upstream.calls.Load())
			if test.age != 0 {
				expireCodexModelsManifestCache(gateway, test.age)
			}
			upstream.status.Store(http.StatusUnauthorized)
			response, err := gateway.FetchOpenAIModelsListFresh(context.Background(), account)
			require.Nil(t, response)
			var rejected *codexModelsManifestUpstreamError
			require.ErrorAs(t, err, &rejected)
			require.Equal(t, http.StatusUnauthorized, rejected.statusCode)
			require.EqualValues(t, 2, upstream.calls.Load(), "every inspection must wait for its own current upstream response")
			require.Equal(t, 1, credentialProbeCacheSize(gateway))
		})
	}
}

func TestAccountTokenGuardV2FreshProbeDoesNotReadOrPublishCatalogCache(t *testing.T) {
	upstream := newCredentialProbeHTTPFixture(t)
	gateway := &OpenAIGatewayService{}
	account := newCodexModelsTestAccount()
	fresh, err := gateway.FetchOpenAIModelsListFresh(context.Background(), account)
	require.NoError(t, err)
	require.Contains(t, string(fresh.Body), `"id":"gpt-6-sol"`)
	require.Zero(t, credentialProbeCacheSize(gateway), "an inspection must not publish shared directory evidence")
	firstHeaders := <-upstream.headers
	require.Empty(t, firstHeaders.Get("If-None-Match"))
	require.Equal(t, "no-cache", firstHeaders.Get("Cache-Control"))
	require.Equal(t, "Bearer test-access-token", firstHeaders.Get("Authorization"))
	require.Equal(t, "acc-123", firstHeaders.Get("ChatGPT-Account-ID"))

	_, err = gateway.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	_, err = gateway.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	require.EqualValues(t, 2, upstream.calls.Load(), "ordinary directory callers still share their cached catalog")
	ordinaryHeaders := <-upstream.headers
	require.Empty(t, ordinaryHeaders.Get("Cache-Control"))
	gateway.openAIModelsCache.mu.Lock()
	var originalExpiry time.Time
	for _, entry := range gateway.openAIModelsCache.entries {
		originalExpiry = entry.expiresAt
	}
	gateway.openAIModelsCache.mu.Unlock()

	_, err = gateway.FetchOpenAIModelsListFresh(context.Background(), account)
	require.NoError(t, err)
	require.EqualValues(t, 3, upstream.calls.Load())
	secondHeaders := <-upstream.headers
	require.Empty(t, secondHeaders.Get("If-None-Match"), "a cached upstream ETag must not turn an inspection into a catalog revalidation")
	gateway.openAIModelsCache.mu.Lock()
	defer gateway.openAIModelsCache.mu.Unlock()
	require.Len(t, gateway.openAIModelsCache.entries, 1)
	for _, entry := range gateway.openAIModelsCache.entries {
		require.Equal(t, originalExpiry, entry.expiresAt, "inspections must not extend directory cache validity")
	}
}

func TestAccountTokenGuardV2FreshProbeRejectsUnexpectedNotModified(t *testing.T) {
	upstream := newCredentialProbeHTTPFixture(t)
	gateway := &OpenAIGatewayService{}
	account := newCodexModelsTestAccount()
	_, err := gateway.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	upstream.status.Store(http.StatusNotModified)
	response, err := gateway.FetchOpenAIModelsListFresh(context.Background(), account)
	require.Error(t, err)
	require.Nil(t, response, "304 must not reuse a previous inspection result")
	_, err = gateway.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err, "ordinary model reads retain their existing cache behavior")
	require.EqualValues(t, 2, upstream.calls.Load())
}

func TestAccountTokenGuardV2FreshProbeCannotUseBPSGrantAsNativeProof(t *testing.T) {
	upstream := newCredentialProbeHTTPFixture(t)
	upstream.status.Store(http.StatusUnauthorized)
	var bpsCalls atomic.Int32
	gateway := openAIClientToolsTestService(nil)
	gateway.httpUpstream = &codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		bpsCalls.Add(1)
		return ordinaryModelsUpstreamResponse(bpsAccessFixture), nil
	}}
	account := excelAccount()
	models, err := gateway.FetchOpenAIModelsList(context.Background(), account)
	require.NoError(t, err)
	require.Contains(t, string(models.Body), "gpt-7-new")
	require.EqualValues(t, 1, bpsCalls.Load())
	require.Zero(t, upstream.calls.Load(), "an all-BPS catalog intentionally does not inspect the native credential")
	guard := &AccountTokenGuardV2Service{prober: gateway}
	state, detail := guard.probeAccount(context.Background(), account)
	require.Equal(t, AccountTokenGuardV2ProbeAuth, state)
	require.EqualValues(t, 1, upstream.calls.Load())
	require.EqualValues(t, 1, bpsCalls.Load(), "credential inspection must not consult the separate BPS grant")
	require.NotContains(t, detail, "synthetic-private")
}

func TestAccountTokenGuardV2FreshProbePreserves403PolicyClassification(t *testing.T) {
	upstream := newCredentialProbeHTTPFixture(t)
	upstream.status.Store(http.StatusForbidden)
	guard := &AccountTokenGuardV2Service{prober: &OpenAIGatewayService{}}
	state, detail := guard.probeAccount(context.Background(), newCodexModelsTestAccount())
	require.Equal(t, AccountTokenGuardV2ProbeTransient, state)
	require.Equal(t, accountTokenGuardV2PolicyBlocked, detail)
	require.EqualValues(t, 1, upstream.calls.Load())
}

func TestAccountTokenGuardV2FreshProbeCancellationStopsItsUpstreamRequest(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(stopped)
	}))
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() {
		chatgptCodexModelsURL = original
		server.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	gateway := &OpenAIGatewayService{}
	done := make(chan error, 1)
	go func() {
		_, err := gateway.FetchOpenAIModelsListFresh(ctx, newCodexModelsTestAccount())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("credential inspection did not reach the mock upstream")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("credential inspection ignored caller cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("a background upstream request survived the cancelled inspection")
	}
	require.Zero(t, credentialProbeCacheSize(gateway))
}

func TestAccountTokenGuardV2FreshProbeCannotResetBudgetUsingCachedSuccess(t *testing.T) {
	upstream := newCredentialProbeHTTPFixture(t)
	gateway := &OpenAIGatewayService{}
	reauth, reader, _, _, _, _ := newReauthTestService("acct-1")
	_, err := gateway.FetchOpenAIModelsList(context.Background(), reader.account)
	require.NoError(t, err)
	finishedAt := time.Now().Add(-time.Minute)
	repo := &accountTokenGuardV2TestRepo{
		record: &AccountTokenGuardV2Record{
			AccountID: 42, Enabled: true, AutoReloginEnabled: true,
			ReloginFailStreak: 2, AutoReloginExhausted: true,
		},
		outcomes: []AccountReloginOutcome{{ID: 7, Status: OpenAIOAuthReauthStatusSucceeded, FinishedAt: &finishedAt}},
	}
	upstream.status.Store(http.StatusUnauthorized)
	guard := NewAccountTokenGuardV2Service(repo, nil, reader, gateway, reauth)
	t.Cleanup(guard.cancel)
	view, err := guard.ProbeNow(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, AccountTokenGuardV2ProbeAuth, view.ProbeState)
	require.Equal(t, 3, view.ReloginFailStreak, "a currently rejected token cannot clear its previous re-login failures")
	require.True(t, view.AutoReloginExhausted)
	require.EqualValues(t, 2, upstream.calls.Load())
	require.NotNil(t, view.ProbeFailure)
	require.Equal(t, "requires_relogin", view.ProbeFailure.Code)
}
