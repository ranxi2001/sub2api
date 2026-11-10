//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func degradationPolicyFixture(t *testing.T) (*accountRepository, service.OAuthAutoConfig) {
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
	c := service.DefaultOAuthAutoConfig()
	threshold := 50.0
	c.Revision = fmt.Sprintf("degradation-fixture-%d", time.Now().UnixNano())
	c.DegradationAction.Enabled = true
	c.DegradationAction.ThresholdPercent = &threshold
	c.DegradationAction.Models = []string{"gpt-6-luna"}
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	return newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil), c
}
func degradationAccountFixture(t *testing.T, concurrency int, mapping map[string]any, rounds []string, age time.Duration) *service.Account {
	t.Helper()
	ctx := context.Background()
	a := mustCreateAccount(t, integrationEntClient, &service.Account{Name: fmt.Sprintf("degradation-fixture-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: concurrency, Credentials: map[string]any{"model_mapping": mapping, "keep": "credential"}, Extra: map[string]any{"manual": "retained", "model_rate_limits": map[string]any{"other": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}}})
	t.Cleanup(func() {
		for _, query := range []string{"DELETE FROM scheduler_outbox WHERE account_id=$1", "DELETE FROM account_auto_config_events WHERE account_id=$1", "DELETE FROM scheduled_test_plans WHERE account_id=$1", "DELETE FROM accounts WHERE id=$1"} {
			_, err := integrationDB.ExecContext(ctx, query, a.ID)
			require.NoError(t, err)
		}
	})
	if len(rounds) == 0 {
		return a
	}
	var plan int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at) VALUES($1,'gpt-6-luna','*/30 * * * *',true,'{"quality":{}}',NOW()-INTERVAL '2 hours') RETURNING id`, a.ID).Scan(&plan))
	at := time.Now().Add(-age)
	for i, verdict := range rounds {
		status, message := "success", ""
		if verdict == "incorrect" {
			status, message = "failed", "state_degraded"
		}
		if verdict == "unknown" {
			status, message = "failed", "timeout"
		}
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message) VALUES($1,$2,$3,$4,$4,'{"parallel_count":1}',$5,'evaluated',jsonb_build_object('verdict',$6::text),$7)`, plan, status, at, at.Add(time.Second), fmt.Sprintf("fixture-%d-%d", a.ID, i), verdict, message)
		require.NoError(t, err)
	}
	return a
}
func TestOAuthDegradationPostgresMatrix(t *testing.T) {
	repo, c := degradationPolicyFixture(t)
	ctx := t.Context()
	for _, tc := range []struct {
		name    string
		rounds  []string
		current int
		mapping map[string]any
		applied bool
		allowed bool
	}{
		{"below", []string{"correct", "correct", "incorrect"}, 20, nil, false, true},
		{"equal", []string{"correct", "incorrect"}, 20, nil, true, true},
		{"above", []string{"incorrect"}, 20, nil, true, true},
		{"zero samples", nil, 20, nil, false, true},
		{"network failure", []string{"unknown"}, 20, nil, false, true},
		{"low concurrency", []string{"incorrect"}, 3, nil, true, true},
		{"strict zero min", []string{"incorrect"}, 0, nil, true, true},
		{"strict negative min", []string{"incorrect"}, -1, nil, true, true},
		{"empty intersection", []string{"incorrect"}, 20, map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := degradationAccountFixture(t, tc.current, tc.mapping, tc.rounds, time.Minute)
			if tc.current == 0 {
				// mustCreateAccount supplies 3 for zero; restore the actual legacy
				// value to verify strict numeric min without changing its meaning.
				_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET concurrency=0 WHERE id=$1", a.ID)
				require.NoError(t, err)
			}
			require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
			got, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, tc.applied, got.HasOAuthDegradationAction())
			expected := tc.current
			if tc.applied {
				expected = min(expected, c.DegradationAction.TargetConcurrency)
			}
			require.Equal(t, expected, got.Concurrency)
			require.Equal(t, "credential", got.Credentials["keep"])
			require.Equal(t, "retained", got.Extra["manual"])
			require.Contains(t, got.Extra, "model_rate_limits", "unrelated isolation must remain")
			if tc.applied {
				require.Equal(t, tc.allowed, got.IsModelSupported("gpt-6-luna"))
				if tc.allowed {
					require.False(t, got.IsModelSupported("gpt-6.1-sol"))
				} else {
					require.False(t, got.Schedulable, "empty native model intersection visibly pauses scheduling")
					require.Equal(t, tc.mapping["gpt-6.1-sol"], got.GetMappedModel("gpt-6.1-sol"), "pause retains the editable original mapping")
				}
			}
		})
	}
	// Live window settings must immediately change the identical production query.
	a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, 16*time.Minute)
	var previousWindow string
	oldErr := integrationDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", service.SettingKeyAccountPerformanceWindowMinutes).Scan(&previousWindow)
	require.True(t, oldErr == nil || oldErr == sql.ErrNoRows)
	t.Cleanup(func() {
		if oldErr == nil {
			_, err := integrationDB.ExecContext(context.Background(), "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyAccountPerformanceWindowMinutes, previousWindow)
			require.NoError(t, err)
		} else {
			_, err := integrationDB.ExecContext(context.Background(), "DELETE FROM settings WHERE key=$1", service.SettingKeyAccountPerformanceWindowMinutes)
			require.NoError(t, err)
		}
	})
	_, err := integrationDB.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES($1,'15') ON CONFLICT(key) DO UPDATE SET value='15'", service.SettingKeyAccountPerformanceWindowMinutes)
	require.NoError(t, err)
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.HasOAuthDegradationAction())
	_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value='60' WHERE key=$1", service.SettingKeyAccountPerformanceWindowMinutes)
	require.NoError(t, err)
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.HasOAuthDegradationAction(), "live 60-minute window includes the unchanged 16-minute-old probe")
}

func TestOAuthDegradationSettingsCASAndDisableFence(t *testing.T) {
	repo, c := degradationPolicyFixture(t)
	ctx := t.Context()
	settings := NewSettingRepository(integrationEntClient).(*settingRepository)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			next := c
			next.Revision = c.Revision + fmt.Sprint(index)
			raw, err := json.Marshal(next)
			if err == nil {
				err = settings.CompareAndSetOAuthAutoConfig(ctx, string(raw), c.Revision)
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			require.Contains(t, err.Error(), "reload before saving")
			conflict++
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), "DELETE FROM account_auto_config_events WHERE kind='config_saved' AND details->'config'->>'revision' LIKE $1", c.Revision+"%")
		require.NoError(t, err)
	})
	a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var locked string
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1 FOR UPDATE", service.SettingKeyOAuthAutoConfig).Scan(&locked))
	result := make(chan error, 1)
	go func() { result <- repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()) }()
	select {
	case err := <-result:
		t.Fatalf("evaluation passed a locked configuration: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	c.DegradationAction.Enabled = false
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-result)
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.HasOAuthDegradationAction(), "disabled live revision must fence a waiting evaluation")
}

func TestOAuthDegradationRedisMetadataScope(t *testing.T) {
	cache := NewSchedulerCache(testRedis(t))
	ctx := t.Context()
	a := service.Account{ID: 17, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 10, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-luna": "gpt-6-luna"}, "model_mapping_mode": "whitelist"}, Extra: map[string]any{service.OAuthDegradationActionExtraKey: service.OAuthDegradationState{ConcurrencyCap: 10, Models: []string{"gpt-6-luna"}}}}
	bucket := service.SchedulerBucket{Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{a}))
	accounts, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, accounts, 1)
	require.True(t, accounts[0].HasOAuthDegradationAction())
	require.True(t, accounts[0].IsModelSupported("gpt-6-luna"))
	require.False(t, accounts[0].IsModelSupported("gpt-6.1-sol"))
}

func TestOAuthDegradationReevaluationRestartAndUpgradeHold(t *testing.T) {
	repo, c := degradationPolicyFixture(t)
	ctx := t.Context()
	group := mustCreateGroup(t, integrationEntClient, &service.Group{Name: fmt.Sprintf("degradation-upgrade-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI, Status: service.StatusActive})
	t.Cleanup(func() {
		require.NoError(t, integrationEntClient.Group.DeleteOneID(group.ID).Exec(context.Background()))
	})
	a := degradationAccountFixture(t, 20, nil, []string{"incorrect"}, time.Minute)
	require.NoError(t, repo.BindGroups(ctx, a.ID, []int64{group.ID}))
	c.UpgradeEnabled = true
	c.UpgradeGroupIDs = []int64{group.ID}
	c.SuccessesPerStep = 1
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	appliedAt := got.UpdatedAt
	require.Equal(t, 10, got.Concurrency)
	var state service.AutoConfigConcurrencyState
	stateRaw, err := json.Marshal(got.Extra[service.AutoConfigConcurrencyExtraKey])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(stateRaw, &state))
	require.True(t, state.DegradationHold)
	// A new repository has no process-local memory from the original evaluator.
	restarted := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	for range 3 {
		require.NoError(t, restarted.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	}
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, appliedAt, got.UpdatedAt, "re-evaluation is read-only after first action")
	got.Concurrency = 3
	require.NoError(t, repo.Update(ctx, got))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 3, got.Concurrency, "native manual concurrency edit is persisted")
	stateRaw, err = json.Marshal(got.Extra[service.AutoConfigConcurrencyExtraKey])
	require.NoError(t, err)
	// False is omitted by the JSON schema. Decode into a fresh value so the
	// earlier hold=true fixture does not survive an absent JSON field.
	var manualState service.AutoConfigConcurrencyState
	require.NoError(t, json.Unmarshal(stateRaw, &manualState))
	require.NotContains(t, string(stateRaw), "degradation_hold")
	require.False(t, manualState.DegradationHold, "native manual edit releases automatic upgrade hold")
	c.CooldownSeconds = 0
	raw, err = json.Marshal(c)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, restarted.RecordConcurrencyResult(ctx, service.AccountConcurrencyResult{AccountID: a.ID, StartedAt: time.Now(), Success: true}, c))
	}
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 6, got.Concurrency, "native manual take-over permits configured success upgrades")
	var events int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", a.ID).Scan(&events))
	require.Equal(t, 1, events, "audit marker prevents a repeat treatment after manual restoration")
}
