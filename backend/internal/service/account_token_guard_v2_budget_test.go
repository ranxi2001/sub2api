package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func (r *accountTokenGuardV2TestRepo) ReconcileReloginBudget(_ context.Context, _ int64, _ string, rules AccountTokenGuardV2Rules, at time.Time, state string) (*AccountTokenGuardV2Record, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil {
		return nil, false, errors.New("missing record")
	}
	updated, pending := AdvanceAccountReloginBudget(*r.record, r.outcomes, rules, at, state)
	*r.record = updated
	return &updated, pending, nil
}

func (r *accountTokenGuardV2TestRepo) CreateAutomaticRelogin(ctx context.Context, id int64, hash, owner string, rules AccountTokenGuardV2Rules, now time.Time) (*OpenAIOAuthReauthTaskRecord, error) {
	record, pending, err := r.ReconcileReloginBudget(ctx, id, owner, rules, now, AccountTokenGuardV2ProbePending)
	if err != nil {
		return nil, err
	}
	if pending || record.AutoReloginExhausted || !record.Enabled || !record.AutoReloginEnabled || (record.CooldownUntil != nil && record.CooldownUntil.After(now)) || r.createTask == nil {
		return nil, infraerrors.Conflict("TOKEN_GUARD_V2_RELOGIN_GATED", "Automatic re-login budget gated")
	}
	return r.createTask(ctx, id, hash)
}

func (r *accountTokenGuardV2TestRepo) ResumeReloginBudget(_ context.Context, _ int64, rules AccountTokenGuardV2Rules, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil {
		return errors.New("missing record")
	}
	r.record.ReloginFailStreak, r.record.AutoReloginExhausted = 0, false
	until := now.Add(time.Duration(rules.ReloginCooldownSeconds) * time.Second)
	r.record.CooldownUntil = &until
	return nil
}

func budgetTestRules() AccountTokenGuardV2Rules {
	return AccountTokenGuardV2Rules{ProbeIntervalSeconds: 180, RetryIntervalSeconds: 120, ReloginCooldownSeconds: 60,
		FailStreakThreshold: 1, ReloginBackoffMaxSeconds: 240, ReloginMaxFailures: 5}
}

func TestReloginBudgetFakeClockBoundariesAndIdempotence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rules := budgetTestRules()
	record := AccountTokenGuardV2Record{Enabled: true, AutoReloginEnabled: true}
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 4 * time.Minute, 4 * time.Minute} {
		finished := now.Add(time.Duration(i) * time.Hour)
		event := AccountReloginOutcome{ID: int64(i + 1), Status: OpenAIOAuthReauthStatusFailed, FinishedAt: &finished}
		updated, pending := AdvanceAccountReloginBudget(record, []AccountReloginOutcome{event}, rules, finished, AccountTokenGuardV2ProbeAuth)
		require.False(t, pending)
		require.Equal(t, i+1, updated.ReloginFailStreak)
		require.Equal(t, finished.Add(want), *updated.CooldownUntil)
		require.Equal(t, i == 4, updated.AutoReloginExhausted)
		again, _ := AdvanceAccountReloginBudget(updated, []AccountReloginOutcome{event}, rules, finished.Add(time.Second), AccountTokenGuardV2ProbeAuth)
		require.Equal(t, updated, again, "multiple observers cannot count one terminal task twice")
		record = updated
	}
	require.True(t, record.Enabled)
	require.True(t, record.AutoReloginEnabled, "exhaustion does not change the user's switch")
	rules.ReloginMaxFailures = 20
	unchanged, _ := AdvanceAccountReloginBudget(record, nil, rules, now, AccountTokenGuardV2ProbeOK)
	require.True(t, unchanged.AutoReloginExhausted, "raising a limit or a plain successful probe is not an explicit recovery")
}

func TestReloginBudgetRequiresPostSuccessProbeAndRejectsLateSuccess(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rules := budgetTestRules()
	failed := AccountReloginOutcome{ID: 1, Status: OpenAIOAuthReauthStatusFailed, FinishedAt: &now}
	record, _ := AdvanceAccountReloginBudget(AccountTokenGuardV2Record{}, []AccountReloginOutcome{failed}, rules, now, AccountTokenGuardV2ProbeAuth)
	success := AccountReloginOutcome{ID: 2, Status: OpenAIOAuthReauthStatusSucceeded, FinishedAt: &now}
	before, pending := AdvanceAccountReloginBudget(record, []AccountReloginOutcome{success}, rules, now.Add(-time.Second), AccountTokenGuardV2ProbeOK)
	require.True(t, pending)
	require.Equal(t, 1, before.ReloginFailStreak)
	auth, _ := AdvanceAccountReloginBudget(record, []AccountReloginOutcome{success}, rules, now, AccountTokenGuardV2ProbeAuth)
	require.Equal(t, 2, auth.ReloginFailStreak, "a new saved token rejected by its first fresh probe consumes one attempt")
	again, _ := AdvanceAccountReloginBudget(auth, []AccountReloginOutcome{success}, rules, now.Add(time.Second), AccountTokenGuardV2ProbeAuth)
	require.Equal(t, auth.ReloginFailStreak, again.ReloginFailStreak)
	ok, _ := AdvanceAccountReloginBudget(auth, []AccountReloginOutcome{success}, rules, now.Add(time.Second), AccountTokenGuardV2ProbeOK)
	require.Zero(t, ok.ReloginFailStreak)
	require.Nil(t, ok.CooldownUntil)
	failed.ID = 3
	newFailure, _ := AdvanceAccountReloginBudget(auth, []AccountReloginOutcome{success, failed}, rules, now, AccountTokenGuardV2ProbeOK)
	require.Equal(t, 3, newFailure.ReloginFailStreak, "a late older success cannot erase a newer failed task")
}

func TestReloginBudgetWatermarkWaitsForOutOfOrderTask(t *testing.T) {
	now := time.Now()
	later := AccountReloginOutcome{ID: 2, Status: OpenAIOAuthReauthStatusFailed, FinishedAt: &now}
	earlier := AccountReloginOutcome{ID: 1, Status: OpenAIOAuthReauthStatusRunning}
	record, pending := AdvanceAccountReloginBudget(AccountTokenGuardV2Record{}, []AccountReloginOutcome{later, earlier}, budgetTestRules(), now, AccountTokenGuardV2ProbeAuth)
	require.True(t, pending)
	require.Zero(t, record.LastCountedTaskID)
	earlier.Status, earlier.FinishedAt = OpenAIOAuthReauthStatusFailed, &now
	record, pending = AdvanceAccountReloginBudget(record, []AccountReloginOutcome{later, earlier}, budgetTestRules(), now, AccountTokenGuardV2ProbeAuth)
	require.False(t, pending)
	require.Equal(t, 2, record.ReloginFailStreak)
	require.EqualValues(t, 2, record.LastCountedTaskID)
}

func TestReloginBudgetOldSettingsDefaultsAndValidation(t *testing.T) {
	svc := NewAccountTokenGuardV2Service(&accountTokenGuardV2TestRepo{}, &accountTokenGuardV2TestSettings{raw: `{"probe_interval_seconds":180,"retry_interval_seconds":120,"relogin_cooldown_seconds":7200,"fail_streak_threshold":1}`}, nil, nil, nil)
	rules, err := svc.GetRules(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, rules.ReloginMaxFailures)
	require.Equal(t, 7200, rules.ReloginBackoffMaxSeconds, "old long cooldowns are never silently shortened")
	for _, limit := range []int{-1, 21} {
		bad := budgetTestRules()
		bad.ReloginMaxFailures = limit
		require.Error(t, ValidateAccountTokenGuardV2Rules(bad))
	}
	bad := budgetTestRules()
	bad.ReloginBackoffMaxSeconds = 59
	require.Error(t, ValidateAccountTokenGuardV2Rules(bad))
}

func TestExhaustedReloginStillProbesAndManualFailureDoesNotRestartAutomation(t *testing.T) {
	reauth, reader, tasks, _, _, _ := newReauthTestService("acct-1")
	repo := &accountTokenGuardV2TestRepo{record: &AccountTokenGuardV2Record{
		AccountID: 42, Enabled: true, AutoReloginEnabled: true, ReloginFailStreak: 3, AutoReloginExhausted: true,
	}}
	svc := NewAccountTokenGuardV2Service(repo, nil, reader, accountTokenGuardV2TestProber{err: errors.New("invalid token")}, reauth)
	require.NoError(t, svc.runProbe(context.Background(), *repo.record))
	require.NotNil(t, repo.completion)
	require.Equal(t, AccountTokenGuardV2ProbeAuth, repo.completion.ProbeState)
	require.Contains(t, repo.completion.BlockedReason, "exhausted")
	require.Zero(t, tasks.createCalls)
	require.True(t, repo.record.Enabled)
	// A manually initiated failure is accounted too; observing it cannot resume.
	now := time.Now()
	repo.outcomes = []AccountReloginOutcome{{ID: 9, Status: OpenAIOAuthReauthStatusFailed, FinishedAt: &now}}
	require.NoError(t, svc.runProbe(context.Background(), *repo.record))
	require.True(t, repo.record.AutoReloginExhausted)
	require.Zero(t, tasks.createCalls)
}

func TestReloginBudgetPartialSuccessAdvancesButCannotReset(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	record := AccountTokenGuardV2Record{ReloginFailStreak: 2, AutoReloginExhausted: true, CooldownUntil: &until}
	first := make([]AccountReloginOutcome, 100)
	for i := range first {
		first[i] = AccountReloginOutcome{ID: int64(i + 1), Status: OpenAIOAuthReauthStatusSucceeded, FinishedAt: &now}
	}
	partial, pending := AdvanceAccountReloginBudgetPage(record, first, budgetTestRules(), now, AccountTokenGuardV2ProbeOK, false)
	require.False(t, pending)
	require.EqualValues(t, 100, partial.LastCountedTaskID, "a verified success must not pin a partial page")
	require.Equal(t, record.ReloginFailStreak, partial.ReloginFailStreak)
	require.True(t, partial.AutoReloginExhausted, "a partial page cannot prove the latest overall outcome succeeded")
	require.Equal(t, &until, partial.CooldownUntil)
	last := AccountReloginOutcome{ID: 101, Status: OpenAIOAuthReauthStatusFailed, FinishedAt: &now}
	final, pending := AdvanceAccountReloginBudgetPage(partial, []AccountReloginOutcome{first[99], last}, budgetTestRules(), now, AccountTokenGuardV2ProbeOK, true)
	require.False(t, pending)
	require.EqualValues(t, 101, final.LastCountedTaskID)
	require.Equal(t, 3, final.ReloginFailStreak)
	again, _ := AdvanceAccountReloginBudgetPage(final, []AccountReloginOutcome{last}, budgetTestRules(), now, AccountTokenGuardV2ProbeOK, true)
	require.Equal(t, final, again, "the inclusive page cursor must not recount a failed task")
}

func TestRestoredBudgetPlainOKKeepsCooldownBeforeAnotherAuthProbe(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reauth, reader, tasks, _, _, _ := newReauthTestService("acct-1")
	require.NoError(t, tasks.UpsertConfig(ctx, reauthEmailConfig(42, "user@example.test", "encrypted:https://mail.example.test/code")))
	rules := budgetTestRules()
	raw, err := json.Marshal(rules)
	require.NoError(t, err)
	repo := &accountTokenGuardV2TestRepo{record: &AccountTokenGuardV2Record{
		AccountID: 42, Enabled: true, AutoReloginEnabled: true, ReloginFailStreak: 5,
		LastCountedTaskID: 1, AutoReloginExhausted: true,
	}, createTask: tasks.CreateTask, outcomes: []AccountReloginOutcome{{ID: 1, Status: OpenAIOAuthReauthStatusFailed, FinishedAt: &now}}}
	prober := &accountTokenGuardV2TestProber{}
	svc := NewAccountTokenGuardV2Service(repo, &accountTokenGuardV2TestSettings{raw: string(raw)}, reader, prober, reauth)
	svc.now = func() time.Time { return now }
	_, err = svc.ResumeReloginBudget(ctx, 42)
	require.NoError(t, err)
	until := now.Add(time.Minute)
	now = now.Add(time.Second)
	require.NoError(t, svc.runProbe(ctx, *repo.record))
	require.Equal(t, AccountTokenGuardV2ProbeOK, repo.completion.ProbeState)
	require.Equal(t, &until, repo.completion.CooldownUntil)
	prober.err = errors.New("invalid token")
	now = until.Add(-time.Nanosecond)
	require.NoError(t, svc.runProbe(ctx, *repo.record))
	require.Zero(t, tasks.createCalls)
	require.Equal(t, &until, repo.completion.CooldownUntil)
	now = until
	require.NoError(t, svc.runProbe(ctx, *repo.record))
	require.Equal(t, 1, tasks.createCalls, "the explicit recovery cooldown opens only at its boundary")
}

func TestRestoredBudgetCannotReuseAHistoricalSuccessToClearCooldown(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := now.Add(time.Minute)
	restored := AccountTokenGuardV2Record{LastCountedTaskID: 7, CooldownUntil: &until}
	oldSuccess := AccountReloginOutcome{ID: 7, Status: OpenAIOAuthReauthStatusSucceeded, FinishedAt: &now}
	plain, _ := AdvanceAccountReloginBudget(restored, []AccountReloginOutcome{oldSuccess}, budgetTestRules(), now.Add(time.Second), AccountTokenGuardV2ProbeOK)
	require.Equal(t, &until, plain.CooldownUntil, "explicit recovery's old watermark is not a new successful login")
	newSuccess := oldSuccess
	newSuccess.ID = 8
	verified, _ := AdvanceAccountReloginBudget(restored, []AccountReloginOutcome{oldSuccess, newSuccess}, budgetTestRules(), now.Add(time.Second), AccountTokenGuardV2ProbeOK)
	require.Nil(t, verified.CooldownUntil, "a new successful task and fresh OK still clear the cooldown")
}
