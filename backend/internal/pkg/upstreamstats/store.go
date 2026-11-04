// Package upstreamstats keeps bounded process-local HTTP response evidence.
// It never infers application/stream success from HTTP 200.
package upstreamstats

import (
	"sync"
	"time"
)

const retention = 24 * time.Hour

// Default is shared by the real HTTP writer and batched account reader.
// Restarting resets evidence; no cluster-wide completeness is implied.
var Default = NewStore(65536, time.Now())

type Evidence struct {
	Percent        *float64  `json:"percent"`
	Failures       int64     `json:"failures"`
	Total          int64     `json:"total"`
	Source         string    `json:"source"`
	ObservedSince  time.Time `json:"observed_since"`
	WindowComplete bool      `json:"window_complete"`
}

type sample struct {
	account int64
	status  int
	at      time.Time
}
type Store struct {
	mu            sync.Mutex
	samples       []sample
	head, size    int
	observedSince time.Time
}

func NewStore(capacity int, started time.Time) *Store {
	return &Store{samples: make([]sample, max(1, capacity)), observedSince: started}
}

// Record accepts HTTP response headers. Local admission, cancellation and
// transport errors without a response are not HTTP response samples.
func (s *Store) Record(account int64, status int, at time.Time) {
	if account <= 0 || status < 200 || status > 599 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.size == len(s.samples) {
		dropped := s.samples[s.head].at.Add(time.Nanosecond)
		if dropped.After(s.observedSince) {
			s.observedSince = dropped
		}
	} else {
		s.size++
	}
	s.samples[s.head] = sample{account, status, at}
	s.head = (s.head + 1) % len(s.samples)
}

// Snapshot scans the bounded ring once for the entire account page. Bounds
// are inclusive and use response-header receipt time, not usage inserts.
func (s *Store) Snapshot(ids []int64, since, now time.Time) map[int64]Evidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	observed := s.observedSince
	cutoff := now.Add(-retention)
	if cutoff.After(observed) {
		observed = cutoff
	}
	out := make(map[int64]Evidence, len(ids))
	for _, id := range ids {
		out[id] = Evidence{Source: "process_local_http_responses", ObservedSince: observed, WindowComplete: !observed.After(since)}
	}
	for i := 0; i < s.size; i++ {
		v := s.samples[i]
		e, ok := out[v.account]
		if !ok || v.at.Before(since) || v.at.After(now) || v.at.Before(cutoff) {
			continue
		}
		e.Total++
		if v.status >= 400 {
			e.Failures++
		}
		out[v.account] = e
	}
	for id, e := range out {
		if e.Total > 0 {
			p := 100 * float64(e.Failures) / float64(e.Total)
			e.Percent = &p
			out[id] = e
		}
	}
	return out
}
