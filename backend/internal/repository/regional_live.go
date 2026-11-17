package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional/slots"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type regionalLiveMember struct {
	Resource slots.Resource
	Source   string
	Limited  bool
}

func liveMembers(r service.LiveLeaseTransferRequest) []regionalLiveMember {
	return []regionalLiveMember{{slots.Resource{Kind: "account", ID: r.AccountID}, r.AccountRequestID, r.AccountMax > 0}, {slots.Resource{Kind: "api_key", ID: r.APIKeyID}, r.KeyRequestID, r.APIKeyMax > 0}, {slots.Resource{Kind: "user", ID: r.UserID}, r.UserRequestID, r.UserMax > 0}}
}

// All multi-resource mutations lock the handoff first, then sorted budgets.
func (c *regionalConcurrencyCache) lockLive(ctx context.Context, tx *sql.Tx, r service.LiveLeaseTransferRequest) (service.LiveLeaseTransferRequest, string, error) {
	var old service.LiveLeaseTransferRequest
	if r.LeaseID == "" || len(r.LeaseID) > 120 || r.AccountID <= 0 || r.UserID <= 0 || r.APIKeyID <= 0 || r.AccountMax < 0 || r.UserMax < 0 || r.APIKeyMax < 0 {
		return old, "", errors.New("invalid regional Live identity")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return old, "", err
	}
	// A transaction-scoped lock fences an abort that arrives before insertion.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "regional-live:"+c.region()+":"+r.LeaseID); err != nil {
		return old, "", err
	}
	var state string
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT identity,state FROM regional_live_handoffs WHERE region_id=$1 AND lease_id=$2 FOR UPDATE", c.region(), r.LeaseID).Scan(&data, &state)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO regional_live_handoffs(region_id,lease_id,identity,state)
VALUES($1,$2,$3::jsonb || jsonb_build_object('_user_stats_expires_at',extract(epoch FROM clock_timestamp())+$4),'active')`, c.region(), r.LeaseID, string(raw), liveLeaseTTLSeconds)
		old = r
		state = "new"
	} else if err == nil {
		err = json.Unmarshal(data, &old)
	}
	if err != nil {
		return old, "", err
	}
	members := append(liveMembers(r), liveMembers(old)...)
	if r.ReplacedAccountID > 0 {
		members = append(members, regionalLiveMember{Resource: slots.Resource{Kind: "account", ID: r.ReplacedAccountID}})
	}
	sort.Slice(members, func(i, j int) bool {
		a, b := members[i].Resource, members[j].Resource
		if a.Kind == b.Kind {
			return a.ID < b.ID
		}
		return a.Kind < b.Kind
	})
	var previous slots.Resource
	for _, m := range members {
		if m.Resource == previous {
			continue
		}
		previous = m.Resource
		if _, err = tx.ExecContext(ctx, "INSERT INTO regional_slot_budgets(kind,resource_id) VALUES($1,$2) ON CONFLICT DO NOTHING", m.Resource.Kind, m.Resource.ID); err != nil {
			return old, "", err
		}
		var id int64
		if err = tx.QueryRowContext(ctx, "SELECT resource_id FROM regional_slot_budgets WHERE kind=$1 AND resource_id=$2 FOR UPDATE", m.Resource.Kind, m.Resource.ID).Scan(&id); err != nil {
			return old, "", err
		}
	}
	return old, state, nil
}
func (c *regionalConcurrencyCache) moveLiveMember(ctx context.Context, tx *sql.Tx, m regionalLiveMember, destination string) error {
	if !m.Limited {
		return nil
	}
	if m.Source == "" {
		return service.ErrLiveLeaseSourceLost
	}
	var result sql.Result
	var err error
	if c.primary != nil {
		result, err = tx.ExecContext(ctx, "UPDATE regional_primary_slots SET request_id=$4 WHERE kind=$1 AND resource_id=$2 AND request_id=$3 AND NOT released", m.Resource.Kind, m.Resource.ID, m.Source, destination)
	} else {
		result, err = tx.ExecContext(ctx, "UPDATE regional_local_slots s SET request_id=$4 FROM regional_local_slot_grants g WHERE s.lease_id=g.lease_id AND g.region_id=$5 AND s.kind=$1 AND s.resource_id=$2 AND s.request_id=$3 AND NOT s.released", m.Resource.Kind, m.Resource.ID, m.Source, destination, c.region())
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrLiveLeaseSourceLost
	}
	// Source remains permanently retired; late ordinary releases cannot touch Live.
	if c.primary != nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO regional_primary_slots(kind,resource_id,request_id,released) VALUES($1,$2,$3,true)", m.Resource.Kind, m.Resource.ID, m.Source)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO regional_slot_cancellations(region_id,kind,resource_id,request_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", c.region(), m.Resource.Kind, m.Resource.ID, m.Source)
	}
	return err
}
func (c *regionalConcurrencyCache) AcquireLiveLeaseTransferring(ctx context.Context, r service.LiveLeaseTransferRequest) (bool, error) {
	return c.transferLive(ctx, r, false)
}
func (c *regionalConcurrencyCache) MigrateLiveLeaseAccount(ctx context.Context, r service.LiveLeaseTransferRequest) (bool, error) {
	return c.transferLive(ctx, r, true)
}
func (c *regionalConcurrencyCache) transferLive(ctx context.Context, r service.LiveLeaseTransferRequest, migrate bool) (bool, error) {
	if c.runtimeFailed.Load() {
		return false, service.ErrLiveUnavailable
	}
	tx, err := c.database().BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	old, state, err := c.lockLive(ctx, tx, r)
	if err != nil {
		return false, err
	}
	if state == "aborted" || state == "closed" {
		return false, service.ErrLiveLeaseTransferFenced
	}
	if migrate && state != "new" {
		// Existing joint dimensions are retained; the service supplies only account
		// fields on migration, never fresh user/key limits or source identities.
		r.UserMax = old.UserMax
		r.APIKeyMax = old.APIKeyMax
		r.UserRequestID = old.UserRequestID
		r.KeyRequestID = old.KeyRequestID
	}
	if state != "new" && old == r {
		return true, tx.Commit()
	}
	if migrate {
		if state == "new" || old.UserID != r.UserID || old.APIKeyID != r.APIKeyID || (r.ReplacedAccountID != 0 && old.AccountID != r.ReplacedAccountID) {
			return false, service.ErrLiveLeaseSourceLost
		}
		if r.ReplacedAccountID == 0 && old.AccountMax > 0 {
			var held bool
			if c.primary != nil {
				err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM regional_primary_slots WHERE kind='account' AND resource_id=$1 AND request_id=$2 AND NOT released)", old.AccountID, "live:"+r.LeaseID).Scan(&held)
			} else {
				err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM regional_local_slots WHERE kind='account' AND resource_id=$1 AND request_id=$2 AND NOT released)", old.AccountID, "live:"+r.LeaseID).Scan(&held)
			}
			if err != nil {
				return false, err
			}
			if held {
				return false, service.ErrLiveLeaseSourceLost
			}
		}
		if err = c.moveLiveMember(ctx, tx, liveMembers(r)[0], "live:"+r.LeaseID); err != nil {
			return false, err
		}
		if old.AccountMax > 0 {
			if err = c.releaseTx(ctx, tx, slots.Resource{Kind: "account", ID: old.AccountID}, "live:"+r.LeaseID); err != nil {
				return false, err
			}
		}
	} else {
		if state != "new" {
			return false, slots.ErrConflict
		}
		for _, m := range liveMembers(r) {
			if err = c.moveLiveMember(ctx, tx, m, "live:"+r.LeaseID); err != nil {
				return false, err
			}
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	// Preserve the original statistics deadline on migration. A retry or a new
	// account must not revive expired statistics or manufacture a new proof.
	if _, err = tx.ExecContext(ctx, `UPDATE regional_live_handoffs SET identity=$3::jsonb ||
jsonb_build_object('_user_stats_expires_at',identity->'_user_stats_expires_at')
WHERE region_id=$1 AND lease_id=$2`, c.region(), r.LeaseID, string(raw)); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, errors.Join(service.ErrLiveLeaseTransferUncertain, err)
	}
	if r.APIKeyMax == 0 && r.KeyRequestID != "" && c.keyStats != nil {
		_ = c.keyStats.ReleaseAPIKeySlot(context.WithoutCancel(ctx), r.APIKeyID, r.KeyRequestID)
	}
	return true, nil
}
func (c *regionalConcurrencyCache) AcquireLiveLease(ctx context.Context, a int64, am int, u int64, um int, k int64, km int, id string, replacing bool) (bool, error) {
	if replacing {
		return false, service.ErrLiveLeaseSourceLost
	}
	r := service.LiveLeaseTransferRequest{AccountID: a, AccountMax: am, UserID: u, UserMax: um, APIKeyID: k, APIKeyMax: km, LeaseID: id}
	tx, err := c.database().BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	old, state, err := c.lockLive(ctx, tx, r)
	if err != nil {
		return false, err
	}
	if state != "new" {
		return state == "active" && old == r, tx.Commit()
	}
	for _, m := range liveMembers(r) {
		if m.Limited {
			ok, err := c.acquireTx(ctx, tx, m.Resource, "live:"+id)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
	}
	return true, tx.Commit()
}
func (c *regionalConcurrencyCache) RefreshLiveLease(ctx context.Context, a, u, k int64, id string) (bool, error) {
	var raw []byte
	var state string
	err := c.database().QueryRowContext(ctx, "SELECT identity,state FROM regional_live_handoffs WHERE region_id=$1 AND lease_id=$2", c.region(), id).Scan(&raw, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var r service.LiveLeaseTransferRequest
	if err = json.Unmarshal(raw, &r); err != nil {
		return false, err
	}
	held := state == "active" && r.AccountID == a && r.UserID == u && r.APIKeyID == k
	if held && r.UserMax == 0 {
		// This auxiliary proof never changes the existing SQL admission/refresh
		// outcome. Missing/expired proof stays unknown until the lease is closed.
		// State and account identity fence a refresh delayed behind close/migrate.
		_ = c.refreshUserLiveLoadProof(ctx, r)
	}
	return held, nil
}
func (c *regionalConcurrencyCache) finishLive(ctx context.Context, r service.LiveLeaseTransferRequest, abort bool, accountOnly bool) error {
	tx, err := c.database().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	old, state, err := c.lockLive(ctx, tx, r)
	if err != nil {
		return err
	}
	if old.UserID != r.UserID || old.APIKeyID != r.APIKeyID {
		return slots.ErrConflict
	}
	commit := func() error {
		if err := tx.Commit(); err != nil {
			return err
		}
		// The handoff owns unlimited-user statistics too. Retire any remaining
		// ordinary source on close/abort, even if the SDP handler has not returned.
		// Auxiliary failures never change admission or a successful SQL release.
		if !accountOnly && old.UserMax == 0 && old.UserRequestID != "" {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_ = c.ReleaseTrackedUserSlot(cleanupCtx, old.UserID, old.UserRequestID)
		}
		return nil
	}
	if state == "closed" || state == "aborted" {
		return commit()
	}
	if accountOnly && old.AccountID != r.AccountID {
		return slots.ErrConflict
	}
	for _, m := range liveMembers(old) {
		if accountOnly && m.Resource.Kind != "account" {
			continue
		}
		if m.Limited {
			if err = c.releaseTx(ctx, tx, m.Resource, "live:"+r.LeaseID); err != nil {
				return err
			}
		}
	}
	if abort {
		for _, m := range liveMembers(r) {
			if m.Limited && m.Source != "" {
				if err = c.releaseTx(ctx, tx, m.Resource, m.Source); err != nil {
					return err
				}
			}
		}
	}
	if !accountOnly {
		next := "closed"
		if abort {
			next = "aborted"
		}
		if _, err = tx.ExecContext(ctx, "UPDATE regional_live_handoffs SET state=$3 WHERE region_id=$1 AND lease_id=$2", c.region(), r.LeaseID, next); err != nil {
			return err
		}
	}
	return commit()
}
func (c *regionalConcurrencyCache) AbortLiveLeaseTransfer(ctx context.Context, r service.LiveLeaseTransferRequest) error {
	return c.finishLive(ctx, r, true, false)
}
func (c *regionalConcurrencyCache) loadLive(ctx context.Context, id string) (service.LiveLeaseTransferRequest, error) {
	var r service.LiveLeaseTransferRequest
	var raw []byte
	err := c.database().QueryRowContext(ctx, "SELECT identity FROM regional_live_handoffs WHERE region_id=$1 AND lease_id=$2", c.region(), id).Scan(&raw)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (c *regionalConcurrencyCache) ReleaseLiveLease(ctx context.Context, a, u, k int64, id string) error {
	r, err := c.loadLive(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.AccountID != a || r.UserID != u || r.APIKeyID != k {
		return slots.ErrConflict
	}
	return c.finishLive(ctx, r, false, false)
}
func (c *regionalConcurrencyCache) ReleaseLiveLeaseAccount(ctx context.Context, a int64, id string) error {
	r, err := c.loadLive(ctx, id)
	if err != nil {
		return err
	}
	if r.AccountID != a {
		return slots.ErrConflict
	}
	return c.finishLive(ctx, r, false, true)
}

var _ service.LiveConcurrencyCache = (*regionalConcurrencyCache)(nil)
var _ service.LiveLeaseTransferCache = (*regionalConcurrencyCache)(nil)
