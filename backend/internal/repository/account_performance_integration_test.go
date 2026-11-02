//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Uses only the harness's migrated disposable PostgreSQL container. testEntTx
// rolls back accounts, usage, traces and probes together, even on assertion failure.
func TestAccountPerformancePostgresWindowAndSuccessfulAverages(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	suffix := uuid.NewString()
	user := mustCreateUser(t, client, &service.User{Email: "performance-" + suffix + "@test.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "performance-" + suffix, Name: "synthetic"})
	account := mustCreateAccount(t, client, &service.Account{Name: "performance-" + suffix, Platform: service.PlatformOpenAI})
	empty := mustCreateAccount(t, client, &service.Account{Name: "performance-empty-" + suffix, Platform: service.PlatformOpenAI})
	usage := newUsageLogRepositoryWithSQL(client, tx)
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-60 * time.Minute)

	type sample struct {
		name        string
		event       time.Time
		recorded    time.Time
		output      int
		duration    int
		ttft        int
		traces      int
		mutateTrace func(map[string]any)
	}
	add := func(s sample) {
		t.Helper()
		billingMode := "token"
		row := &service.UsageLog{
			UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
			RequestID: suffix + "-" + s.name, Model: "synthetic-performance",
			OutputTokens: s.output, DurationMs: &s.duration, FirstTokenMs: &s.ttft,
			Stream: true, RateMultiplier: 1, BillingMode: &billingMode, CreatedAt: s.recorded,
		}
		inserted, err := usage.Create(ctx, row)
		require.NoError(t, err)
		require.True(t, inserted)
		trace := map[string]any{
			"version": 1, "started_at": s.event.Format(time.RFC3339Nano),
			"outcome": "success", "status": 200, "body_complete": true,
			"canceled": false, "truncated": false, "client_disconnect": false,
			"downstream_error": false, "terminal": "completed",
		}
		if s.mutateTrace != nil {
			s.mutateTrace(trace)
		}
		raw, err := json.Marshal(trace)
		require.NoError(t, err)
		for i := 0; i < s.traces; i++ {
			_, err := tx.ExecContext(ctx, `INSERT INTO request_timing_details(usage_log_id,trace_id,created_at,detail) VALUES($1,$2::uuid,$3,$4::jsonb)`, row.ID, uuid.NewString(), s.recorded, string(raw))
			require.NoError(t, err)
		}
	}
	// Lower boundary is inclusive; completion/insert happens after event start.
	add(sample{name: "inclusive-start-z", event: since, recorded: since.Add(2 * time.Second), output: 100, duration: 2000, ttft: 500, traces: 1})
	// RFC3339Nano and explicit timezone offset must work on PostgreSQL 14-18.
	add(sample{name: "nanoseconds-offset", event: now.Add(-5 * time.Minute).Add(123456789 * time.Nanosecond).In(time.FixedZone("fixture", 9*3600)), recorded: now.Add(-4 * time.Minute), output: 400, duration: 4000, ttft: 1500, traces: 1})

	for _, s := range []sample{
		// Delayed insertion cannot pull an older event into the window.
		{name: "before-window", event: since.Add(-time.Microsecond), traces: 1},
		{name: "after-window", event: now.Add(time.Second), traces: 1},
		{name: "failed", traces: 1, mutateTrace: func(d map[string]any) { d["outcome"] = "failed" }},
		{name: "incomplete", traces: 1, mutateTrace: func(d map[string]any) { d["terminal"] = "incomplete" }},
		{name: "body-incomplete", traces: 1, mutateTrace: func(d map[string]any) { d["body_complete"] = false }},
		{name: "canceled", traces: 1, mutateTrace: func(d map[string]any) { d["canceled"] = true }},
		{name: "ambiguous-traces", traces: 2},
		{name: "missing-trace", traces: 0},
		{name: "missing-time", traces: 1, mutateTrace: func(d map[string]any) { delete(d, "started_at") }},
		{name: "invalid-time", traces: 1, mutateTrace: func(d map[string]any) { d["started_at"] = "unobserved" }},
	} {
		if s.event.IsZero() {
			s.event = now.Add(-time.Minute)
		}
		s.recorded = now.Add(-time.Second)
		if s.event.After(now) {
			s.recorded = s.event.Add(time.Second)
		}
		s.output, s.duration, s.ttft = 9000, 1000, 900
		add(s)
	}

	metrics, err := readAccountPerformance(ctx, tx, []int64{account.ID, empty.ID}, now)
	require.NoError(t, err, "execute production metrics SQL against migrated PostgreSQL")
	got := metrics[account.ID]
	require.Equal(t, 2, got.TPSSamples)
	require.Equal(t, 2, got.TTFTSamples)
	require.NotNil(t, got.AvgTPS)
	require.NotNil(t, got.AvgTTFTMs)
	require.InDelta(t, 75, *got.AvgTPS, 0.00001, "arithmetic mean of 100/2 and 400/4, not pooled tokens/duration")
	require.InDelta(t, 1000, *got.AvgTTFTMs, 0.00001)
	require.Nil(t, got.DegradationPercent, "no quality probes is unknown, never 0%%")
	require.Zero(t, got.QualitySamples)
	require.Equal(t, since, got.WindowStart)
	require.Equal(t, now, got.WindowEnd)
	require.Nil(t, metrics[empty.ID].AvgTPS)
	require.Nil(t, metrics[empty.ID].AvgTTFTMs)
	require.Nil(t, metrics[empty.ID].DegradationPercent)

	// A saved global window immediately changes both latency sample sets.
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,'15') ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, service.SettingKeyAccountPerformanceWindowMinutes)
	require.NoError(t, err)
	metrics, err = readAccountPerformance(ctx, tx, []int64{account.ID}, now)
	require.NoError(t, err)
	got = metrics[account.ID]
	require.Equal(t, now.Add(-15*time.Minute), got.WindowStart)
	require.Equal(t, 1, got.TPSSamples)
	require.Equal(t, 1, got.TTFTSamples)
	require.InDelta(t, 100, *got.AvgTPS, 0.00001)
	require.InDelta(t, 1500, *got.AvgTTFTMs, 0.00001)
}

func TestAccountPerformancePostgresCompletedQualityProbeRounds(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	suffix := uuid.NewString()
	account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "performance-quality-" + suffix, Platform: service.PlatformOpenAI})
	now := time.Now().UTC().Truncate(time.Second)
	const config = `{"quality":{},"parallel_count":2,"model_ids":["probe-a","probe-b"]}`
	rows, err := tx.QueryContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at) VALUES($1,'probe-a','*/30 * * * *',true,$2::jsonb,$3) RETURNING id`, account.ID, config, now.Add(-2*time.Hour))
	require.NoError(t, err)
	require.True(t, rows.Next())
	var plan int64
	require.NoError(t, rows.Scan(&plan))
	require.NoError(t, rows.Close())

	probe := func(round, status, verdict, message, action string, at time.Time, cfg string) {
		t.Helper()
		_, err := tx.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,jsonb_build_object('verdict',$9::text),$10)`, plan, status, at, at.Add(time.Second), now.Add(-time.Second), cfg, round, action, verdict, message)
		require.NoError(t, err)
	}
	// Two models × two samples. Partial saves must never count as full rounds.
	for _, round := range []string{"passed", "failed", "transport", "partial", "past15", "old", "future", "stale", "malformed"} {
		count := 4
		if round == "partial" {
			count = 3
		}
		for i := 0; i < count; i++ {
			status, verdict, message, action := "success", "correct", "", "passed"
			at, cfg := now.Add(-time.Minute), config
			if (round == "failed" || round == "partial") && i == 0 {
				status, verdict, message, action = "failed", "incorrect", "state_degraded", "failure_counted:1/2"
			}
			if round == "transport" && i == 0 {
				status, verdict, message, action = "failed", "unknown", "timeout", "inconclusive"
			}
			if round == "past15" {
				at = now.Add(-16 * time.Minute)
			}
			if round == "old" {
				at = now.Add(-61 * time.Minute)
			}
			if round == "future" {
				at = now.Add(time.Second)
			}
			if round == "stale" && i == 0 {
				action = "stale_run"
			}
			if round == "malformed" {
				cfg = `{"parallel_count":"invalid","model_ids":{}}`
			}
			probe(fmt.Sprintf("%s-%s", suffix, round), status, verdict, message, action, at, cfg)
		}
	}
	metrics, err := readAccountPerformance(ctx, tx, []int64{account.ID}, now)
	require.NoError(t, err)
	got := metrics[account.ID]
	require.Equal(t, 3, got.QualitySamples, "60-minute default includes the complete conclusive round older than 15 minutes")
	require.NotNil(t, got.DegradationPercent)
	require.InDelta(t, 100.0/3, *got.DegradationPercent, 0.00001)
	require.Nil(t, got.AvgTPS)
	require.Nil(t, got.AvgTTFTMs)
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,'15') ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, service.SettingKeyAccountPerformanceWindowMinutes)
	require.NoError(t, err)
	metrics, err = readAccountPerformance(ctx, tx, []int64{account.ID}, now)
	require.NoError(t, err)
	require.Equal(t, 2, metrics[account.ID].QualitySamples)
	require.InDelta(t, 50, *metrics[account.ID].DegradationPercent, 0.00001)
	_, err = tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET enabled=false WHERE id=$1`, plan)
	require.NoError(t, err)
	metrics, err = readAccountPerformance(ctx, tx, []int64{account.ID}, now)
	require.NoError(t, err)
	require.Nil(t, metrics[account.ID].DegradationPercent)
	require.Zero(t, metrics[account.ID].QualitySamples)
}
