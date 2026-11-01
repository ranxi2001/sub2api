package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestAccountPerformanceBounds(t *testing.T) {
	n := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name   string
		value  *float64
		bounds PerformanceBounds
		want   bool
	}{
		{"missing", nil, PerformanceBounds{Min: n(10)}, false},
		{"below", n(9), PerformanceBounds{Min: n(10)}, true},
		{"equal", n(10), PerformanceBounds{Min: n(10), Max: n(10)}, false},
		{"above", n(11), PerformanceBounds{Max: n(10)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, tc.bounds.Breached(tc.value)) })
	}
	require.Error(t, (PerformanceBounds{Min: n(20), Max: n(10)}).Validate(false))
	require.Error(t, (PerformanceBounds{Max: n(101)}).Validate(true))
	require.Error(t, (PerformanceBounds{Min: n(math.NaN())}).Validate(false))
}
func TestAccountPerformancePauseIsIndependent(t *testing.T) {
	a := &Account{Status: StatusActive, Schedulable: true, PerformancePaused: true}
	require.False(t, a.IsSchedulable())
	a.Schedulable = false
	a.PerformancePaused = false
	require.False(t, a.IsSchedulable())
	a.Schedulable = true
	require.True(t, a.IsSchedulable())
}

type performanceAccountRepoStub struct {
	AccountRepository
	paused map[int64]bool
	err    error
	calls  int
}

func (r *performanceAccountRepoStub) PerformancePaused(context.Context, []int64) (map[int64]bool, error) {
	r.calls++
	return r.paused, r.err
}
func TestAccountPerformanceSnapshotRejectsStaleCacheAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	cached := &Account{ID: 1, Status: StatusActive, Schedulable: true}
	repo := &performanceAccountRepoStub{paused: map[int64]bool{1: true}}
	cache := &snapshotReaderCache{accounts: []*Account{cached}, hit: true}
	s := NewSchedulerSnapshotService(cache, nil, repo, nil, snapshotReaderConfig())
	rows, _, err := s.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Empty(t, rows)
	a, err := s.GetAccount(ctx, 1)
	require.NoError(t, err)
	require.False(t, a.IsSchedulable())
	batch, err := s.GetAccounts(ctx, []int64{1})
	require.NoError(t, err)
	require.False(t, batch[1].IsSchedulable())
	require.False(t, cached.PerformancePaused, "never mutate shared cache pointers")
	repo.err = errors.New("authority unavailable")
	rows, _, err = s.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)
	require.Error(t, err)
	require.Nil(t, rows)
	a, err = s.GetAccount(ctx, 1)
	require.Error(t, err)
	require.Nil(t, a)
	batch, err = s.GetAccounts(ctx, []int64{1})
	require.Error(t, err)
	require.Nil(t, batch)
}
func TestAccountPerformanceSlotRejectsPauseBeforeUnlimitedOrCache(t *testing.T) {
	repo := &performanceAccountRepoStub{paused: map[int64]bool{1: true}}
	svc := NewConcurrencyService(nil)
	svc.performancePauses = repo
	for _, limit := range []int{0, 5} {
		r, err := svc.AcquireAccountSlot(context.Background(), 1, limit)
		require.NoError(t, err)
		require.False(t, r.Acquired)
	}
	repo.err = errors.New("authority unavailable")
	r, err := svc.AcquireAccountSlot(context.Background(), 1, 0)
	require.Error(t, err)
	require.Nil(t, r)
}

type performanceLocalReader struct{ account *Account }

func (r performanceLocalReader) GetLocalSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return []*Account{r.account}, true, nil
}
func (r performanceLocalReader) GetLocalAccounts(context.Context, []int64) (map[int64]*Account, error) {
	return map[int64]*Account{r.account.ID: r.account}, nil
}
func (r performanceLocalReader) GetLocalGroup(context.Context, int64) (*Group, error) {
	return nil, nil
}
func TestAccountPerformanceRegionalHydrationChecksAuthorityOnce(t *testing.T) {
	repo := &performanceAccountRepoStub{paused: map[int64]bool{1: true}}
	account := &Account{ID: 1, Status: StatusActive, Schedulable: true}
	s := NewSchedulerSnapshotService(nil, nil, repo, nil, snapshotReaderConfig())
	s.SetLocalSnapshotReader(performanceLocalReader{account: account})
	got, err := s.GetAccount(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, got.IsSchedulable())
	require.Equal(t, 1, repo.calls)
	require.False(t, account.PerformancePaused, "regional snapshot stays immutable")
}
