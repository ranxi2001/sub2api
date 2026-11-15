package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *channelRepository) ReserveMatureProviderKey(parent context.Context, id int64, key, hash string) (bool, int, []byte, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	result, err := r.db.ExecContext(ctx, `INSERT INTO mature_provider_key_operations(provider_id,request_key_hash,input_hash) SELECT id,$2,$3 FROM mature_providers WHERE id=$1 ON CONFLICT DO NOTHING`, id, key, hash)
	if err != nil {
		return false, 0, nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, 0, nil, err
	}
	if n == 1 {
		return true, 0, nil, nil
	}
	var stored string
	var status sql.NullInt64
	var body []byte
	err = r.db.QueryRowContext(ctx, `SELECT input_hash,http_status,result FROM mature_provider_key_operations WHERE provider_id=$1 AND request_key_hash=$2`, id, key).Scan(&stored, &status, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil, service.ErrMatureProviderNotFound
	}
	if err != nil {
		return false, 0, nil, err
	}
	if stored != hash {
		return false, 0, nil, service.ErrMatureProviderConflict
	}
	if !status.Valid {
		return false, 0, nil, service.ErrMatureProviderKeyPending
	}
	return false, int(status.Int64), body, nil
}

func (r *channelRepository) CompleteMatureProviderKey(parent context.Context, id int64, key, hash string, status int, body []byte) error {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	result, err := r.db.ExecContext(ctx, `UPDATE mature_provider_key_operations SET http_status=$4,result=$5::jsonb,completed_at=NOW() WHERE provider_id=$1 AND request_key_hash=$2 AND input_hash=$3 AND http_status IS NULL`, id, key, hash, status, string(body))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrMatureProviderConflict
	}
	return nil
}
