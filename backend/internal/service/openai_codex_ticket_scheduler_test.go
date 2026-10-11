package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Mirror the persisted metadata projection, including a JSON round trip.
// Repository tests separately exercise the actual Redis projection writer.
func ticketSchedulerProjection(t *testing.T, full *Account) *Account {
	t.Helper()
	projected := *full
	projected.Credentials = map[string]any{"plan_type": full.GetCredential("plan_type")}
	projected.Extra = nil
	payload, err := json.Marshal(projected)
	require.NoError(t, err)
	var decoded Account
	require.NoError(t, json.Unmarshal(payload, &decoded))
	return &decoded
}

func ticketSchedulerFixture(t *testing.T, advanced, loadBatch bool) (*OpenAIGatewayService, *openAISnapshotCacheStub, *Account, int64) {
	t.Helper()
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	groupID := int64(2)
	account := ticketTestAccount(400)
	account.GroupIDs = []int64{groupID}
	account.Concurrency = 10
	account.Credentials["email"] = "fixture@example.invalid"
	account.Credentials["plan_type"] = "pro"
	account.Extra = map[string]any{
		openAICodexTicketExtraKey("gpt-6-astra"): &openAICodexTicket{
			AccountID: account.ID, Model: "gpt-6-astra",
			State: fakeCodexTicketState(292), Length: 292,
			CapturedAt: time.Now(), IssuedAt: time.Now(),
			ExpiresAt: time.Now().Add(30 * time.Minute), Identity: ticketIdentity(account),
		},
	}
	cache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{ticketSchedulerProjection(t, account)},
		accountsByID:     map[int64]*Account{account.ID: account},
	}
	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{
		Enabled: true, FailClosed: true, TargetLength: 292, Models: []string{"gpt-6-astra"},
	}
	cfg.Gateway.Scheduling.LoadBatchEnabled = loadBatch
	cfg.Gateway.OpenAIWS.LBTopK = 1
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{*account}}
	svc := &OpenAIGatewayService{
		cfg: cfg, accountRepo: repo, cache: &schedulerTestGatewayCache{},
		schedulerSnapshot:  &SchedulerSnapshotService{cache: cache, accountRepo: repo},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	if advanced {
		svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
	}
	return svc, cache, account, groupID
}

func TestCodexTicketSchedulerProjectedCandidate(t *testing.T) {
	for _, path := range []struct {
		name                string
		advanced, loadBatch bool
	}{
		{"advanced", true, true},
		{"legacy_batch", false, true},
		{"legacy_nonbatch", false, false},
	} {
		for _, warm := range []bool{false, true} {
			name := path.name + "/cold"
			if warm {
				name = path.name + "/warm"
			}
			t.Run(name, func(t *testing.T) {
				svc, _, account, groupID := ticketSchedulerFixture(t, path.advanced, path.loadBatch)
				if warm {
					require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
				}
				result, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "",
					"gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, account.ID, result.Account.ID)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestCodexTicketSchedulerAdmissionSafety(t *testing.T) {
	fixtureTicket := func(t *testing.T, a *Account) *openAICodexTicket {
		t.Helper()
		ticket, ok := a.Extra[openAICodexTicketExtraKey("gpt-6-astra")].(*openAICodexTicket)
		require.True(t, ok, "fixture must contain a typed ticket")
		require.NotNil(t, ticket)
		return ticket
	}
	for _, advanced := range []bool{false, true} {
		mode := "legacy"
		if advanced {
			mode = "advanced"
		}
		for _, tc := range []struct {
			name string
			edit func(*testing.T, *OpenAIGatewayService, *Account)
			ok   bool
		}{
			{"missing", func(_ *testing.T, _ *OpenAIGatewayService, a *Account) { a.Extra = nil }, false},
			{"expired", func(t *testing.T, _ *OpenAIGatewayService, a *Account) {
				fixtureTicket(t, a).ExpiresAt = time.Now().Add(-time.Minute)
			}, false},
			{"changed_identity", func(_ *testing.T, _ *OpenAIGatewayService, a *Account) {
				a.Credentials["chatgpt_account_id"] = "changed-fixture"
			}, false},
			{"revoked_in_memory", func(t *testing.T, s *OpenAIGatewayService, a *Account) {
				tombstone := *fixtureTicket(t, a)
				tombstone.Revoked = true
				s.openaiCodexTickets.Store(openAICodexTicketKey(a.ID, "gpt-6-astra"), &tombstone)
			}, false},
			{"valid_standby", func(t *testing.T, _ *OpenAIGatewayService, a *Account) {
				ticket := fixtureTicket(t, a)
				standby := *ticket
				ticket.ExpiresAt = time.Now().Add(-time.Minute)
				ticket.Standby = &standby
			}, true},
			{"stopped_after_snapshot", func(_ *testing.T, _ *OpenAIGatewayService, a *Account) { a.Schedulable = false }, false},
			{"removed_group_after_snapshot", func(_ *testing.T, _ *OpenAIGatewayService, a *Account) { a.GroupIDs = nil }, false},
			{"database_final_recheck_stopped", func(_ *testing.T, s *OpenAIGatewayService, a *Account) {
				latest := *a
				latest.Schedulable = false
				s.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{latest}}
			}, false},
			{"database_final_recheck_removed_group", func(_ *testing.T, s *OpenAIGatewayService, a *Account) {
				latest := *a
				latest.GroupIDs = nil
				s.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{latest}}
			}, false},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				svc, _, account, groupID := ticketSchedulerFixture(t, advanced, true)
				tc.edit(t, svc, account)
				result, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "",
					"gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
				if tc.ok {
					require.NoError(t, err)
					require.NotNil(t, result)
				} else {
					require.Error(t, err)
					require.Nil(t, result)
				}
				if result != nil && result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestCodexTicketSchedulerTopKSkipsTicketlessAccount(t *testing.T) {
	svc, cache, valid, groupID := ticketSchedulerFixture(t, true, true)
	ticketless := *valid
	ticketless.ID = 399
	ticketless.Priority = -100
	ticketless.Extra = nil
	cache.snapshotAccounts = append([]*Account{ticketSchedulerProjection(t, &ticketless)}, cache.snapshotAccounts...)
	cache.accountsByID[ticketless.ID] = &ticketless
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{ticketless, *valid}}
	result, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "",
		"gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, valid.ID, result.Account.ID)
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
}

type ticketCountingSnapshotCache struct {
	*openAISnapshotCacheStub
	batchReads int
	err        error
}

func (c *ticketCountingSnapshotCache) GetAccounts(_ context.Context, ids []int64) (map[int64]*Account, error) {
	c.batchReads++
	if c.err != nil {
		return nil, c.err
	}
	out := make(map[int64]*Account, len(ids))
	for _, id := range ids {
		out[id] = c.accountsByID[id]
	}
	return out, nil
}

func enableTicketFirstScheduling(svc *OpenAIGatewayService) {
	svc.codexHarvest = &CodexHarvestService{
		current:     CodexHarvestControls{Version: 1, Transport: "sse", TargetGateway: "unified-88", TicketFirstScheduling: true},
		loadedUntil: time.Now().Add(time.Hour),
	}
}

func TestCodexTicketCandidateHydrationScope(t *testing.T) {
	for _, name := range []string{"enabled", "fail_open", "fail_open_ticket_first", "disabled", "ungated", "excluded", "compact_ungated"} {
		t.Run(name, func(t *testing.T) {
			svc, snapshot, account, groupID := ticketSchedulerFixture(t, true, true)
			cache := &ticketCountingSnapshotCache{openAISnapshotCacheStub: snapshot}
			svc.schedulerSnapshot.cache = cache
			model, compact := "gpt-6-astra", false
			var excluded map[int64]struct{}
			switch name {
			case "fail_open":
				svc.cfg.Gateway.OpenAICodexTicket.FailClosed = false
			case "fail_open_ticket_first":
				svc.cfg.Gateway.OpenAICodexTicket.FailClosed = false
				enableTicketFirstScheduling(svc)
			case "disabled":
				svc.cfg.Gateway.OpenAICodexTicket.Enabled = false
			case "ungated":
				model = "gpt-5.5"
			case "excluded":
				excluded = map[int64]struct{}{account.ID: {}}
			case "compact_ungated":
				compact = true
				svc.cfg.Gateway.OpenAICompactModel = "gpt-5.5"
			}
			_, err := svc.listSchedulableAccountsForRequest(context.Background(), &groupID,
				PlatformOpenAI, model, compact, excluded)
			require.NoError(t, err)
			if name == "enabled" || name == "fail_open_ticket_first" {
				require.Equal(t, 1, cache.batchReads)
			} else {
				require.Zero(t, cache.batchReads)
			}
		})
	}
}

func TestCodexTicketSchedulerCompactUsesOutboundModel(t *testing.T) {
	for _, path := range []struct {
		name                string
		advanced, loadBatch bool
	}{
		{"advanced", true, true},
		{"legacy_batch", false, true},
		{"legacy_nonbatch", false, false},
	} {
		for _, scenario := range []string{"ungated_outbound", "gated_valid_ticket", "gated_missing_ticket"} {
			t.Run(path.name+"/"+scenario, func(t *testing.T) {
				svc, _, account, groupID := ticketSchedulerFixture(t, path.advanced, path.loadBatch)
				requested, outbound := "gpt-5.5", "gpt-6-astra"
				if scenario == "ungated_outbound" {
					requested, outbound = outbound, requested
				}
				if scenario != "gated_valid_ticket" {
					account.Extra = nil
				}
				svc.cfg.Gateway.OpenAICompactModel = outbound
				result, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "",
					requested, nil, OpenAIUpstreamTransportAny, true)
				if scenario == "gated_missing_ticket" {
					require.Error(t, err)
					require.Nil(t, result)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestCodexTicketSchedulerStorageFailureDoesNotFailOpen(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "advanced"}[advanced], func(t *testing.T) {
			svc, snapshot, _, groupID := ticketSchedulerFixture(t, advanced, true)
			cache := &ticketCountingSnapshotCache{openAISnapshotCacheStub: snapshot, err: errors.New("cache unavailable")}
			svc.schedulerSnapshot.cache = cache
			svc.schedulerSnapshot.cfg = svc.cfg // DB fallback explicitly disabled.
			result, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "",
				"gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
			require.ErrorContains(t, err, "codex ticket scheduling state unavailable")
			require.Nil(t, result)
			require.Equal(t, 1, cache.batchReads)
		})
	}
}

func TestCodexTicketSchedulerFailOpenHydrationFailureKeepsServing(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "advanced"}[advanced], func(t *testing.T) {
			svc, snapshot, account, groupID := ticketSchedulerFixture(t, advanced, true)
			svc.cfg.Gateway.OpenAICodexTicket.FailClosed = false
			enableTicketFirstScheduling(svc)
			cache := &ticketCountingSnapshotCache{openAISnapshotCacheStub: snapshot, err: errors.New("cache unavailable")}
			svc.schedulerSnapshot.cache = cache
			svc.schedulerSnapshot.cfg = svc.cfg
			result, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "",
				"gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, account.ID, result.Account.ID)
			require.GreaterOrEqual(t, cache.batchReads, 1)
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
}

// A higher-priority account without a ticket must not outrank a ticket holder
// under fail-open, where the ticketless account is still admissible.
func ticketFirstFixture(t *testing.T, advanced, loadBatch bool) (*OpenAIGatewayService, *Account, *Account, int64) {
	t.Helper()
	svc, cache, ticketed, groupID := ticketSchedulerFixture(t, advanced, loadBatch)
	svc.cfg.Gateway.OpenAICodexTicket.FailClosed = false
	enableTicketFirstScheduling(svc)
	ticketed.Priority = 50
	cache.snapshotAccounts = []*Account{ticketSchedulerProjection(t, ticketed)}
	ticketless := *ticketed
	ticketless.ID = 399
	ticketless.Priority = -100
	ticketless.Extra = nil
	cache.snapshotAccounts = append([]*Account{ticketSchedulerProjection(t, &ticketless)}, cache.snapshotAccounts...)
	cache.accountsByID[ticketless.ID] = &ticketless
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{ticketless, *ticketed}}
	return svc, ticketed, &ticketless, groupID
}

func TestCodexTicketSchedulerPrefersTicketHolders(t *testing.T) {
	for _, path := range []struct {
		name                string
		advanced, loadBatch bool
	}{
		{"advanced", true, true},
		{"legacy_batch", false, true},
		{"legacy_nonbatch", false, false},
	} {
		for _, scenario := range []string{"ticket_first", "fallback_when_excluded", "ungated_keeps_priority", "sticky_migrates", "switch_off", "switch_off_sticky_stays"} {
			t.Run(path.name+"/"+scenario, func(t *testing.T) {
				svc, ticketed, ticketless, groupID := ticketFirstFixture(t, path.advanced, path.loadBatch)
				model, session, want := "gpt-6-astra", "", ticketed.ID
				var excluded map[int64]struct{}
				var cache *schedulerTestGatewayCache
				switch scenario {
				case "fallback_when_excluded":
					excluded, want = map[int64]struct{}{ticketed.ID: {}}, ticketless.ID
				case "ungated_keeps_priority":
					model, want = "gpt-5.5", ticketless.ID
				case "switch_off":
					svc.codexHarvest.current.TicketFirstScheduling = false
					want = ticketless.ID
				case "switch_off_sticky_stays":
					svc.codexHarvest.current.TicketFirstScheduling = false
					want = ticketless.ID
					fallthrough
				case "sticky_migrates":
					session = "synthetic-session"
					cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + session: ticketless.ID}}
					svc.cache = cache
				}
				result, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", session,
					model, excluded, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, want, result.Account.ID)
				if cache != nil {
					require.Equal(t, want, cache.sessionBindings["openai:"+session])
				}
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestCodexTicketSchedulerAffinityCannotBypassFinalState(t *testing.T) {
	for _, binding := range []string{"session", "previous_response"} {
		for _, mutation := range []string{"stopped", "removed_group"} {
			t.Run(binding+"/"+mutation, func(t *testing.T) {
				svc, snapshot, stale, groupID := ticketSchedulerFixture(t, true, true)
				svc.cfg.Gateway.OpenAIWS.LBTopK = 2 // Include the healthy fallback in the probe budget.
				backup := *stale
				backup.ID = 401
				backup.Priority = 10
				snapshot.snapshotAccounts = append(snapshot.snapshotAccounts, ticketSchedulerProjection(t, &backup))
				snapshot.accountsByID[backup.ID] = &backup
				latest := *stale
				if mutation == "stopped" {
					latest.Schedulable = false
				} else {
					latest.GroupIDs = nil
				}
				svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{latest, backup}}
				previous, session := "", ""
				if binding == "session" {
					session = "synthetic-session"
					svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + session: stale.ID}}
				} else {
					previous = "resp_synthetic"
					svc.cfg.Gateway.OpenAIWS.Enabled = true
					svc.cfg.Gateway.OpenAIWS.OAuthEnabled = true
					svc.cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
					require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(context.Background(), groupID, previous, stale.ID, time.Hour))
				}
				result, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, previous, session,
					"gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, backup.ID, result.Account.ID)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}
