package task

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
)

// The non-EVM workers retain a partial enqueue cursor instead of forgetting an
// order when its height lookup fails or the work queue fills up.
type replayCursor struct {
	next, end int64
	orders    []int64
	epoch     uint64
}

var lookbackEpochs sync.Map

func lookbackEpoch(network model.Network) *atomic.Uint64 {
	value, _ := lookbackEpochs.LoadOrStore(network, &atomic.Uint64{})
	return value.(*atomic.Uint64)
}

func invalidateLookback(network model.Network) { lookbackEpoch(network).Add(1) }

var replayCursors = struct {
	sync.Mutex
	items map[model.Network]*replayCursor
}{items: make(map[model.Network]*replayCursor)}

func replayOrders(ctx context.Context, network model.Network, batch int64, interval time.Duration, blocked func() bool,
	resolve func(context.Context, int64, int64) (int64, int64, error), enqueue func(context.Context, int64, int64) error) {
	// Each network has a single scheduled lookback worker. The map lock only
	// protects cursor creation; queues and RPCs never hold it.
	replayCursors.Lock()
	cursor := replayCursors.items[network]
	if cursor == nil {
		cursor = &replayCursor{}
		replayCursors.items[network] = cursor
	}
	replayCursors.Unlock()
	epoch := lookbackEpoch(network).Load()
	if len(cursor.orders) == 0 || cursor.epoch != epoch {
		startAt, endAt, ids, ok := pendingLookbackUnix(network)
		if !ok {
			return
		}
		start, end, err := resolve(ctx, startAt, endAt)
		if err != nil || start < 0 || end < start {
			conf.RecordError(string(network), "Unable to locate historical blocks; retrying")
			return
		}
		cursor.next, cursor.end, cursor.orders = start, end, ids
		cursor.epoch = epoch
	}
	for cursor.next <= cursor.end {
		if ctx.Err() != nil || blocked() {
			return
		}
		end := min(cursor.next+batch-1, cursor.end)
		if err := enqueue(ctx, cursor.next, end); err != nil {
			return
		}
		cursor.next = end + 1
		if interval > 0 && cursor.next <= cursor.end {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
	markLookbackDone(cursor.orders, cursor.epoch)
	cursor.orders = nil
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func retryScan(ctx context.Context, process func() bool) {
	for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, 15*time.Second) {
		if process() {
			return
		}
		if !waitRetry(ctx, delay) {
			return
		}
	}
}

// Locate a timestamp using near-head exponential search followed by bisection.
// Returning the lower neighbour deliberately includes the boundary block.
func heightAtTime(ctx context.Context, at, head int64, timestamp func(context.Context, int64) (int64, error)) (int64, error) {
	if head < 0 {
		return 0, errors.New("invalid chain height")
	}
	high := head
	t, err := timestamp(ctx, high)
	if err != nil {
		return 0, err
	}
	if at >= t {
		return high, nil
	}
	low := high
	for step := int64(1024); ; step *= 2 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		low = max(0, high-step)
		t, err = timestamp(ctx, low)
		if err != nil {
			return 0, err
		}
		if t <= at || low == 0 {
			break
		}
		high = low
	}
	for high-low > 1 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		mid := low + (high-low)/2
		t, err = timestamp(ctx, mid)
		if err != nil {
			return 0, err
		}
		if t <= at {
			low = mid
		} else {
			high = mid
		}
	}
	return low, nil
}

func timeRange(ctx context.Context, startAt, endAt, head int64, timestamp func(context.Context, int64) (int64, error)) (int64, int64, error) {
	start, err := heightAtTime(ctx, startAt-1, head, timestamp)
	if err != nil {
		return 0, 0, err
	}
	end, err := heightAtTime(ctx, endAt+1, head, timestamp)
	if err != nil {
		return 0, 0, err
	}
	return start, min(end+1, head), nil
}
