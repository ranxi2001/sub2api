package admin

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Tests may shorten this. Production jobs keep the 30-minute limit.
var profitJobTimeout = 30 * time.Minute

var errProfitJobsBusy = errors.New("another bill sync is running")

type profitJobStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	service.ProviderProfitSyncResult
}

type profitJob struct {
	id       string
	provider int64
	window   service.ProviderProfitWindow
	ended    time.Time
	cancel   context.CancelFunc
	done     chan struct{}
	status   profitJobStatus
}

// Only sanitized status and counts survive completion. The receipt ledger,
// not this process-local handle, is the durable result of a successful sync.
type providerProfitJobs struct {
	mu     sync.Mutex
	jobs   map[string]*profitJob
	closed bool
	wg     sync.WaitGroup
}

func (q *providerProfitJobs) start(provider int64, w service.ProviderProfitWindow, run func(context.Context) profitJobStatus) (*profitJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil, errProfitJobsBusy
	}
	if q.jobs == nil {
		q.jobs = make(map[string]*profitJob)
	}
	now := time.Now()
	var oldest *profitJob
	active := 0
	for key, j := range q.jobs {
		if !j.ended.IsZero() {
			if now.Sub(j.ended) >= time.Hour {
				delete(q.jobs, key)
				continue
			}
			if oldest == nil || j.ended.Before(oldest.ended) {
				oldest = j
			}
			continue
		}
		active++
		if j.provider == provider {
			if j.window.From.Equal(w.From) && j.window.Until.Equal(w.Until) {
				return j, nil
			}
			return nil, errProfitJobsBusy
		}
	}
	if active >= 2 {
		return nil, errProfitJobsBusy
	}
	if len(q.jobs) >= 128 && oldest != nil {
		delete(q.jobs, oldest.id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), profitJobTimeout)
	j := &profitJob{id: rand.Text(), provider: provider, window: w, cancel: cancel, done: make(chan struct{}), status: profitJobStatus{Status: "running"}}
	q.jobs[j.id] = j
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		defer cancel()
		result := profitJobStatus{Status: "failed", Reason: "internal"}
		defer func() {
			// A background worker has no HTTP recovery middleware. Do not expose
			// panic text (it may contain an upstream payload) or strand its slot.
			_ = recover()
			q.mu.Lock()
			j.status, j.ended = result, time.Now()
			close(j.done)
			q.mu.Unlock()
		}()
		result = run(ctx)
	}()
	return j, nil
}

func (q *providerProfitJobs) get(id int64, handle string) (profitJobStatus, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.jobs[handle]
	if j == nil || j.provider != id || (!j.ended.IsZero() && time.Since(j.ended) >= time.Hour) {
		return profitJobStatus{}, false
	}
	return j.status, true
}

func (q *providerProfitJobs) stop() {
	q.mu.Lock()
	q.closed = true
	for _, j := range q.jobs {
		if j.ended.IsZero() {
			j.cancel()
		}
	}
	q.mu.Unlock()
	q.wg.Wait()
}
