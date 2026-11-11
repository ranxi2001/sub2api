package upstreamstats

import (
	"sync"
	"testing"
	"time"
)

func TestResponseWindow(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s := NewStore(8, now.Add(-2*time.Hour))
	since := now.Add(-time.Hour)
	if got := s.Snapshot([]int64{1}, since, now)[1]; got.Percent != nil || got.Total != 0 {
		t.Fatalf("unknown: %+v", got)
	}
	s.Record(1, 200, since.Add(-time.Nanosecond))
	s.Record(1, 503, since)
	s.Record(1, 200, now)
	s.Record(1, 500, now.Add(time.Nanosecond))
	got := s.Snapshot([]int64{1, 2}, since, now)
	if got[1].Percent == nil || *got[1].Percent != 50 || got[1].Failures != 1 || got[1].Total != 2 || !got[1].WindowComplete {
		t.Fatalf("failed retry then success must remain two attempts: %+v", got[1])
	}
	if got[2].Percent != nil {
		t.Fatal("unseen account must be unknown")
	}
	s.Record(2, 200, now)
	if got := s.Snapshot([]int64{2}, since, now)[2]; got.Percent == nil || *got.Percent != 0 {
		t.Fatalf("measured success: %+v", got)
	}
}

func TestResponseConcurrentBatch(t *testing.T) {
	now := time.Now()
	s := NewStore(1000, now.Add(-time.Hour))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				s.Record(1, 500, now)
				s.Snapshot([]int64{1, 2}, now.Add(-time.Minute), now)
			}
		})
	}
	wg.Wait()
	got := s.Snapshot([]int64{1, 2}, now.Add(-time.Minute), now)
	if got[1].Total != 800 || got[1].Failures != 800 || got[2].Percent != nil {
		t.Fatalf("concurrent evidence: %+v", got)
	}
}

func TestResponseWindowRestartRetentionAndRefresh(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s := NewStore(2, now.Add(-time.Minute))
	s.Record(1, 500, now.Add(-time.Second))
	s.Record(1, 200, now)
	s.Record(1, 200, now)
	got := s.Snapshot([]int64{1}, now.Add(-time.Hour), now)[1]
	if got.WindowComplete || !got.ObservedSince.After(now.Add(-time.Second)) || got.Total != 2 {
		t.Fatalf("retention must disclose incomplete history: %+v", got)
	}
	if got := s.Snapshot([]int64{1}, now.Add(time.Minute), now.Add(2*time.Minute))[1]; got.Percent != nil {
		t.Fatal("refresh must expire samples")
	}
	if got := NewStore(2, now).Snapshot([]int64{1}, now.Add(-time.Hour), now)[1]; got.Percent != nil || got.WindowComplete {
		t.Fatal("restart must be unknown")
	}
}

func TestResponseLongWindowAndCapacityValidity(t *testing.T) {
	now := time.Now()
	s := NewStore(8, now.Add(-48*time.Hour))
	s.Record(1, 503, now.Add(-90*time.Minute))
	s.Record(1, 200, now)
	got := s.Snapshot([]int64{1}, now.Add(-120*time.Minute), now)[1]
	if got.Total != 2 || got.Percent == nil || *got.Percent != 50 || !got.WindowComplete {
		t.Fatalf("120-minute window must retain evidence beyond 60m: %+v", got)
	}
	s.Record(1, 500, now.Add(-25*time.Hour))
	got = s.Snapshot([]int64{1}, now.Add(-24*time.Hour), now)[1]
	if got.Total != 2 || !got.WindowComplete {
		t.Fatalf("maximum supported window excludes expired evidence: %+v", got)
	}
	if s.Snapshot([]int64{1}, now.Add(-25*time.Hour), now)[1].WindowComplete {
		t.Fatal("unsupported history must not be complete")
	}
	bounded := NewStore(1, now.Add(-48*time.Hour))
	bounded.Record(1, 500, now.Add(-90*time.Minute))
	bounded.Record(1, 200, now)
	if bounded.Snapshot([]int64{1}, now.Add(-120*time.Minute), now)[1].WindowComplete {
		t.Fatal("eviction must invalidate affected windows")
	}
}

func TestResponse429ShareUsesSameWindowAndAccounts(t *testing.T) {
	now := time.Now()
	s := NewStore(30, now.Add(-time.Hour))
	for i := 0; i < 25; i++ {
		status := 200
		if i < 16 {
			status = 429
		} else if i < 20 {
			status = 503
		}
		s.Record(7, status, now)
	}
	s.Record(7, 429, now.Add(-2*time.Hour))
	s.Record(8, 429, now)
	s.Record(7, 429, now.Add(time.Minute))
	e := s.Snapshot([]int64{7}, now.Add(-time.Hour), now)[7]
	if e.Total != 25 || e.Failures != 20 || e.RateLimited != 16 || e.Percent == nil || *e.Percent != 80 {
		t.Fatalf("inconsistent evidence: %+v", e)
	}
	if later := s.Snapshot([]int64{7}, now.Add(2*time.Minute), now.Add(time.Hour))[7]; later.RateLimited != 0 {
		t.Fatal("expired 429 samples must leave the window")
	}
}
