package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

var _ service.ProviderProfitChargeRepository = (*channelRepository)(nil)

// A bound on a single SQL input or write, not on the size of a bill.
const providerProfitBatchSize = 512

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

func stageProviderProfitRemote(ctx context.Context, tx *sql.Tx, w service.ProviderProfitWindow, remote []service.ProviderProfitRemoteCharge) error {
	// Carry no request text or raw DTO into even the database parameter log.
	type row struct {
		UsageID      int64     `json:"usage_id"`
		KeyID        int64     `json:"key_id"`
		UserID       int64     `json:"user_id"`
		Hash         string    `json:"hash"`
		At           time.Time `json:"at"`
		Amount       string    `json:"amount"`
		Settled      *string   `json:"settled"`
		UniqueAtSite bool      `json:"unique_at_site"`
	}
	rows := make([]row, 0, min(providerProfitBatchSize, len(remote)))
	for batch := range slices.Chunk(remote, providerProfitBatchSize) {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows = rows[:0]
		for _, v := range batch {
			if !w.Contains(v.CreatedAt) {
				continue
			}
			if v.KeyID <= 0 || v.UsageID <= 0 || v.UserID <= 0 || !service.ProviderProfitRequestIDUsable(v.RequestID) || v.CreatedAt.Nanosecond()%1000 != 0 {
				return service.ErrMatureProviderValidation
			}
			rows = append(rows, row{v.UsageID, v.KeyID, v.UserID, providerProfitDigest(v.RequestID), v.CreatedAt, v.Amount, v.SettledAmount, v.UniqueAtSite})
		}
		if len(rows) == 0 {
			continue
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, providerProfitStageRemoteSQL, string(raw)); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `CREATE INDEX ON pg_temp.profit_sync_remote(hash); ANALYZE pg_temp.profit_sync_remote`)
	return err
}

func (r *channelRepository) ReconcileProviderProfitCharges(ctx context.Context, id int64, epoch time.Time, base string, w service.ProviderProfitWindow, remote []service.ProviderProfitRemoteCharge) (service.ProviderProfitSyncResult, error) {
	var zero service.ProviderProfitSyncResult
	if id <= 0 || epoch.IsZero() || !w.Valid() {
		return zero, service.ErrMatureProviderValidation
	}
	if err := validateProviderProfitOrigin(base); err != nil {
		return zero, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, providerProfitStageSQL); err != nil {
		return zero, err
	}
	if err = stageProviderProfitRemote(ctx, tx, w, remote); err != nil {
		return zero, err
	}
	// Only identity/catalog changes touch the link. Lock it, not the provider's
	// session row: unrelated token refreshes needn't wait for a large local bill.
	// A change after the staging snapshot is a REPEATABLE READ conflict (40001).
	var origin string
	err = tx.QueryRowContext(ctx, `SELECT provider_profit_origin(p.base_url) FROM mature_providers p JOIN provider_profit_links l ON l.provider_id=p.id WHERE p.id=$1 AND p.site_type='sub2api' AND p.base_url=$2 AND l.identity_from=$3 FOR UPDATE OF l`, id, base, epoch).Scan(&origin)
	var sqlErr *pq.Error
	if errors.Is(err, sql.ErrNoRows) || (errors.As(err, &sqlErr) && sqlErr.Code == "40001") {
		return zero, service.ErrMatureProviderConflict
	}
	if err != nil {
		return zero, err
	}
	from, until := profitSQLBoundary(w.From), profitSQLBoundary(w.Until)
	directory, err := profitURLDirectory(ctx, tx)
	if err != nil {
		return zero, err
	}
	site := service.ProviderProfitSiteKey(base)
	provider := directory[site]
	var urlChannel int64
	if provider.ProviderID == id && provider.OriginHash == origin {
		urlChannel = provider.ChannelID
	}
	// Include possible competing local rows even for a catalog alias. Only
	// the unique live directory entry, however, may claim a hand-entered row.
	projection, err := operationsAccounts(ctx, tx, service.UpstreamOperationsFilter{From: from.Add(-service.ProviderProfitClockSkew), Until: until.Add(service.ProviderProfitClockSkew)})
	if err != nil {
		return zero, err
	}
	var accounts []operationsAccount
	if err = json.Unmarshal(projection, &accounts); err != nil {
		return zero, err
	}
	for batch := range slices.Chunk(accounts, providerProfitBatchSize) {
		var ids []int64
		for _, a := range batch {
			if site != "" && a.Attribution == "current_config" && service.ProviderProfitSiteKey(a.Key) == site {
				ids = append(ids, a.ID)
			}
		}
		if len(ids) > 0 {
			if _, err = tx.ExecContext(ctx, `INSERT INTO pg_temp.profit_sync_accounts SELECT unnest($1::bigint[])`, pq.Array(ids)); err != nil {
				return zero, err
			}
		}
	}
	skewSeconds := int(service.ProviderProfitClockSkew.Seconds())
	if _, err = tx.ExecContext(ctx, `ANALYZE pg_temp.profit_sync_accounts`); err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, providerProfitStageLocalsSQL, id, from, until, epoch, origin, skewSeconds, urlChannel); err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, `CREATE UNIQUE INDEX ON pg_temp.profit_sync_locals(id); ANALYZE pg_temp.profit_sync_locals`); err != nil {
		return zero, err
	}
	// Both local and remote rows were staged only inside [from,until).
	var out service.ProviderProfitSyncResult
	var after sql.NullInt64
	for {
		var n, matched, conflicts int
		err = tx.QueryRowContext(ctx, providerProfitChargeSQL, id, epoch, origin, skewSeconds, after, providerProfitBatchSize).Scan(&after, &n, &matched, &conflicts)
		if err != nil {
			return zero, err
		}
		if conflicts != 0 {
			return zero, service.ErrMatureProviderConflict
		}
		out.Matched += matched
		out.Unmatched += n - matched
		if n < providerProfitBatchSize {
			break
		}
	}
	// Record the completed window in the same transaction as the charges. A
	// conflict or a failed commit leaves the previous success untouched.
	if _, err = tx.ExecContext(ctx, providerProfitSyncRunSQL, id, w.From, w.Until, out.Matched, out.Unmatched); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return out, nil
}
