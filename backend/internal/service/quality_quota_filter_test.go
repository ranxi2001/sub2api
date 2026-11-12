//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func issue817Account(now time.Time) *Account {
	return &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{"codex_usage_updated_at": now.Format(time.RFC3339Nano), "codex_5h_used_percent": 20.0, "codex_7d_used_percent": 30.0}}
}

func TestIssue817QuotaEvidence(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func(*Account)
		want   bool
	}{
		{"available", func(*Account) {}, true},
		{"five_hour_exhausted", func(a *Account) { a.Extra["codex_5h_used_percent"] = 100.0 }, false},
		{"weekly_exhausted", func(a *Account) { a.Extra["codex_7d_used_percent"] = 100.0 }, false},
		{"over_100", func(a *Account) { a.Extra["codex_7d_used_percent"] = 101.0 }, false},
		{"unknown", func(a *Account) { a.Extra = nil }, true},
		{"undated", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			delete(a.Extra, "codex_usage_updated_at")
		}, true},
		{"invalid_date", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Extra["codex_usage_updated_at"] = "unknown"
		}, true},
		{"future_date", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Extra["codex_usage_updated_at"] = now.Add(time.Hour).Format(time.RFC3339)
		}, true},
		{"stale_without_reset", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Extra["codex_usage_updated_at"] = now.Add(-3 * time.Hour).Format(time.RFC3339)
		}, true},
		{"old_but_current_week", func(a *Account) {
			a.Extra["codex_7d_used_percent"] = 100.0
			a.Extra["codex_usage_updated_at"] = now.Add(-3 * time.Hour).Format(time.RFC3339)
			a.Extra["codex_7d_reset_at"] = now.Add(time.Hour).Format(time.RFC3339)
		}, false},
		{"five_hour_reset", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Extra["codex_5h_reset_at"] = now.Format(time.RFC3339)
		}, true},
		{"weekly_reset", func(a *Account) {
			a.Extra["codex_7d_used_percent"] = 100.0
			a.Extra["codex_7d_reset_at"] = now.Add(-time.Second).Format(time.RFC3339)
		}, true},
		{"relative_reset", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Extra["codex_usage_updated_at"] = now.Add(-time.Hour).Format(time.RFC3339)
			a.Extra["codex_5h_reset_after_seconds"] = 3600
		}, true},
		{"nan", func(a *Account) { a.Extra["codex_5h_used_percent"] = math.NaN() }, true},
		{"infinite", func(a *Account) { a.Extra["codex_5h_used_percent"] = math.Inf(1) }, true},
		{"bad_percent", func(a *Account) { a.Extra["codex_5h_used_percent"] = "unknown" }, true},
		{"other_platform", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Platform = PlatformAnthropic
		}, true},
		{"api_key", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Type = AccountTypeAPIKey
		}, true},
		{"other_identity", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Credentials = map[string]any{"chatgpt_account_id": "fixture-a"}
			a.Extra["chatgpt_account_id"] = "fixture-b"
		}, true},
		{"live_429", func(a *Account) {
			reset := now.Add(time.Minute)
			a.RateLimitResetAt = &reset
			a.Extra = nil
		}, false},
		{"expired_429", func(a *Account) {
			reset := now
			a.RateLimitResetAt = &reset
		}, true},
		{"auto_pause_disabled_still_exhausted", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Extra["auto_pause_5h_disabled"] = true
		}, false},
		{"below_full_even_with_lower_pause_threshold", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 99.0
			a.Extra["auto_pause_5h_threshold"] = 50
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := issue817Account(now)
			tc.change(a)
			require.Equal(t, tc.want, QualityTemplateQuotaAvailable(QualityRuleAccountFilter{Statuses: []string{StatusActive}}, a, now))
		})
	}
	for _, statuses := range [][]string{nil, {"rate_limited"}, {"active", "rate_limited"}, {"active", "error"}, {"temp_unschedulable"}, {"unschedulable"}, {"inactive"}} {
		a := issue817Account(now)
		a.Extra["codex_5h_used_percent"] = 100.0
		require.True(t, QualityTemplateQuotaAvailable(QualityRuleAccountFilter{Statuses: statuses}, a, now), "explicit status selection %v", statuses)
	}
}

func TestIssue817CoalescedQuotaObservationsKeepFieldTimes(t *testing.T) {
	now := time.Now()
	a := issue817Account(now)
	a.Extra["codex_7d_used_percent"] = 100.0
	a.Extra[CodexUsageFieldVersionsKey] = map[string]any{
		"codex_5h_used_percent": now.UnixNano(),
		"codex_7d_used_percent": now.Add(-3 * time.Hour).UnixNano(),
	}
	filter := QualityRuleAccountFilter{Statuses: []string{"active"}}
	require.True(t, QualityTemplateQuotaAvailable(filter, a, now), "new 5h observation must not refresh stale weekly exhaustion")
	a.Extra["codex_7d_reset_after_seconds"] = 3600
	a.Extra[CodexUsageFieldVersionsKey].(map[string]any)["codex_7d_reset_after_seconds"] = now.Add(-3 * time.Hour).UnixNano()
	require.True(t, QualityTemplateQuotaAvailable(filter, a, now), "relative reset must use its original field observation")
	a.Extra["codex_7d_reset_at"] = now.Add(time.Hour).Format(time.RFC3339Nano)
	require.False(t, QualityTemplateQuotaAvailable(filter, a, now), "known current window retains exhaustion until reset")
	delete(a.Extra[CodexUsageFieldVersionsKey].(map[string]any), "codex_7d_used_percent")
	require.True(t, QualityTemplateQuotaAvailable(filter, a, now), "missing version in versioned observations is unknown")
}

type issue817Templates struct {
	QualityRuleTemplateRepository
	template *QualityRuleTemplate
	err      error
}

func (r *issue817Templates) ForPlan(context.Context, int64) (*QualityRuleTemplate, error) {
	return r.template, r.err
}

type issue817Claims struct {
	qualityPlanRepo
	afterClaim func()
	lease      time.Time
	finishedAt time.Time
	next       time.Time
}

func (r *issue817Claims) ClaimPelican(ctx context.Context, p *ScheduledTestPlan, now, until, next time.Time) (bool, error) {
	claimed, err := r.qualityPlanRepo.ClaimPelican(ctx, p, now, until, next)
	if claimed {
		r.lease, r.next = until, next
		if r.afterClaim != nil {
			r.afterClaim()
		}
	}
	return claimed, err
}

func (r *issue817Claims) FinishPelican(ctx context.Context, id int64, until, finished time.Time) error {
	r.finishedAt = until
	return r.qualityPlanRepo.FinishPelican(ctx, id, until, finished)
}

func TestIssue817RunnerRechecksQuotaAfterClaim(t *testing.T) {
	for _, tc := range []struct {
		name      string
		statuses  []string
		manual    bool
		unlinked  bool
		lookupErr bool
		wantRun   bool
	}{
		{"normal_only_five_hour", []string{"active"}, false, false, false, false},
		{"normal_only_weekly", []string{"active"}, false, false, false, false},
		{"normal_only_429", []string{"active"}, false, false, false, false},
		{"explicit_rate_limited", []string{"active", "rate_limited"}, false, false, false, true},
		{"explicit_other_status", []string{"temp_unschedulable"}, false, false, false, true},
		{"all_statuses", nil, false, false, false, true},
		{"manual_run", []string{"active"}, true, false, false, true},
		{"independent_rule", []string{"active"}, false, true, false, true},
		{"template_lookup_failed", []string{"active"}, false, false, true, false},
		{"quota_recovers", []string{"active"}, false, false, false, true},
		{"owned_quality_penalty", []string{"active"}, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := issue817Account(time.Now())
			plans := &issue817Claims{}
			plans.afterClaim = func() {
				// The selection snapshot was usable. Change only after acquiring
				// the persisted execution lease to expose the original race.
				a.Extra["codex_5h_used_percent"] = 100.0
				if tc.name == "normal_only_weekly" {
					a.Extra["codex_5h_used_percent"], a.Extra["codex_7d_used_percent"] = 20.0, 100.0
				}
				if tc.name == "normal_only_429" {
					reset := time.Now().Add(time.Hour)
					a.RateLimitResetAt = &reset
					a.Extra = nil
				}
				if tc.name == "quota_recovers" {
					a.Extra["codex_5h_reset_at"] = time.Now().Add(-time.Second).Format(time.RFC3339)
				}
				if tc.name == "owned_quality_penalty" {
					a.Extra["codex_5h_used_percent"] = 20.0
					a.Schedulable = false
					cooldown := time.Now().Add(time.Hour)
					a.TempUnschedulableUntil = &cooldown
				}
			}
			templates := &issue817Templates{template: &QualityRuleTemplate{AccountFilter: QualityRuleAccountFilter{Statuses: tc.statuses}}}
			if tc.unlinked {
				templates.template = nil
			}
			if tc.lookupErr {
				templates.err = errors.New("fixture storage unavailable")
			}
			results := &pelicanResults{}
			svc := NewScheduledTestService(plans, results)
			svc.templateRepo = templates
			svc.qualityModels = func(context.Context, int64) ([]string, error) { return []string{"gpt-6-astra"}, nil }
			runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: svc, accountTestSvc: &AccountTestService{accountRepo: &qualitySupportAccounts{account: a}}}
			calls := 0
			runner.runPelican = func(_ context.Context, _ int64, _ string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
				calls++
				return &ScheduledTestResult{Status: "success", ResponseText: "21", PelicanConfig: cfg, QualityJudgment: &QualityJudgment{Verdict: "correct"}}, nil
			}
			plan := pelicanPlan()
			plan.ManualQualityRun = tc.manual
			plan.PelicanConfig.QuestionKind = "candy"
			plan.PelicanConfig.ParallelCount = 1
			plan.PelicanConfig.Quality = &QualityPolicy{ExpectedAnswer: "21", Action: "disable_scheduling"}
			started := time.Now()
			require.Equal(t, tc.wantRun, runner.runOnePlan(context.Background(), plan))
			require.True(t, plans.finished, "even a skipped round must release its lease")
			require.Equal(t, plans.lease, plans.finishedAt, "finish must use the claimed lease")
			require.True(t, plans.next.After(started), "skip keeps the advanced schedule")
			if tc.wantRun {
				require.Equal(t, 1, calls)
				require.Len(t, results.results, 1)
			} else {
				require.Zero(t, calls, "exhausted automatic round must not call upstream")
				require.Empty(t, results.results, "no fabricated quality sample")
				require.Empty(t, plans.outcomes, "no quality penalty or restoration")
			}
		})
	}
}
