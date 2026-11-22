package repository

import (
	"context"
	"errors"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var errStagedBillingBatchOutcomeMismatch = errors.New("durable billing batch outcome count mismatch")

const stagedBillingBatchSize = 128
const stagedBillingMaxPending = 4096

var errStagedBillingQueueFull = errors.New("durable billing owner queue full; event remains pending")

type stagedBillingOutcome struct {
	recovery *service.UsageBillingRecovery
	err      error
}

type stagedBillingRequest struct {
	ctx  context.Context
	key  service.UsageBillingEventKey
	done chan stagedBillingOutcome
}

// Like the upstream Apply lane, the first request runs immediately and arrivals
// during settlement form the next batch. Queued events are already durable.
// No goroutine or SQL connection is created per waiting request.
type stagedBillingExecutor interface {
	applyStagedUsageBillingOne(context.Context, service.UsageBillingEventKey, string) (*service.UsageBillingRecovery, error)
	applyStagedUsageBillingBatch(context.Context, string, []service.UsageBillingEventKey) ([]*service.UsageBillingRecovery, error)
}

type stagedBillingLanes struct {
	mu     sync.Mutex
	owners map[string][]*stagedBillingRequest
}

func (l *stagedBillingLanes) apply(ctx context.Context, owner string, key service.UsageBillingEventKey, repo stagedBillingExecutor) (*service.UsageBillingRecovery, error) {
	req := &stagedBillingRequest{ctx: ctx, key: key, done: make(chan stagedBillingOutcome, 1)}
	l.mu.Lock()
	if l.owners == nil {
		l.owners = make(map[string][]*stagedBillingRequest)
	}
	queue, running := l.owners[owner]
	if len(queue) >= stagedBillingMaxPending {
		l.mu.Unlock()
		return nil, errStagedBillingQueueFull
	}
	l.owners[owner] = append(queue, req)
	l.mu.Unlock()
	if !running {
		go l.drain(owner, repo)
	}
	// A transaction may have started. Always join it; a caller cancellation
	// cannot prove rollback and must not launch an independent monetary replay.
	out := <-req.done
	return out.recovery, out.err
}

func (l *stagedBillingLanes) drain(owner string, repo stagedBillingExecutor) {
	for {
		l.mu.Lock()
		queue := l.owners[owner]
		if len(queue) == 0 {
			delete(l.owners, owner)
			l.mu.Unlock()
			return
		}
		n := min(len(queue), stagedBillingBatchSize)
		batch := append([]*stagedBillingRequest(nil), queue[:n]...)
		l.owners[owner] = append([]*stagedBillingRequest(nil), queue[n:]...)
		l.mu.Unlock()
		l.execute(owner, repo, batch)
	}
}

func (l *stagedBillingLanes) execute(owner string, repo stagedBillingExecutor, batch []*stagedBillingRequest) {
	finished := make(map[*stagedBillingRequest]bool, len(batch))
	deliver := func(req *stagedBillingRequest, out stagedBillingOutcome) {
		finished[req] = true
		req.done <- out
	}
	defer func() {
		if recover() != nil {
			for _, req := range batch {
				if !finished[req] {
					req.done <- stagedBillingOutcome{err: service.ErrUsageBillingOutcomeUnknown}
				}
			}
		}
	}()
	active := make([]*stagedBillingRequest, 0, len(batch))
	for _, req := range batch {
		if err := req.ctx.Err(); err != nil {
			deliver(req, stagedBillingOutcome{err: err})
		} else {
			active = append(active, req)
		}
	}
	if len(active) == 0 {
		return
	}
	if len(active) == 1 {
		req := active[0]
		r, err := repo.applyStagedUsageBillingOne(req.ctx, req.key, owner)
		deliver(req, stagedBillingOutcome{recovery: r, err: err})
		return
	}
	keys := make([]service.UsageBillingEventKey, len(active))
	for i, req := range active {
		keys[i] = req.key
	}
	out, err := repo.applyStagedUsageBillingBatch(active[0].ctx, owner, keys)
	if err == nil && len(out) != len(active) {
		err = errStagedBillingBatchOutcomeMismatch
	}
	// Do not fall back to N independent transactions on SQL failure. Pending
	// events remain recoverable; the recovery worker can isolate a poison event.
	for i, req := range active {
		result := stagedBillingOutcome{err: err}
		if err == nil {
			result.recovery = out[i]
		}
		deliver(req, result)
	}
}
