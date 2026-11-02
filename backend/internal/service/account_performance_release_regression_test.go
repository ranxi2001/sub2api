package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type performanceEvaluationRepo struct {
	AccountPerformanceRepository
	ids      []int64
	attempts []int64
	cancel   context.CancelFunc
}

func (r *performanceEvaluationRepo) EnabledIDs(context.Context) ([]int64, error) {
	return r.ids, nil
}

func (r *performanceEvaluationRepo) Evaluate(ctx context.Context, id int64, _ time.Time) error {
	r.attempts = append(r.attempts, id)
	r.cancel() // Deterministically exhaust this round, without a wall-clock wait.
	return ctx.Err()
}

func TestAccountPerformanceEvaluationResumesAfterCanceledAttempt(t *testing.T) {
	r := &performanceEvaluationRepo{ids: []int64{1, 2, 3}}
	s := NewAccountPerformanceService(r)
	run := func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r.cancel = cancel
		s.evaluate(ctx)
	}
	for range 4 {
		run()
	}
	require.Equal(t, []int64{1, 2, 3, 1}, r.attempts, "a failing head must not starve the tail")
	r.ids = []int64{3, 4} // The previous cursor's account can disappear.
	run()
	run()
	run()
	require.Equal(t, []int64{1, 2, 3, 1, 3, 4, 3}, r.attempts)
	r.ids = nil
	run()
	require.Len(t, r.attempts, 7)
}

type performanceExportRepo struct {
	performanceAccountRepoStub
	accounts []Account
}

func (r *performanceExportRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	return r.accounts, nil
}

func TestAccountPerformanceRegionalExportChecksPauseAndFailsClosed(t *testing.T) {
	r := &performanceExportRepo{
		performanceAccountRepoStub: performanceAccountRepoStub{paused: map[int64]bool{1: true}},
		accounts:                   []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}, {ID: 2, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}},
	}
	s := NewSchedulerSnapshotService(nil, nil, r, nil, nil)
	buckets := []SchedulerBucket{{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}}
	payload, err := s.ExportRegionalScheduler(context.Background(), buckets, []int64{1, 2})
	require.NoError(t, err)
	doc, err := decodeRegionalScheduler(payload)
	require.NoError(t, err)
	require.Len(t, doc.Buckets[0].Accounts, 1)
	require.Equal(t, int64(2), doc.Buckets[0].Accounts[0].ID)
	require.Equal(t, 1, r.calls)
	r.err = errors.New("pause authority unavailable")
	payload, err = s.ExportRegionalScheduler(context.Background(), buckets, []int64{1, 2})
	require.Error(t, err)
	require.Nil(t, payload, "failed authority must never refresh regional permission")
}

func TestAccountPerformanceSnapshotPauseCannotBeClearedByEmptyAuthority(t *testing.T) {
	r := &performanceAccountRepoStub{paused: map[int64]bool{}}
	s := NewSchedulerSnapshotService(nil, nil, r, nil, nil)
	a := &Account{ID: 1, Status: StatusActive, Schedulable: true, PerformancePaused: true}
	got, err := s.markPerformanceAccounts(context.Background(), map[int64]*Account{1: a})
	require.NoError(t, err)
	require.True(t, got[1].PerformancePaused)
	filtered, err := s.filterPerformanceAccounts(context.Background(), []Account{*a})
	require.NoError(t, err)
	require.Empty(t, filtered)
}

func TestAccountPerformanceRegionalSlotUsesLocalAuthority(t *testing.T) {
	r := &performanceAccountRepoStub{err: errors.New("primary must not be queried")}
	a := &Account{ID: 1, Status: StatusActive, Schedulable: true}
	snapshot := NewSchedulerSnapshotService(nil, nil, r, nil, snapshotReaderConfig())
	snapshot.SetLocalSnapshotReader(performanceLocalReader{account: a})
	s := ProvideConcurrencyService(nil, r, snapshotReaderConfig(), snapshot)
	defer s.StopAPIKeyQueue()
	got, err := s.AcquireAccountSlot(context.Background(), 1, 0)
	require.NoError(t, err)
	require.True(t, got.Acquired)
	got, err = s.AcquireAccountSlot(context.Background(), 2, 0)
	require.NoError(t, err)
	require.False(t, got.Acquired, "removed accounts cannot reuse old grants")
	a.PerformancePaused = true
	got, err = s.AcquireAccountSlot(context.Background(), 1, 0)
	require.NoError(t, err)
	require.False(t, got.Acquired)
	require.Zero(t, r.calls)
	s.performancePauses = localPerformancePauseReader{reader: NewLocalSchedulerSnapshotStore(nil)}
	got, err = s.AcquireAccountSlot(context.Background(), 1, 0)
	require.ErrorIs(t, err, ErrLocalSchedulerSnapshotUnavailable)
	require.Nil(t, got)
}

type performanceStartupRepo struct {
	AccountPerformanceRepository
	called chan struct{}
}

func (r *performanceStartupRepo) EnabledIDs(context.Context) ([]int64, error) {
	r.called <- struct{}{}
	return nil, nil
}

func TestAccountPerformancePublisherStillEvaluates(t *testing.T) {
	r := &performanceStartupRepo{called: make(chan struct{}, 1)}
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleFull, RegionalSchedulerFile: "publisher-config"}}
	s := ProvideAccountPerformanceService(r, cfg)
	defer s.Stop()
	select {
	case <-r.called:
	case <-time.After(time.Second):
		t.Fatal("regional publisher must retain the control-plane evaluator")
	}
}
