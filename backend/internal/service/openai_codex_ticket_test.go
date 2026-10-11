package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func fakeCodexTicketState(n int) string {
	return fakeCodexTicketStateSalt(n, 0)
}

func fakeCodexTicketStateSalt(n int, salt byte) string {
	if n == 292 || n == 332 {
		blocks := openAICodexTicketPersonalBlocks
		if n == 332 {
			blocks = openAICodexTicketTeamBlocks
		}
		raw := make([]byte, 57+16*blocks)
		raw[0] = 0x80
		binary.BigEndian.PutUint64(raw[1:9], uint64(time.Now().Unix()-60))
		raw[56] = salt
		return base64.URLEncoding.EncodeToString(raw)
	}
	if n < len(openAICodexTicketStatePrefix) {
		return strings.Repeat("A", n)
	}
	return openAICodexTicketStatePrefix + strings.Repeat("B", n-len(openAICodexTicketStatePrefix))
}

func ticketTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "tok", "chatgpt_account_id": "acc-1"},
	}
}

func attachReadyCodexTicket(account *Account, model string) {
	if account == nil {
		return
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra[openAICodexTicketExtraKey(model)] = openAICodexTicket{
		AccountID:  account.ID,
		Model:      model,
		State:      fakeCodexTicketState(292),
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}
}

func ticketTestService(t *testing.T, cfg config.OpenAICodexTicketConfig, upstream HTTPUpstream) *OpenAIGatewayService {
	t.Helper()
	return &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{OpenAICodexTicket: cfg},
		},
		httpUpstream: upstream,
	}
}

func TestApplyOpenAICodexTicket_ReplacesHeader(t *testing.T) {
	state := fakeCodexTicketState(292)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
	}, nil)
	account := ticketTestAccount(41)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      state,
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))

	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(312))
	err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h)
	require.NoError(t, err)
	require.Equal(t, state, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, 292, len(h.Get(openAICodexTurnStateHeader)))
}

func TestApplyOpenAICodexTicket_DoesNotReuseOtherModelOrAccount(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:         true,
		TargetLength:    292,
		TTLSeconds:      3600,
		FailClosed:      true,
		HarvestProxyURL: "socks5h://harvest",
	}, &httpUpstreamRecorder{err: io.EOF})
	a := ticketTestAccount(41)
	b := ticketTestAccount(42)
	astra := fakeCodexTicketState(292)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), a, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      astra,
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))

	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "keep-ungated")
	err := svc.applyOpenAICodexTicket(context.Background(), a, "gpt-5.5", h)
	require.NoError(t, err)
	require.Equal(t, "keep-ungated", h.Get(openAICodexTurnStateHeader))
	require.False(t, svc.openAICodexTicketBlocksAccount(a, "gpt-5.5"))
	require.True(t, svc.openAICodexTicketBlocksAccount(b, "gpt-6-astra"))
	require.False(t, svc.openAICodexTicketBlocksAccount(a, "gpt-6-astra"))

	h = http.Header{}
	err = svc.applyOpenAICodexTicket(context.Background(), b, "gpt-6-astra", h)
	require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestLookupOpenAICodexTicket_PrefersNewerExtra(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292, TTLSeconds: 3600}, nil)
	account := ticketTestAccount(41)
	oldState := fakeCodexTicketState(292)
	newState := openAICodexTicketStatePrefix + strings.Repeat("C", 286)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      oldState,
		Length:     292,
		CapturedAt: time.Now().Add(-30 * time.Minute),
		ExpiresAt:  time.Now().Add(-time.Minute),
	}))
	account.Extra = map[string]any{openAICodexTicketExtraKey("gpt-6-astra"): &openAICodexTicket{
		Model:      "gpt-6-astra",
		State:      newState,
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	},
	}
	got := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, got)
	require.Equal(t, newState, got.State)
	require.True(t, got.valid(time.Now(), 292))
}

func TestApplyOpenAICodexTicket_ExpiredNotInjected(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:         true,
		TargetLength:    292,
		TTLSeconds:      3600,
		FailClosed:      true,
		HarvestProxyURL: "socks5h://harvest",
	}, &httpUpstreamRecorder{err: io.EOF})
	account := ticketTestAccount(41)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      fakeCodexTicketState(292),
		Length:     292,
		CapturedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt:  time.Now().Add(-time.Minute),
	}))
	h := http.Header{}
	err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h)
	require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestApplyOpenAICodexTicket_WrongLengthNotInjected(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
	}, nil)
	account := ticketTestAccount(41)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      fakeCodexTicketState(312),
		Length:     312,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))
	h := http.Header{}
	err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h)
	require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestApplyOpenAICodexTicket_FailOpenSkipsInject(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:    true,
		FailClosed: false,
	}, nil)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	err := svc.applyOpenAICodexTicket(context.Background(), ticketTestAccount(41), "gpt-6-astra", h)
	require.NoError(t, err)
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))
	require.False(t, svc.openAICodexTicketBlocksAccount(ticketTestAccount(41), "gpt-6-astra"))
}

func TestApplyOpenAICodexTicket_DisabledNoop(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: false, FailClosed: true}, nil)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	err := svc.applyOpenAICodexTicket(context.Background(), ticketTestAccount(41), "gpt-6-astra", h)
	require.NoError(t, err)
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))
}

func TestHarvestOpenAICodexTicket_StopsAt292AndUsesHarvestProxy(t *testing.T) {
	state312 := fakeCodexTicketState(312)
	state292 := fakeCodexTicketState(292)
	header312 := http.Header{}
	header312.Set(openAICodexTurnStateHeader, state312)
	header292 := http.Header{}
	header292.Set(openAICodexTurnStateHeader, state292)
	upstream := &httpUpstreamRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header:     header312,
				Body:       io.NopCloser(strings.NewReader(`{"status":"completed"}`)),
			},
			{
				StatusCode: http.StatusOK,
				Header:     header292,
				Body:       io.NopCloser(strings.NewReader(`{"status":"completed"}`)),
			},
		},
	}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 292,
		TTLSeconds:                   3600,
		HarvestProxyURL:              "socks5h://user:pass@harvest.example:31",
		HarvestAttemptTimeoutSeconds: 5,
		FailClosed:                   true,
	}, upstream)
	account := ticketTestAccount(41)

	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	svc.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(account.ID, "gpt-6-astra"))
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.Equal(t, state292, ticket.State)
	require.Equal(t, "socks5h://user:pass@harvest.example:31", ticket.HarvestProxyURL)
	require.Empty(t, ticket.HarvestNodeID)
	require.NotEqual(t, upstream.requests[0].Header.Get("session_id"), upstream.requests[1].Header.Get("session_id"))
	require.NotEmpty(t, upstream.requests[0].Header.Get("session_id"))
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "stale")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, state292, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, "socks5h://user:pass@harvest.example:31", upstream.lastProxyURL)
	require.Len(t, upstream.requests, 2)
	require.Empty(t, upstream.requests[0].Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, openAICodexAstraMinVersion, upstream.requests[0].Header.Get("version"))
	require.Equal(t, HTTPUpstreamProfileOpenAIHarvest, HTTPUpstreamProfileFromContext(upstream.requests[0].Context()))
	require.True(t, upstream.requests[0].Close)
}

func TestHarvestOpenAICodexTicket_HTTP503DoesNotAbortHunt(t *testing.T) {
	state292 := fakeCodexTicketState(292)
	header503 := http.Header{}
	header292 := http.Header{}
	header292.Set(openAICodexTurnStateHeader, state292)
	responses := make([]*http.Response, 0, 3)
	responses = append(responses, &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     header503,
		Body:       io.NopCloser(strings.NewReader(`{"error":"overloaded"}`)),
	})
	responses = append(responses, &http.Response{
		StatusCode: http.StatusOK,
		Header:     header292,
		Body:       io.NopCloser(strings.NewReader(`{"status":"completed"}`)),
	})
	upstream := &httpUpstreamRecorder{responses: responses}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 292,
		TTLSeconds:                   3600,
		HarvestProxyURL:              "socks5h://harvest.example:31",
		HarvestAttemptTimeoutSeconds: 5,
		FailClosed:                   true,
	}, upstream)
	account := ticketTestAccount(41)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	svc.openaiCodexTicketProbeCooldown.Delete(openAICodexTicketKey(account.ID, "gpt-6-astra"))
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.Equal(t, state292, ticket.State)
	require.Len(t, upstream.requests, 2)
}

func TestLookupOpenAICodexTicket_HydratesFromExtra(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292, TTLSeconds: 3600}, nil)
	state := fakeCodexTicketState(292)
	account := ticketTestAccount(9)
	account.Extra = map[string]any{
		openAICodexTicketExtraKey("gpt-6-astra"): map[string]any{
			"state":       state,
			"length":      292,
			"model":       "gpt-6-astra",
			"captured_at": time.Now().Add(-time.Minute),
			"expires_at":  time.Now().Add(time.Hour),
		},
	}
	got := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, got)
	require.Equal(t, state, got.State)
	require.True(t, got.valid(time.Now(), 292))
}

func TestLookupOpenAICodexTicket_SchedulerSnapshotRequiresIdentityHydration(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
	}, nil)
	full := ticketTestAccount(2)
	full.Credentials["email"] = "user@example.com"
	state := fakeCodexTicketState(292)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), full, &openAICodexTicket{
		AccountID:  2,
		Model:      "gpt-6-astra",
		State:      state,
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))

	snapshot := &Account{
		ID:          2,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"plan_type": "plus"},
		Extra: map[string]any{
			openAICodexTicketExtraKey("gpt-6-astra"): map[string]any{
				"state":       state,
				"length":      292,
				"identity":    ticketIdentity(full),
				"captured_at": time.Now(),
				"expires_at":  time.Now().Add(time.Hour),
			},
		},
	}

	got := svc.lookupOpenAICodexTicket(snapshot, "gpt-6-astra")
	require.Nil(t, got)
	require.True(t, svc.openAICodexTicketBlocksAccount(snapshot, "gpt-6-astra"))

	statuses := OpenAICodexTicketStatuses(snapshot, config.OpenAICodexTicketConfig{
		Enabled:    true,
		FailClosed: true,
		Models:     []string{"gpt-6-astra"},
	}, time.Now())
	require.Len(t, statuses, 1)
	require.False(t, statuses[0].Ready)
	require.True(t, statuses[0].Blocked)
}

func TestLookupOpenAICodexTicket_IdentityMismatchStillRejected(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
	}, nil)
	original := ticketTestAccount(2)
	original.Credentials["email"] = "user@example.com"
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), original, &openAICodexTicket{
		AccountID:  2,
		Model:      "gpt-6-astra",
		State:      fakeCodexTicketState(292),
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))

	rotated := ticketTestAccount(2)
	rotated.Credentials["chatgpt_account_id"] = "acc-rotated"
	rotated.Credentials["email"] = "other@example.com"
	require.Nil(t, svc.lookupOpenAICodexTicket(rotated, "gpt-6-astra"))
	require.True(t, svc.openAICodexTicketBlocksAccount(rotated, "gpt-6-astra"))
}

func TestOpenAICodexTicketStatuses_ReportsRemainingTTL(t *testing.T) {
	account := ticketTestAccount(41)
	account.Extra = map[string]any{
		openAICodexTicketExtraKey("gpt-6-astra"): map[string]any{
			"state":       fakeCodexTicketState(292),
			"length":      292,
			"model":       "gpt-6-astra",
			"captured_at": time.Now().Add(-10 * time.Minute),
			"expires_at":  time.Now().Add(50 * time.Minute),
		},
	}
	now := time.Now()
	got := OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, now)
	require.Len(t, got, 2)
	require.Equal(t, "gpt-6-astra", got[0].Model)
	require.True(t, got[0].Ready)
	require.Greater(t, got[0].RemainingSeconds, int64(40*60))
	require.LessOrEqual(t, got[0].RemainingSeconds, int64(50*60))
	require.Equal(t, "gpt-5.6-sol", got[1].Model)
	require.False(t, got[1].Ready)
}

func TestOpenAICodexTicketStatuses_ReportsIssuedBoundRemaining(t *testing.T) {
	now := time.Now()
	account := ticketTestAccount(42)
	account.Extra = map[string]any{
		openAICodexTicketExtraKey("gpt-6-astra"): map[string]any{
			"state":      fakeCodexTicketState(292),
			"length":     292,
			"model":      "gpt-6-astra",
			"issued_at":  now.Add(-50 * time.Minute),
			"expires_at": now.Add(2 * time.Hour),
		},
	}
	got := OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, now)
	require.True(t, got[0].Ready)
	require.False(t, got[0].Standby)
	require.Greater(t, got[0].RemainingSeconds, int64(8*60))
	require.Less(t, got[0].RemainingSeconds, int64(12*60))
}

func TestOpenAICodexTicketStatuses_PromotedStandbyKeepsRemainingAndFlag(t *testing.T) {
	now := time.Now()
	account := ticketTestAccount(43)
	account.Extra = map[string]any{
		openAICodexTicketExtraKey("gpt-6-astra"): &openAICodexTicket{
			State:     fakeCodexTicketState(292),
			Length:    292,
			Model:     "gpt-6-astra",
			IssuedAt:  now.Add(-70 * time.Minute),
			ExpiresAt: now.Add(-10 * time.Minute),
			Standby: &openAICodexTicket{
				State:     fakeCodexTicketState(292),
				Length:    292,
				IssuedAt:  now.Add(-10 * time.Minute),
				ExpiresAt: now.Add(50 * time.Minute),
			},
		},
	}
	got := OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, now)
	require.True(t, got[0].Ready)
	require.True(t, got[0].Standby)
	require.Nil(t, got[0].StandbyExpiresAt)
	require.Greater(t, got[0].RemainingSeconds, int64(40*60))
	require.LessOrEqual(t, got[0].RemainingSeconds, int64(50*60))
}

func TestOpenAICodexTicketStatuses_PrimaryKeepsStandbyExpiry(t *testing.T) {
	now := time.Now()
	account := ticketTestAccount(44)
	account.Extra = map[string]any{
		openAICodexTicketExtraKey("gpt-6-astra"): &openAICodexTicket{
			State:     fakeCodexTicketState(292),
			Length:    292,
			Model:     "gpt-6-astra",
			IssuedAt:  now.Add(-10 * time.Minute),
			ExpiresAt: now.Add(50 * time.Minute),
			Standby: &openAICodexTicket{
				State:     fakeCodexTicketState(292),
				Length:    292,
				IssuedAt:  now.Add(-2 * time.Minute),
				ExpiresAt: now.Add(58 * time.Minute),
			},
		},
	}
	got := OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, now)
	require.True(t, got[0].Ready)
	require.False(t, got[0].Standby)
	require.NotNil(t, got[0].StandbyExpiresAt)
	require.Greater(t, got[0].RemainingSeconds, int64(40*60))
	require.LessOrEqual(t, got[0].RemainingSeconds, int64(50*60))
}

func TestExtractOpenAICodexTicketModel(t *testing.T) {
	require.Equal(t, "gpt-6-astra", extractOpenAICodexTicketModel([]byte(`{"model":"gpt-6-astra"}`)))
	require.Empty(t, extractOpenAICodexTicketModel([]byte(`{}`)))
}

// These stubs exercise the real continuous refresh path with both default models
// completing together. Run under -race to catch writes to the shared account maps.
type codexTicketRefreshRepo struct {
	AccountRepository
	accounts []Account
	mu       sync.Mutex
	updates  map[string]any
}

func (r *codexTicketRefreshRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	return r.accounts, nil
}
func (r *codexTicketRefreshRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updates == nil {
		r.updates = make(map[string]any)
	}
	for k, v := range updates {
		r.updates[k] = v
	}
	return nil
}

type codexTicketConcurrentUpstream struct {
	HTTPUpstream
	started atomic.Int64
	ready   chan struct{}
}

func (u *codexTicketConcurrentUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if u.started.Add(1) == 2 {
		close(u.ready)
	}
	select {
	case <-u.ready:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(292))
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(`{"status":"completed"}`))}, nil
}
func TestRefreshOpenAICodexTickets_ConcurrentModelsPreserveAccountSnapshot(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	account.Extra = map[string]any{"existing": true}
	repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
	upstream := &codexTicketConcurrentUpstream{ready: make(chan struct{})}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "socks5h://proxy.example.com:1080"}, upstream)
	svc.accountRepo = repo
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, int64(2), upstream.started.Load())
	require.Equal(t, map[string]any{"existing": true}, account.Extra)
	require.Len(t, repo.updates, 4)
	require.Contains(t, repo.updates, codexProbeSummaryKey("gpt-6-astra"))
	require.Contains(t, repo.updates, codexProbeSummaryKey("gpt-5.6-sol"))
	for _, model := range []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel} {
		ticket := svc.lookupOpenAICodexTicket(account, model)
		require.NotNil(t, ticket)
		require.True(t, ticket.valid(time.Now(), 292))
	}
	// Valid tickets do not produce another probe on the next cycle.
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, int64(2), upstream.started.Load())
}

func TestRefreshOpenAICodexTickets_SkipsUnschedulableAccounts(t *testing.T) {
	paused := ticketTestAccount(3)
	paused.Schedulable = false
	active := ticketTestAccount(2)
	upstream := &httpUpstreamRecorder{err: io.EOF}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:         true,
		HarvestProxyURL: "http://proxy.example.com:8080",
		Models:          []string{"gpt-6-astra"},
	}, upstream)
	svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*paused, *active}}
	svc.refreshOpenAICodexTickets(context.Background())
	require.Len(t, upstream.requests, 1)
}

func TestOpenAICodexTicketStatuses_RespectRuntimeConfiguration(t *testing.T) {
	account := ticketTestAccount(41)
	require.Empty(t, OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{}, time.Now()))
	cfg := config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"custom-model"}}
	status := OpenAICodexTicketStatuses(account, cfg, time.Now())
	require.Len(t, status, 1)
	require.Equal(t, "custom-model", status[0].Model)
	require.False(t, status[0].Blocked)
	cfg.FailClosed = true
	require.True(t, OpenAICodexTicketStatuses(account, cfg, time.Now())[0].Blocked)
}
func TestProbeOpenAICodexTicket_RejectsInvalidState(t *testing.T) {
	for _, state := range []string{fakeCodexTicketState(312), strings.Repeat("X", 292), ""} {
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, state)
		upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(""))}}}
		svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example.com:8080"}, upstream)
		account := ticketTestAccount(41)
		svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
		require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	}
}
func TestOpenAICodexTicket_RequiresActualLengthAndExpiry(t *testing.T) {
	ticket := &openAICodexTicket{State: fakeCodexTicketState(312), Length: 292, ExpiresAt: time.Now().Add(time.Hour)}
	require.False(t, ticket.valid(time.Now(), 292))
	ticket.State = fakeCodexTicketState(292)
	ticket.ExpiresAt = time.Time{}
	require.False(t, ticket.valid(time.Now(), 292))
}

func TestParseOpenAICodexTicketShape(t *testing.T) {
	shape, err := parseOpenAICodexTicketShape(fakeCodexTicketState(292))
	require.NoError(t, err)
	require.Equal(t, openAICodexTicketPersonalBlocks, shape.Blocks)
	require.False(t, shape.IssuedAt.IsZero())
	_, err = parseOpenAICodexTicketShape(fakeCodexTicketState(312))
	require.Error(t, err)
}

// /responses/compact 的出站模型被 Forward 改写为 gateway.openai_compact_model
// （默认非空），门票门控必须按该出站模型判定。否则对门控模型发 compact 请求时，
// 所有无票账号都会被 fail_closed 误判为不可调度，而这些请求实际不需要票。
func TestOpenAICodexTicketGate_CompactRequestUsesForwardOutboundModel(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		OpenAICompactModel: "gpt-5.5",
		OpenAICodexTicket: config.OpenAICodexTicketConfig{
			Enabled:      true,
			TargetLength: 292,
			TTLSeconds:   3600,
			FailClosed:   true,
			Models:       []string{"gpt-6-astra"},
		},
	}}}
	account := ticketTestAccount(41) // 无票

	// 出站模型预测必须与 Forward 的解析链一致。
	require.Equal(t, "gpt-6-astra", svc.openAICodexTicketOutboundModel(account, "gpt-6-astra", false))
	require.Equal(t, "gpt-5.5", svc.openAICodexTicketOutboundModel(account, "gpt-6-astra", true))

	// 普通请求：出站仍是门控模型且无票 → fail_closed 必须拦号。
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-6-astra", false))

	// compact 请求：出站已被改写成非门控的 gpt-5.5 → 不得拦号。
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-6-astra", true))

	// 回归锚点：按客户端原始模型判定（旧实现的口径）在 compact 下必然误拦。
	require.True(t, svc.openAICodexTicketBlocksAccount(account, canonicalOpenAIAccountSchedulingModel(account, "gpt-6-astra")))
}

// TestOpenAICodexTicketExpectedBlocksByPlanType 锁定 plan_type → 票据形态的判定口径。
// 上游并不只上报 "team"：商务自助订阅会上报 "self_serve_business_prolite" 这类变体，
// 等值判定会把它错判为个人号，导致上游返回的 12 块 / 332 门票被判为 probe miss。
func TestOpenAICodexTicketExpectedBlocksByPlanType(t *testing.T) {
	cases := []struct {
		plan string
		want int
	}{
		{"", openAICodexTicketPersonalBlocks},
		{"plus", openAICodexTicketPersonalBlocks},
		{"pro", openAICodexTicketPersonalBlocks},
		{"k12", openAICodexTicketPersonalBlocks},
		{"free", openAICodexTicketPersonalBlocks},
		{"team", openAICodexTicketTeamBlocks},
		{"business", openAICodexTicketTeamBlocks},
		{"enterprise", openAICodexTicketTeamBlocks},
		{"self_serve_business_prolite", openAICodexTicketTeamBlocks},
		{"self_serve_business_usage_based", openAICodexTicketTeamBlocks},
		{"SELF_SERVE_BUSINESS_PRO", openAICodexTicketTeamBlocks},
		{" Team ", openAICodexTicketTeamBlocks},
	}

	for _, tc := range cases {
		account := ticketTestAccount(1)
		if tc.plan != "" {
			account.Credentials["plan_type"] = tc.plan
		}
		require.Equal(t, tc.want, openAICodexTicketExpectedBlocks(account), "plan_type=%q", tc.plan)
	}
}

// TestOpenAICodexTicketExpectedLengthTeamVariant 验证 12 块换算出的目标长度是 332，
// 个人号仍是 292；这是探针校验与票据有效性判定共用的口径。
func TestOpenAICodexTicketExpectedLengthTeamVariant(t *testing.T) {
	personal := ticketTestAccount(1)
	personal.Credentials["plan_type"] = "plus"
	require.Equal(t, 292, openAICodexTicketExpectedLength(personal))
	require.Equal(t, openAICodexTicketPersonalBlocks, openAICodexTicketExpectedBlocks(personal))

	team := ticketTestAccount(2)
	team.Credentials["plan_type"] = "self_serve_business_prolite"
	require.Equal(t, 332, openAICodexTicketExpectedLength(team))
	require.Equal(t, openAICodexTicketTeamBlocks, openAICodexTicketExpectedBlocks(team))

	// 332 与 12 块必须自洽（57 字节信封 + 16 字节/块，base64 后取整）。
	require.Equal(t, base64.URLEncoding.EncodedLen(57+16*openAICodexTicketTeamBlocks), openAICodexTicketExpectedLength(team))
}

// TestOpenAICodexTicketTeamVariantAccepts332Probe 回归：商务变体账号在探针拿到
// 12 块 / 332 时必须被判为命中（修复前会被 expectedBlocks=10 / expectedLength=292 打回）。
func TestOpenAICodexTicketTeamVariantAccepts332Probe(t *testing.T) {
	account := ticketTestAccount(7)
	account.Credentials["plan_type"] = "self_serve_business_prolite"

	state := fakeCodexTicketState(332)
	shape, err := parseOpenAICodexTicketShape(state)
	require.NoError(t, err)
	require.Equal(t, openAICodexTicketTeamBlocks, shape.Blocks)
	require.Equal(t, openAICodexTicketExpectedBlocks(account), shape.Blocks)
	require.Equal(t, openAICodexTicketExpectedLength(account), len(state))

	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, state)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(`{"status":"completed"}`)),
	}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 292,
		TTLSeconds:                   3600,
		HarvestProxyURL:              "socks5h://harvest.example:31",
		HarvestAttemptTimeoutSeconds: 5,
		FailClosed:                   true,
	}, upstream)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.Equal(t, state, ticket.State)
	require.Equal(t, openAICodexTicketTeamBlocks, ticket.Blocks)

	outbound := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", outbound))
	require.Equal(t, state, outbound.Get(openAICodexTurnStateHeader))
	require.Len(t, upstream.requests, 1)
}

func TestOpenAICodexTicketShouldYieldStickyToHigherPriorityReadyAccount(t *testing.T) {
	low := ticketTestAccount(5)
	low.Name = "5x"
	low.Priority = 100
	high := ticketTestAccount(20)
	high.Name = "20x"
	high.Priority = 1
	attachReadyCodexTicket(low, "gpt-6-astra")
	attachReadyCodexTicket(high, "gpt-6-astra")

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
		Models:       []string{"gpt-6-astra", "gpt-5.6-sol"},
	}, nil)
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*low, *high}}

	require.True(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), low, nil, PlatformOpenAI, "gpt-6-astra", false, nil))
	require.False(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), high, nil, PlatformOpenAI, "gpt-6-astra", false, nil))
}

func TestOpenAICodexTicketShouldNotYieldStickyWithoutBetterTicket(t *testing.T) {
	low := ticketTestAccount(5)
	low.Priority = 100
	high := ticketTestAccount(20)
	high.Priority = 1
	attachReadyCodexTicket(low, "gpt-6-astra")

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
		Models:       []string{"gpt-6-astra"},
	}, nil)
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*low, *high}}

	require.False(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), low, nil, PlatformOpenAI, "gpt-6-astra", false, nil))
}

func TestOpenAICodexTicketTicketlessStickyYieldsRegardlessOfPriority(t *testing.T) {
	ticketless := ticketTestAccount(20)
	ticketless.Priority = 1
	ticketed := ticketTestAccount(5)
	ticketed.Priority = 100
	attachReadyCodexTicket(ticketed, "gpt-6-astra")

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		Models:       []string{"gpt-6-astra"},
	}, nil)
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*ticketless, *ticketed}}

	require.False(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), ticketless, nil, PlatformOpenAI, "gpt-6-astra", false, nil), "switch off keeps priority")
	enableTicketFirstScheduling(svc)
	require.True(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), ticketless, nil, PlatformOpenAI, "gpt-6-astra", false, nil))
	require.False(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), ticketless, nil, PlatformOpenAI, "gpt-6-astra", false, map[int64]struct{}{ticketed.ID: {}}))
	require.False(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), ticketless, nil, PlatformOpenAI, "gpt-5.5", false, nil))
	require.False(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), ticketed, nil, PlatformOpenAI, "gpt-6-astra", false, nil))
}

func TestOpenAICodexTicketDeferredAccounts(t *testing.T) {
	ticketed := ticketTestAccount(1)
	attachReadyCodexTicket(ticketed, "gpt-6-astra")
	ticketless := ticketTestAccount(2)
	apiKey := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		Models:       []string{"gpt-6-astra"},
	}, nil)

	require.Nil(t, svc.openAICodexTicketDeferredAccounts([]*Account{ticketed, ticketless}, "gpt-6-astra", false), "switch off")
	enableTicketFirstScheduling(svc)
	require.Equal(t, map[int64]bool{2: true}, svc.openAICodexTicketDeferredAccounts([]*Account{ticketed, ticketless, apiKey}, "gpt-6-astra", false))
	require.Equal(t, map[int64]bool{2: true}, svc.openAICodexTicketDeferredAccounts([]*Account{apiKey, ticketless}, "gpt-6-astra", false))
	require.Nil(t, svc.openAICodexTicketDeferredAccounts([]*Account{ticketless, ticketTestAccount(4)}, "gpt-6-astra", false), "nothing to prefer")
	require.Nil(t, svc.openAICodexTicketDeferredAccounts([]*Account{ticketed, ticketless}, "gpt-5.5", false), "ungated model")
}

func TestOpenAICodexTicketShouldNotYieldStickyWhenPriorityTied(t *testing.T) {
	a := ticketTestAccount(5)
	a.Priority = 1
	b := ticketTestAccount(20)
	b.Priority = 1
	attachReadyCodexTicket(a, "gpt-6-astra")
	attachReadyCodexTicket(b, "gpt-6-astra")

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
		Models:       []string{"gpt-6-astra"},
	}, nil)
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*a, *b}}

	require.False(t, svc.openAICodexTicketShouldYieldSticky(context.Background(), a, nil, PlatformOpenAI, "gpt-6-astra", false, nil))
}

func TestOpenAICodexSkipHarvestKeepsAccountUsable(t *testing.T) {
	skipped := ticketTestAccount(5)
	skipped.Name = "5x"
	skipped.Extra = map[string]any{OpenAICodexSkipHarvestExtraKey: true}
	attachReadyCodexTicket(skipped, "gpt-6-astra")
	bare := ticketTestAccount(6)
	bare.Name = "5x-bare"
	bare.Extra = map[string]any{OpenAICodexSkipHarvestExtraKey: true}
	harvester := ticketTestAccount(20)
	harvester.Name = "20x"
	attachReadyCodexTicket(harvester, "gpt-6-astra")

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
		Models:       []string{"gpt-6-astra", "gpt-5.6-sol"},
	}, nil)

	require.False(t, svc.openAICodexTicketHarvestExcluded(skipped))
	require.False(t, svc.openAICodexTicketHarvestExcluded(bare))
	require.False(t, svc.openAICodexTicketHarvestExcluded(harvester))
	require.False(t, svc.openAICodexTicketBlocksAccount(skipped, "gpt-6-astra"))
	require.False(t, svc.openAICodexTicketBlocksAccount(skipped, "gpt-5.6-sol"))
	require.False(t, svc.openAICodexTicketBlocksAccount(bare, "gpt-6-astra"))
	require.False(t, svc.openAICodexTicketBlocksAccount(harvester, "gpt-6-astra"))
	require.True(t, svc.openAICodexTicketReadyForRequest(skipped, "gpt-6-astra", false))
	require.False(t, svc.openAICodexTicketReadyForRequest(bare, "gpt-6-astra", false))
	require.True(t, svc.openAICodexTicketReadyForRequest(harvester, "gpt-6-astra", false))

	own := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), skipped, "gpt-6-astra", own))
	require.NotEmpty(t, own.Get(openAICodexTurnStateHeader))
	empty := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), bare, "gpt-6-astra", empty))
	require.Empty(t, empty.Get(openAICodexTurnStateHeader))
	harvested := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), harvester, "gpt-6-astra", harvested))
	require.NotEmpty(t, harvested.Get(openAICodexTurnStateHeader))

	cfg := config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, Models: []string{"gpt-6-astra", "gpt-5.6-sol"}}
	skippedStatuses := OpenAICodexTicketStatuses(skipped, cfg, time.Now())
	require.True(t, skippedStatuses[0].Ready)
	require.False(t, skippedStatuses[0].Blocked)
	require.False(t, skippedStatuses[1].Ready)
	require.False(t, skippedStatuses[1].Blocked)
	bareStatuses := OpenAICodexTicketStatuses(bare, cfg, time.Now())
	require.False(t, bareStatuses[0].Ready)
	require.False(t, bareStatuses[0].Blocked)
}

func TestOpenAICodexTicketBlocksOutOfHarvestScopeLeftover(t *testing.T) {
	out := harvestScopeAccount(5, true, 26)
	attachReadyCodexTicket(&out, "gpt-6-astra")
	in := harvestScopeAccount(20, true, 3)
	attachReadyCodexTicket(&in, "gpt-6-astra")
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{
		SettingKeyOpenAICodexTicketHarvestScope: `{"mode":"selected","group_ids":[3]}`,
		SettingKeyOpenAICodexTicketFailClosed:   "true",
	}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600, FailClosed: true, Models: []string{"gpt-6-astra"},
	}, nil)
	svc.settingService = NewSettingService(repo, &config.Config{})

	require.True(t, svc.openAICodexTicketHarvestExcluded(&out))
	require.False(t, svc.openAICodexTicketHarvestExcluded(&in))
	require.True(t, svc.openAICodexTicketBlocksAccount(&out, "gpt-6-astra"))
	require.False(t, svc.openAICodexTicketBlocksAccount(&in, "gpt-6-astra"))
}

func TestDoOpenAIUpstream_BoundTicketUsesHarvestProxy(t *testing.T) {
	harvest := "socks5h://harvest.example:31"
	account := ticketTestAccount(41)
	account.Proxy = &Proxy{ID: 9, Protocol: "http", Host: "account.example", Port: 8080, Status: StatusActive}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: harvest}, upstream)
	state := fakeCodexTicketState(292)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		Model: "gpt-6-astra", State: state, Length: 292, HarvestProxyURL: harvest,
		HarvestSessionID: "harvest-session-sticky",
		CapturedAt:       time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	body := `{"model":"gpt-6-astra","prompt_cache_key":"isolated-session","client_metadata":{"session_id":"isolated-session","thread_id":"client-thread"}}`
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/responses", strings.NewReader(body))
	require.NoError(t, err)
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set(openAICodexTurnStateHeader, state)
	req.Header.Set("session_id", "isolated-session")
	req.Header.Set("session-id", "isolated-hyphen")
	req.Header.Set("conversation_id", "isolated-session")
	req.Header.Set("x-codex-installation-id", "install-1")
	req.Header.Set("thread-id", "thread-1")
	resp, err := svc.doOpenAIUpstream(req, account.Proxy.URL(), account)
	require.NoError(t, err)
	require.Equal(t, harvest, upstream.lastProxyURL)
	require.NotEqual(t, account.Proxy.URL(), upstream.lastProxyURL)
	require.Equal(t, "harvest-session-sticky", upstream.lastReq.Header.Get("session_id"))
	require.Empty(t, upstream.lastReq.Header.Get("session-id"))
	require.Empty(t, upstream.lastReq.Header.Get("conversation_id"))
	require.Empty(t, upstream.lastReq.Header.Get("x-codex-installation-id"))
	require.Empty(t, upstream.lastReq.Header.Get("thread-id"))
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "client_metadata").Exists())
	require.Equal(t, HTTPUpstreamProfileOpenAIHarvest, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.True(t, upstream.lastReq.Close)
	require.NoError(t, resp.Body.Close())
}

func TestDoOpenAIUpstream_BoundTicketUsesConfiguredHarvestProxy(t *testing.T) {
	harvest := "socks5h://harvest.example:31"
	account := ticketTestAccount(41)
	account.Proxy = &Proxy{ID: 9, Protocol: "http", Host: "account.example", Port: 8080, Status: StatusActive}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: harvest}, upstream)
	state := fakeCodexTicketState(292)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		Model: "gpt-6-astra", State: state, Length: 292,
		CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set(openAICodexTurnStateHeader, state)
	resp, err := svc.doOpenAIUpstream(req, account.Proxy.URL(), account)
	require.NoError(t, err)
	require.Equal(t, harvest, upstream.lastProxyURL)
	require.NoError(t, resp.Body.Close())
}

func TestDoOpenAIUpstream_UnboundKeepsAccountProxy(t *testing.T) {
	account := ticketTestAccount(41)
	account.Proxy = &Proxy{ID: 9, Protocol: "http", Host: "account.example", Port: 8080, Status: StatusActive}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "socks5h://harvest.example:31"}, upstream)
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("session_id", "isolated-session")
	req.Header.Set("conversation_id", "isolated-session")
	resp, err := svc.doOpenAIUpstream(req, account.Proxy.URL(), account)
	require.NoError(t, err)
	require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
	require.Equal(t, "isolated-session", upstream.lastReq.Header.Get("session_id"))
	require.Equal(t, "isolated-session", upstream.lastReq.Header.Get("conversation_id"))
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.False(t, upstream.lastReq.Close)
	require.NoError(t, resp.Body.Close())
}

func TestDoOpenAIProxyAttempt_BoundTicketSkipsPlugin(t *testing.T) {
	account := ticketTestAccount(41)
	state := fakeCodexTicketState(292)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}}, upstream)
	mgr := &PluginManager{}
	mgr.route.Store(&pluginRoute{rolloutPercent: 100})
	svc.pluginManager = mgr
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		Model: "gpt-6-astra", State: state, Length: 292,
		HarvestProxyURL: "http://harvest.invalid:8080", HarvestSessionID: "harvest-session-sticky",
		CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set(openAICodexTurnStateHeader, state)
	resp, err := svc.doOpenAIProxyAttempt(req, account, runtimeProxyEgress{url: "http://harvest.invalid:8080", proxyID: -1})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, upstream.requests, 1)
	require.NoError(t, resp.Body.Close())
}

func TestPinCodexTicketWSAcquireUsesHarvestProxy(t *testing.T) {
	harvest := "socks5h://harvest.example:31"
	account := ticketTestAccount(41)
	account.Proxy = &Proxy{ID: 9, Protocol: "http", Host: "account.example", Port: 8080, Status: StatusActive}
	account.ProxyID = &account.Proxy.ID
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: harvest}, nil)
	state := fakeCodexTicketState(292)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		Model: "gpt-6-astra", State: state, Length: 292, HarvestProxyURL: harvest,
		HarvestSessionID: "harvest-session-sticky",
		CapturedAt:       time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, state)
	h.Set("session_id", "isolated-session")
	h.Set("session-id", "isolated-hyphen")
	h.Set("conversation_id", "isolated-session")
	h.Set("OpenAI-Beta", openAIWSBetaV2Value)
	h.Set("x-codex-beta-features", "remote-compaction-v2")
	h.Set("x-codex-routing-hint", "model=gpt-6-astra")
	proxy, release, err := svc.pinCodexTicketWSAcquire(context.Background(), h, account)
	require.NoError(t, err)
	release()
	require.Equal(t, harvest, proxy)
	require.NotEqual(t, account.Proxy.URL(), proxy)
	require.Equal(t, "harvest-session-sticky", h.Get("session_id"))
	require.Empty(t, h.Get("session-id"))
	require.Empty(t, h.Get("conversation_id"))
	require.Empty(t, h.Get("x-codex-beta-features"))
	require.Empty(t, h.Get("x-codex-routing-hint"))
	require.Equal(t, openAIWSBetaV2Value, h.Get("OpenAI-Beta"))
	require.Equal(t, openAICodexAstraMinVersion, h.Get("version"))
	require.Equal(t, buildCodexCLIUserAgent(openAICodexAstraMinVersion), h.Get("user-agent"))
}

func TestPinCodexTicketWSAcquireConfiguredHarvestWhenTicketHasNoProxy(t *testing.T) {
	harvest := "socks5h://harvest.example:31"
	account := ticketTestAccount(41)
	account.Proxy = &Proxy{ID: 9, Protocol: "http", Host: "account.example", Port: 8080, Status: StatusActive}
	account.ProxyID = &account.Proxy.ID
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: harvest}, nil)
	state := fakeCodexTicketState(292)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		Model: "gpt-6-astra", State: state, Length: 292,
		HarvestSessionID: "harvest-session-sticky",
		CapturedAt:       time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, state)
	h.Set("session_id", "isolated-session")
	proxy, release, err := svc.pinCodexTicketWSAcquire(context.Background(), h, account)
	require.NoError(t, err)
	release()
	require.Equal(t, harvest, proxy)
	require.Equal(t, "harvest-session-sticky", h.Get("session_id"))
}

func TestPinCodexTicketWSAcquireUnboundKeepsAccountProxy(t *testing.T) {
	account := ticketTestAccount(41)
	account.Proxy = &Proxy{ID: 9, Protocol: "http", Host: "account.example", Port: 8080, Status: StatusActive}
	account.ProxyID = &account.Proxy.ID
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "socks5h://harvest.example:31"}, nil)
	h := http.Header{}
	h.Set("session_id", "isolated-session")
	h.Set("conversation_id", "isolated-session")
	proxy, release, err := svc.pinCodexTicketWSAcquire(context.Background(), h, account)
	require.NoError(t, err)
	release()
	require.Equal(t, account.Proxy.URL(), proxy)
	require.Equal(t, "isolated-session", h.Get("session_id"))
	require.Equal(t, "isolated-session", h.Get("conversation_id"))
}

func TestPinBoundCodexTicketHarvestIdentityBody(t *testing.T) {
	ticket := &openAICodexTicket{HarvestSessionID: "harvest-session-sticky"}
	body := []byte(`{"model":"gpt-6-astra","prompt_cache_key":"client-session","device_id":"client-device","client_metadata":{"session_id":"client-session","thread_id":"client-thread","x-codex-turn-metadata":"{\"session_id\":\"client-session\",\"thread_id\":\"client-thread\"}"}}`)
	next, changed, err := pinBoundCodexTicketHarvestIdentityBody(body, ticket)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "gpt-6-astra", gjson.GetBytes(next, "model").String())
	require.False(t, gjson.GetBytes(next, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(next, "client_metadata").Exists())
	require.False(t, gjson.GetBytes(next, "device_id").Exists())

	decoded := map[string]any{
		"model":            "gpt-6-astra",
		"prompt_cache_key": "client-session",
		"device_id":        "client-device",
		"client_metadata":  map[string]any{"session_id": "client-session", "thread_id": "client-thread"},
	}
	require.True(t, pinBoundCodexTicketHarvestIdentityMaps(decoded, ticket))
	require.Equal(t, "gpt-6-astra", decoded["model"])
	_, hasCache := decoded["prompt_cache_key"]
	_, hasMetadata := decoded["client_metadata"]
	_, hasDevice := decoded["device_id"]
	require.False(t, hasCache)
	require.False(t, hasMetadata)
	require.False(t, hasDevice)

	unchanged, changed, err := pinBoundCodexTicketHarvestIdentityBody(body, &openAICodexTicket{})
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(unchanged))
}

func TestHarvestPinnedSessionForModel(t *testing.T) {
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600, Models: []string{"gpt-6-astra"},
	}, nil)
	require.Empty(t, svc.harvestPinnedSessionForModel(context.Background(), account, "gpt-6-astra"))
	require.False(t, svc.harvestPinsCodexIdentity(context.Background(), account, "gpt-6-astra"))

	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292,
		HarvestSessionID: "harvest-session-sticky",
		CapturedAt:       time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.Equal(t, "harvest-session-sticky", svc.harvestPinnedSessionForModel(context.Background(), account, "gpt-6-astra"))
	require.True(t, svc.harvestPinsCodexIdentity(context.Background(), account, "gpt-6-astra"))
	require.Empty(t, svc.harvestPinnedSessionForModel(context.Background(), account, "gpt-5.6-sol"))
}

func TestBuildUpstreamRequest_BoundTicketPinsHarvestIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600, Models: []string{"gpt-6-astra"},
	}, nil)
	state := fakeCodexTicketState(292)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		Model: "gpt-6-astra", State: state, Length: 292,
		HarvestSessionID: "harvest-session-sticky",
		CapturedAt:       time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}))
	body := []byte(`{"model":"gpt-6-astra","stream":true,"prompt_cache_key":"client-session","client_metadata":{"session_id":"client-session","thread_id":"client-thread"}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.0")
	c.Request.Header.Set("session-id", "client-session")
	c.Request.Header.Set("thread-id", "client-thread")
	c.Request.Header.Set("x-codex-installation-id", "client-install")
	c.Request.Header.Set("x-codex-beta-features", "remote-compaction-v2")
	c.Request.Header.Set("accept-language", "zh-CN")
	req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "tok", true, "client-session", true)
	require.NoError(t, err)
	require.Equal(t, state, req.Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, "harvest-session-sticky", req.Header.Get("session_id"))
	require.Empty(t, req.Header.Get("conversation_id"))
	require.Empty(t, req.Header.Get("session-id"))
	require.Empty(t, req.Header.Get("thread-id"))
	require.Empty(t, req.Header.Get("x-codex-installation-id"))
	require.Empty(t, req.Header.Get("x-codex-beta-features"))
	require.Empty(t, req.Header.Get("accept-language"))
	require.Empty(t, req.Header.Get("x-codex-routing-hint"))
	require.Equal(t, openAICodexAstraMinVersion, req.Header.Get("version"))
	require.Equal(t, buildCodexCLIUserAgent(openAICodexAstraMinVersion), req.Header.Get("user-agent"))
	require.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
	got, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(got, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(got, "client_metadata").Exists())
}
