//go:build unit

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type stagedLaneProbe struct {
	started      chan struct{}
	proceed      chan struct{}
	mu           sync.Mutex
	singles      int
	batches      []int
	batchErr     error
	batchStarted chan context.Context
	batchRelease chan struct{}
}

func (p *stagedLaneProbe) applyStagedUsageBillingOne(ctx context.Context, key service.UsageBillingEventKey, owner string) (*service.UsageBillingRecovery, error) {
	p.mu.Lock()
	p.singles++
	p.mu.Unlock()
	close(p.started)
	<-p.proceed
	return &service.UsageBillingRecovery{Settled: true}, nil
}
func (p *stagedLaneProbe) applyStagedUsageBillingBatch(ctx context.Context, owner string, keys []service.UsageBillingEventKey) ([]*service.UsageBillingRecovery, error) {
	p.mu.Lock()
	p.batches = append(p.batches, len(keys))
	p.mu.Unlock()
	if p.batchStarted != nil {
		p.batchStarted <- ctx
		<-p.batchRelease
	}
	out := make([]*service.UsageBillingRecovery, len(keys))
	for i := range out {
		out[i] = &service.UsageBillingRecovery{Settled: true}
	}
	return out, p.batchErr
}

func TestStagedLaneCombinesConcurrentOwnerAndDoesNotFanOutFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			var lanes stagedBillingLanes
			probe := &stagedLaneProbe{started: make(chan struct{}), proceed: make(chan struct{})}
			if fail {
				probe.batchErr = errors.New("database unavailable")
			}
			results := make(chan error, 33)
			go func() {
				_, e := lanes.apply(context.Background(), "1", service.UsageBillingEventKey{RequestID: "first"}, probe, false)
				results <- e
			}()
			select {
			case <-probe.started:
			case <-time.After(time.Second):
				t.Fatal("first settlement did not start")
			}
			for i := range 32 {
				go func() {
					_, e := lanes.apply(context.Background(), "1", service.UsageBillingEventKey{RequestID: fmt.Sprint(i)}, probe, false)
					results <- e
				}()
			}
			require.Eventually(t, func() bool { lanes.mu.Lock(); defer lanes.mu.Unlock(); return len(lanes.owners["1"]) == 32 }, time.Second, time.Millisecond)
			close(probe.proceed)
			failures := 0
			for range 33 {
				select {
				case e := <-results:
					if e != nil {
						failures++
					}
				case <-time.After(time.Second):
					t.Fatal("lane failed to join")
				}
			}
			if fail {
				require.Equal(t, 32, failures)
			} else {
				require.Zero(t, failures)
			}
			probe.mu.Lock()
			defer probe.mu.Unlock()
			require.Equal(t, 1, probe.singles)
			require.Equal(t, []int{32}, probe.batches)
		})
	}
}

func TestBillingSQLPhasesShareSettlementBudget(t *testing.T) {
	var gate billingAdmission
	ctx := context.Background()
	one, err := gate.acquire(ctx, "1", 8)
	require.NoError(t, err)
	two, err := gate.acquireSQL(ctx, 8)
	require.NoError(t, err)
	_, ok := gate.trySQL(8)
	require.False(t, ok, "failure metadata cannot bypass the two-slot budget")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = gate.acquireSQL(canceled, 8)
	require.ErrorIs(t, err, context.Canceled)
	one()
	two()
	release, ok := gate.trySQL(8)
	require.True(t, ok)
	release()
}

func TestStagedLaneCancellationAndRecoveryIsolation(t *testing.T) {
	var lanes stagedBillingLanes
	probe := &stagedLaneProbe{started: make(chan struct{}), proceed: make(chan struct{}), batchStarted: make(chan context.Context, 1), batchRelease: make(chan struct{})}
	submit := func(ctx context.Context, id string) <-chan error {
		result := make(chan error, 1)
		go func() {
			_, err := lanes.apply(ctx, "1", service.UsageBillingEventKey{RequestID: id}, probe, false)
			result <- err
		}()
		return result
	}
	first := submit(context.Background(), "first")
	<-probe.started
	waitQueue := func(n int) {
		require.Eventually(t, func() bool { lanes.mu.Lock(); defer lanes.mu.Unlock(); return len(lanes.owners["1"]) == n }, time.Second, time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	queued := submit(ctx, "canceled")
	waitQueue(1)
	cancel()
	select {
	case err := <-queued:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("queued cancellation waited for settlement")
	}
	_, err := lanes.apply(context.Background(), "1", service.UsageBillingEventKey{}, probe, true)
	require.ErrorIs(t, err, errStagedBillingOwnerBusy)
	other := &stagedLaneProbe{started: make(chan struct{}), proceed: make(chan struct{})}
	close(other.proceed)
	_, err = lanes.apply(context.Background(), "2", service.UsageBillingEventKey{}, other, true)
	require.NoError(t, err)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	batchHead := submit(ctx, "head")
	waitQueue(1)
	tail := submit(context.Background(), "tail")
	waitQueue(2)
	close(probe.proceed)
	shared := <-probe.batchStarted
	cancel()
	require.NoError(t, shared.Err())
	select {
	case <-batchHead:
		t.Fatal("started transaction was abandoned")
	default:
	}
	close(probe.batchRelease)
	for _, result := range []<-chan error{first, batchHead, tail} {
		require.NoError(t, <-result)
	}
}
