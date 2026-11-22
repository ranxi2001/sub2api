package repository

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

// A hot wallet can queue without borrowing SQL connections or crowding other
// wallets out of the shared settlement budget.
type billingAdmission struct {
	mu     sync.Mutex
	owners map[string]*billingOwner
	global *semaphore.Weighted
}

type billingOwner struct {
	turn *semaphore.Weighted
	refs int
}

func (a *billingAdmission) acquire(ctx context.Context, user string, maxOpen int) (func(), error) {
	a.mu.Lock()
	a.initLocked(maxOpen)
	o := a.owners[user]
	if o == nil {
		o = &billingOwner{turn: semaphore.NewWeighted(1)}
		a.owners[user] = o
	}
	o.refs++
	a.mu.Unlock()

	drop := func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		o.refs--
		if o.refs == 0 {
			delete(a.owners, user)
		}
	}
	if err := o.turn.Acquire(ctx, 1); err != nil {
		drop()
		return nil, err
	}
	if err := a.global.Acquire(ctx, 1); err != nil {
		o.turn.Release(1)
		drop()
		return nil, err
	}
	return func() {
		a.global.Release(1)
		o.turn.Release(1)
		drop()
	}, nil
}

func (a *billingAdmission) initLocked(maxOpen int) {
	if a.global != nil {
		return
	}
	a.owners = make(map[string]*billingOwner)
	limit := 32
	if maxOpen > 0 {
		limit = min(limit, max(1, maxOpen/4))
	}
	a.global = semaphore.NewWeighted(int64(limit))
}

// Every journal phase shares the settlement budget. In particular a canceled
// owner waiter must not bypass it by spawning another SQL retry update.
func (a *billingAdmission) acquireSQL(ctx context.Context, maxOpen int) (func(), error) {
	a.mu.Lock()
	a.initLocked(maxOpen)
	gate := a.global
	a.mu.Unlock()
	if err := gate.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	return func() { gate.Release(1) }, nil
}

func (a *billingAdmission) trySQL(maxOpen int) (func(), bool) {
	a.mu.Lock()
	a.initLocked(maxOpen)
	gate := a.global
	a.mu.Unlock()
	if !gate.TryAcquire(1) {
		return nil, false
	}
	return func() { gate.Release(1) }, true
}
