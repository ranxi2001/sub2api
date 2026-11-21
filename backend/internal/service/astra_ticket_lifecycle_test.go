package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type astraTicketRepo struct {
	AccountRepository
	mu       sync.Mutex
	accounts map[int64]*Account
	writes   int
	err      error
}

func (r *astraTicketRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.accounts[id]; a != nil {
		copy := *a
		return &copy, nil
	}
	return nil, errors.New("missing fixture")
}
func (r *astraTicketRepo) UpdateExtra(_ context.Context, id int64, extra map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.writes++
	a := *r.accounts[id]
	a.Extra = MergeOpenAICodexTicketExtra(extra, a.Extra)
	// UpdateExtra merges keys; preserve a detached account snapshot in this mock.
	for key, value := range extra {
		a.Extra[key] = value
	}
	r.accounts[id] = &a
	return nil
}

func unifiedAstraFixture(t *testing.T) (*OpenAIGatewayService, *astraTicketRepo, *openAICodexTicket) {
	t.Helper()
	a, b := ticketTestAccount(1), ticketTestAccount(2)
	a.Credentials = map[string]any{"access_token": "source-token", "chatgpt_account_id": "source-workspace"}
	b.Credentials = map[string]any{"access_token": "target-token", "chatgpt_account_id": "target-workspace"}
	repo := &astraTicketRepo{accounts: map[int64]*Account{1: a, 2: b}}
	s := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 780, TTLSeconds: 240, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://self.invalid:8080", HarvestAttemptTimeoutSeconds: 10}, nil)
	s.accountRepo = repo
	s.cfg.Gateway.CodexGatewayPin = config.CodexGatewayPinConfig{Enabled: true, SourceAccountIDs: []int64{1}, TargetAccountIDs: []int64{2}, IPAffinity: true, TTLSeconds: 240}
	now := time.Now().Add(-5 * time.Second)
	seed := &openAICodexTicket{AccountID: 1, Model: "gpt-6-astra", State: mint780State(now), Length: 780, Blocks: 33, IssuedAt: now, CapturedAt: now, ExpiresAt: now.Add(240 * time.Second), ProbeVerified: true, Transport: "sse", Gateway: "unified-42", HarvestCookies: mint780Pair(time.Now().Add(time.Hour), "unified-42"), HarvestProxyURL: "http://source.invalid:8080"}
	require.NoError(t, s.storeOpenAICodexTicket(t.Context(), a, seed))
	repo.writes = 0
	return s, repo, seed
}

func targetTicketHeaders() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer target-token")
	h.Set("ChatGPT-Account-ID", "target-workspace")
	h.Set("Cookie", "__oailb=unrelated; __cflb=unrelated")
	h.Set(openAICodexTurnStateHeader, "unrelated")
	return h
}

func TestUnifiedAstraReusePersistsTargetBundleAndUsesItWithoutReprobing(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	targetState := mint780State(time.Now().Add(-time.Second))
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
		calls++
		require.Equal(t, "http://source.invalid:8080", proxy)
		require.Equal(t, "Bearer target-token", req.Header.Get("Authorization"))
		require.Equal(t, "target-workspace", req.Header.Get("ChatGPT-Account-ID"))
		require.Equal(t, strings.Join(seed.HarvestCookies, "; "), req.Header.Get("Cookie"))
		require.True(t, IsOpenAICodexStateProbeRequest(req.Context()))
		h := http.Header{}
		if calls == 1 {
			require.Equal(t, seed.State, req.Header.Get(openAICodexTurnStateHeader))
			h.Set(openAICodexTurnStateHeader, targetState)
		} else {
			require.Equal(t, targetState, req.Header.Get(openAICodexTurnStateHeader))
		}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
	}}
	require.NoError(t, s.VerifyStoredAstraTarget(t.Context(), 2))
	h := targetTicketHeaders()
	proxy, release, err := s.ApplyStoredAstraTarget(t.Context(), h, 2, "sse")
	require.NoError(t, err)
	require.True(t, s.codexTicketChatHeld(2))
	release()
	require.False(t, s.codexTicketChatHeld(2))
	require.Equal(t, 2, calls)
	require.Equal(t, 1, repo.writes)
	require.Equal(t, "http://source.invalid:8080", proxy)
	require.Equal(t, targetState, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, strings.Join(seed.HarvestCookies, "; "), h.Get("Cookie"))
	a, _ := repo.GetByID(t.Context(), 2)
	ticket := s.lookupOpenAICodexTicket(a, "gpt-6-astra")
	require.Equal(t, "reuse", ticket.Acquisition)
	require.Equal(t, int64(1), ticket.SourceAccountID)
	require.True(t, ticketIdentityMatches(a, ticket))
	require.NotEqual(t, seed.State, ticket.State)
	// A fresh service recovers exactly the same material from persistence.
	restarted := ticketTestService(t, s.openAICodexTicketConfig(), nil)
	restarted.accountRepo = repo
	restarted.cfg.Gateway.CodexGatewayPin = s.cfg.Gateway.CodexGatewayPin
	h = targetTicketHeaders()
	_, release, err = restarted.ApplyStoredAstraTarget(t.Context(), h, 2, "sse")
	require.NoError(t, err)
	release()
	require.Equal(t, targetState, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, 2, calls)
	require.True(t, astraAccountSchedulingReady(s.cfg.AstraRouting(t.Context()), s.StoredAstraSnapshot(t.Context()), 2, time.Now()))
	public, err := json.Marshal(s.StoredAstraSnapshot(t.Context()))
	require.NoError(t, err)
	for _, secret := range []string{targetState, seed.State, "source-token", "target-token", "source.invalid"} {
		require.NotContains(t, string(public), secret)
	}
}

func TestUnifiedAstraReuseRejectsUnverifiedOrUnpersistedTarget(t *testing.T) {
	for _, kind := range []string{"echo_source", "reissued_target", "changed_route", "incomplete", "unauthorized", "persist", "cancel", "identity_change", "configuration_change"} {
		t.Run(kind, func(t *testing.T) {
			s, repo, seed := unifiedAstraFixture(t)
			calls := 0
			// State fixtures encode whole seconds; derive both from one clock read.
			issuedAt := time.Now().Add(-time.Second)
			target := mint780State(issuedAt)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "persist" {
				repo.err = errors.New("private database detail")
			}
			s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
				calls++
				h := http.Header{}
				status := 200
				body := harvestProbeCompleteFixture("gpt-6-astra")
				if calls == 1 {
					h.Set(openAICodexTurnStateHeader, target)
					if kind == "echo_source" {
						h.Set(openAICodexTurnStateHeader, seed.State)
					}
				}
				if kind == "unauthorized" {
					status = 401
				}
				if calls == 2 {
					switch kind {
					case "reissued_target":
						h.Set(openAICodexTurnStateHeader, mint780State(issuedAt.Add(-time.Second)))
					case "changed_route":
						h["Set-Cookie"] = mint780Pair(time.Now().Add(time.Hour), "unified-43")
					case "incomplete":
						body = "data: {\"type\":\"response.created\"}\n\n"
					case "cancel":
						cancel()
					case "identity_change":
						repo.accounts[2].Credentials = map[string]any{"access_token": "other", "chatgpt_account_id": "other"}
					case "configuration_change":
						s.cfg.Gateway.CodexGatewayPin.TargetAccountIDs = nil
					}
				}
				return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			}}
			err := s.VerifyStoredAstraTarget(ctx, 2)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private")
			require.LessOrEqual(t, calls, 2)
			require.Zero(t, repo.writes)
			a, _ := repo.GetByID(t.Context(), 2)
			require.False(t, s.lookupOpenAICodexTicket(a, "gpt-6-astra").valid(time.Now(), 780))
		})
	}
}

func TestUnifiedAstraReuseBudgetAndProtocol(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(_ *http.Request, _ string) (*http.Response, error) {
		calls++
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, mint780State(time.Now()))
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
	}}
	_, _, err := s.ApplyStoredAstraTarget(t.Context(), targetTicketHeaders(), 2, "websocket")
	require.Error(t, err)
	require.Zero(t, calls)
	left := 1
	a, _ := repo.GetByID(t.Context(), 2)
	ok, result := s.tryBorrowStoredAstraTicket(t.Context(), a, "gpt-6-astra", time.Second, func() bool {
		if left == 0 {
			return false
		}
		left--
		return true
	})
	require.True(t, ok)
	require.NotEqual(t, "success", result.Kind)
	require.Equal(t, 1, calls)
	require.Zero(t, repo.writes)
	seed.Revoked = true
	repo.accounts[1].Extra["codex_skip_harvest"] = true
	require.Error(t, s.PrepareStoredAstraSources(t.Context()))
}

func TestUnifiedAstraSourceBootstrapUsesSameStoreAndBudget(t *testing.T) {
	s, repo, _ := unifiedAstraFixture(t)
	s.openaiCodexTickets.Delete(openAICodexTicketKey(1, "gpt-6-astra"))
	repo.accounts[1].Extra = nil
	s.cfg.Gateway.OpenAICodexTicket.MaxProbesPerRound = 2
	calls := 0
	state := mint780State(time.Now())
	cookies := mint780Pair(time.Now().Add(time.Hour), "unified-77")
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
		calls++
		require.Equal(t, "Bearer source-token", req.Header.Get("Authorization"))
		require.Equal(t, "http://self.invalid:8080", proxy)
		h := http.Header{}
		if calls == 1 {
			require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
			h.Set(openAICodexTurnStateHeader, state)
			h["Set-Cookie"] = cookies
		} else {
			require.Equal(t, state, req.Header.Get(openAICodexTurnStateHeader))
		}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
	}}
	require.NoError(t, s.PrepareStoredAstraSources(t.Context()))
	require.Equal(t, 2, calls)
	require.Equal(t, 2, repo.writes, "ticket and probe summary are separate existing writes")
	_, donor := s.storedAstraDonor(t.Context(), 2)
	require.Equal(t, state, donor.State)
	require.Equal(t, "self", donor.Acquisition)
	require.NoError(t, s.PrepareStoredAstraSources(t.Context()))
	require.Equal(t, 2, calls, "ready source must not be recollected")
}

func TestUnifiedAstraObservesTicketInstalledOnClonedBusinessRequest(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	account, _ := repo.GetByID(t.Context(), 2)
	state := mint780State(time.Now())
	body := &codexTicketHeaderOnlyBody{}
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
		calls++
		req = req.Clone(req.Context())
		// Emulate the fast adapter applying its bundle after requesttiming
		// cloned the original business request's header map.
		ticket := *seed
		ticket.Identity = ""
		ticket.AccountID, ticket.State = account.ID, state
		require.NoError(t, s.storeOpenAICodexTicket(req.Context(), account, &ticket))
		req.Header.Set(openAICodexTurnStateHeader, state)
		return &http.Response{StatusCode: 200, Request: req, Header: http.Header{http.CanonicalHeaderKey(openAICodexTurnStateHeader): []string{"malformed"}}, Body: body}, nil
	}}
	values := map[string]string{SettingKeyOpenAICodexTicketStrict: "true"}
	s.settingService = NewSettingService(&codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: values}}, s.cfg)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	resp, err := s.doOpenAIUpstream(req, "", account)
	require.ErrorIs(t, err, ErrCodexTicketResponseRejected)
	require.Nil(t, resp)
	require.Equal(t, 1, calls)
	require.Zero(t, body.reads)
	require.Equal(t, 1, body.closes)
	require.Empty(t, req.Header.Get(openAICodexTurnStateHeader), "trace clone must remain independent")
	require.False(t, s.lookupOpenAICodexTicket(account, "gpt-6-astra").valid(time.Now(), 780))
}

func TestUnifiedAstraOwnTicketWinsAndHarvestLockPreventsDuplicateAcquisition(t *testing.T) {
	s, repo, seed := unifiedAstraFixture(t)
	s.codexHarvestRunMu.Lock()
	err := s.VerifyStoredAstraTarget(t.Context(), 2)
	require.EqualError(t, err, "target_validation_in_progress")
	s.codexHarvestRunMu.Unlock()
	account, _ := repo.GetByID(t.Context(), 2)
	own := *seed
	own.AccountID = 2
	own.Identity = ""
	own.State = mint780State(time.Now())
	own.HarvestProxyURL = "http://own.invalid:8080"
	own.Acquisition = "self"
	own.HarvestCookies = mint780Pair(time.Now().Add(time.Hour), "unified-99")
	own.Gateway = "unified-99"
	require.NoError(t, s.storeOpenAICodexTicket(t.Context(), account, &own))
	s.httpUpstream = &harvestProxyUpstream{do: func(*http.Request, string) (*http.Response, error) {
		t.Fatal("own ready ticket must not probe")
		return nil, nil
	}}
	h := targetTicketHeaders()
	proxy, release, err := s.ApplyStoredAstraTarget(t.Context(), h, 2, "sse")
	require.NoError(t, err)
	release()
	require.Equal(t, "http://own.invalid:8080", proxy)
	require.Equal(t, own.State, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, strings.Join(own.HarvestCookies, "; "), h.Get("Cookie"))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, chatgptCodexURL, nil)
	req.Header = h
	bound := s.stickBoundCodexTicketRequest(req, account)
	require.True(t, IsBoundAstraTicketRequest(bound.Context()))
	until := time.Now().Add(time.Hour)
	repo.accounts[2].RateLimitResetAt = &until
	require.Error(t, s.VerifyStoredAstraTarget(t.Context(), 2), "an existing ticket must not clear cooldowns")
}

func TestUnifiedAstraBackgroundFallsBackToSelfWithinSharedBudget(t *testing.T) {
	for _, budget := range []int{2, 4} {
		t.Run(string(rune('0'+budget)), func(t *testing.T) {
			s, repo, seed := unifiedAstraFixture(t)
			s.cfg.Gateway.OpenAICodexTicket.MaxProbesPerRound = budget
			calls := 0
			borrowed := mint780State(time.Now().Add(-2 * time.Second))
			self := mint780State(time.Now().Add(-time.Second))
			selfCookies := mint780Pair(time.Now().Add(time.Hour), "unified-99")
			s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
				calls++
				h := http.Header{}
				switch calls {
				case 1:
					require.Equal(t, seed.State, req.Header.Get(openAICodexTurnStateHeader))
					h.Set(openAICodexTurnStateHeader, borrowed)
				case 2:
					require.Equal(t, borrowed, req.Header.Get(openAICodexTurnStateHeader))
					h.Set(openAICodexTurnStateHeader, self) // Failed reuse must not be persisted.
				case 3:
					require.Equal(t, "http://self.invalid:8080", proxy)
					require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
					require.Empty(t, req.Header.Get("Cookie"))
					h.Set(openAICodexTurnStateHeader, self)
					h["Set-Cookie"] = selfCookies
				case 4:
					require.Equal(t, self, req.Header.Get(openAICodexTurnStateHeader))
					require.Equal(t, strings.Join(selfCookies, "; "), req.Header.Get("Cookie"))
				default:
					t.Fatal("exceeded shared acquisition budget")
				}
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
			}}
			account, _ := repo.GetByID(t.Context(), 2)
			s.probeOnceOpenAICodexTicket(t.Context(), account, "gpt-6-astra")
			require.Equal(t, budget, calls)
			ticket := s.lookupOpenAICodexTicket(account, "gpt-6-astra")
			require.Equal(t, budget == 4, ticket.valid(time.Now(), 780))
			if budget == 4 {
				require.Equal(t, self, ticket.State)
				require.Equal(t, "http://self.invalid:8080", ticket.HarvestProxyURL)
				require.Zero(t, ticket.SourceAccountID)
			}
		})
	}
}

func TestUnifiedAstraConcurrentVerificationDoesNotDuplicateMint(t *testing.T) {
	s, _, _ := unifiedAstraFixture(t)
	started, proceed := make(chan struct{}), make(chan struct{})
	calls := 0
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
		calls++
		h := http.Header{}
		if calls == 1 {
			close(started)
			select {
			case <-proceed:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			h.Set(openAICodexTurnStateHeader, mint780State(time.Now()))
		}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(harvestProbeCompleteFixture("gpt-6-astra")))}, nil
	}}
	done := make(chan error, 1)
	go func() { done <- s.VerifyStoredAstraTarget(t.Context(), 2) }()
	<-started
	require.EqualError(t, s.VerifyStoredAstraTarget(t.Context(), 2), "target_validation_in_progress")
	close(proceed)
	require.NoError(t, <-done)
	require.Equal(t, 2, calls)
}

func TestUnifiedAstraForwardingNeverAcquiresMissingTicket(t *testing.T) {
	s, repo, _ := unifiedAstraFixture(t)
	s.httpUpstream = &harvestProxyUpstream{do: func(*http.Request, string) (*http.Response, error) {
		t.Fatal("forwarding must not probe")
		return nil, nil
	}}
	for _, cooling := range []bool{false, true} {
		if cooling {
			s.openaiCodexTicketProbeCooldown.Store(openAICodexTicketKey(2, "gpt-6-astra"), time.Now().Add(time.Minute))
		}
		h := targetTicketHeaders()
		before := h.Clone()
		_, release, err := s.ApplyStoredAstraTarget(t.Context(), h, 2, "sse")
		require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
		release()
		require.Equal(t, before, h)
		require.Zero(t, repo.writes)
	}
}
