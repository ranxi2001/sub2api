package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type accountTokenGuardV2TestRepo struct {
	mu          sync.Mutex
	createTask  func(context.Context, int64, string) (*OpenAIOAuthReauthTaskRecord, error)
	outcomes    []AccountReloginOutcome
	record      *AccountTokenGuardV2Record
	claimBusy   bool
	completion  *AccountTokenGuardV2ProbeCompletion
	rescheduled bool
	pruneErr    error
	pruneOrphan bool
	claimed     bool
}

func (r *accountTokenGuardV2TestRepo) PruneDeletedAccounts(context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pruneErr != nil {
		return 0, r.pruneErr
	}
	if r.pruneOrphan && r.record != nil {
		r.record = nil
		return 1, nil
	}
	return 0, nil
}

func (r *accountTokenGuardV2TestRepo) UpsertAccount(_ context.Context, accountID int64, enabled, autoRelogin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil {
		r.record = &AccountTokenGuardV2Record{AccountID: accountID, NextProbeAt: time.Now()}
	}
	r.record.Enabled = enabled
	r.record.AutoReloginEnabled = autoRelogin
	return nil
}
func (r *accountTokenGuardV2TestRepo) DeleteAccount(context.Context, int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record = nil
	return nil
}
func (r *accountTokenGuardV2TestRepo) GetAccount(context.Context, int64) (*AccountTokenGuardV2Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil {
		return nil, nil
	}
	copy := *r.record
	return &copy, nil
}
func (r *accountTokenGuardV2TestRepo) ListAccounts(context.Context) ([]AccountTokenGuardV2Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil {
		return nil, nil
	}
	return []AccountTokenGuardV2Record{*r.record}, nil
}
func (r *accountTokenGuardV2TestRepo) ClaimDue(_ context.Context, owner string, duration time.Duration, _ int, dueBefore time.Time) ([]AccountTokenGuardV2Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimed = true
	if r.record == nil || r.claimBusy || !r.record.Enabled || r.record.NextProbeAt.After(dueBefore) || (r.record.LeaseUntil != nil && r.record.LeaseUntil.After(time.Now())) {
		return nil, nil
	}
	until := time.Now().Add(duration)
	r.record.LeaseOwner, r.record.LeaseUntil = owner, &until
	return []AccountTokenGuardV2Record{*r.record}, nil
}
func (r *accountTokenGuardV2TestRepo) ClaimAccount(_ context.Context, _ int64, owner string, duration time.Duration) (*AccountTokenGuardV2Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record == nil || r.claimBusy {
		return nil, nil
	}
	until := time.Now().Add(duration)
	r.record.LeaseOwner, r.record.LeaseUntil = owner, &until
	copy := *r.record
	return &copy, nil
}
func (r *accountTokenGuardV2TestRepo) ReleaseProbe(_ context.Context, _ int64, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record != nil && r.record.LeaseOwner == owner {
		r.record.LeaseOwner, r.record.LeaseUntil = "", nil
	}
	return nil
}
func (r *accountTokenGuardV2TestRepo) CompleteProbe(_ context.Context, _ int64, _ string, result AccountTokenGuardV2ProbeCompletion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completion = &result
	if r.record != nil {
		r.record.ProbeState = result.ProbeState
		r.record.ProbeDetail = result.ProbeDetail
		r.record.FailStreak = result.FailStreak
		r.record.LastProbeAt = &result.LastProbeAt
		r.record.LastReauthAt = result.LastReauthAt
		r.record.NextProbeAt = result.NextProbeAt
		r.record.CooldownUntil = result.CooldownUntil
		r.record.BlockedReason = result.BlockedReason
		r.record.LeaseOwner, r.record.LeaseUntil = "", nil
	}
	return nil
}
func (r *accountTokenGuardV2TestRepo) MarkReauthRequested(context.Context, int64, time.Time, time.Time) error {
	return nil
}
func (r *accountTokenGuardV2TestRepo) RescheduleEnabled(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rescheduled = true
	return nil
}

type accountTokenGuardV2TestSettings struct{ raw string }

func (s *accountTokenGuardV2TestSettings) GetValue(_ context.Context, key string) (string, error) {
	if s.raw == "" || key == openAIOAuthReauthRuntimeSettingsKey {
		return "", ErrSettingNotFound
	}
	return s.raw, nil
}
func (s *accountTokenGuardV2TestSettings) Set(_ context.Context, _ string, value string) error {
	s.raw = value
	return nil
}

type accountTokenGuardV2TestProber struct{ err error }

type accountTokenGuardV2BlockingRepo struct {
	accountTokenGuardV2TestRepo
	started      chan struct{}
	finished     chan struct{}
	once         sync.Once
	finishedOnce sync.Once
}

func (r *accountTokenGuardV2BlockingRepo) ClaimDue(ctx context.Context, _ string, _ time.Duration, _ int, _ time.Time) ([]AccountTokenGuardV2Record, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	r.finishedOnce.Do(func() { close(r.finished) })
	return nil, ctx.Err()
}

func TestAccountTokenGuardV2StopCancelsCycle(t *testing.T) {
	reauth, reader, _, _, _, _ := newReauthTestService("acct-1")
	repo := &accountTokenGuardV2BlockingRepo{started: make(chan struct{}), finished: make(chan struct{})}
	svc := NewAccountTokenGuardV2Service(repo, nil, reader, accountTokenGuardV2TestProber{}, reauth)
	svc.Start()
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("cycle did not start")
	}
	svc.Stop()
	select {
	case <-repo.finished:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the in-flight cycle")
	}
	svc.Start()
	svc.Stop()
}

func (p accountTokenGuardV2TestProber) FetchOpenAIModelsListFresh(context.Context, *Account) (*OpenAIModelsResponse, error) {
	return nil, p.err
}

func TestAccountTokenGuardV2Explicit403PolicyDoesNotQueueRelogin(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"session_blocked_by_cyber_policy","message":"synthetic-private"}}`,
		`{"error":{"code":"cyber_policy","message":"synthetic-private"}}`,
		`{"error":{"type":"content_policy_violation","message":"synthetic-private"}}`,
		`{"error":{"code":"upstream_error","type":"upstream_error","message":"failed to WebSocket dial: expected handshake response status code 101 but got 403"}}`,
	} {
		for _, exhausted := range []bool{false, true} {
			t.Run(fmt.Sprintf("exhausted=%t/%s", exhausted, body[:min(len(body), 65)]), func(t *testing.T) {
				reauth, reader, reauthRepo, updater, _, _ := newReauthTestService("acct-1")
				require.NoError(t, reauthRepo.UpsertConfig(context.Background(), reauthEmailConfig(42, "user@example.test", "encrypted:https://mail.example.test/code")))
				cooldown := time.Now().Add(-time.Hour)
				failures := 0
				if exhausted {
					failures = 3
				}
				guardRepo := &accountTokenGuardV2TestRepo{record: &AccountTokenGuardV2Record{
					AccountID: 42, Enabled: true, AutoReloginEnabled: true, FailStreak: 390,
					ReloginFailStreak: failures, AutoReloginExhausted: exhausted, CooldownUntil: &cooldown,
				}}
				guardRepo.createTask = reauthRepo.CreateTask
				svc := NewAccountTokenGuardV2Service(guardRepo, nil, reader, accountTokenGuardV2TestProber{err: fmt.Errorf("wrapped: %w", &codexModelsManifestUpstreamError{
					err: errors.New("unauthorized synthetic-private"), statusCode: http.StatusForbidden, body: []byte(body),
				})}, reauth)
				require.NoError(t, svc.runProbe(context.Background(), *guardRepo.record))
				require.NotNil(t, guardRepo.completion)
				require.Equal(t, AccountTokenGuardV2ProbeTransient, guardRepo.completion.ProbeState)
				require.Equal(t, accountTokenGuardV2PolicyBlocked, guardRepo.completion.BlockedReason)
				require.NotContains(t, guardRepo.completion.ProbeDetail, "synthetic-private")
				require.Equal(t, 390, guardRepo.completion.FailStreak)
				require.Equal(t, &cooldown, guardRepo.completion.CooldownUntil)
				require.Zero(t, reauthRepo.createCalls)
				require.False(t, updater.applied)
				require.Equal(t, failures, guardRepo.record.ReloginFailStreak)
				require.Equal(t, exhausted, guardRepo.record.AutoReloginExhausted)
			})
		}
	}
}

func TestAccountTokenGuardV2PolicyClassifierDoesNotTrustMessagesOrEchoes(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"account_deactivated","message":"session_blocked_by_cyber_policy"}}`,
		`{"error":{"code":"account_deactivated"},"input":{"error":{"code":"cyber_policy"}}}`,
		`{"error":{"code":"account_deactivated","message":"synthetic-private"}}`,
	} {
		svc := &AccountTokenGuardV2Service{prober: accountTokenGuardV2TestProber{err: &codexModelsManifestUpstreamError{
			err: errors.New("synthetic-private"), statusCode: http.StatusForbidden, body: []byte(body),
		}}}
		state, detail := svc.probeAccount(context.Background(), &Account{Platform: PlatformOpenAI, Credentials: map[string]any{"access_token": "synthetic"}})
		require.Equal(t, AccountTokenGuardV2ProbeTransient, state)
		require.Equal(t, credentialOperationDetail("account_deactivated"), detail)
		require.Equal(t, "account_deactivated", probeOperationFailure(state, detail, nil).Code)
	}
}

func TestAccountTokenGuardV2SecondAuthFailureQueuesRelogin(t *testing.T) {
	reauth, reader, reauthRepo, _, _, _ := newReauthTestService("acct-1")
	require.NoError(t, reauthRepo.UpsertConfig(context.Background(), reauthEmailConfig(42, "user@example.com", "encrypted:https://mail.example.com/code")))
	guardRepo := &accountTokenGuardV2TestRepo{record: &AccountTokenGuardV2Record{
		AccountID: 42, Enabled: true, AutoReloginEnabled: true, FailStreak: 1, NextProbeAt: time.Now(),
	}}
	guardRepo.createTask = reauthRepo.CreateTask
	svc := NewAccountTokenGuardV2Service(guardRepo, nil, reader, accountTokenGuardV2TestProber{err: &codexModelsManifestUpstreamError{
		err: errors.New("unauthorized"), statusCode: http.StatusUnauthorized,
	}}, reauth)
	record, err := guardRepo.GetAccount(context.Background(), 42)
	require.NoError(t, err)

	require.NoError(t, svc.runProbe(context.Background(), *record))

	require.NotNil(t, guardRepo.completion)
	require.Equal(t, AccountTokenGuardV2ProbeAuth, guardRepo.completion.ProbeState)
	require.Equal(t, 2, guardRepo.completion.FailStreak)
	require.NotNil(t, guardRepo.completion.LastReauthAt)
	require.NotNil(t, guardRepo.completion.CooldownUntil)
	require.Contains(t, guardRepo.completion.ProbeDetail, "automatic re-login queued")
	require.Equal(t, 1, reauthRepo.createCalls)
	require.NotNil(t, reauthRepo.task)
}

func TestAccountTokenGuardV2ActiveReloginTaskIsNotDuplicated(t *testing.T) {
	reauth, reader, reauthRepo, _, _, _ := newReauthTestService("acct-1")
	require.NoError(t, reauthRepo.UpsertConfig(context.Background(), reauthEmailConfig(42, "user@example.com", "encrypted:https://mail.example.com/code")))
	existing := &OpenAIOAuthReauthTaskRecord{ID: 99, AccountID: 42, Status: OpenAIOAuthReauthStatusRunning, Stage: OpenAIOAuthReauthStageStarting}
	reauthRepo.task = existing
	reauthRepo.createErr = infraerrors.Conflict("OPENAI_REAUTH_TASK_ACTIVE", "task already active")
	guardRepo := &accountTokenGuardV2TestRepo{record: &AccountTokenGuardV2Record{
		AccountID: 42, Enabled: true, AutoReloginEnabled: true, FailStreak: 1, NextProbeAt: time.Now(),
	}}
	guardRepo.createTask = reauthRepo.CreateTask
	svc := NewAccountTokenGuardV2Service(guardRepo, nil, reader, accountTokenGuardV2TestProber{err: errors.New("invalid token")}, reauth)
	record, err := guardRepo.GetAccount(context.Background(), 42)
	require.NoError(t, err)

	require.NoError(t, svc.runProbe(context.Background(), *record))

	require.Same(t, existing, reauthRepo.task)
	require.Equal(t, 1, reauthRepo.createCalls)
	require.Contains(t, guardRepo.completion.ProbeDetail, "re-login task already active")
	require.Nil(t, guardRepo.completion.LastReauthAt)
}

func TestAccountTokenGuardV2ProbeNowDistinguishesMissingAndBusy(t *testing.T) {
	reauth, reader, _, _, _, _ := newReauthTestService("acct-1")
	prober := accountTokenGuardV2TestProber{}

	missing := NewAccountTokenGuardV2Service(&accountTokenGuardV2TestRepo{}, nil, reader, prober, reauth)
	_, err := missing.ProbeNow(context.Background(), 42)
	require.Equal(t, http.StatusNotFound, infraerrors.Code(err))

	busyRepo := &accountTokenGuardV2TestRepo{
		record:    &AccountTokenGuardV2Record{AccountID: 42, NextProbeAt: time.Now()},
		claimBusy: true,
	}
	busy := NewAccountTokenGuardV2Service(busyRepo, nil, reader, prober, reauth)
	_, err = busy.ProbeNow(context.Background(), 42)
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
}

func TestAccountTokenGuardV2RulesDefaultAndPersistence(t *testing.T) {
	repo := &accountTokenGuardV2TestRepo{}
	settings := &accountTokenGuardV2TestSettings{}
	svc := NewAccountTokenGuardV2Service(repo, settings, nil, nil, nil)

	rules, err := svc.GetRules(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultAccountTokenGuardV2Rules(), rules)

	want := AccountTokenGuardV2Rules{
		ProbeIntervalSeconds: 900, RetryIntervalSeconds: 60,
		ReloginCooldownSeconds: 7200, FailStreakThreshold: 3,
	}
	want = NormalizeReloginBudgetRules(want)
	saved, err := svc.SaveRules(context.Background(), want)
	require.NoError(t, err)
	require.Equal(t, want, saved)
	require.True(t, repo.rescheduled)

	var persisted AccountTokenGuardV2Rules
	require.NoError(t, json.Unmarshal([]byte(settings.raw), &persisted))
	require.Equal(t, want, persisted)
	reread, err := svc.GetRules(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, reread)
}

func TestAccountTokenGuardV2RulesRejectInvalidRange(t *testing.T) {
	repo := &accountTokenGuardV2TestRepo{}
	settings := &accountTokenGuardV2TestSettings{}
	svc := NewAccountTokenGuardV2Service(repo, settings, nil, nil, nil)

	_, err := svc.SaveRules(context.Background(), AccountTokenGuardV2Rules{
		ProbeIntervalSeconds: 59, RetryIntervalSeconds: 60,
		ReloginCooldownSeconds: 1800, FailStreakThreshold: 2,
	})
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Empty(t, settings.raw)
	require.False(t, repo.rescheduled)
}

func TestAccountTokenGuardV2InvalidSavedRulesDoNotQueueRelogin(t *testing.T) {
	for _, test := range []struct{ name, raw, wantError string }{
		{"invalid JSON", "invalid-json", "invalid character"},
		{"invalid interval", `{"probe_interval_seconds":59,"retry_interval_seconds":60,"relogin_cooldown_seconds":1800,"fail_streak_threshold":2}`, "probe_interval_seconds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reauth, reader, reauthRepo, _, _, _ := newReauthTestService("acct-1")
			next := time.Now().Add(-time.Minute)
			until := time.Now().Add(time.Minute)
			record := AccountTokenGuardV2Record{
				AccountID: 42, Enabled: true, AutoReloginEnabled: true, FailStreak: 2,
				NextProbeAt: next, LeaseOwner: "invalid-rules-claim", LeaseUntil: &until,
			}
			repo := &accountTokenGuardV2TestRepo{record: &record}
			settings := &accountTokenGuardV2TestSettings{raw: test.raw}
			svc := NewAccountTokenGuardV2Service(repo, settings, reader, accountTokenGuardV2TestProber{err: errors.New("invalid token")}, reauth)
			t.Cleanup(svc.cancel)

			require.ErrorContains(t, svc.runProbe(context.Background(), record), test.wantError)

			require.Zero(t, reauthRepo.createCalls)
			require.Nil(t, repo.completion)
			require.Equal(t, 2, repo.record.FailStreak)
			require.Equal(t, next, repo.record.NextProbeAt)
			require.Nil(t, repo.record.LastProbeAt)
			require.Empty(t, repo.record.LeaseOwner)
			require.Nil(t, repo.record.LeaseUntil)
		})
	}
}

func TestAccountTokenGuardV2CustomRulesDriveProbe(t *testing.T) {
	reauth, reader, reauthRepo, _, _, _ := newReauthTestService("acct-1")
	require.NoError(t, reauthRepo.UpsertConfig(context.Background(), reauthEmailConfig(42, "user@example.com", "encrypted:https://mail.example.com/code")))
	guardRepo := &accountTokenGuardV2TestRepo{record: &AccountTokenGuardV2Record{
		AccountID: 42, Enabled: true, AutoReloginEnabled: true, NextProbeAt: time.Now(),
	}}
	guardRepo.createTask = reauthRepo.CreateTask
	settings := &accountTokenGuardV2TestSettings{}
	svc := NewAccountTokenGuardV2Service(guardRepo, settings, reader, accountTokenGuardV2TestProber{err: errors.New("invalid token")}, reauth)
	_, err := svc.SaveRules(context.Background(), AccountTokenGuardV2Rules{
		ProbeIntervalSeconds: 600, RetryIntervalSeconds: 45,
		ReloginCooldownSeconds: 7200, FailStreakThreshold: 1,
	})
	require.NoError(t, err)
	record, err := guardRepo.GetAccount(context.Background(), 42)
	require.NoError(t, err)

	require.NoError(t, svc.runProbe(context.Background(), *record))

	require.Equal(t, 1, reauthRepo.createCalls)
	require.WithinDuration(t, guardRepo.completion.LastProbeAt.Add(45*time.Second), guardRepo.completion.NextProbeAt, time.Second)
	require.NotNil(t, guardRepo.completion.CooldownUntil)
	require.WithinDuration(t, guardRepo.completion.LastProbeAt.Add(2*time.Hour), *guardRepo.completion.CooldownUntil, time.Second)
}

func TestAccountTokenGuardV2RunDuePrunesBeforeClaim(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			repo := &accountTokenGuardV2TestRepo{
				record:      &AccountTokenGuardV2Record{AccountID: 42, Enabled: enabled, NextProbeAt: time.Now().Add(time.Hour)},
				pruneOrphan: true,
			}
			// No account reader/prober: orphaned records must never reach probing.
			svc := NewAccountTokenGuardV2Service(repo, nil, nil, nil, nil)
			defer svc.cancel()
			n, err := svc.RunDue(context.Background())
			require.NoError(t, err)
			require.Zero(t, n)
			require.Nil(t, repo.record)
			require.True(t, repo.claimed)
			n, err = svc.RunDue(context.Background())
			require.NoError(t, err)
			require.Zero(t, n, "cleanup must be idempotent")
		})
	}
}

func TestAccountTokenGuardV2RunDuePruneFailurePreservesRecords(t *testing.T) {
	repo := &accountTokenGuardV2TestRepo{
		record:   &AccountTokenGuardV2Record{AccountID: 42},
		pruneErr: errors.New("database unavailable"),
	}
	svc := NewAccountTokenGuardV2Service(repo, nil, nil, nil, nil)
	defer svc.cancel()
	n, err := svc.RunDue(context.Background())
	require.ErrorIs(t, err, repo.pruneErr)
	require.Zero(t, n)
	require.NotNil(t, repo.record)
	require.False(t, repo.claimed)
}
