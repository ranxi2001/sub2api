package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const oauth429DedupLimit = 8192

var errOAuth429DedupCapacity = errors.New("oauth429 live request identity capacity exhausted")

func oauth429AccountEpoch(ctx context.Context, tx *sql.Tx, id int64) (int64, time.Time, error) {
	var epoch int64
	var since time.Time
	// Callers already hold the account row lock, shared with the epoch trigger.
	err := tx.QueryRowContext(ctx, `SELECT epoch,valid_since FROM oauth_429_account_epochs WHERE account_id=$1`, id).Scan(&epoch, &since)
	return epoch, since, err
}

// A duplicate never replaces the first accepted HTTP response, even after its
// statistical sample has left the 200-entry ring or another worker restarts.
// The account/task lock serializes capacity and insert across all replicas.
func oauth429RememberRequest(ctx context.Context, tx *sql.Tx, id int64, model, request string, at time.Time) (bool, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_429_request_dedup WHERE account_id=$1 AND model=$2 AND expires_at<=clock_timestamp()`, id, model); err != nil {
		return false, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM oauth_429_request_dedup WHERE account_id=$1 AND model=$2 AND request_hash=$3)`, id, model, request).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM oauth_429_request_dedup WHERE account_id=$1 AND model=$2`, id, model).Scan(&count); err != nil {
		return false, err
	}
	if count >= oauth429DedupLimit {
		return false, errOAuth429DedupCapacity
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO oauth_429_request_dedup(account_id,model,request_hash,observed_at,expires_at)
 VALUES($1,$2,$3,$4,clock_timestamp()+interval '1 hour')`, id, model, request, at)
	return err == nil, err
}
