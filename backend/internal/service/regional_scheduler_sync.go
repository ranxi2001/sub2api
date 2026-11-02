package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional"
)

// RegionalSchedulerDocument is a complete replacement, not a patch. Account
// catalog access is checked against a separately provisioned regional allowlist.
// An allowlist does not allocate global concurrency for the shared pool.
// Full Account JSON is sensitive and must only travel in encrypted snapshots.
type RegionalSchedulerDocument struct {
	Version int                       `json:"version"`
	Buckets []RegionalSchedulerBucket `json:"buckets"`
	Groups  []*Group                  `json:"groups,omitempty"`
}
type RegionalSchedulerBucket struct {
	Bucket   SchedulerBucket `json:"bucket"`
	Accounts []*Account      `json:"accounts"`
}

// RegionalSchedulerValidator freezes the receiver's assignment policy. Empty
// assignments allow only empty views. Shared account concurrency requires the
// separate slot lease authority and local admission; the allowlist alone cannot
// authorize serving model requests.
func RegionalSchedulerValidator(allowedIDs []int64) regional.PayloadValidator {
	allowed := make(map[int64]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		if id > 0 {
			allowed[id] = true
		}
	}
	return func(payload []byte) error {
		doc, err := decodeRegionalScheduler(payload)
		if err != nil {
			return err
		}
		for _, entry := range doc.Buckets {
			for _, account := range entry.Accounts {
				if !allowed[account.ID] {
					return errors.New("account outside regional assignment")
				}
			}
		}
		return nil
	}
}
func decodeRegionalScheduler(payload []byte) (RegionalSchedulerDocument, error) {
	var doc RegionalSchedulerDocument
	if len(payload) > regional.MaxSyncBytes/2 {
		return doc, errors.New("regional scheduler payload too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return doc, errors.New("invalid regional scheduler document")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return doc, errors.New("trailing regional scheduler data")
	}
	if doc.Version != 1 || doc.Buckets == nil || len(doc.Buckets) > 4096 {
		return doc, errors.New("invalid regional scheduler version or buckets")
	}
	groups := make(map[int64]bool, len(doc.Groups))
	for _, g := range doc.Groups {
		if g == nil || g.ID <= 0 || groups[g.ID] {
			return doc, errors.New("invalid regional group")
		}
		groups[g.ID] = true
	}
	// A repeated account must have identical credentials and policy in every
	// bucket; otherwise hydration would depend on bucket iteration order.
	accountViews := make(map[int64][]byte)
	seen := make(map[SchedulerBucket]bool, len(doc.Buckets))
	for _, entry := range doc.Buckets {
		b := entry.Bucket
		if b.GroupID < 0 || b.Platform == "" || (b.Mode != SchedulerModeSingle && b.Mode != SchedulerModeForced && b.Mode != SchedulerModeMixed) || seen[b] || entry.Accounts == nil {
			return doc, errors.New("invalid regional scheduler bucket")
		}
		if b.Mode == SchedulerModeMixed && b.Platform != PlatformAnthropic && b.Platform != PlatformGemini {
			return doc, errors.New("unsupported mixed scheduler bucket")
		}
		seen[b] = true
		if b.GroupID > 0 && len(entry.Accounts) > 0 && !groups[b.GroupID] {
			return doc, errors.New("regional bucket missing group policy")
		}
		ids := make(map[int64]bool, len(entry.Accounts))
		for _, a := range entry.Accounts {
			if a == nil || a.ID <= 0 || ids[a.ID] || a.ParentAccountID != nil {
				return doc, errors.New("invalid or unsupported regional account")
			}
			ids[a.ID] = true
			encoded, err := json.Marshal(a)
			if err != nil {
				return doc, errors.New("invalid regional account encoding")
			}
			if previous, ok := accountViews[a.ID]; ok && !bytes.Equal(previous, encoded) {
				return doc, errors.New("conflicting regional account views")
			}
			accountViews[a.ID] = encoded
			if a.Platform != b.Platform && (b.Mode != SchedulerModeMixed || a.Platform != PlatformAntigravity || !a.IsMixedSchedulingEnabled()) {
				return doc, errors.New("regional account platform mismatch")
			}
			if b.GroupID > 0 {
				member := false
				for _, id := range a.GroupIDs {
					if id == b.GroupID {
						member = true
					}
				}
				if !member {
					return doc, errors.New("regional account group mismatch")
				}
			}
		}
	}
	return doc, nil
}

// RegionalSchedulerReader reads one atomic full-stream view per request. It
// never makes network/DB calls. Replica.Current also gates failed persistence,
// missing state and expiry, so no separately cached view can outlive revocation.
type RegionalSchedulerReader struct{ replica *regional.Replica }

func NewRegionalSchedulerReader(replica *regional.Replica) (*RegionalSchedulerReader, error) {
	if replica == nil {
		return nil, errors.New("regional replica required")
	}
	return &RegionalSchedulerReader{replica: replica}, nil
}
func (r *RegionalSchedulerReader) GetLocalSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	snapshot, err := r.replica.Current()
	if err != nil {
		return nil, false, err
	}
	if snapshot.Kind != regional.SnapshotScheduler {
		return nil, false, ErrLocalSchedulerSnapshotUnavailable
	}
	doc, err := decodeRegionalScheduler(snapshot.Payload)
	if err != nil {
		return nil, false, err
	}
	out := make([]*Account, 0)
	for _, entry := range doc.Buckets {
		if entry.Bucket != bucket {
			continue
		}
		for _, a := range entry.Accounts {
			if a.IsSchedulable() {
				out = append(out, a)
			}
		}
		break
	}
	// Missing bucket is an authoritative empty result: removal must never fall
	// through to old Redis/DB membership. Payload version is a complete view.
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !time.Now().Before(snapshot.ExpiresAt) {
		return nil, false, ErrLocalSchedulerSnapshotExpired
	}
	return out, true, nil
}

// ExportRegionalScheduler loads the supplied buckets directly from the existing
// authoritative repository query path, never the local reader or Redis cache.
// Caller supplies the region assignment and executes this on a consistent DB
// read scope when atomic multi-bucket capture is required. No revision is issued
// here; failed/partial exports must not be published.
func (s *SchedulerSnapshotService) ExportRegionalScheduler(ctx context.Context, buckets []SchedulerBucket, allowedIDs []int64) ([]byte, error) {
	if s == nil || !s.writesSnapshots() || len(buckets) > 4096 {
		return nil, errors.New("invalid regional export")
	}
	allowed := make(map[int64]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		if id > 0 {
			allowed[id] = true
		}
	}
	doc := RegionalSchedulerDocument{Version: 1, Buckets: make([]RegionalSchedulerBucket, 0, len(buckets))}
	seenGroups := make(map[int64]bool)
	for _, bucket := range buckets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := RegionalSchedulerBucket{Bucket: bucket, Accounts: make([]*Account, 0)}
		if bucket.GroupID > 0 {
			if s.groupRepo == nil {
				return nil, errors.New("regional export requires group authority")
			}
			group, err := s.groupRepo.GetByIDLite(ctx, bucket.GroupID)
			if err != nil {
				return nil, err
			}
			if group != nil && !seenGroups[group.ID] {
				doc.Groups = append(doc.Groups, group)
				seenGroups[group.ID] = true
			}
			if group == nil || !group.IsActive() {
				doc.Buckets = append(doc.Buckets, entry)
				continue
			}
		}
		accounts, err := s.loadAccountsFromDB(ctx, bucket, bucket.Mode == SchedulerModeMixed)
		if err != nil {
			return nil, err
		}
		// Use the same authoritative read scope as the account catalog. A
		// failed pause lookup must not renew an old regional permission view.
		accounts, err = s.filterPerformanceAccounts(ctx, accounts)
		if err != nil {
			return nil, err
		}
		for i := range accounts {
			a := &accounts[i]
			if allowed[a.ID] && a.IsSchedulable() {
				entry.Accounts = append(entry.Accounts, a)
			}
		}
		doc.Buckets = append(doc.Buckets, entry)
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, errors.New("cannot encode regional scheduler export")
	}
	if err = RegionalSchedulerValidator(allowedIDs)(payload); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return payload, nil
}

// RunRegionalSchedulerSync polls immediately, then periodically with bounded
// exponential backoff on failure. It runs outside model request handling and
// exits on cancellation. Callers must authenticate the HTTPS source via transport.
func RunRegionalSchedulerSync(ctx context.Context, replica *regional.Replica, endpoint string, transport http.RoundTripper, interval, maxBackoff time.Duration) error {
	if replica == nil || interval < time.Second || maxBackoff < interval || maxBackoff > time.Hour {
		return errors.New("invalid regional sync worker configuration")
	}
	delay := interval
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := replica.Pull(ctx, endpoint, transport)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			delay = interval
		} else {
			delay = min(delay*2, maxBackoff)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// PublishRegionalScheduler exports a complete view before assigning a durable
// version. An export failure preserves the old view and never refreshes its TTL.
func (s *SchedulerSnapshotService) PublishRegionalScheduler(ctx context.Context, replica *regional.Replica, keyID string, lifetime time.Duration, buckets []SchedulerBucket, allowedIDs []int64) error {
	if s == nil || replica == nil {
		return errors.New("regional publisher required")
	}
	s.regionalExportMu.Lock()
	defer s.regionalExportMu.Unlock()
	payload, err := s.ExportRegionalScheduler(ctx, buckets, allowedIDs)
	if err != nil {
		return err
	}
	return replica.PublishNext(ctx, keyID, lifetime, payload)
}

// detailView is decoded afresh on every call. Both account and group details
// share the replica's expiry, revocation and persistence failure boundary.
func (r *RegionalSchedulerReader) detailView(ctx context.Context) (RegionalSchedulerDocument, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return RegionalSchedulerDocument{}, time.Time{}, err
	}
	snapshot, err := r.replica.Current()
	if err != nil {
		return RegionalSchedulerDocument{}, time.Time{}, err
	}
	if snapshot.Kind != regional.SnapshotScheduler {
		return RegionalSchedulerDocument{}, time.Time{}, ErrLocalSchedulerSnapshotUnavailable
	}
	doc, err := decodeRegionalScheduler(snapshot.Payload)
	return doc, snapshot.ExpiresAt, err
}
func regionalReadFinished(ctx context.Context, expiry time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(expiry) {
		return ErrLocalSchedulerSnapshotExpired
	}
	return nil
}
func (r *RegionalSchedulerReader) GetLocalAccounts(ctx context.Context, ids []int64) (map[int64]*Account, error) {
	doc, expiry, err := r.detailView(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id > 0 {
			wanted[id] = true
		}
	}
	out := make(map[int64]*Account)
	for _, bucket := range doc.Buckets {
		for _, a := range bucket.Accounts {
			if wanted[a.ID] && a.IsSchedulable() {
				out[a.ID] = a
			}
		}
	}
	if err := regionalReadFinished(ctx, expiry); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *RegionalSchedulerReader) GetLocalGroup(ctx context.Context, id int64) (*Group, error) {
	doc, expiry, err := r.detailView(ctx)
	if err != nil {
		return nil, err
	}
	var out *Group
	for _, g := range doc.Groups {
		if g.ID == id && g.IsActive() {
			out = g
			break
		}
	}
	if err := regionalReadFinished(ctx, expiry); err != nil {
		return nil, err
	}
	// Missing groups are authoritative denial, including older documents that
	// predate group projection. Callers must not reconstruct incomplete policy.
	if out == nil {
		return nil, ErrLocalSchedulerSnapshotUnavailable
	}
	return out, nil
}
