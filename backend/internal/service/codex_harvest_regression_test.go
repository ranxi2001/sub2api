package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexHarvestAdmissionMatchesInjection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failClosed bool
		skip       bool
		ready      bool
		scopeError bool
	}{
		{name: "excluded leftover", failClosed: true, ready: true},
		{name: "scope error leftover", failClosed: true, ready: true, scopeError: true},
		{name: "skip with own leftover", failClosed: true, skip: true, ready: true},
		{name: "skip without ticket", failClosed: true, skip: true},
		{name: "skip with scope error", failClosed: true, skip: true, ready: true, scopeError: true},
		{name: "fail open leftover", ready: true},
		{name: "fail open missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := harvestScopeAccount(5, true, 26)
			account.Extra = map[string]any{OpenAICodexSkipHarvestExtraKey: tc.skip}
			if tc.ready {
				attachReadyCodexTicket(&account, "gpt-6-astra")
			}
			svc, _, repo := harvestScopeService(t, `{"mode":"selected","group_ids":[3]}`, nil, 1)
			if tc.failClosed {
				repo.values[SettingKeyOpenAICodexTicketFailClosed] = "true"
			}
			if tc.scopeError {
				repo.values[SettingKeyOpenAICodexTicketHarvestScope] = `{`
			}
			blocked := tc.failClosed && !tc.skip
			require.Equal(t, blocked, svc.openAICodexTicketBlocksAccount(&account, "gpt-6-astra"))
			headers := http.Header{}
			err := svc.applyOpenAICodexTicket(context.Background(), &account, "gpt-6-astra", headers)
			if blocked {
				require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
				require.Empty(t, headers.Get(openAICodexTurnStateHeader))
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.ready, headers.Get(openAICodexTurnStateHeader) != "")
			}
			require.Equal(t, tc.ready && !blocked, svc.openAICodexTicketReadyForRequest(&account, "gpt-6-astra", false))
		})
	}
}

func TestCodexHarvestSnapshotPreservesEmptyModels(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: config.OpenAICodexTicketConfig{
		Enabled: true, FailClosed: true, Models: []string{},
	}}}
	gateway := &OpenAIGatewayService{cfg: cfg}
	require.Empty(t, gateway.openAICodexTicketConfig().Models)
	require.False(t, gateway.openAICodexTicketGatedModel("gpt-6-astra"))
	cfg.Gateway.OpenAICodexTicket.Models = nil
	require.Equal(t, []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}, gateway.openAICodexTicketConfig().Models)
}

type delayedHarvestScopeRepo struct {
	SettingRepository
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *delayedHarvestScopeRepo) GetValue(ctx context.Context, key string) (string, error) {
	if key != SettingKeyOpenAICodexTicketHarvestScope {
		return "", ErrSettingNotFound
	}
	if r.calls.Add(1) == 1 {
		close(r.started)
		select {
		case <-r.release:
			return `{"mode":"all"}`, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return `{"mode":"selected","group_ids":[26]}`, nil
}

func TestCodexHarvestScopeInvalidationDiscardsInflightRead(t *testing.T) {
	repo := &delayedHarvestScopeRepo{started: make(chan struct{}), release: make(chan struct{})}
	settings := NewSettingService(repo, &config.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		scope CodexTicketHarvestScope
		err   error
	}
	done := make(chan result, 1)
	go func() {
		scope, err := settings.GetCodexTicketHarvestScope(ctx)
		done <- result{scope, err}
	}()
	select {
	case <-repo.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	settings.InvalidateOpenAICodexTicketHarvestScopeCache()
	scope, err := settings.GetCodexTicketHarvestScope(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{26}, scope.GroupIDs)
	close(repo.release)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, []int64{26}, got.scope.GroupIDs)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	scope, err = settings.GetCodexTicketHarvestScope(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{26}, scope.GroupIDs)
}

func TestCodexHarvestScopeStorageErrorDoesNotCacheAll(t *testing.T) {
	svc, _, repo := harvestScopeService(t, "", nil, 1)
	repo.err = errors.New("database unavailable")
	_, err := svc.settingService.GetCodexTicketHarvestScope(context.Background())
	require.Error(t, err)
	repo.err = nil
	repo.values[SettingKeyOpenAICodexTicketHarvestScope] = `{"mode":"selected","group_ids":[]}`
	scope, err := svc.settingService.GetCodexTicketHarvestScope(context.Background())
	require.NoError(t, err)
	require.Equal(t, "selected", scope.Mode)
}
