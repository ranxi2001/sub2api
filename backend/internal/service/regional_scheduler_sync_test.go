//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional"
)

func regionalTestReplica(t *testing.T, path string, bootstrap bool) *regional.Replica {
	t.Helper()
	codec, err := regional.NewCodec(map[string][]byte{"test": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	r, err := regional.OpenReplica(path, regional.Config{RegionID: "us", MaxSnapshotAgeSeconds: 60}, regional.SnapshotScheduler, codec, RegionalSchedulerValidator([]int64{1, 2}), bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func regionalDoc(t *testing.T, accounts ...*Account) []byte {
	t.Helper()
	if accounts == nil {
		accounts = []*Account{}
	}
	b, err := json.Marshal(RegionalSchedulerDocument{Version: 1, Buckets: []RegionalSchedulerBucket{{Bucket: SchedulerBucket{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}, Accounts: accounts}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestRegionalSchedulerSyncActivationAndRestore(t *testing.T) {
	ctx := context.Background()
	source := regionalTestReplica(t, filepath.Join(t.TempDir(), "source"), true)
	targetPath := filepath.Join(t.TempDir(), "target")
	target := regionalTestReplica(t, targetPath, true)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}
	if err := source.PublishNext(ctx, "test", time.Minute, regionalDoc(t, account)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(source.Handler())
	defer server.Close()
	if err := target.Pull(ctx, server.URL, server.Client().Transport); err != nil {
		t.Fatal(err)
	}
	reader, err := NewRegionalSchedulerReader(target)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	svc.SetLocalSnapshotReader(reader)
	got, _, err := svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)
	if err != nil || len(got) != 1 {
		t.Fatalf("activation: %v", err)
	}
	account.Status = "disabled"
	if err := source.PublishNext(ctx, "test", time.Minute, regionalDoc(t, account)); err != nil {
		t.Fatal(err)
	}
	if err := target.Pull(ctx, server.URL, server.Client().Transport); err != nil {
		t.Fatal(err)
	}
	got, _, err = svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)
	if err != nil || len(got) != 0 {
		t.Fatal("disabled account remained selectable")
	}
	// Whole bucket deletion also removes the previous members.
	if err := source.PublishNext(ctx, "test", time.Minute, []byte(`{"version":1,"buckets":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := target.Pull(ctx, server.URL, server.Client().Transport); err != nil {
		t.Fatal(err)
	}
	restored := regionalTestReplica(t, targetPath, false)
	restoredReader, err := NewRegionalSchedulerReader(restored)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLocalSnapshotReader(restoredReader)
	got, _, err = svc.ListSchedulableAccounts(ctx, nil, PlatformOpenAI, false)
	if err != nil || len(got) != 0 {
		t.Fatal("restart resurrected deleted bucket")
	}
}
func TestRegionalSchedulerRejectsForeignAndBrokenDocuments(t *testing.T) {
	validate := RegionalSchedulerValidator([]int64{1})
	for _, a := range []*Account{
		{ID: 2, Platform: PlatformOpenAI},
		{ID: 1, Platform: PlatformAnthropic},
		{ID: 0, Platform: PlatformOpenAI},
	} {
		if err := validate(regionalDoc(t, a)); err == nil {
			t.Fatal("invalid account accepted")
		}
	}
	if err := validate(regionalDoc(t, &Account{ID: 1, Platform: PlatformOpenAI}, &Account{ID: 1, Platform: PlatformOpenAI})); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := validate([]byte(`{"version":1,"buckets":[]} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
	if err := validate([]byte(`{"version":2,"buckets":[]}`)); err == nil {
		t.Fatal("unknown version accepted")
	}
}

type regionalExportRepo struct {
	AccountRepository
	accounts []Account
	fail     bool
}

func (r *regionalExportRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	if r.fail {
		return nil, errors.New("db unavailable")
	}
	return r.accounts, nil
}
func TestRegionalSchedulerExportFiltersAssignmentAndPreservesOnFailure(t *testing.T) {
	ctx := context.Background()
	repo := &regionalExportRepo{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}, {ID: 2, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}}
	svc := NewSchedulerSnapshotService(nil, nil, repo, nil, nil)
	replica := regionalTestReplica(t, filepath.Join(t.TempDir(), "state"), true)
	buckets := []SchedulerBucket{{Platform: PlatformOpenAI, Mode: SchedulerModeSingle}}
	if err := svc.PublishRegionalScheduler(ctx, replica, "test", time.Minute, buckets, []int64{1}); err != nil {
		t.Fatal(err)
	}
	s, err := replica.Current()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeRegionalScheduler(s.Payload)
	if err != nil || len(doc.Buckets[0].Accounts) != 1 || doc.Buckets[0].Accounts[0].ID != 1 {
		t.Fatal("foreign account exported")
	}
	repo.fail = true
	if err := svc.PublishRegionalScheduler(ctx, replica, "test", time.Minute, buckets, []int64{1}); err == nil {
		t.Fatal("partial export published")
	}
	after, err := replica.Current()
	if err != nil || after.Revision != s.Revision || !after.ExpiresAt.Equal(s.ExpiresAt) {
		t.Fatal("failure refreshed snapshot")
	}
}
func TestRegionalSchedulerWorkerCancellation(t *testing.T) {
	r := regionalTestReplica(t, filepath.Join(t.TempDir(), "state"), true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunRegionalSchedulerSync(ctx, r, "https://unused.invalid", nil, time.Second, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
