package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This test uses the existing public service entrypoint and compiles unchanged
// at e395. Its SQL-reader barrier proves the resource boundary, not a timeout.
type analysisBarrierHistory struct {
	UsageLogRepository
	entered chan context.Context
	release chan struct{}
	calls   atomic.Int32
	active  atomic.Int32
	peak    atomic.Int32
	fail    atomic.Bool
}

func (r *analysisBarrierHistory) ReadPrioritySchedulingSignals(ctx context.Context, q PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	r.calls.Add(1)
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for peak := r.peak.Load(); n > peak && !r.peak.CompareAndSwap(peak, n); peak = r.peak.Load() {
	}
	r.entered <- ctx
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
	}
	if r.fail.Load() {
		return nil, fmt.Errorf("fixture history failure")
	}
	rows := make(map[int64]PrioritySchedulingSignal)
	for _, id := range q.AccountIDs {
		rows[id] = PrioritySchedulingSignal{Samples: 1, P90TTFTMs: 100}
	}
	return rows, nil
}
func (r *analysisBarrierHistory) ReadPriorityLatencySeries(ctx context.Context, q PriorityLatencySeriesQuery) ([]PriorityLatencyBucket, error) {
	return []PriorityLatencyBucket{{AccountID: q.AccountIDs[0], Samples: 1, P90TTFTMs: 100}}, ctx.Err()
}
func newAnalysisBarrierService() (*OpenAIGatewayService, *analysisBarrierHistory) {
	history := &analysisBarrierHistory{entered: make(chan context.Context, 64), release: make(chan struct{})}
	settings := &SettingService{settingRepo: &prioritySettingStub{}}
	settings.prioritySchedulingConfig.config = DefaultPrioritySchedulingConfig()
	settings.prioritySchedulingConfig.expires = time.Now().Add(time.Hour)
	return &OpenAIGatewayService{settingService: settings, accountRepo: &analysisAccounts{accounts: []Account{{ID: 1, Name: "fixture", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}}}, usageLogRepo: history}, history
}
func awaitAnalysisEntry(t *testing.T, history *analysisBarrierHistory) context.Context {
	t.Helper()
	select {
	case ctx := <-history.entered:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatal("analysis did not reach SQL reader barrier")
		return nil
	}
}
func TestPriorityAnalysisConcurrentScopesHaveBoundedSQLReaders(t *testing.T) {
	svc, history := newAnalysisBarrierService()
	defer close(history.release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, err := svc.PriorityAnalysis(ctx, PriorityAnalysisQuery{Model: fmt.Sprint(i), Minutes: 60})
			done <- err
		}(i)
		awaitAnalysisEntry(t, history)
	}
	for i := 2; i < 12; i++ {
		request, stop := context.WithTimeout(ctx, 30*time.Millisecond)
		_, err := svc.PriorityAnalysis(request, PriorityAnalysisQuery{Model: fmt.Sprint(i), Minutes: 15})
		stop()
		require.Error(t, err, "excess diagnostic scopes must be rejected")
	}
	require.EqualValues(t, 2, history.calls.Load(), "diagnostic SQL readers must never exceed two active scopes")
	require.EqualValues(t, 2, history.peak.Load())
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled reader leaked")
		}
	}
	require.Eventually(t, func() bool { return history.active.Load() == 0 }, time.Second, time.Millisecond)
}
