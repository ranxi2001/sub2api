package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type analysisReadResult struct {
	value *PriorityAnalysis
	err   error
}

func startAnalysis(svc *OpenAIGatewayService, ctx context.Context, q PriorityAnalysisQuery) <-chan analysisReadResult {
	done := make(chan analysisReadResult, 1)
	go func() { value, err := svc.PriorityAnalysis(ctx, q); done <- analysisReadResult{value, err} }()
	return done
}
func finishAnalysis(t *testing.T, done <-chan analysisReadResult) analysisReadResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(15 * time.Second):
		t.Fatal("analysis request leaked")
		return analysisReadResult{}
	}
}
func analysisWaiters(t *testing.T, svc *OpenAIGatewayService, count int) {
	t.Helper()
	require.Eventually(t, func() bool {
		svc.priorityAnalysis.mu.Lock()
		defer svc.priorityAnalysis.mu.Unlock()
		n := 0
		for _, flight := range svc.priorityAnalysis.flights {
			n += flight.waiters
		}
		return n == count
	}, 5*time.Second, time.Millisecond, "waiters must reach the actual flight before releasing SQL")
}
func analysisDrained(t *testing.T, svc *OpenAIGatewayService) {
	t.Helper()
	require.Eventually(t, func() bool {
		svc.priorityAnalysis.mu.Lock()
		defer svc.priorityAnalysis.mu.Unlock()
		return svc.priorityAnalysis.active == 0 && len(svc.priorityAnalysis.flights) == 0
	}, time.Second, time.Millisecond, "completed analysis must release flights and slots")
}
func TestPriorityAnalysisCoalescesAndOwnsMutableResponses(t *testing.T) {
	svc, history := newAnalysisBarrierService()
	ctx := WithPriorityAnalysisPrincipal(context.Background(), "admin:1:jwt")
	q := PriorityAnalysisQuery{Model: "fixture", Minutes: 60}
	done := []<-chan analysisReadResult{startAnalysis(svc, ctx, q)}
	awaitAnalysisEntry(t, history)
	for i := 0; i < 8; i++ {
		done = append(done, startAnalysis(svc, ctx, q))
	}
	analysisWaiters(t, svc, 9)
	require.EqualValues(t, 1, history.calls.Load(), "same authorized query must share the in-flight SQL")
	close(history.release)
	first := finishAnalysis(t, done[0])
	require.NoError(t, first.err)
	first.value.Snapshot.Candidates[0].AccountName = "mutated"
	*first.value.Snapshot.Candidates[0].Performance.AvgTTFTMs = 999
	for _, pending := range done[1:] {
		other := finishAnalysis(t, pending)
		require.NoError(t, other.err)
		require.Equal(t, "fixture", other.value.Snapshot.Candidates[0].AccountName)
		require.Equal(t, 100.0, *other.value.Snapshot.Candidates[0].Performance.AvgTTFTMs)
	}
	analysisDrained(t, svc)
	_, err := svc.PriorityAnalysis(ctx, q)
	require.NoError(t, err)
	require.EqualValues(t, 2, history.calls.Load(), "completed results must not be cached")
}
func TestPriorityAnalysisFirstWaiterCancellationKeepsOtherReadersAlive(t *testing.T) {
	svc, history := newAnalysisBarrierService()
	defer close(history.release)
	ctx := WithPriorityAnalysisPrincipal(context.Background(), "admin:1:jwt")
	first, cancel := context.WithCancel(ctx)
	q := PriorityAnalysisQuery{Model: "fixture", Minutes: 60}
	a := startAnalysis(svc, first, q)
	work := awaitAnalysisEntry(t, history)
	b := startAnalysis(svc, ctx, q)
	analysisWaiters(t, svc, 2)
	cancel()
	require.ErrorIs(t, finishAnalysis(t, a).err, context.Canceled)
	require.NoError(t, work.Err(), "first HTTP cancellation must not cancel another live waiter")
	// Release only after the cancellation branch has actually completed.
	history.release <- struct{}{}
	require.NoError(t, finishAnalysis(t, b).err)
	analysisDrained(t, svc)
}
func TestPriorityAnalysisLastCancellationHoldsSlotUntilRepositoryExit(t *testing.T) {
	var budget priorityAnalysisFlights
	release := make(chan struct{})
	entered := make(chan context.Context, 2)
	run := func(ctx context.Context) (*PriorityAnalysis, error) {
		entered <- ctx
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	}
	requests := make([]context.CancelFunc, 0, 2)
	pending := make([]chan error, 0, 2)
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		requests = append(requests, cancel)
		done := make(chan error, 1)
		pending = append(pending, done)
		go func(i int) { _, err := budget.read(ctx, priorityAnalysisFlightKey{minutes: i}, run); done <- err }(i)
		<-entered
	}
	for _, cancel := range requests {
		cancel()
	}
	for _, done := range pending {
		require.ErrorIs(t, <-done, context.Canceled)
	}
	_, err := budget.read(context.Background(), priorityAnalysisFlightKey{minutes: 3}, run)
	require.ErrorIs(t, err, ErrPriorityAnalysisBusy, "cancelled SQL still holds its hard concurrency slot until exit")
	close(release)
	require.Eventually(t, func() bool {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		return budget.active == 0 && len(budget.flights) == 0
	}, time.Second, time.Millisecond)
}
func TestPriorityAnalysisDifferentPrincipalsAndScopesNeverMerge(t *testing.T) {
	for _, change := range []string{"principal", "role", "permission", "model", "group", "window", "config", "route"} {
		t.Run(change, func(t *testing.T) {
			svc, history := newAnalysisBarrierService()
			ctx := WithPriorityAnalysisPrincipal(context.Background(), "admin:1:jwt:policy1")
			q := PriorityAnalysisQuery{Model: "fixture", Minutes: 60}
			a := startAnalysis(svc, ctx, q)
			awaitAnalysisEntry(t, history)
			switch change {
			case "principal":
				ctx = WithPriorityAnalysisPrincipal(ctx, "admin:2:jwt:policy1")
			case "role":
				ctx = WithPriorityAnalysisPrincipal(ctx, "observer:1:jwt:policy1")
			case "permission":
				ctx = WithPriorityAnalysisPrincipal(ctx, "admin:1:jwt:policy2")
			case "model":
				q.Model = "other"
			case "group":
				id := int64(17)
				q.GroupID = &id
			case "window":
				q.Minutes = 15
			case "route":
				q.RouteModel = "other-route"
			case "config":
				cfg := DefaultPrioritySchedulingConfig()
				cfg.TargetTTFTMs++
				require.NoError(t, svc.settingService.SavePrioritySchedulingConfig(ctx, cfg))
			}
			b := startAnalysis(svc, ctx, q)
			awaitAnalysisEntry(t, history)
			require.EqualValues(t, 2, history.calls.Load(), "different authorization or query semantics must never merge")
			close(history.release)
			require.NoError(t, finishAnalysis(t, a).err)
			require.NoError(t, finishAnalysis(t, b).err)
			analysisDrained(t, svc)
		})
	}
}
func TestPriorityAnalysisErrorsAndShutdownReleaseLifecycle(t *testing.T) {
	svc, history := newAnalysisBarrierService()
	ctx := WithPriorityAnalysisPrincipal(context.Background(), "admin:1")
	q := PriorityAnalysisQuery{Model: "fixture", Minutes: 60}
	history.fail.Store(true)
	a := startAnalysis(svc, ctx, q)
	awaitAnalysisEntry(t, history)
	close(history.release)
	require.ErrorContains(t, finishAnalysis(t, a).err, "fixture history failure")
	analysisDrained(t, svc)
	history.fail.Store(false)
	_, err := svc.PriorityAnalysis(ctx, q)
	require.NoError(t, err)
	analysisDrained(t, svc)
	svc.CloseOpenAIWSPool()
	_, err = svc.PriorityAnalysis(ctx, q)
	require.Error(t, err)

	svc, history = newAnalysisBarrierService()
	a = startAnalysis(svc, ctx, q)
	work := awaitAnalysisEntry(t, history)
	svc.CloseOpenAIWSPool()
	require.Error(t, finishAnalysis(t, a).err)
	require.ErrorIs(t, work.Err(), context.Canceled)
	analysisDrained(t, svc)
}
func TestPriorityAnalysisBudgetTimeoutReleasesLifecycle(t *testing.T) {
	svc, history := newAnalysisBarrierService()
	ctx := WithPriorityAnalysisPrincipal(context.Background(), "admin:1")
	a := startAnalysis(svc, ctx, PriorityAnalysisQuery{Model: "fixture", Minutes: 60})
	work := awaitAnalysisEntry(t, history)
	deadline, ok := work.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(10*time.Second), deadline, time.Second, "analysis budget must remain ten seconds")
	result := finishAnalysis(t, a)
	require.True(t, errors.Is(result.err, context.DeadlineExceeded), result.err)
	analysisDrained(t, svc)
	close(history.release)
	_, err := svc.PriorityAnalysis(ctx, PriorityAnalysisQuery{Model: "fixture", Minutes: 60})
	require.NoError(t, err, "timeout must permit fresh work")
}

// Configuration/channel preparation is bounded separately from heavy readers.
type analysisPreparationSettings struct {
	SettingRepository
	entered chan context.Context
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (r *analysisPreparationSettings) GetValue(ctx context.Context, _ string) (string, error) {
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for peak := r.peak.Load(); n > peak && !r.peak.CompareAndSwap(peak, n); peak = r.peak.Load() {
	}
	r.entered <- ctx
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.release:
		return "", ErrSettingNotFound
	}
}
func TestPriorityAnalysisPreparationIsBoundedAndCancelled(t *testing.T) {
	svc, history := newAnalysisBarrierService()
	prep := &analysisPreparationSettings{entered: make(chan context.Context, 16), release: make(chan struct{})}
	svc.settingService = &SettingService{settingRepo: prep}
	ctx := WithPriorityAnalysisPrincipal(context.Background(), "admin:1")
	q := PriorityAnalysisQuery{Model: "fixture", Minutes: 60}
	done := make([]<-chan analysisReadResult, 0, 9)
	for i := 0; i < 9; i++ {
		done = append(done, startAnalysis(svc, ctx, q))
	}
	for i := 0; i < 2; i++ {
		select {
		case <-prep.entered:
		case <-time.After(time.Second):
			t.Fatal("preparation did not enter")
		}
	}
	require.EqualValues(t, 2, prep.active.Load())
	close(prep.release)
	awaitAnalysisEntry(t, history)
	analysisWaiters(t, svc, 9)
	require.LessOrEqual(t, prep.peak.Load(), int32(2), "configuration/channel SQL must be bounded too")
	close(history.release)
	for _, pending := range done {
		require.NoError(t, finishAnalysis(t, pending).err)
	}
	analysisDrained(t, svc)

	svc, _ = newAnalysisBarrierService()
	prep = &analysisPreparationSettings{entered: make(chan context.Context, 16), release: make(chan struct{})}
	svc.settingService = &SettingService{settingRepo: prep}
	done = done[:0]
	for i := 0; i < 3; i++ {
		done = append(done, startAnalysis(svc, ctx, q))
	}
	for i := 0; i < 2; i++ {
		<-prep.entered
	}
	svc.CloseOpenAIWSPool()
	for _, pending := range done {
		require.Error(t, finishAnalysis(t, pending).err)
	}
	require.Eventually(t, func() bool { return prep.active.Load() == 0 }, time.Second, time.Millisecond)
}
func TestPriorityAnalysisCancelledWorkCannotPublishSuccess(t *testing.T) {
	var budget priorityAnalysisFlights
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := budget.read(context.Background(), priorityAnalysisFlightKey{}, func(ctx context.Context) (*PriorityAnalysis, error) {
			close(entered)
			<-ctx.Done()
			return &PriorityAnalysis{}, nil
		})
		done <- err
	}()
	<-entered
	budget.close()
	require.ErrorIs(t, <-done, context.Canceled, "cancelled work cannot publish a late successful result")
}

type analysisCatalogAccounts struct {
	AccountRepository
	entered chan context.Context
	active  atomic.Int32
	peak    atomic.Int32
}

func (r *analysisCatalogAccounts) ListModelAvailabilityCandidates(ctx context.Context, _ *int64, _ []string, _ bool) ([]Account, error) {
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for peak := r.peak.Load(); n > peak && !r.peak.CompareAndSwap(peak, n); peak = r.peak.Load() {
	}
	r.entered <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestPriorityAnalysisCatalogSharesPreparationBudgetAndCancels(t *testing.T) {
	svc, _ := newAnalysisBarrierService()
	accounts := &analysisCatalogAccounts{entered: make(chan context.Context, 5)}
	svc.accountRepo = accounts
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() { _, err := svc.PriorityAnalysisModels(ctx, nil); done <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-accounts.entered:
		case <-time.After(time.Second):
			t.Fatal("catalog did not enter")
		}
	}
	require.EqualValues(t, 2, accounts.active.Load())
	cancel()
	for i := 0; i < 5; i++ {
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("catalog cancellation leaked")
		}
	}
	require.LessOrEqual(t, accounts.peak.Load(), int32(2))
	require.Zero(t, accounts.active.Load())
}
