//go:build unit && bps_request_integration

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func enableLocalSIWCRequestProxies(t *testing.T, gateway *OpenAIGatewayService, account *Account) {
	t.Helper()
	reauth, reader, repo, _, _, _ := newReauthTestService("acct-1")
	t.Cleanup(reauth.oauth.Stop)
	account.Credentials = cloneReauthMap(reader.account.Credentials)
	account.Extra[OpenAISIWCEnabledKey] = true
	account.Extra[OpenAIProxyModeKey] = OpenAIProxyModeRequest
	account.Extra[OpenAIProxyIDsKey] = []int64{1, 2, 3}
	reader.account = account
	store := &siwcTestRepo{reauthTestRepo: repo}
	raw, err := json.Marshal(siwcGrant{AccessToken: "independent-siwc", RefreshToken: "siwc-refresh", ClientID: "issued", Subject: "subscriber", Workspace: "acct-1", Email: "user@example.com", Scope: openai.SIWCDirectScope, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	require.NoError(t, err)
	store.ciphertext = "encrypted:" + string(raw)
	reauth.repo = store
	reauth.oauth.proxyRepo = gateway.proxyRepo
	gateway.excelOAuthReauth = reauth
}

func TestSIWCRequestProxyForwardUsesSelectedExit(t *testing.T) {
	observed := make(chan int, 1)
	svc, account := localBPSRequestProxyFixture(t, 4, func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		require.Equal(t, "api.openai.com", r.URL.Host)
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Equal(t, "Bearer independent-siwc", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		require.Empty(t, r.Header.Get("ChatGPT-Account-ID"))
		observed <- id
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	})
	enableLocalSIWCRequestProxies(t, svc, account)
	account.Extra[OpenAIProxyIDsKey] = []int64{2}
	code, response, err := forwardLocalBPSRequest(t.Context(), svc, account, []byte(`{"model":"gpt-5.4","input":"test","stream":true}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, response, "response.completed")
	require.Equal(t, 2, <-observed)
	require.Equal(t, "old-access", account.Credentials["access_token"])
}

func TestSIWCRequestProxyAccountTestUsesSelectedExit(t *testing.T) {
	observed := make(chan int, 1)
	gateway, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		require.Equal(t, "Bearer independent-siwc", r.Header.Get("Authorization"))
		observed <- id
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	})
	enableLocalSIWCRequestProxies(t, gateway, account)
	account.Extra[OpenAIProxyIDsKey] = []int64{2}
	svc := &AccountTestService{accountRepo: &requestProxyAccountTestRepo{account: account}, openaiGatewayService: gateway}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/accounts/300/test", nil)
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", ""))
	require.Contains(t, rec.Body.String(), "test_complete")
	require.Equal(t, 2, <-observed)
}

type siwcRequestProxyAdmissionRepo struct {
	AccountRepository
	request, latest *Account
	reads           int
}

func (r *siwcRequestProxyAdmissionRepo) GetOpenAITurnAdmission(context.Context, int64) (*Account, *Account, error) {
	r.reads++
	if r.reads == 1 {
		return r.request, nil, nil
	}
	return r.latest, nil, nil
}

func TestSIWCRequestProxyAdmissionModeChangeRejectsBeforeSend(t *testing.T) {
	for _, mode := range []string{OpenAIProxyModeRequest, OpenAIProxyModeSingle} {
		t.Run(mode, func(t *testing.T) {
			observed := make(chan int, 1)
			svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, _ *http.Request) {
				observed <- id
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, bpsRequestCompletedWire)
			})
			enableLocalSIWCRequestProxies(t, svc, account)
			account.Status, account.Schedulable = StatusActive, true
			account.Extra[OpenAIProxyIDsKey] = []int64{2}
			proxy, err := svc.proxyRepo.GetByID(t.Context(), 2)
			require.NoError(t, err)
			account.ProxyID, account.Proxy = &proxy.ID, proxy
			latest := *account
			latest.Extra = cloneReauthMap(account.Extra)
			latest.Extra[OpenAIProxyModeKey] = mode
			repo := &siwcRequestProxyAdmissionRepo{request: account, latest: &latest}
			svc.accountRepo, svc.requireLatestTurnAdmission = repo, true
			svc.cfg.Gateway.Scheduling.OpenAITurnAdmissionCacheTTLSeconds = 0
			_, _, err = forwardLocalBPSRequest(t.Context(), svc, account, []byte(`{"model":"gpt-5.4","input":"test","stream":true}`))
			require.GreaterOrEqual(t, repo.reads, 2)
			if mode == OpenAIProxyModeSingle {
				require.Error(t, err)
				require.True(t, IsOpenAITurnAdmissionError(err))
				require.Empty(t, observed, "a mode change must not bypass the acquired request scope even when the old single proxy matches its URL")
			} else {
				require.NoError(t, err)
				require.Equal(t, 2, <-observed)
			}
		})
	}
}

func TestSIWCRequestProxyCompatibilityUsesSelectedExit(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				observed := make(chan int, 1)
				svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					observed <- id
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, bpsRequestCompletedWire)
				})
				enableLocalSIWCRequestProxies(t, svc, account)
				account.Extra[OpenAIProxyIDsKey] = []int64{2}
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"test"}],"input":"test","max_tokens":32,"stream":%t}`, stream))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				var result *OpenAIForwardResult
				var err error
				switch path {
				case "/v1/chat/completions":
					result, err = svc.ForwardAsChatCompletions(t.Context(), c, account, body, "", "")
				case "/v1/messages":
					result, err = svc.ForwardAsAnthropic(t.Context(), c, account, body, "", "")
				default:
					result, err = svc.Forward(t.Context(), c, account, body)
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, http.StatusOK, rec.Code)
				require.NotEmpty(t, rec.Body.String())
				require.Equal(t, 2, <-observed)
			})
		}
	}
}

func TestSIWCRequestProxySameSessionDistributesSixThroughThree(t *testing.T) {
	arrived := make(chan int, 6)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	svc, account := localBPSRequestProxyFixture(t, 4, func(id int, w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		arrived <- id
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	})
	enableLocalSIWCRequestProxies(t, svc, account)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() {
			code, response, err := forwardLocalBPSRequest(ctx, svc, account, []byte(`{"model":"gpt-5.4","input":"test","stream":true,"prompt_cache_key":"same-session"}`))
			if err == nil && (code != http.StatusOK || !strings.Contains(response, "response.completed")) {
				err = fmt.Errorf("incomplete SIWC response: %d %s", code, response)
			}
			done <- err
		}()
	}
	hits := map[int]int{}
	for i := 0; i < 6; i++ {
		select {
		case id := <-arrived:
			hits[id]++
		case <-ctx.Done():
			t.Fatal("six SIWC requests did not reach selected local proxies")
		}
	}
	once.Do(func() { close(release) })
	for i := 0; i < 6; i++ {
		require.NoError(t, <-done)
	}
	require.Equal(t, map[int]int{1: 2, 2: 2, 3: 2}, hits)
	require.Equal(t, 6, account.Concurrency)
}

func TestSIWCRequestProxyUnavailableNeverSends(t *testing.T) {
	for _, invalid := range []any{[]int64{}, []int64{999999}, "invalid"} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			observed := make(chan int, 1)
			svc, account := localBPSRequestProxyFixture(t, 3, func(id int, _ http.ResponseWriter, _ *http.Request) { observed <- id })
			enableLocalSIWCRequestProxies(t, svc, account)
			account.Extra[OpenAIProxyIDsKey] = invalid
			// Observe every attempted send, including a direct/old-proxy attempt.
			capture := &httpUpstreamRecorder{err: fmt.Errorf("unexpected send")}
			svc.httpUpstream = capture
			account.Proxy = &Proxy{ID: 99, Protocol: "http", Host: "old.invalid", Port: 8080, FallbackMode: "direct"}
			code, response, err := forwardLocalBPSRequest(t.Context(), svc, account, []byte(`{"model":"gpt-5.4","input":"test","stream":true}`))
			require.Error(t, err)
			require.Equal(t, http.StatusServiceUnavailable, code)
			require.Contains(t, response, "openai_proxy_unavailable")
			require.Empty(t, capture.requests)
			require.Empty(t, observed)
		})
	}
}

func TestSIWCRequestProxyFinishedTurnReleasesExit(t *testing.T) {
	for _, terminal := range []string{"completed", "rejected", "canceled", "transport_failure"} {
		t.Run(terminal, func(t *testing.T) {
			arrived := make(chan int, 4)
			var mu sync.Mutex
			calls := 0
			svc, account := localBPSRequestProxyFixture(t, 4, func(id int, w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				mu.Lock()
				calls++
				order := calls
				mu.Unlock()
				arrived <- id
				if order <= 2 || (order == 3 && terminal == "canceled") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"held\",\"status\":\"in_progress\"}}\n\n")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				if order == 3 {
					switch terminal {
					case "rejected":
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"code":"bad_request","message":"fixture rejected"}}`)
						return
					case "transport_failure":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
						return
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, bpsRequestCompletedWire)
			})
			enableLocalSIWCRequestProxies(t, svc, account)
			account.Proxy = &Proxy{ID: 99, Protocol: "http", Host: "old.invalid", Port: 8080, FallbackMode: "direct"}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			body := []byte(`{"model":"gpt-5.4","input":"test","stream":true}`)
			start := func() (context.CancelFunc, <-chan error, int) {
				child, stop := context.WithCancel(ctx)
				done := make(chan error, 1)
				go func() { _, _, err := forwardLocalBPSRequest(child, svc, account, body); done <- err }()
				select {
				case id := <-arrived:
					return stop, done, id
				case <-ctx.Done():
					stop()
					t.Fatal("SIWC stream did not start")
					return nil, nil, 0
				}
			}
			stopA, doneA, proxyA := start()
			defer stopA()
			stopB, doneB, proxyB := start()
			defer stopB()
			stopTurn, doneTurn, finished := start()
			defer stopTurn()
			if terminal == "canceled" {
				stopTurn()
			}
			select {
			case err := <-doneTurn:
				if terminal == "completed" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			case <-ctx.Done():
				t.Fatal("finished SIWC turn did not release")
			}
			require.ElementsMatch(t, []int{1, 2, 3}, []int{proxyA, proxyB, finished})
			require.Empty(t, arrived, "a failed turn must not be replayed through another proxy")
			code, _, err := forwardLocalBPSRequest(ctx, svc, account, body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, finished, <-arrived, "reuse the released exit while the other streams stay busy")
			cancel()
			for _, done := range []<-chan error{doneA, doneB} {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("held SIWC stream did not finish")
				}
			}
			mu.Lock()
			total := calls
			mu.Unlock()
			require.Equal(t, 4, total)
		})
	}
}

// Refresh uses a real local HTTP proxy just like inference; only the external
// OAuth endpoint is replaced, and the production grant/CAS path stays intact.
type siwcRequestProxyTokenClient struct{ *reauthTestOAuthClient }

func (c *siwcRequestProxyTokenClient) SIWCToken(ctx context.Context, form url.Values, proxy, _ string) (*openai.TokenResponse, *openai.SIWCIdentity, error) {
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" {
		return nil, nil, fmt.Errorf("refresh must use a configured proxy")
	}
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://auth.openai.test/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	var token openai.TokenResponse
	err = json.NewDecoder(resp.Body).Decode(&token)
	return &token, nil, err
}

func TestSIWCRequestProxyRefreshAndInferenceKeepSelectedExit(t *testing.T) {
	observed := make(chan requestProxyObservation, 2)
	svc, account := localBPSRequestProxyFixture(t, 3, func(id int, w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		observed <- requestProxyObservation{id: id, path: r.URL.Path, body: raw}
		if r.URL.Path == "/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"refreshed-siwc","refresh_token":"rotated-siwc","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") == "Bearer independent-siwc" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"held\",\"status\":\"in_progress\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		require.Equal(t, "Bearer refreshed-siwc", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, bpsRequestCompletedWire)
	})
	enableLocalSIWCRequestProxies(t, svc, account)
	// Keep one real inference busy, then expire the saved grant. The next
	// request's refresh must follow its less-busy exit, rather than independently
	// taking the first configured maintenance proxy.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	heldCtx, stopHeld := context.WithCancel(ctx)
	defer stopHeld()
	heldDone := make(chan error, 1)
	go func() {
		_, _, err := forwardLocalBPSRequest(heldCtx, svc, account, []byte(`{"model":"gpt-5.4","input":"hold","stream":true}`))
		heldDone <- err
	}()
	var held requestProxyObservation
	select {
	case held = <-observed:
	case <-ctx.Done():
		t.Fatal("initial SIWC request did not reach a selected proxy")
	}
	reauth := svc.excelOAuthReauth
	store := reauth.repo.(*siwcTestRepo)
	var grant siwcGrant
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(store.ciphertext, "encrypted:")), &grant))
	grant.ExpiresAt = 0
	raw, err := json.Marshal(grant)
	require.NoError(t, err)
	store.ciphertext = "encrypted:" + string(raw)
	reauth.oauth.oauthClient = &siwcRequestProxyTokenClient{}
	code, _, err := forwardLocalBPSRequest(ctx, svc, account, []byte(`{"model":"gpt-5.4","input":"test","stream":true}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	refresh, inference := <-observed, <-observed
	require.Equal(t, "/oauth/token", refresh.path)
	require.Equal(t, "/v1/responses", inference.path)
	require.Equal(t, refresh.id, inference.id)
	require.NotEqual(t, held.id, inference.id)
	form, err := url.ParseQuery(string(refresh.body))
	require.NoError(t, err)
	require.Equal(t, "siwc-refresh", form.Get("refresh_token"))
	require.Equal(t, "old-refresh", account.Credentials["refresh_token"])
	stopHeld()
	select {
	case <-heldDone:
	case <-ctx.Done():
		t.Fatal("held SIWC inference did not finish")
	}
}
