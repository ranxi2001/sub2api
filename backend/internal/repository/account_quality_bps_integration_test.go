//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type qualityBPSFixture struct {
	t       *testing.T
	plans   service.ScheduledTestPlanRepository
	account int64
	plan    *service.ScheduledTestPlan
	until   time.Time
}

func newQualityBPSFixture(t *testing.T, extra string, policy *service.QualityPolicy) *qualityBPSFixture {
	t.Helper()
	ctx := context.Background()
	f := &qualityBPSFixture{t: t, plans: NewScheduledTestPlanRepository(integrationDB)}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable,extra) VALUES('quality-bps','openai','oauth','active',true,$1::jsonb) RETURNING id`, extra).Scan(&f.account))
	// This fixture represents accounts already authorized for BPS; tests below
	// explicitly remove the grant when exercising preparation failures.
	_, grantErr := integrationDB.ExecContext(ctx, `INSERT INTO openai_excel_oauth_credentials(account_id,credentials_ciphertext) VALUES($1,'test-encrypted-grant')`, f.account)
	require.NoError(t, grantErr)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, f.account)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduled_test_plans WHERE account_id=$1`, f.account)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, f.account)
	})
	svc := service.NewScheduledTestService(f.plans, NewScheduledTestResultRepository(integrationDB))
	plan, err := svc.CreatePlan(ctx, &service.ScheduledTestPlan{AccountID: f.account, ModelID: "gpt-6-astra", CronExpression: "*/30 * * * *", Enabled: true, MaxResults: 100,
		PelicanConfig: &service.PelicanTestConfig{QuestionKind: service.OpenAICodexStateProbeQuestionKind, ReasoningEffort: "medium", ParallelCount: 1, Quality: policy}})
	require.NoError(t, err)
	f.plan = plan
	f.claim()
	return f
}

func (f *qualityBPSFixture) claim() {
	f.t.Helper()
	ctx := context.Background()
	require.NoError(f.t, f.plans.TriggerQuality(ctx, f.plan.ID))
	plan, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(f.t, err)
	// Windows 上 Go 的墙钟按系统时钟节拍更新，可能略早于数据库 NOW()，留一秒余量。
	now := time.Now().Add(time.Second).Truncate(time.Microsecond)
	f.until = now.Add(15 * time.Minute)
	ok, err := f.plans.ClaimPelican(ctx, plan, now, f.until, now.Add(30*time.Minute))
	require.NoError(f.t, err)
	require.True(f.t, ok)
	f.plan = plan
}

func (f *qualityBPSFixture) apply(outcome string) string {
	f.t.Helper()
	got, err := f.plans.ApplyQualityOutcome(context.Background(), f.plan, f.until, outcome)
	require.NoError(f.t, err)
	return got
}

func (f *qualityBPSFixture) exec(query string, args ...any) {
	f.t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), query, append([]any{f.account}, args...)...)
	require.NoError(f.t, err)
}

func (f *qualityBPSFixture) extra() map[string]any {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), `SELECT extra FROM accounts WHERE id=$1`, f.account).Scan(&raw))
	extra := map[string]any{}
	require.NoError(f.t, json.Unmarshal(raw, &extra))
	return extra
}

func (f *qualityBPSFixture) count(query string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, integrationDB.QueryRowContext(context.Background(), query, f.account).Scan(&n))
	return n
}

// events 返回待处理的 account_changed 事件数并清空，模拟调度器已消费（未消费的同类事件会去重合并）。
func (f *qualityBPSFixture) events() int {
	f.t.Helper()
	n := f.count(`SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type='account_changed'`)
	f.exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`)
	return n
}

func TestQualityEnableBPSLifecycle(t *testing.T) {
	f := newQualityBPSFixture(t, `{"codex_7d_used_percent":10,"unrelated":"kept","openai_excel_bps_cache_creation_as_input":true}`, &service.QualityPolicy{
		Action: service.QualityActionEnableBPS, AutoRestore: true,
		BPS: &service.QualityBPSPolicy{FailureThreshold: 2, UsagePercent: 80, PassThreshold: 2, HoldOnUsage: true,
			Models: []string{"gpt-6-astra"}, OmitUnsupportedTools: true, IgnoreEncryptedContent: true},
	})
	states := `SELECT count(*) FROM account_quality_states s JOIN scheduled_test_plans p ON p.id=s.plan_id WHERE p.account_id=$1`
	passStreak := `SELECT COALESCE((s.state->>'pass_streak')::int,0) FROM account_quality_states s JOIN scheduled_test_plans p ON p.id=s.plan_id WHERE p.account_id=$1`

	// 连续次数：降智累加、无法判断不打断、通过清零。
	require.Equal(t, "failure_counted:1/2", f.apply("failed"))
	require.Equal(t, "inconclusive", f.apply("inconclusive"))
	require.Equal(t, "passed", f.apply("passed"))
	require.Zero(t, f.count(states), "a zero streak leaves no state row")
	require.Zero(t, f.events())
	require.Equal(t, "failure_counted:1/2", f.apply("failed"))
	require.Equal(t, "inconclusive", f.apply("inconclusive"))
	require.Equal(t, "bps_enabled", f.apply("failed"))
	extra := f.extra()
	require.Equal(t, true, extra["openai_excel_bps"])
	require.Equal(t, []any{"gpt-6-astra"}, extra["openai_excel_bps_models"])
	require.Equal(t, true, extra[service.ExcelBPSOmitUnsupportedToolsKey])
	require.Equal(t, false, extra["openai_excel_bps_cache_creation_as_input"])
	require.Equal(t, true, extra[service.ExcelBPSIgnoreEncryptedContentKey])
	require.NotContains(t, extra, service.ExcelBPS403TargetGroupIDKey)
	require.Equal(t, "kept", extra["unrelated"])
	require.Equal(t, 1, f.events())

	// 关闭：连续满血够次数才关；降智清零、无法判断不打断；满血次数不看用量，用量高只挡住最后的关闭。
	require.Equal(t, "already_quarantined", f.apply("failed"))
	require.Equal(t, "inconclusive", f.apply("inconclusive"))
	f.exec(`UPDATE accounts SET extra=extra||'{"codex_7d_used_percent":95}' WHERE id=$1`)
	require.Equal(t, "restore_counted:1/2", f.apply("passed"))
	require.Equal(t, 1, f.count(passStreak))
	require.Equal(t, "already_quarantined", f.apply("failed"))
	require.Zero(t, f.count(passStreak), "a degraded round restarts the healthy count")
	require.Equal(t, "restore_counted:1/2", f.apply("passed"))
	require.Equal(t, "inconclusive", f.apply("inconclusive"))
	require.Equal(t, "bps_kept_usage", f.apply("passed"), "usage still over the trigger keeps BPS on")
	require.Equal(t, "bps_kept_usage", f.apply("passed"))
	require.Equal(t, 2, f.count(passStreak), "the healthy count stops at the threshold")
	require.Equal(t, true, f.extra()["openai_excel_bps"])
	require.Zero(t, f.events(), "counting rounds leave the account untouched")
	f.exec(`UPDATE accounts SET extra=extra||'{"codex_7d_used_percent":10}', name='edited elsewhere' WHERE id=$1`)
	require.Equal(t, "restored", f.apply("passed"))
	extra = f.extra()
	for _, key := range service.QualityBPSManagedKeys {
		if key != "openai_excel_bps_cache_creation_as_input" {
			require.NotContains(t, extra, key, "keys absent before BPS are removed on restore")
		}
	}
	require.Equal(t, true, extra["openai_excel_bps_cache_creation_as_input"], "previous values come back")
	require.Equal(t, "kept", extra["unrelated"])
	require.Zero(t, f.count(states))
	require.Equal(t, 1, f.events())

	// 用量触发：探针满血也会开，并按用量命中单独标记。
	f.exec(`UPDATE accounts SET extra=extra||'{"codex_7d_used_percent":85}' WHERE id=$1`)
	require.Equal(t, "bps_enabled_usage", f.apply("passed"))
	require.Equal(t, 1, f.events())
	// 管理员改过 BPS 选项后不自动覆盖。
	f.exec(`UPDATE accounts SET extra=extra||'{"codex_7d_used_percent":5,"openai_excel_bps_models":["gpt-5.6-sol"]}' WHERE id=$1`)
	require.Equal(t, "restore_counted:1/2", f.apply("passed"))
	require.Equal(t, "restore_conflict", f.apply("passed"))
	require.Equal(t, []any{"gpt-5.6-sol"}, f.extra()["openai_excel_bps_models"])

	// 403 自动关闭后，规则不再自动重新打开，直到管理员手动开启。
	f.exec(`UPDATE accounts SET extra=extra||'{"openai_excel_bps":false,"openai_excel_bps_403_disabled_at":"2026-09-28T00:00:00Z"}' WHERE id=$1`)
	require.Equal(t, "bps_blocked_403", f.apply("failed"))
	f.exec(`DELETE FROM account_quality_states WHERE plan_id=(SELECT id FROM scheduled_test_plans WHERE account_id=$1)`)
	require.Equal(t, "failure_counted:1/2", f.apply("failed"))
	require.Equal(t, "bps_blocked_403", f.apply("failed"))
	require.Equal(t, false, f.extra()["openai_excel_bps"])

	// 管理员自己开着 BPS：规则不接管也不覆盖。
	f.exec(`UPDATE accounts SET extra=(extra||'{"openai_excel_bps":true}') - 'openai_excel_bps_403_disabled_at' WHERE id=$1`)
	require.Equal(t, "bps_already_enabled", f.apply("failed"))
	require.Equal(t, []any{"gpt-5.6-sol"}, f.extra()["openai_excel_bps_models"])

	f.exec(`UPDATE accounts SET credentials='{"plan_type":"free"}', extra=extra - 'openai_excel_bps' WHERE id=$1`)
	require.Equal(t, "bps_unsupported", f.apply("failed"))
	require.NotContains(t, f.extra(), "openai_excel_bps")

	f.exec(`UPDATE accounts SET type='apikey', extra=extra - 'openai_excel_bps' WHERE id=$1`)
	require.Equal(t, "bps_unsupported", f.apply("failed"))
	require.NotContains(t, f.extra(), "openai_excel_bps")
	require.Zero(t, f.events(), "conflicts and blocked rounds leave the account untouched")
}

func TestQualityBPSObservationReleasesOwnershipWithoutTogglingBPS(t *testing.T) {
	f := newQualityBPSFixture(t, `{"unrelated":"kept"}`, &service.QualityPolicy{
		Action: service.QualityActionEnableBPS, AutoRestore: true,
		BPS: &service.QualityBPSPolicy{FailureThreshold: 1, AllModels: true},
	})
	require.Equal(t, "bps_enabled", f.apply("failed"))
	before := f.extra()
	require.Equal(t, true, before["openai_excel_bps"])
	_ = f.events()
	oldPlan := *f.plan
	f.plan.PelicanConfig = &service.PelicanTestConfig{QuestionKind: "candy", TestChannel: "bps", Prompt: "Return 21", ReasoningEffort: "high", ParallelCount: 1,
		Quality: &service.QualityPolicy{Action: service.QualityActionObserveOnly, ExpectedAnswer: "21", AutoRestore: true}}
	svc := service.NewScheduledTestService(f.plans, NewScheduledTestResultRepository(integrationDB))
	_, err := svc.CreatePlan(context.Background(), &service.ScheduledTestPlan{AccountID: f.account, ModelID: "gpt-6-astra", CronExpression: "*/30 * * * *", Enabled: false,
		PelicanConfig: &service.PelicanTestConfig{QuestionKind: "candy", Prompt: "Return 21", ReasoningEffort: "high", ParallelCount: 1,
			Quality: &service.QualityPolicy{Action: "disable_scheduling", ExpectedAnswer: "21"}}})
	require.NoError(t, err)
	updated, err := svc.UpdatePlan(context.Background(), f.plan)
	require.NoError(t, err)
	require.False(t, updated.PelicanConfig.Quality.AutoRestore)
	replacement := oldPlan
	replacement.ID, replacement.Enabled = 0, false
	_, err = svc.CreatePlan(context.Background(), &replacement)
	require.NoError(t, err, "BPS control remains independent of observation and quarantine")
	require.Equal(t, 3, f.count("SELECT count(*) FROM scheduled_test_plans WHERE account_id=$1"))
	require.Equal(t, before, f.extra(), "conversion preserves the current BPS settings")
	require.Zero(t, f.count(`SELECT count(*) FROM account_quality_states s JOIN scheduled_test_plans p ON p.id=s.plan_id WHERE p.account_id=$1`))
	result, err := f.plans.ApplyQualityOutcome(context.Background(), &oldPlan, f.until, "passed")
	require.NoError(t, err)
	require.Equal(t, "stale_run", result, "a previous native probe cannot restore after conversion")
	require.NoError(t, f.plans.FinishPelican(context.Background(), f.plan.ID, f.until, time.Now()))
	f.plan = updated
	f.claim()
	for _, outcome := range []string{"passed", "failed", "inconclusive"} {
		require.Equal(t, "observed", f.apply(outcome))
		require.Equal(t, before, f.extra())
	}
	require.Zero(t, f.events())
	// A separate policy may turn BPS off without observation turning it back on.
	f.exec(`UPDATE accounts SET extra=extra||'{"openai_excel_bps":false}' WHERE id=$1`)
	require.Equal(t, "observed", f.apply("failed"))
	require.Equal(t, false, f.extra()["openai_excel_bps"])
}

func TestQualityEnableBPSStateSurvivesActionChange(t *testing.T) {
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{
		Action: service.QualityActionEnableBPS, AutoRestore: true,
		BPS: &service.QualityBPSPolicy{FailureThreshold: 3, AllModels: true},
	})
	require.False(t, f.plan.PelicanConfig.BPSRecoveryPending)
	require.Equal(t, "failure_counted:1/3", f.apply("failed"))

	// 规则改成停调度后丢掉残留的连续次数：改回来要重新计数，也不挡住停调度写状态。
	f.exec(`UPDATE scheduled_test_plans SET pelican_config=jsonb_set(pelican_config,'{quality,action}','"disable_scheduling"'), running_until=NULL, updated_at=NOW() WHERE account_id=$1`)
	f.claim()
	require.Equal(t, "passed", f.apply("passed"))
	require.Zero(t, f.count(`SELECT count(*) FROM account_quality_states s JOIN scheduled_test_plans p ON p.id=s.plan_id WHERE p.account_id=$1`))
	require.Equal(t, "scheduling_disabled", f.apply("failed"))
	require.Equal(t, "restored", f.apply("passed"))

	// 已由本规则开启的 BPS，即使规则改了动作也仍按快照恢复。
	f.exec(`UPDATE scheduled_test_plans SET pelican_config=jsonb_set(pelican_config,'{quality,action}','"enable_bps"'), running_until=NULL, updated_at=NOW() WHERE account_id=$1`)
	f.claim()
	require.Equal(t, "failure_counted:1/3", f.apply("failed"))
	require.Equal(t, "failure_counted:2/3", f.apply("failed"))
	require.Equal(t, "bps_enabled", f.apply("failed"))
	require.NotContains(t, f.extra(), "openai_excel_bps_models", "all models = no scope key")
	f.exec(`UPDATE scheduled_test_plans SET pelican_config=jsonb_set(pelican_config,'{quality,action}','"disable_scheduling"') #- '{quality,bps}', running_until=NULL, updated_at=NOW() WHERE account_id=$1`)
	f.claim()
	require.True(t, f.plan.PelicanConfig.BPSRecoveryPending, "claim retains BPS ownership after the configured action changes")
	require.Equal(t, "already_quarantined", f.apply("failed"))
	require.Equal(t, "restored", f.apply("passed"))
	require.NotContains(t, f.extra(), "openai_excel_bps")
	require.NoError(t, f.plans.FinishPelican(context.Background(), f.plan.ID, f.until, time.Now()))
	f.claim()
	require.False(t, f.plan.PelicanConfig.BPSRecoveryPending, "restored ownership must not leak into future runs")
}

func TestQualityEnableBPSRestoreWithoutUsageHold(t *testing.T) {
	f := newQualityBPSFixture(t, `{"codex_5h_used_percent":90}`, &service.QualityPolicy{
		Action: service.QualityActionEnableBPS, AutoRestore: true,
		BPS: &service.QualityBPSPolicy{FailureThreshold: 1, UsagePercent: 80, AllModels: true},
	})
	passStreak := `SELECT COALESCE((s.state->>'pass_streak')::int,0) FROM account_quality_states s JOIN scheduled_test_plans p ON p.id=s.plan_id WHERE p.account_id=$1`
	require.Equal(t, "bps_enabled", f.apply("failed"))

	// 未开自动关闭时不计满血次数。
	f.exec(`UPDATE scheduled_test_plans SET pelican_config=jsonb_set(pelican_config,'{quality,auto_restore}','false'), running_until=NULL, updated_at=NOW() WHERE account_id=$1`)
	f.claim()
	require.Equal(t, "passed", f.apply("passed"))
	require.Zero(t, f.count(passStreak))

	// 没勾「用量高时不关」：旧规则（未填次数）满血一次就关，用量仍高也关；下一轮再按用量重新开启。
	f.exec(`UPDATE scheduled_test_plans SET pelican_config=jsonb_set(pelican_config,'{quality,auto_restore}','true'), running_until=NULL, updated_at=NOW() WHERE account_id=$1`)
	f.claim()
	require.Equal(t, "restored", f.apply("passed"))
	require.NotContains(t, f.extra(), "openai_excel_bps")
	require.Equal(t, "bps_enabled_usage", f.apply("passed"))
}

func TestQualityEnableBPSRestoreAfterAccountEditorSave(t *testing.T) {
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{
		Action: service.QualityActionEnableBPS, AutoRestore: true,
		BPS: &service.QualityBPSPolicy{FailureThreshold: 1, PassThreshold: 1, AllModels: true, OmitUnsupportedTools: true, IgnoreEncryptedContent: true},
	})
	require.Equal(t, "bps_enabled", f.apply("failed"))
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	account, err := repo.GetByID(context.Background(), f.account)
	require.NoError(t, err)
	// EditAccountModal emits absent keys for unchecked options and the default
	// proxy source even when the operator only changes the account's name.
	account.Name = "renamed without changing BPS settings"
	for key, value := range account.Extra {
		if value == false {
			delete(account.Extra, key)
		}
	}
	delete(account.Extra, service.ExcelBPSProxySourceKey)
	require.True(t, account.IsExcelBPSEnabledForModel("gpt-6-astra"))
	require.NoError(t, repo.Update(context.Background(), account))
	require.Equal(t, "restored", f.apply("passed"))
}

func TestQualityEnableBPSRequiresExcelAuthorization(t *testing.T) {
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionEnableBPS, BPS: &service.QualityBPSPolicy{FailureThreshold: 1, AllModels: true}})
	f.exec(`DELETE FROM openai_excel_oauth_credentials WHERE account_id=$1`)
	require.Equal(t, "bps_authorizing", f.apply("failed"))
	require.Equal(t, true, f.extra()["openai_excel_bps"])
	require.Equal(t, true, f.extra()[service.ExcelBPSAuthorizationPendingKey])
	f.exec(`INSERT INTO openai_excel_oauth_credentials(account_id,credentials_ciphertext) VALUES($1,'test-encrypted-grant')`)
	// The worker callback, tested separately, clears preparation automatically.
}

func TestQualityBPSSearchRestorePreservesManualChanges(t *testing.T) {
	for _, key := range []string{service.ExcelBPSWebSearchKey, service.ExcelBPSWebSearchAutoKey} {
		t.Run(key, func(t *testing.T) {
			f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionEnableBPS, AutoRestore: true, BPS: &service.QualityBPSPolicy{FailureThreshold: 1, PassThreshold: 1, AllModels: true, WebSearch: true, WebSearchAuto: true}})
			require.Equal(t, "bps_enabled", f.apply("failed"))
			require.Equal(t, true, f.extra()[key])
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			account, err := repo.GetByID(context.Background(), f.account)
			require.NoError(t, err)
			delete(account.Extra, key)
			require.NoError(t, repo.Update(context.Background(), account))
			require.Equal(t, "restore_conflict", f.apply("passed"), "manually disabling an enabled search option must retain the ownership conflict")
		})
	}
}
