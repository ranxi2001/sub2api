//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProfitOperationsCachePagesSortAndInvalidation(t *testing.T) {
	var cache profitReportCache
	calls := 0
	f := service.UpstreamOperationsFilter{From: time.Now().Add(-time.Hour), Until: time.Now(), Dimension: "upstream", Sort: "upstream_cost", Direction: "asc", Page: 1, PageSize: 1}
	load := func(_ context.Context, collect bool) (*service.ProfitOperationsSnapshot, error) {
		calls++
		require.True(t, collect)
		return &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{Scope: f, QueriedAt: f.Until, Usage: json.RawMessage(`{"summary":{"requests":1000000,"request_income":123},"rows":[{"key":"missing","upstream_cost":null},{"key":"two","upstream_cost":2},{"key":"one","upstream_cost":1}],"total":3,"members":[],"member_total":0,"catalog":[],"catalog_total":0}`)}}, nil
	}
	first, err := cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Contains(t, string(first.Usage), `"key":"one"`)
	f.Page = 2
	second, err := cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Contains(t, string(second.Usage), `"key":"two"`)
	require.Equal(t, first.QueriedAt, second.QueriedAt)
	f.Page = 1
	f.Direction = "desc"
	third, err := cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Contains(t, string(third.Usage), `"key":"two"`)
	require.Equal(t, 1, calls)
	f.Page = 3
	last, err := cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Contains(t, string(last.Usage), `"key":"missing"`)
	cache.clear()
	_, err = cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	f.AccountID = 4
	_, err = cache.read(context.Background(), f, load)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}
func TestProfitOperationsCacheDoesNotCacheOversizedReports(t *testing.T) {
	var cache profitReportCache
	calls := 0
	f := service.UpstreamOperationsFilter{Page: 1, PageSize: 20}
	load := func(_ context.Context, collect bool) (*service.ProfitOperationsSnapshot, error) {
		calls++
		raw := `{"total":10001}`
		if !collect {
			raw = `{"total":10001,"rows":[]}`
		}
		return &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{Usage: json.RawMessage(raw)}}, nil
	}
	for i := 0; i < 2; i++ {
		_, err := cache.read(context.Background(), f, load)
		require.NoError(t, err)
	}
	require.Equal(t, 4, calls)
}

// readOnce evaluates Done after registering the singleflight waiter. This
// handshake lets the test cancel the leader only once the follower has joined.
type profitCacheWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *profitCacheWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestProfitOperationsCacheLiveWaiterRecoversFromCanceledLeader(t *testing.T) {
	var cache profitReportCache
	f := service.UpstreamOperationsFilter{Page: 1, PageSize: 20}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	leader, cancel := context.WithCancel(ctx)
	defer cancel()
	started := make(chan struct{})
	var calls atomic.Int32
	load := func(ctx context.Context, _ bool) (*service.ProfitOperationsSnapshot, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, fmt.Errorf("read upstream usage: %w", ctx.Err())
		}
		return &service.ProfitOperationsSnapshot{UpstreamOperationsSnapshot: service.UpstreamOperationsSnapshot{
			Usage: json.RawMessage(`{"summary":{"requests":7},"rows":[],"total":0}`),
		}}, nil
	}
	leaderDone := make(chan error, 1)
	go func() { _, err := cache.read(leader, f, load); leaderDone <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("leader did not start")
	}
	waiter := &profitCacheWaitContext{Context: ctx, waiting: make(chan struct{})}
	type result struct {
		report *service.ProfitOperationsSnapshot
		err    error
	}
	followerDone := make(chan result, 1)
	go func() { report, err := cache.read(waiter, f, load); followerDone <- result{report, err} }()
	select {
	case <-waiter.waiting:
	case <-ctx.Done():
		t.Fatal("follower did not join")
	}
	cancel()
	require.ErrorIs(t, <-leaderDone, context.Canceled)
	follower := <-followerDone
	require.NoError(t, follower.err)
	require.Contains(t, string(follower.report.Usage), `"requests":7`)
	require.EqualValues(t, 2, calls.Load())
	_, err := cache.read(ctx, f, load)
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load(), "the recovered result should be cached")
}

func TestProfitOperationsCacheCancellationRetryIsBounded(t *testing.T) {
	queryError := errors.New("query failed")
	for _, tc := range []struct {
		name  string
		err   error
		calls int
	}{
		{"canceled", context.Canceled, 2},
		{"deadline", context.DeadlineExceeded, 1},
		{"query failure", queryError, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cache profitReportCache
			calls := 0
			_, err := cache.read(context.Background(), service.UpstreamOperationsFilter{}, func(context.Context, bool) (*service.ProfitOperationsSnapshot, error) {
				calls++
				return nil, tc.err
			})
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, tc.calls, calls)
		})
	}
}
