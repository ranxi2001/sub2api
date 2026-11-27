package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/regional"
)

var ErrRegionalAuthUnavailable = errors.New("regional authorization unavailable")

// Full replacements make deletion and permission revocation authoritative. Raw
// API keys and password hashes never enter this document. Monetary fields in the
// reused projection are informational, never a regional spending authorization.
type RegionalAuthDocument struct {
	APIKeyMultiGroupEnabled *bool               `json:"api_key_multi_group_enabled,omitempty"`
	Version                 int                 `json:"version"`
	Entries                 []RegionalAuthEntry `json:"entries"`
}
type RegionalAuthEntry struct {
	Digest       string                    `json:"digest"`
	Snapshot     *APIKeyAuthSnapshot       `json:"snapshot"`
	Subscription *RegionalAuthSubscription `json:"subscription,omitempty"`
}
type RegionalAuthSubscription struct {
	StartsAt  time.Time `json:"starts_at"`
	ExpiresAt time.Time `json:"expires_at"`
}
type RegionalAuthRecord struct {
	APIKey       *APIKey
	Subscription *RegionalAuthSubscription
}

// Source must bind keys, user/group edges, model restrictions, RPM overrides and
// subscription grants to the SAME repeatable-read transaction. Errors abort the
// entire publication; a partial document must never replace the current view.
type RegionalAuthSource interface {
	ListRegionalAuth(context.Context, []int64) ([]RegionalAuthRecord, error)
}
type RegionalAuthAuthority interface {
	Restore(context.Context, string, bool) ([]byte, error)
	Publish(context.Context, string, func(context.Context, uint64, RegionalAuthSource) ([]byte, error)) ([]byte, error)
}

func regionalAuthDigest(key string, lookupKey []byte) string {
	mac := hmac.New(sha256.New, lookupKey)
	_, _ = mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}
func ExportRegionalAuth(ctx context.Context, source RegionalAuthSource, allowed []int64, lookupKey []byte) ([]byte, error) {
	if source == nil || len(lookupKey) != 32 {
		return nil, errors.New("regional auth source and lookup key required")
	}
	records, err := source.ListRegionalAuth(ctx, allowed)
	if err != nil {
		return nil, err
	}
	doc := RegionalAuthDocument{Version: 1, Entries: make([]RegionalAuthEntry, 0, len(records))}
	if policy, ok := source.(APIKeyGroupPolicy); ok {
		enabled, err := policy.APIKeyMultiGroupEnabled(ctx)
		if err != nil {
			return nil, err
		}
		doc.APIKeyMultiGroupEnabled = &enabled
	}
	mapper := &APIKeyService{}
	for _, record := range records {
		key := record.APIKey
		if key == nil || key.User == nil || key.Key == "" || len(key.Key) > MaxAPIKeyCredentialBytes {
			return nil, errors.New("invalid regional auth source record")
		}
		snapshot := mapper.snapshotFromAPIKey(ctx, key)
		snapshot.User.UserGroupRPMOverride = key.User.UserGroupRPMOverride
		// Notification/contact data is unnecessary for local authorization.
		snapshot.User.Email = ""
		snapshot.User.Username = ""
		snapshot.User.BalanceNotifyExtraEmails = nil
		doc.Entries = append(doc.Entries, RegionalAuthEntry{Digest: regionalAuthDigest(key.Key, lookupKey), Snapshot: snapshot, Subscription: record.Subscription})
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := RegionalAuthValidator(allowed)(payload); err != nil {
		return nil, err
	}
	return payload, ctx.Err()
}
func decodeRegionalAuth(payload []byte) (RegionalAuthDocument, error) {
	var doc RegionalAuthDocument
	if len(payload) > regional.MaxSyncBytes/2 {
		return doc, errors.New("regional auth payload too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&doc) != nil {
		return doc, errors.New("invalid regional auth document")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || doc.Version != 1 || doc.Entries == nil || len(doc.Entries) > 4096 {
		return doc, errors.New("invalid regional auth version or entries")
	}
	digests, ids := map[string]bool{}, map[int64]bool{}
	for _, e := range doc.Entries {
		s := e.Snapshot
		decoded, err := hex.DecodeString(e.Digest)
		if err != nil || len(decoded) != sha256.Size || e.Digest != hex.EncodeToString(decoded) || digests[e.Digest] || s == nil || s.Version != apiKeyAuthSnapshotVersion || s.APIKeyID <= 0 || ids[s.APIKeyID] || s.UserID <= 0 || s.User.ID != s.UserID {
			return doc, errors.New("invalid regional auth identity")
		}
		if (s.GroupID == nil && s.Group != nil) || (s.GroupID != nil && (*s.GroupID <= 0 || (s.Group != nil && s.Group.ID != *s.GroupID))) {
			return doc, errors.New("invalid regional auth group")
		}
		if len(ip.ValidateIPPatterns(s.IPWhitelist)) > 0 || len(ip.ValidateIPPatterns(s.IPBlacklist)) > 0 {
			return doc, errors.New("invalid regional auth IP rules")
		}
		if e.Subscription != nil && (s.Group == nil || s.Group.SubscriptionType != SubscriptionTypeSubscription || e.Subscription.StartsAt.IsZero() || !e.Subscription.ExpiresAt.After(e.Subscription.StartsAt)) {
			return doc, errors.New("invalid regional subscription grant")
		}
		digests[e.Digest], ids[s.APIKeyID] = true, true
	}
	return doc, nil
}
func RegionalAuthValidator(allowedIDs []int64) regional.PayloadValidator {
	allowed := map[int64]bool{}
	for _, id := range allowedIDs {
		allowed[id] = true
	}
	return func(payload []byte) error {
		doc, err := decodeRegionalAuth(payload)
		if err != nil {
			return err
		}
		for _, e := range doc.Entries {
			if !allowed[e.Snapshot.UserID] {
				return errors.New("user outside regional auth assignment")
			}
		}
		return nil
	}
}

type RegionalAuthReader struct {
	replica   *regional.Replica
	lookupKey []byte
}

func NewRegionalAuthReader(replica *regional.Replica, lookupKey []byte) (*RegionalAuthReader, error) {
	if replica == nil || len(lookupKey) != 32 {
		return nil, ErrRegionalAuthUnavailable
	}
	return &RegionalAuthReader{replica: replica, lookupKey: append([]byte(nil), lookupKey...)}, nil
}

// GetByKey always checks the replica's durable revision and expiry. It deliberately
// bypasses L1, Redis, singleflight and DB fallback, including WS turn rechecks.
// A fresh decoded object per lookup prevents handlers from mutating future auth.
func (r *RegionalAuthReader) GetByKey(ctx context.Context, key string) (*APIKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(key) == 0 || len(key) > MaxAPIKeyCredentialBytes {
		return nil, ErrAPIKeyNotFound
	}
	snapshot, err := r.replica.Current()
	if err != nil || snapshot.Kind != regional.SnapshotAuth {
		return nil, ErrRegionalAuthUnavailable
	}
	doc, err := decodeRegionalAuth(snapshot.Payload)
	if err != nil {
		return nil, ErrRegionalAuthUnavailable
	}
	digest := regionalAuthDigest(key, r.lookupKey)
	for _, entry := range doc.Entries {
		if entry.Digest != digest {
			continue
		}
		mapper := &APIKeyService{}
		auth := mapper.snapshotToAPIKey(key, entry.Snapshot)
		now := time.Now()
		if !now.Before(snapshot.ExpiresAt) {
			return nil, ErrRegionalAuthUnavailable
		}
		if !auth.IsActive() || (auth.ExpiresAt != nil && !now.Before(*auth.ExpiresAt)) || !auth.User.IsActive() {
			return nil, ErrAPIKeyNotFound
		}
		if auth.GroupID != nil {
			if auth.Group == nil || !auth.Group.IsActive() {
				return nil, ErrAPIKeyNotFound
			}
			if auth.Group.IsSubscriptionType() {
				grant := entry.Subscription
				if grant == nil || now.Before(grant.StartsAt) || !now.Before(grant.ExpiresAt) {
					return nil, ErrAPIKeyNotFound
				}
			} else if !auth.User.CanBindGroup(auth.Group.ID, auth.Group.IsExclusive) {
				return nil, ErrAPIKeyNotFound
			}
		}
		if doc.APIKeyMultiGroupEnabled != nil && !*doc.APIKeyMultiGroupEnabled {
			auth, err = primaryGroupOnly(auth)
			if err != nil {
				return nil, ErrAPIKeyNotFound
			}
		}
		mapper.compileAPIKeyIPRules(auth)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return auth, nil
	}
	return nil, ErrAPIKeyNotFound
}

// GetRegionalByKey is the preview ingress entrypoint. Even a miswired service
// fails closed rather than using the ordinary primary-backed auth cache.
func (s *APIKeyService) GetRegionalByKey(ctx context.Context, key string) (*APIKey, error) {
	if s == nil || s.localAuth == nil {
		return nil, ErrRegionalAuthUnavailable
	}
	return s.localAuth.GetByKey(ctx, key)
}
