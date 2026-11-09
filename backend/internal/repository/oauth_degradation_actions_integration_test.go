//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// No monitor is enabled: the actual startup worker must find every root OAuth
// account and consume the same PostgreSQL quality evidence as the performance UI.
func TestOAuthDegradationStartupPostgres(t *testing.T) {
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	cfg := service.DefaultOAuthAutoConfig()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	var policy map[string]any
	require.NoError(t, json.Unmarshal(raw, &policy))
	policy["revision"] = "degradation-" + suffix
	policy["degradation_action"] = map[string]any{"enabled": true, "threshold_percent": 50, "target_concurrency": 10, "models": []string{"gpt-6-luna"}}
	raw, err = json.Marshal(policy)
	require.NoError(t, err)
	var prior string
	priorErr := integrationDB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", service.SettingKeyOAuthAutoConfig).Scan(&prior)
	require.True(t, priorErr == nil || priorErr == sql.ErrNoRows)
	t.Cleanup(func() {
		if priorErr == nil {
			_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, prior)
		} else {
			_, err = integrationDB.ExecContext(ctx, "DELETE FROM settings WHERE key=$1", service.SettingKeyOAuthAutoConfig)
		}
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM account_auto_config_events WHERE kind='config_saved' AND details->'config'->>'revision' LIKE $1", "degradation-"+suffix+"%")
		require.NoError(t, err)
	})
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	var ids []int64
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		a := mustCreateAccount(t, integrationEntClient, &service.Account{Name: "degradation-" + suffix + kind, Platform: service.PlatformOpenAI, Type: kind, Concurrency: 20, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-luna": "gpt-6-luna"}, "keep": "credential"}, Extra: map[string]any{"keep": "extra"}})
		ids = append(ids, a.ID)
		t.Cleanup(func() {
			_, cleanupErr := integrationDB.ExecContext(ctx, "DELETE FROM account_auto_config_events WHERE account_id=$1", a.ID)
			require.NoError(t, cleanupErr)
			_, cleanupErr = integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id=$1", a.ID)
			require.NoError(t, cleanupErr)
			_, cleanupErr = integrationDB.ExecContext(ctx, "DELETE FROM scheduled_test_plans WHERE account_id=$1", a.ID)
			require.NoError(t, cleanupErr)
			require.NoError(t, integrationEntClient.Account.DeleteOneID(a.ID).Exec(ctx))
		})
		var plan int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at) VALUES($1,'gpt-6-luna','*/30 * * * *',true,'{"quality":{},"parallel_count":1}',NOW()-INTERVAL '2 hours') RETURNING id`, a.ID).Scan(&plan))
		for i, verdict := range []string{"correct", "incorrect"} {
			status, message := "success", ""
			if verdict == "incorrect" {
				status, message = "failed", "state_degraded"
			}
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message) VALUES($1,$2,NOW()-INTERVAL '1 minute',NOW()-INTERVAL '50 seconds',NOW()-INTERVAL '50 seconds','{"quality":{},"parallel_count":1}',$3,'evaluated',jsonb_build_object('verdict',$4::text),$5)`, plan, status, fmt.Sprintf("degradation-%s-%d-%d", suffix, a.ID, i), verdict, message)
			require.NoError(t, err)
		}
	}
	settings := NewSettingRepository(integrationEntClient)
	start := func() *service.AccountOpsService {
		s := service.NewAccountOpsService(settings, NewAccountOpsRepository(integrationDB), nil)
		s.SetNotificationDependencies(repo, nil, false, "UTC")
		s.Start()
		return s
	}
	// Two real workers simulate independent replicas; both share only PostgreSQL.
	one, two := start(), start()
	defer one.Stop()
	defer two.Stop()
	require.Eventually(t, func() bool {
		a, e := repo.GetByID(ctx, ids[0])
		return e == nil && a.Concurrency == 10
	}, 8*time.Second, 50*time.Millisecond, "OAuth degradation startup must apply threshold equality without a separate monitor")
	for _, s := range []*service.AccountOpsService{one, two} {
		s.Stop()
	}
	a, err := repo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.Equal(t, "credential", a.Credentials["keep"])
	require.Equal(t, "extra", a.Extra["keep"])
	require.True(t, a.IsModelSupported("gpt-6-luna"))
	require.False(t, a.IsModelSupported("gpt-6.1-sol"))
	nonOAuth, err := repo.GetByID(ctx, ids[1])
	require.NoError(t, err)
	require.Equal(t, 20, nonOAuth.Concurrency)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", ids[0]).Scan(&count))
	require.Equal(t, 1, count, "multiple nodes must write one history record")
	restart := start()
	restart.Stop()
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", ids[0]).Scan(&count))
	require.Equal(t, 1, count, "restart must not punish twice")
}
