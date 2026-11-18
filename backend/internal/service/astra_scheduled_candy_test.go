package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func candyStream(answer string) string {
	data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_test", "model": "gpt-6-astra", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": answer}}}}}})
	return "data: " + string(data) + "\n\n"
}

func candyFixture(t *testing.T) (*OpenAIGatewayService, *astraTicketRepo, *Account, *openAICodexTicket, http.Header) {
	t.Helper()
	s, repo, seed := unifiedAstraFixture(t)
	s.settingService = NewSettingService(&codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAICodexTicketCandyObservation: "true", SettingKeyOpenAICodexTicketEnabled: "true"}}}, s.cfg)
	a, _ := repo.GetByID(t.Context(), 2)
	ticket := *seed
	ticket.AccountID, ticket.Identity = a.ID, ticketIdentity(a)
	require.NoError(t, s.storeOpenAICodexTicket(t.Context(), a, &ticket))
	h := targetTicketHeaders()
	h.Set(openAICodexTurnStateHeader, ticket.State)
	restoreBoundCodexTicketHarvestIdentity(h, &ticket)
	return s, repo, a, &ticket, h
}

func waitCandy(t *testing.T, s *OpenAIGatewayService) {
	t.Helper()
	require.Eventually(t, func() bool { s.astraCandyMu.Lock(); defer s.astraCandyMu.Unlock(); return s.astraCandyRunning == 0 }, time.Second*3, time.Millisecond)
}

func TestScheduledAstraCandyIsObservationOnly(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"pass", "candy_passed"}, {"incorrect", "candy_incorrect"}, {"http", "candy_error"}, {"network", "candy_error"}, {"incomplete", "candy_error"}, {"reissued", "candy_ticket_changed"}, {"route", "candy_route_changed"}, {"deleted_cookie", "candy_route_changed"}, {"model", "candy_model_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, a, ticket, h := candyFixture(t)
			before, _ := json.Marshal(repo.accounts[2])
			writes := repo.writes
			calls := 0
			s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, proxy string) (*http.Response, error) {
				calls++
				require.Equal(t, "http://source.invalid:8080", proxy)
				require.Equal(t, h.Get("Authorization"), req.Header.Get("Authorization"))
				require.Equal(t, ticket.State, req.Header.Get(openAICodexTurnStateHeader))
				require.Equal(t, strings.Join(ticket.HarvestCookies, "; "), req.Header.Get("Cookie"))
				require.True(t, IsOpenAICodexStateProbeRequest(req.Context()))
				require.True(t, IsBoundAstraTicketRequest(req.Context()))
				body, _ := io.ReadAll(req.Body)
				require.Contains(t, string(body), CandyPrompt[:24])
				require.NotContains(t, string(body), "private business prompt")
				response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(candyStream("21")))}
				switch tc.name {
				case "incorrect":
					response.Body = io.NopCloser(strings.NewReader(candyStream("20")))
				case "http":
					response.StatusCode = 429
				case "network":
					return nil, errors.New("private upstream error")
				case "incomplete":
					response.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.created\"}\n\n"))
				case "reissued":
					response.Header.Set(openAICodexTurnStateHeader, "private new ticket")
				case "route":
					response.Header.Add("Set-Cookie", "__oailb=other")
				case "deleted_cookie":
					response.Header.Add("Set-Cookie", "__oailb=; Max-Age=0")
				case "model":
					response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(candyStream("21"), "gpt-6-astra", "gpt-6-luna")))
				}
				return response, nil
			}}
			require.Equal(t, tc.reason, s.runScheduledAstraCandy(t.Context(), h, a.ID, ticket))
			require.Equal(t, 1, calls)
			after, _ := json.Marshal(repo.accounts[2])
			require.Equal(t, string(before), string(after))
			require.Equal(t, writes, repo.writes)
			require.False(t, s.codexTicketChatHeld(a.ID))
			require.False(t, s.ticketProbeCoolingDown(a.ID, ticket.Model, time.Now()))
			require.True(t, s.lookupOpenAICodexTicket(a, ticket.Model).valid(time.Now(), 780))
		})
	}
}

func TestScheduledAstraCandyAsyncDeduplicatesAndSurvivesCallerCancellation(t *testing.T) {
	s, _, a, ticket, h := candyFixture(t)
	entered, unblock := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-unblock
		require.NoError(t, req.Context().Err())
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(candyStream("20")))}, nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.observeScheduledAstraCandy(ctx, h, a, ticket) }()
	}
	wg.Wait()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("observation not started")
	}
	require.Equal(t, int32(1), calls.Load())
	close(unblock)
	waitCandy(t, s)
	s.observeScheduledAstraCandy(ctx, h, a, ticket)
	waitCandy(t, s)
	require.Equal(t, int32(1), calls.Load())

}

func TestScheduledAstraCandyBusinessStreamDoesNotWaitOrChange(t *testing.T) {
	s, _, a, ticket, h := candyFixture(t)
	entered, unblock := make(chan struct{}), make(chan struct{})
	s.httpUpstream = &harvestProxyUpstream{do: func(req *http.Request, _ string) (*http.Response, error) {
		if IsOpenAICodexStateProbeRequest(req.Context()) {
			close(entered)
			<-unblock
			return nil, errors.New("beta failed")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("business stream unchanged"))}, nil
	}}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, chatgptCodexURL, strings.NewReader(`{"model":"gpt-6-astra","input":"private business prompt"}`))
	req.Header = h
	resp, err := s.doOpenAIUpstream(req, "", a)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "business stream unchanged", string(body))
	select {
	case <-entered:
		t.Fatal("observation started before stream closed")
	default:
	}
	require.NoError(t, resp.Body.Close())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("observation not started after close")
	}
	require.False(t, s.codexTicketChatHeld(a.ID))
	require.True(t, ticket.valid(time.Now(), 780))
	close(unblock)
	waitCandy(t, s)
}

func TestScheduledAstraCandyBoundsWorkAndIgnoresNonSSETickets(t *testing.T) {
	s, _, a, ticket, h := candyFixture(t)
	unblock := make(chan struct{})
	var calls atomic.Int32
	s.httpUpstream = &harvestProxyUpstream{do: func(_ *http.Request, _ string) (*http.Response, error) {
		calls.Add(1)
		<-unblock
		return nil, errors.New("test failure")
	}}
	ws := *ticket
	ws.Transport = "websocket"
	s.observeScheduledAstraCandy(t.Context(), h, a, &ws)
	old := *ticket
	old.ExpiresAt = time.Now().Add(-time.Second)
	s.observeScheduledAstraCandy(t.Context(), h, a, &old)
	require.Equal(t, int32(0), calls.Load())
	s.observeScheduledAstraCandy(t.Context(), h, a, ticket)
	next := *ticket
	next.State = mint780State(time.Now().Add(-time.Second))
	s.observeScheduledAstraCandy(t.Context(), h, a, &next)
	third := *ticket
	third.State = mint780State(time.Now().Add(-2 * time.Second))
	s.observeScheduledAstraCandy(t.Context(), h, a, &third)
	require.Eventually(t, func() bool { return calls.Load() == 2 }, time.Second, time.Millisecond)
	close(unblock)
	waitCandy(t, s)
	require.Equal(t, int32(2), calls.Load())
	// Capacity rejection is not marked as observed; later traffic may try it.
	s.observeScheduledAstraCandy(t.Context(), h, a, &third)
	waitCandy(t, s)
	require.Equal(t, int32(3), calls.Load())
}

func TestScheduledAstraCandySwitchDefaultsOffAndCanEnableSameTicket(t *testing.T) {
	s, _, a, ticket, h := candyFixture(t)
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAICodexTicketEnabled: "true"}}}
	s.settingService = NewSettingService(repo, s.cfg)
	var calls atomic.Int32
	s.httpUpstream = &harvestProxyUpstream{do: func(_ *http.Request, _ string) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(candyStream("21")))}, nil
	}}
	s.observeScheduledAstraCandy(t.Context(), h, a, ticket)
	waitCandy(t, s)
	require.Zero(t, calls.Load())
	repo.values[SettingKeyOpenAICodexTicketCandyObservation] = "true"
	s.observeScheduledAstraCandy(t.Context(), h, a, ticket)
	waitCandy(t, s)
	require.Equal(t, int32(1), calls.Load())
	repo.values[SettingKeyOpenAICodexTicketCandyObservation] = "false"
	next := *ticket
	next.State = mint780State(time.Now().Add(-time.Second))
	s.observeScheduledAstraCandy(t.Context(), h, a, &next)
	waitCandy(t, s)
	require.Equal(t, int32(1), calls.Load())
	repo.err = errors.New("settings unavailable")
	require.False(t, s.settingService.CodexTicketCandyObservation(t.Context()))
}

func TestScheduledAstraCandySkipsAdminAccountTests(t *testing.T) {
	s, _, a, ticket, h := candyFixture(t)
	s.httpUpstream = &harvestProxyUpstream{do: func(_ *http.Request, _ string) (*http.Response, error) {
		t.Error("administrative tests must not trigger an extra candy request")
		return nil, errors.New("unexpected request")
	}}
	ctx := context.WithValue(t.Context(), astraCandySkipObservationKey{}, true)
	s.observeScheduledAstraCandy(ctx, h, a, ticket)
	s.astraCandyMu.Lock()
	defer s.astraCandyMu.Unlock()
	require.Zero(t, s.astraCandyRunning)
	require.Empty(t, s.astraCandySeen)
}
