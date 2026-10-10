//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type onceObserver interface {
	ObserveThreshold(context.Context, service.AccountOpsEvent, string, time.Time, bool, bool) error
}

func TestAccountOpsOnceThresholdCriteriaTransitionsNotify(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, initialObservation, nextObservation, wantPhase string
	}{
		{name: "compliant to adverse", initialObservation: "resolved", nextObservation: "active", wantPhase: "alert"},
		{name: "adverse to compliant", initialObservation: "active", nextObservation: "resolved", wantPhase: "recovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var id int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES($1,'openai','apikey','active',true) RETURNING id`, "criteria-transition-"+tc.name).Scan(&id))
			defer cleanupOnce(t, id)
			repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
			now := time.Now().UTC().Truncate(time.Microsecond)
			e := service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", Identity: "same", Criteria: "old-rule"}
			require.NoError(t, repo.ObserveThreshold(ctx, e, tc.initialObservation, now, true, true))
			e.Criteria = "new-rule"
			require.NoError(t, repo.ObserveThreshold(ctx, e, tc.nextObservation, now.Add(time.Second), true, true))
			claimed, err := repo.Claim(ctx)
			require.NoError(t, err)
			require.NotNil(t, claimed)
			require.Equal(t, tc.wantPhase, claimed.Phase)
			require.Equal(t, "new-rule", claimed.Criteria)
			require.NoError(t, repo.Complete(ctx, claimed, "sent", time.Hour))
			require.NoError(t, repo.ObserveThreshold(ctx, e, tc.nextObservation, now.Add(2*time.Second), true, true))
			claimed, err = repo.Claim(ctx)
			require.NoError(t, err)
			require.Nil(t, claimed)
		})
	}
}

func TestAccountOpsOnceEpisodeConcurrentRestartRecovery(t *testing.T) {
	ctx := context.Background()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('once-fixture','openai','apikey','active',true) RETURNING id`).Scan(&id))
	defer func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_ops_threshold_events WHERE account_id=$1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_ops_threshold_monitors WHERE account_id=$1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, id)
	}()
	repo := NewAccountOpsRepository(integrationDB)
	observe, ok := repo.(onceObserver)
	require.True(t, ok, "durable episode observer required")
	e := service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", AccountName: "once-fixture", Identity: "same"}
	now := time.Now().UTC()
	require.NoError(t, observe.ObserveThreshold(ctx, e, "resolved", now.Add(-time.Second), true, true))
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- observe.ObserveThreshold(ctx, e, "active", now, true, true) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	count := func(want int) {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_ops_threshold_events WHERE account_id=$1`, id).Scan(&n))
		require.Equal(t, want, n)
	}
	count(1)
	restarted := NewAccountOpsRepository(integrationDB).(onceObserver)
	require.NoError(t, restarted.ObserveThreshold(ctx, e, "active", now.Add(2*time.Hour), true, true))
	count(1)
	require.NoError(t, restarted.ObserveThreshold(ctx, e, "resolved", now.Add(2*time.Hour+time.Second), true, true))
	count(1)
	require.NoError(t, restarted.ObserveThreshold(ctx, e, "resolved", now.Add(2*time.Hour+10*time.Second), true, true))
	count(1)
	require.NoError(t, restarted.ObserveThreshold(ctx, e, "unknown", now.Add(2*time.Hour+16*time.Second), true, true))
	count(1)
	require.NoError(t, restarted.ObserveThreshold(ctx, e, "resolved", now.Add(2*time.Hour+17*time.Second), true, true))
	count(1)
	require.NoError(t, restarted.ObserveThreshold(ctx, e, "resolved", now.Add(2*time.Hour+32*time.Second), true, true))
	count(2)
	require.NoError(t, restarted.ObserveThreshold(ctx, e, "active", now.Add(2*time.Hour+33*time.Second), true, true))
	count(3)
}

func TestAccountOpsOnceIndependentFlagsHistoryAndReceipts(t *testing.T) {
	ctx := context.Background()
	for _, flags := range [][2]bool{{true, true}, {false, true}, {true, false}, {false, false}} {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			var id int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('flags-fixture','openai','apikey','active',true) RETURNING id`).Scan(&id))
			defer cleanupOnce(t, id)
			repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
			now := time.Now().UTC().Truncate(time.Microsecond)
			amount, threshold := 4.0, 5.0
			e := service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", Identity: "same", Criteria: "criteria", Details: &service.AccountOpsDetails{Balance: &amount, Threshold: &threshold, Unit: "USD"}}
			require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now, flags[0], flags[1]))
			claimed, err := repo.Claim(ctx)
			require.NoError(t, err)
			if flags[0] {
				require.NotNil(t, claimed)
				require.Equal(t, "alert", claimed.Phase)
				owned, err := repo.SaveDelivery(ctx, claimed, "email:fake", service.AccountOpsDelivery{Provider: "email", Status: "sent", Attempts: 1})
				require.NoError(t, err)
				require.True(t, owned)
				require.NoError(t, repo.Complete(ctx, claimed, "failed", 0))
				retry, err := repo.Claim(ctx)
				require.NoError(t, err)
				require.NotNil(t, retry)
				require.Equal(t, "sent", retry.Deliveries["email:fake"].Status)
				owned, err = repo.SaveDelivery(ctx, claimed, "email:fake", service.AccountOpsDelivery{Status: "failed"})
				require.NoError(t, err)
				require.False(t, owned)
				require.NoError(t, repo.Complete(ctx, retry, "sent", time.Hour))
			} else {
				require.Nil(t, claimed)
			}
			amount = 8
			require.NoError(t, repo.ObserveThreshold(ctx, e, "resolved", now.Add(time.Second), flags[0], flags[1]))
			require.NoError(t, repo.ObserveThreshold(ctx, e, "resolved", now.Add(16*time.Second), flags[0], flags[1]))
			recovered, err := repo.Claim(ctx)
			require.NoError(t, err)
			if flags[1] {
				require.NotNil(t, recovered)
				require.Equal(t, "recovery", recovered.Phase)
				require.Equal(t, 8.0, *recovered.Details.Balance)
				amount = 3
				require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now.Add(17*time.Second), flags[0], flags[1]))
				valid, err := repo.ThresholdEventEligible(ctx, recovered)
				require.NoError(t, err)
				require.True(t, valid, "historical recovery remains valid after a new episode")
				require.NoError(t, repo.Complete(ctx, recovered, "sent", time.Hour))
			} else {
				require.Nil(t, recovered)
			}
			amount = 3
			require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now.Add(17*time.Second), flags[0], flags[1]))
			items, err := repo.List(ctx, 0, 100)
			require.NoError(t, err)
			events := []service.AccountOpsEvent{}
			for _, item := range items {
				if item.AccountID == id {
					events = append(events, item)
				}
			}
			require.Len(t, events, 3)
			require.Equal(t, 3.0, *events[0].Details.Balance)
			require.Equal(t, 8.0, *events[1].Details.Balance)
			require.Equal(t, 4.0, *events[2].Details.Balance)
			require.NotEqual(t, events[0].EpisodeID, events[2].EpisodeID)
			require.Equal(t, events[1].EpisodeID, events[2].EpisodeID)
		})
	}
}
func cleanupOnce(t *testing.T, id int64) {
	t.Helper()
	for _, table := range []string{"account_ops_threshold_events", "account_ops_threshold_monitors", "account_ops_alerts"} {
		_, err := integrationDB.Exec(`DELETE FROM `+table+` WHERE account_id=$1`, id)
		require.NoError(t, err)
	}
	_, err := integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, id)
	require.NoError(t, err)
}

// applyLegacyThresholdEpisodeTransfer applies the data-transfer part of the
// pre-account-group migration to the current grouped schema. Production runs
// migration 267 before 272, so the original SQL is valid there. These tests
// run against the already-migrated integration database; keeping this adapter
// here exercises the same transfer semantics without inserting blank group
// keys into grouped primary keys.
func applyLegacyThresholdEpisodeTransfer(t *testing.T, ids []int64) {
	t.Helper()
	ctx := context.Background()
	groupID := `('account:' || account_id::text)`
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO account_ops_threshold_monitors(account_id,group_id,kind,identity,episode_id,adverse)
		SELECT account_id,`+groupID+`,kind,identity,'legacy:'||account_id||':'||kind,state<>'resolved'
		FROM account_ops_alerts
		WHERE account_id=ANY($1) AND kind IN ('balance_threshold','quota_threshold')
		ON CONFLICT(group_id,kind) DO NOTHING`, pq.Array(ids))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO account_ops_threshold_events(
			id,episode_id,phase,notification_enabled,account_id,group_id,group_name,kind,account_name,signal,http_status,
			first_seen,last_seen,occurrences,state,last_sent_at,next_send_at,attempts,details,deliveries,identity,criteria)
		SELECT 'legacy-transition:'||account_id||':'||kind,'legacy:'||account_id||':'||kind,
			'alert',TRUE,account_id,`+groupID+`,account_name,kind,account_name,signal,http_status,
			first_seen,last_seen,occurrences,CASE WHEN state='sending' THEN 'pending' ELSE state END,
			last_sent_at,next_send_at,attempts,details,deliveries,identity,''
		FROM account_ops_alerts
		WHERE account_id=ANY($1) AND kind IN ('balance_threshold','quota_threshold')
			AND state IN ('pending','sending','failed')
			AND (last_sent_at IS NULL OR deliveries<>'{}'::jsonb)
		ON CONFLICT(episode_id,phase) DO NOTHING`, pq.Array(ids))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
		UPDATE account_ops_alerts
		SET state='suppressed',lease='',lease_until=NULL
		WHERE account_id=ANY($1) AND kind IN ('balance_threshold','quota_threshold')
			AND state IN ('pending','sending','failed')`, pq.Array(ids))
	require.NoError(t, err)
}

func TestAccountOpsOnceUnknownDefersAndCriterionChangeInvalidates(t *testing.T) {
	ctx := context.Background()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('invalid-fixture','openai','apikey','active',true) RETURNING id`).Scan(&id))
	defer cleanupOnce(t, id)
	repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	now := time.Now().UTC().Truncate(time.Microsecond)
	e := service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", Identity: "same", Criteria: "rule-one"}
	require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now, true, true))
	claimed, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, repo.ObserveThreshold(ctx, e, "unknown", now.Add(time.Second), true, true))
	require.NoError(t, repo.Complete(ctx, claimed, "deferred", 0))
	var state string
	var attempts int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state,attempts FROM account_ops_threshold_events WHERE id=$1`, claimed.ID).Scan(&state, &attempts))
	require.Equal(t, "pending", state)
	require.Zero(t, attempts)
	e.Criteria = "rule-two"
	require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now.Add(2*time.Second), true, true))
	valid, err := repo.ThresholdEventEligible(ctx, claimed)
	require.NoError(t, err)
	require.False(t, valid)
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_ops_threshold_events WHERE account_id=$1`, id).Scan(&n))
	require.Equal(t, 1, n, "criteria edits establish a silent baseline")
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM account_ops_threshold_events WHERE id=$1`, claimed.ID).Scan(&state))
	require.Equal(t, "suppressed", state)
}

func TestAccountOpsOnceMigrationPreservesHistoryWithoutReplay(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA once_migration;SET LOCAL search_path=once_migration;CREATE TABLE account_ops_alerts(LIKE public.account_ops_alerts INCLUDING DEFAULTS);INSERT INTO account_ops_alerts(account_id,kind,identity,state,lease,lease_until,account_name,signal,http_status) VALUES(1,'balance_threshold','identity','sent','',NULL,'fixture','balance_threshold',0),(2,'quota_threshold','identity','pending','old',NOW(),'fixture','quota_threshold',0),(3,'balance_threshold','identity','resolved','',NULL,'fixture','balance_threshold',0)`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("267_account_ops_threshold_episodes.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var state string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state FROM account_ops_alerts WHERE account_id=1`).Scan(&state))
	require.Equal(t, "sent", state)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT state FROM account_ops_alerts WHERE account_id=2`).Scan(&state))
	require.Equal(t, "suppressed", state)
	var adverse bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT adverse FROM account_ops_threshold_monitors WHERE account_id=1`).Scan(&adverse))
	require.True(t, adverse)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT adverse FROM account_ops_threshold_monitors WHERE account_id=3`).Scan(&adverse))
	require.False(t, adverse)
	var n int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_ops_threshold_events`).Scan(&n))
	require.Equal(t, 1, n, "old pending work transfers without replaying fully sent rows")
}

func TestAccountOpsOnceScopedConfigUpdatesAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	settings := NewSettingRepository(integrationEntClient)
	key := "account_ops_notifications_v1"
	previous, previousErr := settings.GetValue(ctx, key)
	defer func() {
		if previousErr == nil {
			_ = settings.Set(ctx, key, previous)
		} else {
			_ = settings.Delete(ctx, key)
		}
	}()
	enc, err := NewAESEncryptor(&config.Config{Totp: config.TotpConfig{EncryptionKey: strings.Repeat("42", 32)}})
	require.NoError(t, err)
	a := service.NewAccountOpsService(settings, NewAccountOpsRepository(integrationDB), nil)
	b := service.NewAccountOpsService(settings, NewAccountOpsRepository(integrationDB), nil)
	a.SetNotificationDependencies(nil, enc, true, "UTC")
	b.SetNotificationDependencies(nil, enc, true, "UTC")
	require.NoError(t, a.SaveConfig(ctx, service.AccountOpsConfig{CooldownMinutes: 60, Webhooks: []service.AccountOpsWebhook{{ID: "existing", Provider: "dingtalk", Enabled: true, URL: "https://oapi.dingtalk.com/robot/send?access_token=synthetic-token", Secret: "synthetic-secret"}}}))
	before, err := settings.GetValue(ctx, key)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		recipient, interval, on, off := "new@example.test", 90, true, false
		_, err := a.SaveNotificationSettings(ctx, service.AccountOpsNotificationSettings{Enabled: &off, Recipient: &recipient, BalanceLow: &on, WeeklyQuota: &on, CooldownMinutes: &interval})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := b.SaveWebhook(ctx, "second", service.AccountOpsWebhook{Provider: "feishu", Enabled: true, URL: "https://open.feishu.cn/open-apis/bot/v2/hook/synthetic-only"})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	saved, err := a.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "new@example.test", saved.Recipient)
	require.Equal(t, 90, saved.CooldownMinutes)
	require.Len(t, saved.Webhooks, 2)
	raw, err := settings.GetValue(ctx, key)
	require.NoError(t, err)
	var old, next struct {
		Bots []map[string]any `json:"encrypted_webhooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(before), &old))
	require.NoError(t, json.Unmarshal([]byte(raw), &next))
	require.Equal(t, old.Bots[0], next.Bots[0])
	public, err := json.Marshal(saved)
	require.NoError(t, err)
	require.NotContains(t, string(public), "cipher")
	require.NotContains(t, string(public), "synthetic-token")
}

func TestAccountOpsOnceOutboxRetryCeilingExpiredLeaseAndRobotCAS(t *testing.T) {
	ctx := context.Background()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('retry-fixture','openai','apikey','active',true) RETURNING id`).Scan(&id))
	defer cleanupOnce(t, id)
	repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	now := time.Now().UTC()
	e := service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", Identity: "same", Criteria: "rule"}
	require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now, true, true))
	var first *service.AccountOpsEvent
	for i := 1; i <= 3; i++ {
		claimed, err := repo.Claim(ctx)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		require.Equal(t, i, claimed.Attempts)
		if i == 1 {
			first = claimed
			owned, err := repo.ReserveRobotDelivery(ctx, claimed, "robot:synthetic:revision", "feishu", "once-synthetic-hash")
			require.NoError(t, err)
			require.True(t, owned)
		}
		if i < 3 {
			require.NoError(t, repo.Complete(ctx, claimed, "failed", 0))
		} else {
			_, err := integrationDB.ExecContext(ctx, `UPDATE account_ops_threshold_events SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, claimed.ID)
			require.NoError(t, err)
			owned, err := repo.SaveDelivery(ctx, claimed, "robot:synthetic:revision", service.AccountOpsDelivery{Status: "sent"})
			require.NoError(t, err)
			require.False(t, owned)
		}
	}
	none, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, none)
	require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now.Add(2*time.Hour), true, true))
	none, err = repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, none)
	require.NoError(t, repo.Complete(ctx, first, "sent", 0))
	valid, err := repo.DeliveryLeaseValid(ctx, first)
	require.NoError(t, err)
	require.False(t, valid)
	owned, err := repo.ReserveRobotDelivery(ctx, first, "robot:other:revision", "feishu", "once-synthetic-hash")
	require.NoError(t, err)
	require.False(t, owned)
	var state string
	var attempts int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state,attempts FROM account_ops_threshold_events WHERE id=$1`, first.ID).Scan(&state, &attempts))
	require.Equal(t, "failed", state)
	require.Equal(t, 3, attempts)
}

func TestAccountOpsOnceRemovalUsesMonitorBeforeEventLockOrder(t *testing.T) {
	ctx := context.Background()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('lock-order-fixture','openai','apikey','active',true) RETURNING id`).Scan(&id))
	defer cleanupOnce(t, id)
	repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	require.NoError(t, repo.ObserveThreshold(ctx, service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", Criteria: "rule"}, "active", time.Now(), true, true))
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var account int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT account_id FROM account_ops_threshold_monitors WHERE account_id=$1 FOR UPDATE`, id).Scan(&account))
	done := make(chan error, 1)
	go func() { done <- repo.SuppressDisabled(ctx, service.AccountOpsConfig{}) }()
	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'UPDATE account_ops_threshold_monitors%')`).Scan(&blocked))
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, blocked, "config invalidation must attempt monitor lock")
	// Simulate the observation transaction's next write while holding its monitor.
	_, writeErr := tx.ExecContext(ctx, `UPDATE account_ops_threshold_events SET signal='lock-order' WHERE account_id=$1`, id)
	if writeErr != nil {
		_ = tx.Rollback()
	} else {
		require.NoError(t, tx.Commit())
	}
	configErr := <-done
	require.NoError(t, writeErr)
	require.NoError(t, configErr)
}

func TestAccountOpsOnceConfigAndOutboxRollbackTogether(t *testing.T) {
	ctx := context.Background()
	settings := NewSettingRepository(integrationEntClient)
	key := "account_ops_notifications_v1"
	previous, previousErr := settings.GetValue(ctx, key)
	defer func() {
		if previousErr == nil {
			_ = settings.Set(ctx, key, previous)
		} else {
			_ = settings.Delete(ctx, key)
		}
	}()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('rollback-fixture','openai','apikey','active',true) RETURNING id`).Scan(&id))
	defer cleanupOnce(t, id)
	repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	svc := service.NewAccountOpsService(settings, repo, nil)
	require.NoError(t, svc.SaveConfig(ctx, service.AccountOpsConfig{Enabled: true, Recipient: "before@example.test", BalanceLow: true, CooldownMinutes: 60}))
	require.NoError(t, repo.ObserveThreshold(ctx, service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", Criteria: "rule"}, "active", time.Now(), true, true))
	before, err := settings.GetValue(ctx, key)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION once_fail_update() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic outbox storage failure'; END$$;CREATE TRIGGER once_fail_update BEFORE UPDATE ON account_ops_threshold_events FOR EACH ROW WHEN (OLD.account_id=%d) EXECUTE FUNCTION once_fail_update()`, id))
	require.NoError(t, err)
	defer func() {
		_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER once_fail_update ON account_ops_threshold_events;DROP FUNCTION once_fail_update()`)
	}()
	recipient, interval, on, off := "after@example.test", 90, true, false
	_, err = svc.SaveNotificationSettings(ctx, service.AccountOpsNotificationSettings{Enabled: &off, Recipient: &recipient, BalanceLow: &on, CooldownMinutes: &interval})
	require.Error(t, err)
	after, readErr := settings.GetValue(ctx, key)
	require.NoError(t, readErr)
	require.Equal(t, before, after, "failed outbox invalidation must roll back settings persistence")
	var state string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM account_ops_threshold_events WHERE account_id=$1`, id).Scan(&state))
	require.Equal(t, "pending", state)
}

func TestAccountOpsOnceServiceAssessorPersistsRecoverySnapshot(t *testing.T) {
	ctx := context.Background()
	settings := NewSettingRepository(integrationEntClient)
	key := "account_ops_notifications_v1"
	previous, previousErr := settings.GetValue(ctx, key)
	defer func() {
		if previousErr == nil {
			_ = settings.Set(ctx, key, previous)
		} else {
			_ = settings.Delete(ctx, key)
		}
	}()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('pipeline-fixture','openai','apikey','active',true) RETURNING id`).Scan(&id))
	defer cleanupOnce(t, id)
	repo := NewAccountOpsRepository(integrationDB)
	svc := service.NewAccountOpsService(settings, repo, nil)
	svc.SetNotificationDependencies(NewAccountRepository(integrationEntClient, integrationDB, nil), nil, false, "UTC")
	require.NoError(t, svc.SaveConfig(ctx, service.AccountOpsConfig{Enabled: true, Recipient: "fake@example.test", CooldownMinutes: 60, BalanceThresholds: []service.AccountOpsBalanceRule{{AccountID: id, Enabled: true, Threshold: 5, Unit: "USD"}}}))
	now := time.Now().UTC().Truncate(time.Microsecond)
	until := now.Add(time.Hour)
	balance := &service.UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &now, FreshUntil: &until, Data: map[string]any{"remaining": 8.0, "unit": "USD"}}
	a := &service.Account{ID: id, Name: "pipeline-fixture", Type: service.AccountTypeAPIKey, Extra: map[string]any{service.UpstreamBillingProbeExtraKey: &service.UpstreamBillingProbeSnapshot{Status: "ok", Balance: balance}}}
	require.NoError(t, svc.ObserveThresholdForTesting(ctx, a, "balance_threshold", now))
	balance.Data["remaining"] = 4.0
	require.NoError(t, svc.ObserveThresholdForTesting(ctx, a, "balance_threshold", now.Add(time.Second)))
	balance.Data["remaining"] = 8.0
	require.NoError(t, svc.ObserveThresholdForTesting(ctx, a, "balance_threshold", now.Add(2*time.Second)))
	balance.Status = "failed"
	require.NoError(t, svc.ObserveThresholdForTesting(ctx, a, "balance_threshold", now.Add(10*time.Second)))
	balance.Status = "ok"
	require.NoError(t, svc.ObserveThresholdForTesting(ctx, a, "balance_threshold", now.Add(17*time.Second)))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_ops_threshold_events WHERE account_id=$1`, id).Scan(&count))
	require.Equal(t, 1, count, "failed sample resets confirmation before a later healthy check")
	require.NoError(t, svc.ObserveThresholdForTesting(ctx, a, "balance_threshold", now.Add(32*time.Second)))
	events, err := repo.List(ctx, 0, 100)
	require.NoError(t, err)
	var recovery *service.AccountOpsEvent
	for i := range events {
		if events[i].AccountID == id && events[i].Phase == "recovery" {
			recovery = &events[i]
			break
		}
	}
	require.NotNil(t, recovery)
	require.NotNil(t, recovery.Details)
	require.Equal(t, 8.0, *recovery.Details.Balance)
	require.Equal(t, 5.0, *recovery.Details.Threshold)
	require.Equal(t, "USD", recovery.Details.Unit)
	require.True(t, recovery.LastSeen.Equal(now.Add(32*time.Second)))
}

func TestAccountOpsOnceMigrationTransfersRemainingWorkWithoutReplay(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	ids := []int64{}
	defer func() {
		for _, id := range ids {
			cleanupOnce(t, id)
		}
	}()
	var original map[int64]service.AccountOpsEvent = map[int64]service.AccountOpsEvent{}
	cases := []struct {
		name, state string
		attempts    int
		deliveries  string
	}{{"pending", "pending", 0, `{}`}, {"partial", "sending", 1, `{"email:synthetic":{"provider":"email","status":"sent","attempts":1},"robot:synthetic:revision":{"provider":"feishu","status":"failed","attempts":1}}`}, {"sent", "sent", 1, `{"email:synthetic":{"provider":"email","status":"sent","attempts":1}}`}, {"exhausted", "failed", 3, `{"robot:synthetic:revision":{"provider":"feishu","status":"failed","attempts":3}}`}, {"periodic", "pending", 0, `{}`}}
	for _, tc := range cases {
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES($1,'openai','apikey','active',true) RETURNING id`, "migrated-"+tc.name).Scan(&id))
		ids = append(ids, id)
		amount, threshold := 4.0, 5.0
		e := service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", AccountName: "migrated-" + tc.name, Identity: "same", Details: &service.AccountOpsDetails{Balance: &amount, Threshold: &threshold, Unit: "USD"}}
		require.NoError(t, repo.Record(ctx, e))
		_, err := integrationDB.ExecContext(ctx, `UPDATE account_ops_alerts SET state=$2,attempts=$3,deliveries=$4::jsonb,next_send_at=NOW()-INTERVAL '1 second',lease='old-lease',lease_until=NOW()+INTERVAL '1 minute' WHERE account_id=$1`, id, tc.state, tc.attempts, tc.deliveries)
		require.NoError(t, err)
		if tc.name == "periodic" {
			_, err = integrationDB.ExecContext(ctx, `UPDATE account_ops_alerts SET last_sent_at=NOW()-INTERVAL '2 hours' WHERE account_id=$1`, id)
			require.NoError(t, err)
		}
		old, err := scanAccountOps(integrationDB.QueryRowContext(ctx, `SELECT `+accountOpsColumns+` FROM account_ops_alerts WHERE account_id=$1`, id))
		require.NoError(t, err)
		original[id] = *old
	}
	applyLegacyThresholdEpisodeTransfer(t, ids)
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_ops_threshold_events WHERE account_id=ANY($1)`, pq.Array(ids)).Scan(&n))
	require.Equal(t, 3, n, "pending/partial/exhausted work must transfer; sent history must not replay")
	cfg := service.AccountOpsConfig{Enabled: true, Recipient: "fake@example.test", CooldownMinutes: 60}
	for _, id := range ids {
		cfg.BalanceThresholds = append(cfg.BalanceThresholds, service.AccountOpsBalanceRule{AccountID: id, Enabled: true, Threshold: 5, Unit: "USD"})
	}
	require.NoError(t, repo.SuppressDisabled(ctx, cfg))
	none, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, none, "transfers require a first fresh valid scan before being claimed")
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range ids {
		e := original[id]
		e.Criteria, _, _, _ = cfg.ThresholdCriteria(id, "balance_threshold")
		require.NoError(t, repo.ObserveThreshold(ctx, e, "unknown", now, true, true))
	}
	none, err = repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, none, "unknown data never adopts delivery eligibility")
	for _, id := range ids {
		e := original[id]
		e.Criteria, _, _, _ = cfg.ThresholdCriteria(id, "balance_threshold")
		require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now.Add(time.Second), true, true))
	}
	claims := map[int64]*service.AccountOpsEvent{}
	for i := 0; i < 2; i++ {
		claimed, err := repo.Claim(ctx)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		claims[claimed.AccountID] = claimed
		require.NoError(t, repo.Complete(ctx, claimed, "sent", time.Hour))
	}
	require.Contains(t, claims, ids[0])
	require.Contains(t, claims, ids[1])
	require.NotContains(t, claims, ids[2])
	require.NotContains(t, claims, ids[3])
	require.NotContains(t, claims, ids[4], "already delivered periodic rearm must not replay")
	require.Equal(t, 1, claims[ids[0]].Attempts)
	require.Equal(t, 2, claims[ids[1]].Attempts)
	require.Equal(t, "sent", claims[ids[1]].Deliveries["email:synthetic"].Status)
	require.Equal(t, "failed", claims[ids[1]].Deliveries["robot:synthetic:revision"].Status)
	for _, id := range ids {
		old, err := scanAccountOps(integrationDB.QueryRowContext(ctx, `SELECT `+accountOpsColumns+` FROM account_ops_alerts WHERE account_id=$1`, id))
		require.NoError(t, err)
		require.Equal(t, original[id].FirstSeen, old.FirstSeen)
		require.Equal(t, original[id].LastSeen, old.LastSeen)
		require.Equal(t, original[id].Attempts, old.Attempts)
		require.Equal(t, original[id].Deliveries, old.Deliveries)
	}
	for _, id := range ids {
		e := original[id]
		e.Criteria, _, _, _ = cfg.ThresholdCriteria(id, "balance_threshold")
		require.NoError(t, repo.ObserveThreshold(ctx, e, "active", now.Add(2*time.Hour), true, true))
	}
	none, err = repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, none, "continuous old adverse state never rearms sent or exhausted transfers")
}

type opsMigratedEmail struct{ calls int }

func (s *opsMigratedEmail) SendEmail(context.Context, string, string, string) error {
	s.calls++
	return nil
}

type opsMigratedRobotTransport func(*http.Request) (*http.Response, error)

func (f opsMigratedRobotTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAccountOpsOnceMigratedPartialReceiptRetriesOnlyRobot(t *testing.T) {
	ctx := context.Background()
	settings := NewSettingRepository(integrationEntClient)
	key := "account_ops_notifications_v1"
	previous, previousErr := settings.GetValue(ctx, key)
	defer func() {
		if previousErr == nil {
			_ = settings.Set(ctx, key, previous)
		} else {
			_ = settings.Delete(ctx, key)
		}
	}()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('migrated-partial-send','openai','apikey','active',true) RETURNING id`).Scan(&id))
	defer cleanupOnce(t, id)
	now := time.Now().UTC().Truncate(time.Microsecond)
	until := now.Add(time.Hour)
	snapshot := service.UpstreamBillingProbeSnapshot{Status: "ok", Balance: &service.UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &now, FreshUntil: &until, Data: map[string]any{"remaining": 4.0, "unit": "USD"}}}
	extra, err := json.Marshal(map[string]any{service.UpstreamBillingProbeExtraKey: snapshot})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=$2::jsonb WHERE id=$1`, id, string(extra))
	require.NoError(t, err)
	accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
	a, err := accounts.GetByID(ctx, id)
	require.NoError(t, err)
	repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	email := &opsMigratedEmail{}
	svc := service.NewAccountOpsService(settings, repo, email)
	enc, err := NewAESEncryptor(&config.Config{Totp: config.TotpConfig{EncryptionKey: strings.Repeat("42", 32)}})
	require.NoError(t, err)
	svc.SetNotificationDependencies(accounts, enc, true, "UTC")
	cfg := service.AccountOpsConfig{Enabled: true, Recipient: "partial-migration@example.test", CooldownMinutes: 60, BalanceThresholds: []service.AccountOpsBalanceRule{{AccountID: id, Enabled: true, Threshold: 5, Unit: "USD"}}, Webhooks: []service.AccountOpsWebhook{{ID: "migrated-robot", Provider: "feishu", Enabled: true, URL: "https://open.feishu.cn/open-apis/bot/v2/hook/migrated-partial-only"}}}
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	stored, err := settings.GetValue(ctx, key)
	require.NoError(t, err)
	var private struct {
		Bots []struct {
			Revision string `json:"revision"`
		} `json:"encrypted_webhooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(stored), &private))
	require.Len(t, private.Bots, 1)
	hash := sha256.Sum256([]byte(cfg.Recipient))
	emailKey := fmt.Sprintf("email:%x", hash[:12])
	robotKey := "robot:migrated-robot:" + private.Bots[0].Revision
	receipts := map[string]service.AccountOpsDelivery{emailKey: {Provider: "email", Status: "sent", Attempts: 1}, robotKey: {Provider: "feishu", Status: "failed", Attempts: 1}}
	rawReceipts, err := json.Marshal(receipts)
	require.NoError(t, err)
	amount, threshold := 4.0, 5.0
	require.NoError(t, repo.Record(ctx, service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", AccountName: a.Name, Details: &service.AccountOpsDetails{Balance: &amount, Threshold: &threshold, Unit: "USD"}}))
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_ops_alerts SET state='failed',attempts=1,deliveries=$2::jsonb,next_send_at=NOW()-INTERVAL '1 second' WHERE account_id=$1`, id, string(rawReceipts))
	require.NoError(t, err)
	applyLegacyThresholdEpisodeTransfer(t, []int64{id})
	require.NoError(t, repo.SuppressDisabled(ctx, cfg))
	none, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, none)
	require.NoError(t, svc.ObserveThresholdForTesting(ctx, a, "balance_threshold", now))
	event, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.NotNil(t, event)
	require.Equal(t, 2, event.Attempts)
	robots := 0
	svc.SetNotificationHTTPClientForTesting(&http.Client{Transport: opsMigratedRobotTransport(func(r *http.Request) (*http.Response, error) {
		robots++
		require.Equal(t, "open.feishu.cn", r.URL.Host)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0}`)), Header: http.Header{}}, nil
	})})
	svc.DeliverNotificationForTesting(ctx, event)
	require.Zero(t, email.calls, "already successful email must not replay")
	require.Equal(t, 1, robots, "only the failed robot destination retries")
	out, err := scanOpsTransition(integrationDB.QueryRowContext(ctx, `SELECT `+opsTransitionColumns+` FROM account_ops_threshold_events WHERE id=$1`, event.ID))
	require.NoError(t, err)
	require.Equal(t, "sent", out.State)
	require.Equal(t, 1, out.Deliveries[emailKey].Attempts)
	require.Equal(t, 2, out.Deliveries[robotKey].Attempts)
	require.Equal(t, "sent", out.Deliveries[robotKey].Status)
}

func TestAccountOpsOnceMigrationRuleEditSuppressesUnboundWarning(t *testing.T) {
	ctx := context.Background()
	settings := NewSettingRepository(integrationEntClient)
	key := "account_ops_notifications_v1"
	previous, previousErr := settings.GetValue(ctx, key)
	defer func() {
		if previousErr == nil {
			_ = settings.Set(ctx, key, previous)
		} else {
			_ = settings.Delete(ctx, key)
		}
	}()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('migrated-criteria-edit','openai','apikey','active',true) RETURNING id`).Scan(&id))
	defer cleanupOnce(t, id)
	now := time.Now().UTC().Truncate(time.Microsecond)
	until := now.Add(time.Hour)
	snapshot := service.UpstreamBillingProbeSnapshot{Status: "ok", Balance: &service.UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &now, FreshUntil: &until, Data: map[string]any{"remaining": 2.0, "unit": "USD"}}}
	extra, err := json.Marshal(map[string]any{service.UpstreamBillingProbeExtraKey: snapshot})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=$2::jsonb WHERE id=$1`, id, string(extra))
	require.NoError(t, err)
	accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
	a, err := accounts.GetByID(ctx, id)
	require.NoError(t, err)
	repo := NewAccountOpsRepository(integrationDB).(*accountOpsRepository)
	email := &opsMigratedEmail{}
	svc := service.NewAccountOpsService(settings, repo, email)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	cfg := service.AccountOpsConfig{Enabled: true, Recipient: "criteria-edit@example.test", CooldownMinutes: 60, BalanceThresholds: []service.AccountOpsBalanceRule{{AccountID: id, Enabled: true, Threshold: 5, Unit: "USD"}}}
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	amount, threshold := 2.0, 5.0
	require.NoError(t, repo.Record(ctx, service.AccountOpsEvent{AccountID: id, Kind: "balance_threshold", AccountName: a.Name, Details: &service.AccountOpsDetails{Balance: &amount, Threshold: &threshold, Unit: "USD"}}))
	applyLegacyThresholdEpisodeTransfer(t, []int64{id})
	// An unchanged startup refresh must preserve the waiting transferred warning.
	require.NoError(t, repo.SuppressDisabled(ctx, cfg))
	var state string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM account_ops_threshold_events WHERE account_id=$1`, id).Scan(&state))
	require.Equal(t, "pending", state)
	newThreshold := 3.0
	_, err = svc.SaveRule(ctx, id, service.AccountOpsRuleUpdate{Metric: "balance", Enabled: true, Threshold: &newThreshold})
	require.NoError(t, err)
	require.NoError(t, svc.ObserveThresholdForTesting(ctx, a, "balance_threshold", now.Add(time.Second)))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM account_ops_threshold_events WHERE account_id=$1`, id).Scan(&state))
	require.Equal(t, "suppressed", state, "editing criteria before first scan invalidates the unbound old warning")
	none, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.Nil(t, none)
	require.Zero(t, email.calls)
	var details string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT details::text FROM account_ops_threshold_events WHERE account_id=$1`, id).Scan(&details))
	require.Contains(t, details, `"threshold": 5`)
}
