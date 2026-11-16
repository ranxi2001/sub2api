//go:build followupcapture

package requestcapture

// FollowupTestState is compiled only into the isolated capture regression.
func FollowupTestState(s *Session) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"turn": s.turn, "turn_ended": s.turnEnded, "error": s.resultError, "failed": s.failed, "closed": s.closed, "reason": s.reason, "outcome": s.clientOutcome, "protocol": s.meta.Protocol, "pending": s.pending.Load()}
}
