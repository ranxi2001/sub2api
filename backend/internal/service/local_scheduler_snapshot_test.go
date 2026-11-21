//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLocalSchedulerSnapshotIsolationAndExpiry(t *testing.T) {
	now := time.Now()
	store := NewLocalSchedulerSnapshotStore(func() time.Time { return now })
	bucket := SchedulerBucket{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	source := []*Account{{ID: 1, Credentials: map[string]any{"nested": map[string]any{"value": "original"}}}}
	if err := store.Publish(bucket, 1, now.Add(time.Minute), source); err != nil {
		t.Fatal(err)
	}
	source[0].Credentials["nested"].(map[string]any)["value"] = "changed"
	got, hit, err := store.GetLocalSnapshot(context.Background(), bucket)
	if err != nil || !hit || got[0].Credentials["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("isolation: %v", err)
	}
	got[0].ID = 99
	again, _, _ := store.GetLocalSnapshot(context.Background(), bucket)
	if again[0].ID != 1 {
		t.Fatal("reader mutated stored view")
	}
	if err := store.Publish(bucket, 1, now.Add(time.Minute), source); !errors.Is(err, ErrLocalSchedulerSnapshotRevision) {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, _, err := store.GetLocalSnapshot(context.Background(), bucket); !errors.Is(err, ErrLocalSchedulerSnapshotExpired) {
		t.Fatal(err)
	}
}

func TestLocalSchedulerSnapshotFailsClosed(t *testing.T) {
	now := time.Now()
	store := NewLocalSchedulerSnapshotStore(func() time.Time { return now })
	// A nil embedded cache/repository would panic on any fallback call.
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	svc.SetLocalSnapshotReader(store)
	if _, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false); !errors.Is(err, ErrLocalSchedulerSnapshotUnavailable) {
		t.Fatal(err)
	}
	bucket := svc.bucketFor(nil, PlatformOpenAI, svc.resolveMode(PlatformOpenAI, false))
	if err := store.Publish(bucket, 1, now.Add(time.Minute), []*Account{}); err != nil {
		t.Fatal(err)
	}
	accounts, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("empty authoritative view: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false); !errors.Is(err, ErrLocalSchedulerSnapshotExpired) {
		t.Fatal(err)
	}
}

func TestLocalSchedulerSnapshotRejectsIncompletePublication(t *testing.T) {
	now := time.Now()
	store := NewLocalSchedulerSnapshotStore(func() time.Time { return now })
	bucket := SchedulerBucket{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	if err := store.Publish(bucket, 2, now.Add(time.Minute), []*Account{{ID: 1}, {ID: 2}}); err != nil {
		t.Fatal(err)
	}
	for _, accounts := range [][]*Account{{nil}, {{ID: 0}}, {{ID: 1}, {ID: 1}}, {{ID: 1, Credentials: map[string]any{"invalid": make(chan int)}}}} {
		if err := store.Publish(bucket, 3, now.Add(time.Minute), accounts); err == nil {
			t.Fatal("accepted incomplete snapshot")
		}
	}
	if err := store.Publish(bucket, 1, now.Add(time.Minute), nil); !errors.Is(err, ErrLocalSchedulerSnapshotRevision) {
		t.Fatal(err)
	}
	got, hit, err := store.GetLocalSnapshot(context.Background(), bucket)
	if err != nil || !hit || len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatal("invalid publication replaced previous view")
	}
	if err := store.Publish(bucket, 3, now.Add(time.Minute), []*Account{{ID: 3}}); err != nil {
		t.Fatal(err)
	}
	got, _, err = store.GetLocalSnapshot(context.Background(), bucket)
	if err != nil || len(got) != 1 || got[0].ID != 3 {
		t.Fatal("replacement retained removed account")
	}
}

func TestLocalSchedulerSnapshotConcurrentReaders(t *testing.T) {
	store := NewLocalSchedulerSnapshotStore(nil)
	bucket := SchedulerBucket{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	expires := time.Now().Add(time.Minute)
	if err := store.Publish(bucket, 1, expires, []*Account{{ID: 1}, {ID: 1 + 1000}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				got, hit, err := store.GetLocalSnapshot(context.Background(), bucket)
				if err != nil || !hit || len(got) != 2 || got[1].ID != got[0].ID+1000 {
					t.Error("reader observed partial publication")
					return
				}
			}
		}()
	}
	for revision := uint64(2); revision < 100; revision++ {
		if err := store.Publish(bucket, revision, expires, []*Account{{ID: int64(revision)}, {ID: int64(revision) + 1000}}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
