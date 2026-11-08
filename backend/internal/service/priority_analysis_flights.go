package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const priorityAnalysisMaxReaders = 2
const priorityAnalysisTimeout = 10 * time.Second

var ErrPriorityAnalysisBusy = errors.New("priority analysis readers busy")

type priorityAnalysisPrincipalKey struct{}

// WithPriorityAnalysisPrincipal receives an authenticated authorization identity
// from the HTTP boundary. Missing identities are never merged with each other.
func WithPriorityAnalysisPrincipal(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, priorityAnalysisPrincipalKey{}, identity)
}

type priorityAnalysisFlightKey struct {
	principal, model, routeModel, revision string
	group                                  int64
	minutes                                int
	private                                *byte
}

type priorityAnalysisFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	data    []byte // Immutable after done closes; every caller decodes its own copy.
	err     error
}

// A separate, zero-value-ready budget. No routing/history cache or completed
// result cache is involved. Saturation rejects new scopes instead of queuing.
type priorityAnalysisFlights struct {
	mu        sync.Mutex
	flights   map[priorityAnalysisFlightKey]*priorityAnalysisFlight
	preparing chan struct{}
	shutdown  context.Context
	stop      context.CancelFunc
	active    int
	closed    bool
}

// Bound the configuration/channel SQL too. At most two preparation queries and
// two heavy pipelines can run per service. Waiting preparation uses the caller's
// total ten-second deadline; heavy scopes never queue behind occupied readers.
func (b *priorityAnalysisFlights) prepare(ctx context.Context) (context.Context, func(), error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, nil, ErrPriorityLatencySeriesUnavailable
	}
	if b.preparing == nil {
		b.preparing = make(chan struct{}, priorityAnalysisMaxReaders)
		b.shutdown, b.stop = context.WithCancel(context.Background())
	}
	preparing, shutdown := b.preparing, b.shutdown
	b.mu.Unlock()
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(shutdown, cancel)
	cleanup := func() { stop(); cancel() }
	select {
	case preparing <- struct{}{}:
		return work, func() { <-preparing; cleanup() }, nil
	case <-work.Done():
		cleanup()
		return nil, nil, work.Err()
	}
}

func (b *priorityAnalysisFlights) read(ctx context.Context, key priorityAnalysisFlightKey, run func(context.Context) (*PriorityAnalysis, error)) (*PriorityAnalysis, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrPriorityLatencySeriesUnavailable
	}
	f := b.flights[key]
	if f == nil {
		if b.active >= priorityAnalysisMaxReaders {
			b.mu.Unlock()
			return nil, ErrPriorityAnalysisBusy
		}
		if b.flights == nil {
			b.flights = make(map[priorityAnalysisFlightKey]*priorityAnalysisFlight)
		}
		// Do not bind shared SQL to the first HTTP request's cancellation/deadline.
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), priorityAnalysisTimeout)
		f = &priorityAnalysisFlight{done: make(chan struct{}), cancel: cancel}
		b.flights[key] = f
		b.active++
		go func() {
			value, err := run(work)
			if err == nil {
				err = work.Err()
			}
			var data []byte
			if err == nil {
				data, err = json.Marshal(value)
			}
			cancel()
			b.mu.Lock()
			f.data, f.err = data, err
			if b.flights[key] == f {
				delete(b.flights, key)
			}
			// Hold the slot until the actual repository call has exited, including
			// when all callers already left or shutdown cancelled the work.
			b.active--
			close(f.done)
			b.mu.Unlock()
		}()
	}
	f.waiters++
	b.mu.Unlock()

	select {
	case <-ctx.Done():
		b.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			if b.flights[key] == f {
				delete(b.flights, key)
			}
			f.cancel()
		}
		b.mu.Unlock()
		return nil, ctx.Err()
	case <-f.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.err != nil {
			return nil, f.err
		}
		var owned PriorityAnalysis
		if err := json.Unmarshal(f.data, &owned); err != nil {
			return nil, err
		}
		return &owned, nil
	}
}

func (b *priorityAnalysisFlights) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	if b.stop != nil {
		b.stop()
	}
	for key, flight := range b.flights {
		flight.cancel()
		delete(b.flights, key)
	}
}
