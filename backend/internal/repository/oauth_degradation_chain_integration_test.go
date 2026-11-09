//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// This file also runs unchanged against production before the feature existed.
// Use the existing worker API and JSON policy, not new degradation-only symbols.
func degradationChainFixture(t *testing.T, current int, empty bool) (*service.Account, *service.Group) {
	t.Helper()
	ctx := context.Background()
	var prior string
	priorErr := integrationDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", service.SettingKeyOAuthAutoConfig).Scan(&prior)
	require.True(t, priorErr == nil || priorErr == sql.ErrNoRows)
	t.Cleanup(func() {
		if priorErr == nil {
			_, err := integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, prior)
			require.NoError(t, err)
		} else {
			_, err := integrationDB.ExecContext(ctx, "DELETE FROM settings WHERE key=$1", service.SettingKeyOAuthAutoConfig)
			require.NoError(t, err)
		}
	})
	raw, err := json.Marshal(service.DefaultOAuthAutoConfig())
	require.NoError(t, err)
	var policy map[string]any
	require.NoError(t, json.Unmarshal(raw, &policy))
	policy["revision"] = fmt.Sprintf("chain-%d", time.Now().UnixNano())
	policy["degradation_action"] = map[string]any{"enabled": true, "threshold_percent": 50, "target_concurrency": 10, "models": []string{"gpt-6-luna"}}
	raw, err = json.Marshal(policy)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	group := mustCreateGroup(t, integrationEntClient, &service.Group{Name: fmt.Sprintf("chain-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Status: service.StatusActive})
	t.Cleanup(func() { require.NoError(t, integrationEntClient.Group.DeleteOneID(group.ID).Exec(ctx)) })
	mapping := map[string]any{"gpt-6-luna": "gpt-6-luna", "gpt-6.1-terra": "gpt-6.1-terra"}
	if empty {
		delete(mapping, "gpt-6-luna")
	}
	a := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "chain-fixture", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: current, Credentials: map[string]any{"model_mapping": mapping, "keep": "fixture"}, Extra: map[string]any{"manual": "retained"}})
	t.Cleanup(func() {
		for _, query := range []string{"DELETE FROM scheduler_outbox_retries WHERE account_id=$1", "DELETE FROM scheduler_outbox WHERE account_id=$1", "DELETE FROM account_auto_config_events WHERE account_id=$1", "DELETE FROM scheduled_test_plans WHERE account_id=$1", "DELETE FROM accounts WHERE id=$1"} {
			_, err := integrationDB.ExecContext(ctx, query, a.ID)
			require.NoError(t, err)
		}
	})
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{group.ID}))
	// Exclude setup events; only the real action may create the observed event.
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id=$1", a.ID)
	require.NoError(t, err)
	var plan int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at) VALUES($1,'gpt-6-luna','*/30 * * * *',true,'{"quality":{}}',NOW()-INTERVAL '2 hours') RETURNING id`, a.ID).Scan(&plan))
	for i, verdict := range []string{"correct", "incorrect"} {
		status, message := "success", ""
		if verdict == "incorrect" {
			status = "failed"
			message = "state_degraded"
		}
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message) VALUES($1,$2,NOW()-INTERVAL '1 minute',NOW()-INTERVAL '50 seconds',NOW()-INTERVAL '50 seconds','{"parallel_count":1}',$3,'evaluated',jsonb_build_object('verdict',$4::text),$5)`, plan, status, fmt.Sprintf("chain-%d-%d", a.ID, i), verdict, message)
		require.NoError(t, err)
	}
	return a, group
}

// A timing gate only: every cache operation still delegates to real Redis.
// Keeping startup here prevents a full rebuild from masquerading as outbox repair.
type degradationStartupGate struct {
	service.SchedulerCache
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *degradationStartupGate) ListBuckets(ctx context.Context) ([]service.SchedulerBucket, error) {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return g.SchedulerCache.ListBuckets(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// This hook observes server responses; it never changes a command or result.
type degradationRedisErrors struct{ denied atomic.Int64 }

func (h *degradationRedisErrors) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *degradationRedisErrors) observe(err error) {
	if err != nil && strings.Contains(err.Error(), "NOPERM") {
		h.denied.Add(1)
	}
}
func (h *degradationRedisErrors) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		h.observe(err)
		return err
	}
}
func (h *degradationRedisErrors) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		err := next(ctx, cmds)
		h.observe(err)
		return err
	}
}

// Extend the shared namespace helper for set commands and the scheduler Lua
// scripts' dynamic old-snapshot prefix arguments. No command/result is mocked.
type degradationNamespace struct{ prefix string }

func (h degradationNamespace) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h degradationNamespace) prefixCmd(cmd redis.Cmder) {
	prefixHook{prefix: h.prefix}.prefixCmd(cmd)
	args := cmd.Args()
	addPrefix := func(index int) {
		if index < len(args) {
			if value, ok := args[index].(string); ok && !strings.HasPrefix(value, h.prefix) {
				args[index] = h.prefix + value
			}
		}
	}
	switch cmd.Name() {
	case "smembers", "sadd", "srem":
		addPrefix(1)
	case "eval", "evalsha":
		if len(args) < 3 {
			return
		}
		keys, err := strconv.Atoi(fmt.Sprint(args[2]))
		if err != nil {
			return
		}
		for index := 3 + keys; index < len(args); index++ {
			if value, ok := args[index].(string); ok && strings.HasPrefix(value, "sched:") {
				addPrefix(index)
			}
		}
	}
}
func (h degradationNamespace) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.prefixCmd(cmd)
		return next(ctx, cmd)
	}
}
func (h degradationNamespace) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			h.prefixCmd(cmd)
		}
		return next(ctx, cmds)
	}
}

func degradationChainRedis(t *testing.T) (*redis.Client, func(bool), *degradationRedisErrors) {
	t.Helper()
	ctx := context.Background()
	user := fmt.Sprintf("degradation-%d", time.Now().UnixNano())
	prefix := user + ":"
	password := "disposable-ci-fixture"
	require.NoError(t, integrationRedis.Do(ctx, "ACL", "SETUSER", user, "on", ">"+password, "+@all", "~"+prefix+"*").Err())
	opts := *integrationRedis.Options()
	opts.Username = user
	opts.Password = password
	rdb := redis.NewClient(&opts)
	rdb.AddHook(degradationNamespace{prefix: prefix})
	observed := &degradationRedisErrors{}
	rdb.AddHook(observed)
	require.NoError(t, rdb.Ping(ctx).Err())
	identity, err := rdb.Do(ctx, "ACL", "WHOAMI").Text()
	require.NoError(t, err)
	require.Equal(t, user, identity, "fault injection must authenticate the isolated user, never default")
	t.Cleanup(func() {
		_ = rdb.Close()
		require.NoError(t, integrationRedis.Do(ctx, "ACL", "DELUSER", user).Err())
		var cursor uint64
		for {
			keys, next, err := integrationRedis.Scan(ctx, cursor, prefix+"*", 500).Result()
			require.NoError(t, err)
			if len(keys) > 0 {
				require.NoError(t, integrationRedis.Unlink(ctx, keys...).Err())
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	})
	setWrites := func(enabled bool) {
		permission := "-@write"
		if enabled {
			permission = "+@write"
		}
		require.NoError(t, integrationRedis.Do(ctx, "ACL", "SETUSER", user, permission).Err())
	}
	return rdb, setWrites, observed
}

func degradationGateway(t *testing.T, repo *accountRepository, snapshot *service.SchedulerSnapshotService, rdb *redis.Client) *service.OpenAIGatewayService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Runtime.Role = config.RuntimeRoleGateway // No upstream background traffic.
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, NewGatewayCache(rdb), cfg, snapshot, service.NewConcurrencyService(NewConcurrencyCache(rdb, 1, 1)), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(func() {
		gateway.StopOpenAICodexTicketHarvester()
		gateway.CloseOpenAIWSPool()
	})
	return gateway
}

func TestOAuthDegradationOutboxSchedulerChain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		restart bool
		empty   bool
		current int
	}{
		{"live_compensation", false, false, 20},
		{"restart_durable_retry", true, false, 20},
		{"empty_intersection_low_concurrency", true, true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			a, group := degradationChainFixture(t, tc.current, tc.empty)
			rdb, setWrites, observed := degradationChainRedis(t)
			cache := NewSchedulerCache(rdb)
			repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, cache)
			cfg := &config.Config{}
			cfg.Gateway.Scheduling.DbFallbackEnabled = true
			cfg.Gateway.Scheduling.OutboxPollIntervalSeconds = 1
			groups := NewGroupRepository(integrationEntClient, integrationDB)
			outbox := NewSchedulerOutboxRepository(integrationDB)
			initialID, err := outbox.MaxID(ctx)
			require.NoError(t, err)
			require.NoError(t, cache.SetOutboxWatermark(ctx, initialID))
			warm := service.NewSchedulerSnapshotService(cache, outbox, repo, groups, cfg)
			accounts, _, err := warm.ListSchedulableAccounts(ctx, &group.ID, service.PlatformOpenAI, true)
			require.NoError(t, err)
			require.Len(t, accounts, 1, "real database fallback must prewarm an untreated candidate")
			before := degradationGateway(t, repo, warm, rdb)
			selection, _, err := before.SelectAccountWithScheduler(ctx, &group.ID, "", "", "gpt-6.1-terra", nil, service.OpenAIUpstreamTransportHTTPSSE, false)
			require.NoError(t, err)
			require.True(t, selection.Acquired, "old model must actually be selectable before treatment")
			require.Equal(t, a.ID, selection.Account.ID)
			selection.ReleaseFunc()

			setWrites(false)
			startOps := func() *service.AccountOpsService {
				s := service.NewAccountOpsService(NewSettingRepository(integrationEntClient), NewAccountOpsRepository(integrationDB), nil)
				s.SetNotificationDependencies(repo, nil, false, "UTC")
				s.Start()
				return s
			}
			ops := startOps()
			defer ops.Stop()
			require.Eventually(t, func() bool {
				var marker bool
				var current int
				err := integrationDB.QueryRowContext(ctx, "SELECT concurrency,extra ? 'oauth_degradation_action' FROM accounts WHERE id=$1", a.ID).Scan(&current, &marker)
				return err == nil && marker && current == min(tc.current, 10)
			}, 8*time.Second, 20*time.Millisecond, "real degradation worker must commit treatment before cache compensation")
			ops.Stop()
			require.Greater(t, observed.denied.Load(), int64(0), "real Redis must have rejected the post-commit cache publication with NOPERM")
			var eventID int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM scheduler_outbox WHERE account_id=$1 AND event_type='account_changed'", a.ID).Scan(&eventID))
			var history int
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", a.ID).Scan(&history))
			require.Equal(t, 1, history)
			stale, err := NewSchedulerCache(rdb).GetAccount(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, tc.current, stale.Concurrency)
			require.NotContains(t, stale.Extra, "oauth_degradation_action", "failed publication must leave the prewarmed old record as evidence")
			t.Logf("DB committed account=%d history=%d outbox=%d; real Redis publication denied=%d; stale concurrency=%d", a.ID, history, eventID, observed.denied.Load(), stale.Concurrency)

			startSnapshots := func() (*service.SchedulerSnapshotService, func()) {
				gate := &degradationStartupGate{SchedulerCache: NewSchedulerCache(rdb), entered: make(chan struct{}), release: make(chan struct{})}
				s := service.NewSchedulerSnapshotService(gate, outbox, repo, groups, cfg)
				var once sync.Once
				release := func() { once.Do(func() { close(gate.release) }) }
				t.Cleanup(func() { release(); s.Stop() })
				s.Start()
				select {
				case <-gate.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("startup full rebuild did not enter the explicit gate")
				}
				return s, release
			}
			snapshots, releaseStartup := startSnapshots()
			require.Eventually(t, func() bool {
				var n int
				return integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox_retries WHERE event_id=$1 AND lease_until IS NULL", eventID).Scan(&n) == nil && n == 1
			}, 12*time.Second, 20*time.Millisecond, "real failed outbox handler must persist a retry in PostgreSQL")
			if tc.restart {
				releaseStartup() // Still denied: this startup cannot repair the cache.
				snapshots.Stop()
				repo = newAccountRepositoryWithSQL(integrationEntClient, integrationDB, NewSchedulerCache(rdb))
				outbox = NewSchedulerOutboxRepository(integrationDB)
				snapshots, releaseStartup = startSnapshots()
				restartedOps := startOps()
				restartedOps.Stop()
				t.Log("restarted evaluator/repository/outbox/snapshot services with only persisted state; startup rebuild remains gated")
			}
			setWrites(true)
			require.Eventually(t, func() bool {
				var n int
				err := integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox_retries WHERE event_id=$1", eventID).Scan(&n)
				watermark, cacheErr := cache.GetOutboxWatermark(ctx)
				return err == nil && cacheErr == nil && n == 0 && watermark >= eventID
			}, 20*time.Second, 20*time.Millisecond, "durable retry must complete and real consumer watermark advance while startup is gated")
			// Fresh objects avoid process-local snapshot memoization. Disable DB
			// fallback so the final choices must use the repaired Redis candidate.
			readerCfg := &config.Config{}
			reader := service.NewSchedulerSnapshotService(NewSchedulerCache(rdb), outbox, repo, groups, readerCfg)
			repaired, _, err := reader.ListSchedulableAccounts(ctx, &group.ID, service.PlatformOpenAI, true)
			require.NoError(t, err, "fresh Redis candidates must be ready without database fallback")
			require.Len(t, repaired, 1)
			require.Equal(t, a.ID, repaired[0].ID)
			require.Contains(t, repaired[0].Extra, "oauth_degradation_action")
			require.Equal(t, min(tc.current, 10), repaired[0].Concurrency)
			fresh := degradationGateway(t, repo, reader, rdb)
			selection, _, err = fresh.SelectAccountWithScheduler(ctx, &group.ID, "", "", "gpt-6.1-terra", nil, service.OpenAIUpstreamTransportHTTPSSE, false)
			require.ErrorIs(t, err, service.ErrNoAvailableAccounts, "fresh actual scheduler must reject the former model after outbox compensation")
			require.Nil(t, selection)
			if tc.empty {
				selection, _, err = fresh.SelectAccountWithScheduler(ctx, &group.ID, "", "", "gpt-6-luna", nil, service.OpenAIUpstreamTransportHTTPSSE, false)
				require.ErrorIs(t, err, service.ErrNoAvailableAccounts, "empty authorization intersection must reject every model at actual selection")
				require.Nil(t, selection)
			} else {
				var releases []func()
				defer func() {
					for _, release := range releases {
						release()
					}
				}()
				for range 10 {
					selection, _, err = fresh.SelectAccountWithScheduler(ctx, &group.ID, "", "", "gpt-6-luna", nil, service.OpenAIUpstreamTransportHTTPSSE, false)
					require.NoError(t, err)
					require.True(t, selection.Acquired)
					releases = append(releases, selection.ReleaseFunc)
					require.Equal(t, a.ID, selection.Account.ID)
					require.Equal(t, 10, selection.Account.Concurrency)
				}
				selection, _, err = fresh.SelectAccountWithScheduler(ctx, &group.ID, "", "", "gpt-6-luna", nil, service.OpenAIUpstreamTransportHTTPSSE, false)
				if selection != nil && selection.Acquired {
					selection.ReleaseFunc()
					t.Fatal("actual scheduler acquired an eleventh Redis slot after concurrency was capped at 10")
				}
				require.True(t, err != nil || selection != nil, "busy scheduling must produce an error or a wait plan")
				if err == nil {
					require.NotNil(t, selection.WaitPlan)
					require.Equal(t, 10, selection.WaitPlan.MaxConcurrency)
				} else {
					require.ErrorIs(t, err, service.ErrNoAvailableAccounts)
				}
				inFlight, err := NewConcurrencyCache(rdb, 1, 1).GetAccountConcurrency(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, 10, inFlight, "actual Redis must contain exactly the ten retained scheduler slots")
			}
			current, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, min(tc.current, 10), current.Concurrency)
			require.Equal(t, "retained", current.Extra["manual"])
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", a.ID).Scan(&history))
			require.Equal(t, 1, history, "restart and re-evaluation must not apply a second penalty")
			t.Logf("outbox retry completed event=%d; startup still gated; actual scheduler rejected old model, empty=%v, concurrency=%d", eventID, tc.empty, current.Concurrency)
			releaseStartup()
			snapshots.Stop()
		})
	}
}
