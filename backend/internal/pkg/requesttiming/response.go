package requesttiming

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/util/transportdiag"
)

// StartResponseAttempt observes one logical non-HTTP response, independently of
// connection reuse or handshake/write routes. It installs no httptrace hooks.
func StartResponseAttempt(ctx context.Context, accountID, proxyID int64, route transportdiag.Route) (context.Context, *Trace) {
	c := From(ctx)
	if c == nil {
		return ctx, nil
	}
	c.mu.Lock()
	if c.finished || len(c.data.Attempts) >= 32 {
		c.data.Truncated = true
		c.mu.Unlock()
		return ctx, nil
	}
	index := len(c.data.Attempts)
	c.data.Attempts = append(c.data.Attempts, Attempt{Route: route, Kind: "egress", Number: index + 1, AccountID: accountID, ProxyID: proxyID, StartMS: c.offset(c.now()), Events: map[string]float64{}})
	c.data.FinalResponseAttempt = index + 1
	c.mu.Unlock()
	t := &Trace{c: c, index: index, logicalResponse: true}
	return context.WithValue(ctx, attemptKey{}, t), t
}

// FinishResponseAttempt ends a logical response once, without inventing HTTP
// response headers, EOF, or socket first-byte evidence. A deferred fallback can
// safely call it after the terminal path has already completed the attempt.
func (t *Trace) FinishResponseAttempt(err error, terminal string) {
	if t == nil {
		return
	}
	t.update(func(a *Attempt) {
		if a.EndMS != nil {
			return
		}
		now := t.c.offset(t.c.now())
		a.EndMS = &now
		if err != nil {
			a.Error = errorKind(err)
		}
		if terminal != "" {
			a.Terminal = terminal
			if _, seen := a.Events["terminal"]; !seen {
				a.Events["terminal"] = now
			}
		}
	})
}
