package handler

import "github.com/Wei-Shaw/sub2api/internal/service"

// The first ambiguous Grok 403 allows one alternate. Retain that ceiling even
// if the alternate returns a different error, rather than draining the pool.
type grokForbiddenFailoverBudget struct {
	active      bool
	switchLimit int
}

func (b *grokForbiddenFailoverBudget) canRetry(err *service.UpstreamFailoverError, switchCount int) bool {
	if err == nil || !err.ShouldRetryNextAccount() {
		return false
	}
	if err.Reason == service.GrokUnknownForbiddenReason && !b.active {
		b.active = true
		b.switchLimit = switchCount + 1
	}
	return !b.active || switchCount < b.switchLimit
}
