package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.ProviderProfitChargeRepository = (*channelRepository)(nil)

func (r *channelRepository) ProviderProfitIdentityFrom(ctx context.Context, id int64) (time.Time, error) {
	var epoch time.Time
	err := r.db.QueryRowContext(ctx, `SELECT identity_from FROM provider_profit_links WHERE provider_id=$1`, id).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		err = service.ErrMatureProviderNotFound
	}
	return epoch, err
}

// Local timestamps are PostgreSQL microseconds. Ceil both half-open bounds
// rather than letting the driver truncate and include/exclude a boundary row.
func profitSQLBoundary(t time.Time) time.Time {
	if t.Nanosecond()%1000 == 0 {
		return t
	}
	return t.Truncate(time.Microsecond).Add(time.Microsecond)
}

func providerProfitDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validateProviderProfitOrigin(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return service.ErrMatureProviderValidation
	}
	return nil
}

func (r *channelRepository) ReconcileProviderProfitCharges(parent context.Context, id int64, epoch time.Time, base string, w service.ProviderProfitWindow, remote []service.ProviderProfitRemoteCharge) (service.ProviderProfitSyncResult, error) {
	var out service.ProviderProfitSyncResult
	if id <= 0 || epoch.IsZero() || !w.Valid() {
		return out, service.ErrMatureProviderValidation
	}
	if len(remote) > service.ProviderProfitMaxRows {
		return out, service.ErrProviderProfitSyncLimit
	}
	if err := validateProviderProfitOrigin(base); err != nil {
		return out, err
	}
	// Carry no request text or raw DTO into even the database parameter log.
	type row struct {
		UsageID int64     `json:"usage_id"`
		KeyID   int64     `json:"key_id"`
		UserID  int64     `json:"user_id"`
		Hash    string    `json:"hash"`
		At      time.Time `json:"at"`
		Amount  string    `json:"amount"`
		Settled *string   `json:"settled"`
	}
	rows := make([]row, 0, len(remote))
	for _, v := range remote {
		if !w.Contains(v.CreatedAt) || v.CreatedAt.Before(epoch) {
			continue
		}
		if v.KeyID <= 0 || v.UsageID <= 0 || v.UserID <= 0 || !service.ProviderProfitRequestIDUsable(v.RequestID) || v.CreatedAt.Nanosecond()%1000 != 0 {
			return out, service.ErrMatureProviderValidation
		}
		rows = append(rows, row{v.UsageID, v.KeyID, v.UserID, providerProfitDigest(v.RequestID), v.CreatedAt, v.Amount, v.SettledAmount})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	// The remote read released its provider lock (and possibly rotated its
	// single-use token). Check the IDENTITY epoch, not the refresh revision.
	var origin string
	err = tx.QueryRowContext(ctx, `SELECT provider_profit_origin(p.base_url) FROM mature_providers p JOIN provider_profit_links l ON l.provider_id=p.id WHERE p.id=$1 AND p.site_type='sub2api' AND p.base_url=$2 AND l.identity_from=$3 FOR UPDATE OF p,l`, id, base, epoch).Scan(&origin)
	if errors.Is(err, sql.ErrNoRows) {
		return out, service.ErrMatureProviderConflict
	}
	if err != nil {
		return out, err
	}
	var n, conflicts int
	err = tx.QueryRowContext(ctx, providerProfitChargeSQL, id, profitSQLBoundary(w.From), profitSQLBoundary(w.Until), epoch, origin, string(raw), service.ProviderProfitMaxRows+1, int(service.ProviderProfitClockSkew.Seconds())).Scan(&n, &out.Matched, &out.Unmatched, &conflicts)
	if err != nil {
		return out, err
	}
	if n > service.ProviderProfitMaxRows {
		return service.ProviderProfitSyncResult{}, service.ErrProviderProfitSyncLimit
	}
	if conflicts != 0 {
		return service.ProviderProfitSyncResult{}, service.ErrMatureProviderConflict
	}
	if err := tx.Commit(); err != nil {
		return service.ProviderProfitSyncResult{}, err
	}
	return out, nil
}
