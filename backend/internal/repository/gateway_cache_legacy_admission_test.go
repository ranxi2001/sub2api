//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const (
	legacyAdmissionGroup   int64 = 949001
	legacyAdmissionA       int64 = 949101
	legacyAdmissionB       int64 = 949102
	legacyAdmissionC       int64 = 949103
	legacyAdmissionSession       = "legacy-admission-redis"
)

type legacyAdmissionRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r *legacyAdmissionRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range r.accounts {
		if account.ID == id {
			return &account, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (r *legacyAdmissionRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	if groupID != legacyAdmissionGroup || platform != service.PlatformOpenAI {
		return nil, nil
	}
	return append([]service.Account(nil), r.accounts...), nil
}

type legacyAdmissionConcurrency struct {
	service.ConcurrencyCache
	blockedA bool
	waitingA int
	loadErr  error
	acquired []int64
	released []int64
}

func (c *legacyAdmissionConcurrency) AcquireAccountSlot(_ context.Context, id int64, _ int, _ string) (bool, error) {
	if id == legacyAdmissionA && c.blockedA {
		return false, nil
	}
	c.acquired = append(c.acquired, id)
	return true, nil
}

func (c *legacyAdmissionConcurrency) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	c.released = append(c.released, id)
	return nil
}

func (c *legacyAdmissionConcurrency) GetAccountWaitingCount(_ context.Context, id int64) (int, error) {
	if id == legacyAdmissionA {
		return c.waitingA, nil
	}
	return 0, nil
}

func (c *legacyAdmissionConcurrency) GetAccountsLoadBatch(_ context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	if c.loadErr != nil {
		return nil, c.loadErr
	}
	loads := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		load := &service.AccountLoadInfo{AccountID: account.ID}
		if account.ID == legacyAdmissionA && c.blockedA {
			load.CurrentConcurrency, load.LoadRate = 1, 100
		}
		loads[account.ID] = load
	}
	return loads, nil
}

func newLegacyAdmissionRedisGateway(t *testing.T, batch bool) (*service.OpenAIGatewayService, *gatewayCache, *legacyAdmissionConcurrency) {
	t.Helper()
	cache, _ := newHTTPResponseOwnerTestCache(t)
	repo := &legacyAdmissionRepo{}
	for _, candidate := range []struct {
		id       int64
		priority int
		ws       bool
	}{{legacyAdmissionA, 10, true}, {legacyAdmissionB, 0, false}, {legacyAdmissionC, 5, true}} {
		repo.accounts = append(repo.accounts, service.Account{
			ID: candidate.id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1,
			Priority: candidate.priority, GroupIDs: []int64{legacyAdmissionGroup},
			Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": candidate.ws},
		})
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Runtime.Role = config.RuntimeRoleGateway
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 3600
	cfg.Gateway.Scheduling.LoadBatchEnabled = batch
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	concurrency := &legacyAdmissionConcurrency{}
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cache, cfg, nil,
		service.NewConcurrencyService(concurrency),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	// The fixture never starts a transport or enables the ticket harvester.
	gateway.StopOpenAICodexTicketHarvester()
	return gateway, cache, concurrency
}

func selectLegacyAdmissionRedisAccount(gateway *service.OpenAIGatewayService, excluded map[int64]struct{}) (*service.AccountSelectionResult, error) {
	groupID := legacyAdmissionGroup
	selection, _, err := gateway.SelectAccountWithSchedulerForCapability(context.Background(), &groupID, "", legacyAdmissionSession, "gpt-5.1", excluded,
		service.OpenAIUpstreamTransportResponsesWebsocketV2Ingress, service.OpenAIEndpointCapabilityChatCompletions, false, false, true)
	return selection, err
}

func TestStickySessionLegacyAdmissionRedis(t *testing.T) {
	for _, batch := range []bool{false, true} {
		mode := "priority_lru"
		if batch {
			mode = "load_batch"
		}
		for _, scenario := range []string{"new_session", "excluded_A", "capacity_spillover", "load_read_failure"} {
			if !batch && (scenario == "capacity_spillover" || scenario == "load_read_failure") {
				continue
			}
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				gateway, cache, concurrency := newLegacyAdmissionRedisGateway(t, batch)
				var excluded map[int64]struct{}
				if scenario != "new_session" {
					require.NoError(t, cache.SetSessionAccountID(context.Background(), legacyAdmissionGroup, "openai:"+legacyAdmissionSession, legacyAdmissionA, time.Hour))
				}
				if scenario == "excluded_A" {
					// A previous attempt already excluded A; the same session spills over.
					excluded = map[int64]struct{}{legacyAdmissionA: {}}
				}
				if scenario == "capacity_spillover" || scenario == "load_read_failure" {
					concurrency.blockedA, concurrency.waitingA = true, 3
				}
				if scenario == "load_read_failure" {
					concurrency.loadErr = errors.New("synthetic load read failure")
				}
				selected, err := selectLegacyAdmissionRedisAccount(gateway, excluded)
				require.NoError(t, err, "LEGACY_ADMISSION_FIXTURE: public selection must succeed")
				require.NotNil(t, selected)
				require.NotNil(t, selected.ReleaseFunc)
				t.Cleanup(selected.ReleaseFunc)
				require.Equal(t, legacyAdmissionC, selected.Account.ID, "LEGACY_ADMISSION_FIXTURE: WSv2 must select C")
				require.True(t, selected.Acquired)
				require.Contains(t, concurrency.acquired, legacyAdmissionB, "LEGACY_ADMISSION_FIXTURE: B must reach outer admission")
				require.Equal(t, []int64{legacyAdmissionB}, concurrency.released, "LEGACY_ADMISSION_FIXTURE: only rejected B must be released before return")
				binding, err := cache.GetSessionAccountID(context.Background(), legacyAdmissionGroup, "openai:"+legacyAdmissionSession)
				require.NoError(t, err)
				require.Equal(t, legacyAdmissionC, binding, "LEGACY_ADMISSION_REDIS: final admitted C must own sticky session")
				admissionCtx := service.ContextWithSelectionProfitGate(context.Background(), selected)
				groupID := legacyAdmissionGroup
				require.NoError(t, gateway.BindStickySessionAfterProfitAdmission(admissionCtx, &groupID, legacyAdmissionSession, legacyAdmissionC))
				binding, err = cache.GetSessionAccountID(context.Background(), legacyAdmissionGroup, "openai:"+legacyAdmissionSession)
				require.NoError(t, err)
				require.Equal(t, legacyAdmissionC, binding)
			})
		}
	}
}

func TestStickySessionLegacyAdmissionRedisLateWaiter(t *testing.T) {
	for _, batch := range []bool{false, true} {
		mode := "priority_lru"
		if batch {
			mode = "load_batch"
		}
		t.Run(mode, func(t *testing.T) {
			gateway, cache, concurrency := newLegacyAdmissionRedisGateway(t, batch)
			ctx := context.Background()
			require.NoError(t, cache.SetSessionAccountID(ctx, legacyAdmissionGroup, "openai:"+legacyAdmissionSession, legacyAdmissionA, time.Hour))
			concurrency.blockedA = true
			queued, err := selectLegacyAdmissionRedisAccount(gateway, nil)
			require.NoError(t, err)
			require.NotNil(t, queued)
			require.False(t, queued.Acquired)
			require.NotNil(t, queued.WaitPlan)
			require.Equal(t, legacyAdmissionA, queued.WaitPlan.AccountID)
			queuedCtx := service.ContextWithSelectionProfitGate(ctx, queued)

			// A different request excludes the waiting account, rejects B, and admits C.
			replacement, err := selectLegacyAdmissionRedisAccount(gateway, map[int64]struct{}{legacyAdmissionA: {}})
			require.NoError(t, err)
			require.NotNil(t, replacement)
			t.Cleanup(replacement.ReleaseFunc)
			require.Equal(t, legacyAdmissionC, replacement.Account.ID)
			binding, err := cache.GetSessionAccountID(ctx, legacyAdmissionGroup, "openai:"+legacyAdmissionSession)
			require.NoError(t, err)
			require.Equal(t, legacyAdmissionC, binding, "LEGACY_ADMISSION_REDIS: replacement must publish C before late admission")

			// Simulate the older waiter's post-slot handler boundary with its original snapshot.
			concurrency.blockedA = false
			groupID := legacyAdmissionGroup
			require.NoError(t, gateway.BindStickySessionAfterProfitAdmission(queuedCtx, &groupID, legacyAdmissionSession, legacyAdmissionA))
			binding, err = cache.GetSessionAccountID(ctx, legacyAdmissionGroup, "openai:"+legacyAdmissionSession)
			require.NoError(t, err)
			require.Equal(t, legacyAdmissionC, binding, "LEGACY_ADMISSION_STALE_WAITER: old A admission cannot overwrite another request's C binding")
		})
	}
}
