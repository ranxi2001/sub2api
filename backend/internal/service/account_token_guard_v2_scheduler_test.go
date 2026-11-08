package service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type schedulerProbeClaim struct {
	id    int64
	owner string
}

// This queue models exclusive claims and rescheduling, including across workers.
// It deliberately has no budget interface: scheduler tests never create logins.
type schedulerProbeQueue struct {
	mu          sync.Mutex
	records     map[int64]AccountTokenGuardV2Record
	claims      []schedulerProbeClaim
	completed   []schedulerProbeClaim
	released    []schedulerProbeClaim
	cutoffs     []time.Time
	releaseErrs []error
}

func newSchedulerProbeQueue(count int, at time.Time) *schedulerProbeQueue {
	r := &schedulerProbeQueue{records: make(map[int64]AccountTokenGuardV2Record)}
	for id := 1; id <= count; id++ {
		r.records[int64(id)] = AccountTokenGuardV2Record{
			AccountID: int64(id), Enabled: true, AutoReloginEnabled: true,
			ProbeState: AccountTokenGuardV2ProbePending, NextProbeAt: at.Add(-time.Minute),
		}
	}
	return r
}

func (r *schedulerProbeQueue) UpsertAccount(context.Context, int64, bool, bool) error {
	return errors.New("unexpected scheduler test upsert")
}
func (r *schedulerProbeQueue) DeleteAccount(context.Context, int64) error {
	return errors.New("unexpected scheduler test delete")
}
func (r *schedulerProbeQueue) PruneDeletedAccounts(context.Context) (int64, error) {
	return 0, nil
}
func (r *schedulerProbeQueue) GetAccount(_ context.Context, id int64) (*AccountTokenGuardV2Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok {
		return nil, nil
	}
	return &record, nil
}
func (r *schedulerProbeQueue) ListAccounts(context.Context) ([]AccountTokenGuardV2Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := make([]AccountTokenGuardV2Record, 0, len(r.records))
	for _, record := range r.records {
		records = append(records, record)
	}
	return records, nil
}
func (r *schedulerProbeQueue) ClaimDue(ctx context.Context, owner string, duration time.Duration, limit int, dueBefore time.Time) ([]AccountTokenGuardV2Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.cutoffs = append(r.cutoffs, dueBefore)
	var due []AccountTokenGuardV2Record
	for _, record := range r.records {
		if record.Enabled && record.LeaseOwner == "" && !record.NextProbeAt.After(dueBefore) {
			due = append(due, record)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].NextProbeAt.Equal(due[j].NextProbeAt) {
			return due[i].AccountID < due[j].AccountID
		}
		return due[i].NextProbeAt.Before(due[j].NextProbeAt)
	})
	if len(due) > limit {
		due = due[:limit]
	}
	for i := range due {
		due[i] = r.claimLocked(due[i], owner, duration)
	}
	return due, nil
}
func (r *schedulerProbeQueue) claimLocked(record AccountTokenGuardV2Record, owner string, duration time.Duration) AccountTokenGuardV2Record {
	until := time.Now().Add(duration)
	record.LeaseOwner, record.LeaseUntil = owner, &until
	r.records[record.AccountID] = record
	r.claims = append(r.claims, schedulerProbeClaim{record.AccountID, owner})
	return record
}
func (r *schedulerProbeQueue) ClaimAccount(ctx context.Context, id int64, owner string, duration time.Duration) (*AccountTokenGuardV2Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, ok := r.records[id]
	if !ok || record.LeaseOwner != "" {
		return nil, nil
	}
	record = r.claimLocked(record, owner, duration)
	return &record, nil
}
func (r *schedulerProbeQueue) CompleteProbe(_ context.Context, id int64, owner string, result AccountTokenGuardV2ProbeCompletion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.records[id]
	if owner == "" || record.LeaseOwner != owner {
		return errors.New("scheduler test completion lost its claim")
	}
	r.completed = append(r.completed, schedulerProbeClaim{id, owner})
	record.ProbeState, record.ProbeDetail = result.ProbeState, result.ProbeDetail
	record.FailStreak, record.LastProbeAt = result.FailStreak, &result.LastProbeAt
	record.NextProbeAt = result.NextProbeAt
	record.LeaseOwner, record.LeaseUntil = "", nil
	r.records[id] = record
	return nil
}
func (r *schedulerProbeQueue) ReleaseProbe(ctx context.Context, id int64, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, schedulerProbeClaim{id, owner})
	r.releaseErrs = append(r.releaseErrs, ctx.Err())
	if err := ctx.Err(); err != nil {
		return err
	}
	record := r.records[id]
	if record.LeaseOwner == owner {
		record.LeaseOwner, record.LeaseUntil = "", nil
		r.records[id] = record
	}
	return nil
}
func (r *schedulerProbeQueue) MarkReauthRequested(context.Context, int64, time.Time, time.Time) error {
	return errors.New("unexpected scheduler test re-login")
}
func (r *schedulerProbeQueue) RescheduleEnabled(context.Context) error { return nil }

type schedulerProbeReader struct{}

func (schedulerProbeReader) GetAccount(_ context.Context, id int64) (*Account, error) {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "synthetic-scheduler-token"}}, nil
}
func (schedulerProbeReader) GetProxy(context.Context, int64) (*Proxy, error) { return nil, nil }

type schedulerProbeFunc func(context.Context, *Account) (*OpenAIModelsResponse, error)

func (f schedulerProbeFunc) FetchOpenAIModelsListFresh(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
	return f(ctx, account)
}

type schedulerCycleResult struct {
	count int
	err   error
}

func schedulerStartCycle(svc *AccountTokenGuardV2Service, ctx context.Context) <-chan schedulerCycleResult {
	done := make(chan schedulerCycleResult, 1)
	go func() {
		count, err := svc.RunDue(ctx)
		done <- schedulerCycleResult{count, err}
	}()
	return done
}

func schedulerAwaitCycle(t *testing.T, done <-chan schedulerCycleResult) schedulerCycleResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not finish its bounded work")
		return schedulerCycleResult{}
	}
}

func schedulerAwaitStarted(t *testing.T, started <-chan int64, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("scheduler did not fill its available probe slots")
		}
	}
}

func TestAccountTokenGuardV2SchedulerDrainsThirtyDueAccounts(t *testing.T) {
	at := time.Now()
	repo := newSchedulerProbeQueue(30, at)
	svc := NewAccountTokenGuardV2Service(repo, nil, schedulerProbeReader{}, accountTokenGuardV2TestProber{}, nil)
	t.Cleanup(svc.cancel)
	svc.now = func() time.Time { return at }
	result := schedulerAwaitCycle(t, schedulerStartCycle(svc, context.Background()))
	require.NoError(t, result.err)
	require.Equal(t, 30, result.count)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.completed, 30, "one cycle must drain beyond the old batch of ten")
	owners := make(map[string]bool)
	for _, claim := range repo.claims {
		require.NotEmpty(t, claim.owner)
		require.False(t, owners[claim.owner], "each acquisition needs a fresh lease token")
		owners[claim.owner] = true
	}
	for _, completion := range repo.completed {
		require.True(t, owners[completion.owner], "completion must use the acquired token")
	}
	for _, record := range repo.records {
		require.Equal(t, AccountTokenGuardV2ProbeOK, record.ProbeState)
		require.Empty(t, record.LeaseOwner)
	}
}

func TestAccountTokenGuardV2SchedulerKeepsCycleCutoffWhenClockAdvances(t *testing.T) {
	at := time.Now()
	repo := newSchedulerProbeQueue(4, at)
	future := repo.records[4]
	future.NextProbeAt = at.Add(30 * time.Second)
	repo.records[4] = future
	var clock atomic.Int64
	clock.Store(at.UnixNano())
	prober := schedulerProbeFunc(func(context.Context, *Account) (*OpenAIModelsResponse, error) {
		clock.Store(at.Add(2 * time.Minute).UnixNano())
		return nil, nil
	})
	svc := NewAccountTokenGuardV2Service(repo, nil, schedulerProbeReader{}, prober, nil)
	t.Cleanup(svc.cancel)
	svc.now = func() time.Time { return time.Unix(0, clock.Load()) }
	result := schedulerAwaitCycle(t, schedulerStartCycle(svc, context.Background()))
	require.NoError(t, result.err)
	require.Equal(t, 3, result.count)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Nil(t, repo.records[4].LastProbeAt, "future work belongs to the next cycle")
	for _, cutoff := range repo.cutoffs {
		require.True(t, cutoff.Equal(at), "all claims in a cycle share its starting cutoff")
	}
}

func TestAccountTokenGuardV2SchedulerRefillsSlotsWhileOneProbeIsSlow(t *testing.T) {
	const total = 20
	repo := newSchedulerProbeQueue(total, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan int64, total)
	fastCompleted := make(chan struct{}, total)
	slowGate, firstWaveGate := make(chan struct{}), make(chan struct{})
	var active, peak atomic.Int32
	prober := schedulerProbeFunc(func(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- account.ID
		gate := firstWaveGate
		if account.ID == 1 {
			gate = slowGate
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
		}
		if account.ID != 1 {
			fastCompleted <- struct{}{}
		}
		return nil, nil
	})
	svc := NewAccountTokenGuardV2Service(repo, nil, schedulerProbeReader{}, prober, nil)
	t.Cleanup(svc.cancel)
	done := schedulerStartCycle(svc, ctx)
	schedulerAwaitStarted(t, started, 3)
	close(firstWaveGate)
	for i := 0; i < total-1; i++ {
		select {
		case <-fastCompleted:
		case <-time.After(3 * time.Second):
			t.Fatal("a slow probe prevented available slots from draining later accounts")
		}
	}
	require.EqualValues(t, 3, peak.Load(), "probe concurrency must stay bounded at three")
	close(slowGate)
	result := schedulerAwaitCycle(t, done)
	require.NoError(t, result.err)
	require.Equal(t, total, result.count)
}

func TestAccountTokenGuardV2SchedulerCancellationReleasesOnlyRunningClaims(t *testing.T) {
	repo := newSchedulerProbeQueue(30, time.Now())
	for id, record := range repo.records {
		record.FailStreak = 2
		repo.records[id] = record
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan int64, 30)
	prober := schedulerProbeFunc(func(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
		started <- account.ID
		<-ctx.Done()
		// Cancellation must win even when a transport reports an auth-shaped error.
		return nil, errors.New("invalid token")
	})
	svc := NewAccountTokenGuardV2Service(repo, nil, schedulerProbeReader{}, prober, nil)
	t.Cleanup(svc.cancel)
	done := schedulerStartCycle(svc, ctx)
	schedulerAwaitStarted(t, started, 3)
	repo.mu.Lock()
	claimed := len(repo.claims)
	repo.mu.Unlock()
	require.Equal(t, 3, claimed, "queued accounts must not be leased before a slot is free")
	cancel()
	result := schedulerAwaitCycle(t, done)
	require.ErrorIs(t, result.err, context.Canceled)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.claims, 3)
	require.Len(t, repo.released, 3)
	for _, err := range repo.releaseErrs {
		require.NoError(t, err, "lease cleanup must survive caller cancellation")
	}
	for _, record := range repo.records {
		require.Empty(t, record.LeaseOwner)
		require.Equal(t, 2, record.FailStreak, "a cancelled request is not another auth failure")
		require.NotEqual(t, AccountTokenGuardV2ProbeAuth, record.ProbeState)
	}
}

func TestAccountTokenGuardV2SchedulerOverlappingCyclesDoNotMultiplyConcurrency(t *testing.T) {
	repo := newSchedulerProbeQueue(12, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan int64, 12)
	gate := make(chan struct{})
	prober := schedulerProbeFunc(func(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
		started <- account.ID
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
			return nil, nil
		}
	})
	svc := NewAccountTokenGuardV2Service(repo, nil, schedulerProbeReader{}, prober, nil)
	t.Cleanup(svc.cancel)
	first := schedulerStartCycle(svc, ctx)
	schedulerAwaitStarted(t, started, 3)
	second := schedulerAwaitCycle(t, schedulerStartCycle(svc, ctx))
	require.Zero(t, second.count, "an overlapping cycle must not start another worker pool")
	repo.mu.Lock()
	claimed := len(repo.claims)
	repo.mu.Unlock()
	require.Equal(t, 3, claimed)
	close(gate)
	result := schedulerAwaitCycle(t, first)
	require.NoError(t, result.err)
	require.Equal(t, 12, result.count)
}

func TestAccountTokenGuardV2ManualProbeUsesFreshClaimToken(t *testing.T) {
	reauth, reader, _, _, _, _ := newReauthTestService("acct-1")
	repo := newSchedulerProbeQueue(0, time.Now())
	repo.records[42] = AccountTokenGuardV2Record{AccountID: 42, Enabled: true}
	svc := NewAccountTokenGuardV2Service(repo, nil, reader, accountTokenGuardV2TestProber{}, reauth)
	t.Cleanup(svc.cancel)
	for attempt := 0; attempt < 2; attempt++ {
		view, err := svc.ProbeNow(context.Background(), 42)
		require.NoError(t, err)
		require.Equal(t, AccountTokenGuardV2ProbeOK, view.ProbeState)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.claims, 2)
	require.Len(t, repo.completed, 2)
	require.NotEqual(t, repo.claims[0].owner, repo.claims[1].owner)
	require.Equal(t, repo.claims, repo.completed, "manual completions must retain their claim token")
}

type schedulerBudgetQueue struct {
	*schedulerProbeQueue
	budgetOwners      []string
	createOwners      []string
	cancelAtReconcile context.CancelFunc
	cancelAtCreate    context.CancelFunc
}

func (r *schedulerBudgetQueue) ReconcileReloginBudget(_ context.Context, id int64, owner string, _ AccountTokenGuardV2Rules, _ time.Time, _ string) (*AccountTokenGuardV2Record, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.records[id]
	if owner == "" || owner != record.LeaseOwner {
		return nil, false, errors.New("budget reconciliation used a stale lease token")
	}
	r.budgetOwners = append(r.budgetOwners, owner)
	if r.cancelAtReconcile != nil {
		r.cancelAtReconcile()
	}
	return &record, false, nil
}
func (r *schedulerBudgetQueue) CreateAutomaticRelogin(_ context.Context, id int64, _ string, owner string, _ AccountTokenGuardV2Rules, _ time.Time) (*OpenAIOAuthReauthTaskRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner == "" || owner != r.records[id].LeaseOwner {
		return nil, errors.New("automatic re-login used a stale lease token")
	}
	r.createOwners = append(r.createOwners, owner)
	if r.cancelAtCreate != nil {
		r.cancelAtCreate()
	}
	return &OpenAIOAuthReauthTaskRecord{ID: 1, AccountID: id, Status: OpenAIOAuthReauthStatusQueued}, nil
}
func (*schedulerBudgetQueue) ResumeReloginBudget(context.Context, int64, AccountTokenGuardV2Rules, time.Time) error {
	return errors.New("unexpected scheduler test budget recovery")
}

func TestAccountTokenGuardV2AutomaticReloginPreservesProbeClaimToken(t *testing.T) {
	reauth, reader, reauthRepo, _, _, _ := newReauthTestService("acct-1")
	require.NoError(t, reauthRepo.UpsertConfig(context.Background(), reauthEmailConfig(42, "user@example.test", "encrypted:https://mail.example.test/code")))
	repo := &schedulerBudgetQueue{schedulerProbeQueue: newSchedulerProbeQueue(0, time.Now())}
	repo.records[42] = AccountTokenGuardV2Record{AccountID: 42, Enabled: true, AutoReloginEnabled: true, FailStreak: 1}
	svc := NewAccountTokenGuardV2Service(repo, nil, reader, accountTokenGuardV2TestProber{err: errors.New("invalid token")}, reauth)
	t.Cleanup(svc.cancel)
	_, err := svc.ProbeNow(context.Background(), 42)
	require.NoError(t, err)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.claims, 1)
	owner := repo.claims[0].owner
	require.Equal(t, []string{owner}, repo.budgetOwners)
	require.Equal(t, []string{owner}, repo.createOwners)
	require.Equal(t, []schedulerProbeClaim{{42, owner}}, repo.completed)
}

func TestAccountTokenGuardV2CancellationAfterProbePreservesInspectionHistory(t *testing.T) {
	for _, stage := range []string{"budget reconciliation", "task creation"} {
		t.Run(stage, func(t *testing.T) {
			reauth, reader, reauthRepo, _, _, _ := newReauthTestService("acct-1")
			require.NoError(t, reauthRepo.UpsertConfig(context.Background(), reauthEmailConfig(42, "user@example.test", "encrypted:https://mail.example.test/code")))
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			repo := &schedulerBudgetQueue{schedulerProbeQueue: newSchedulerProbeQueue(0, time.Now())}
			original := AccountTokenGuardV2Record{
				AccountID: 42, Enabled: true, AutoReloginEnabled: true, FailStreak: 1,
				ProbeState: AccountTokenGuardV2ProbePending, ProbeDetail: "previous inspection",
				BlockedReason: "previous automation state", NextProbeAt: time.Now().Add(-time.Minute),
			}
			repo.records[42] = original
			if stage == "budget reconciliation" {
				repo.cancelAtReconcile = cancel
			} else {
				repo.cancelAtCreate = cancel
			}
			svc := NewAccountTokenGuardV2Service(repo, nil, reader, accountTokenGuardV2TestProber{err: errors.New("invalid token")}, reauth)
			t.Cleanup(svc.cancel)
			view, err := svc.ProbeNow(ctx, 42)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, view)
			repo.mu.Lock()
			defer repo.mu.Unlock()
			require.Empty(t, repo.completed, "cancellation must not publish another inspection outcome")
			require.Len(t, repo.released, 1)
			require.NoError(t, repo.releaseErrs[0], "cleanup must use a context independent of the cancelled operation")
			require.Equal(t, original, repo.records[42], "leave the prior failure count, due time and diagnostics unchanged")
			require.Len(t, repo.budgetOwners, 1)
			if stage == "budget reconciliation" {
				require.Empty(t, repo.createOwners, "a cancelled reconciliation must not start a login")
			} else {
				require.Len(t, repo.createOwners, 1, "an already-created task keeps its own durable lifecycle")
			}
		})
	}
}

func TestSafeTokenGuardReloginReasonDoesNotExposeProviderDetails(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"allowlisted reason", infraerrors.ServiceUnavailable("OPENAI_REAUTH_WORKER_UNAVAILABLE", "synthetic-private-provider-response"), "OPENAI_REAUTH_WORKER_UNAVAILABLE"},
		{"unlisted application reason", infraerrors.ServiceUnavailable("OPENAI_REAUTH_MODE_UNAVAILABLE", "synthetic-private-provider-response"), "unknown"},
		{"arbitrary reason", infraerrors.ServiceUnavailable("synthetic-private-reason", "synthetic-private-provider-response"), "unknown"},
		{"plain provider error", errors.New("synthetic-private-provider-response"), "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reason := safeTokenGuardReloginReason(test.err)
			require.Equal(t, test.want, reason)
			require.NotContains(t, reason, "synthetic-private")
		})
	}
}
