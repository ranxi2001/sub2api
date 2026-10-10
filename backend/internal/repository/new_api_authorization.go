package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"reflect"
	"sort"
	"strconv"
)

type newAPIAuthorizationRepository struct{ db *sql.DB }

func NewNewAPIAuthorizationRepository(db *sql.DB) service.NewAPIAuthorizationRepository {
	return &newAPIAuthorizationRepository{db: db}
}
func (r *newAPIAuthorizationRepository) GetProfile(ctx context.Context, site string, user int64) (*service.NewAPISiteAuthorization, error) {
	p := &service.NewAPISiteAuthorization{SiteURL: site, UserID: user}
	e := r.db.QueryRowContext(ctx, `SELECT id, revision, access_token_ciphertext FROM new_api_site_authorizations WHERE site_url=$1 AND upstream_user_id=$2`, site, user).Scan(&p.ID, &p.Revision, &p.Ciphertext)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return p, e
}
func (r *newAPIAuthorizationRepository) GetBinding(ctx context.Context, id int64) (*service.NewAPIAccountBinding, error) {
	b := &service.NewAPIAccountBinding{}
	e := r.db.QueryRowContext(ctx, `SELECT b.account_id,b.upstream_token_id,b.account_fingerprint,b.token_group,p.id,p.site_url,p.upstream_user_id,p.revision,p.access_token_ciphertext FROM new_api_account_bindings b JOIN new_api_site_authorizations p ON p.id=b.authorization_id WHERE b.account_id=$1`, id).Scan(&b.AccountID, &b.TokenID, &b.Fingerprint, &b.Group, &b.Profile.ID, &b.Profile.SiteURL, &b.Profile.UserID, &b.Profile.Revision, &b.Profile.Ciphertext)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return b, e
}
func matchNewAPIAccountTx(ctx context.Context, tx *sql.Tx, a *service.Account) error {
	var platform, kind string
	var credentials []byte
	var proxy sql.NullInt64
	e := tx.QueryRowContext(ctx, `SELECT platform, type, credentials, proxy_id FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, a.ID).Scan(&platform, &kind, &credentials, &proxy)
	if e != nil {
		return e
	}
	var current map[string]any
	if e = json.Unmarshal(credentials, &current); e != nil {
		return e
	}
	expectedBytes, e := json.Marshal(a.Credentials)
	if e != nil {
		return e
	}
	var expected map[string]any
	if e = json.Unmarshal(expectedBytes, &expected); e != nil {
		return e
	}
	if platform != a.Platform || kind != a.Type || !reflect.DeepEqual(current, expected) || proxy.Valid != (a.ProxyID != nil) || (proxy.Valid && proxy.Int64 != *a.ProxyID) {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	if proxy.Valid {
		if a.Proxy == nil || a.Proxy.ID != proxy.Int64 {
			return service.ErrUpstreamBillingProbeIdentityChanged
		}
		var protocol, host, user, password, status string
		var port int
		e = tx.QueryRowContext(ctx, `SELECT protocol,host,port,COALESCE(username,''),COALESCE(password,''),status FROM proxies WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, proxy.Int64).Scan(&protocol, &host, &port, &user, &password, &status)
		if e != nil {
			return e
		}
		if protocol != a.Proxy.Protocol || host != a.Proxy.Host || port != a.Proxy.Port || user != a.Proxy.Username || password != a.Proxy.Password || status != a.Proxy.Status {
			return service.ErrUpstreamBillingProbeIdentityChanged
		}
	}
	return nil
}
func (r *newAPIAuthorizationRepository) Save(ctx context.Context, p *service.NewAPISiteAuthorization, bindings []service.NewAPIBindingSave) error {
	if p == nil || p.Ciphertext == "" || len(bindings) == 0 {
		return service.ErrNewAPIConfiguration
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, p.SiteURL+":"+strconv.FormatInt(p.UserID, 10)); e != nil {
		return e
	}
	var id, revision int64
	e = tx.QueryRowContext(ctx, `SELECT id, revision FROM new_api_site_authorizations WHERE site_url=$1 AND upstream_user_id=$2 FOR UPDATE`, p.SiteURL, p.UserID).Scan(&id, &revision)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if id != p.ID || revision != p.Revision {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	ordered := append([]service.NewAPIBindingSave(nil), bindings...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Account.ID < ordered[j].Account.ID })
	for _, b := range ordered {
		if e = matchNewAPIAccountTx(ctx, tx, b.Account); e != nil {
			return e
		}
		site, e := service.CanonicalNewAPISite(b.Account.GetCredential("base_url"))
		if e != nil || site != p.SiteURL || b.TokenID <= 0 {
			return service.ErrNewAPIConfiguration
		}
	}
	if id == 0 {
		e = tx.QueryRowContext(ctx, `INSERT INTO new_api_site_authorizations(site_url,upstream_user_id,access_token_ciphertext) VALUES($1,$2,$3) RETURNING id`, p.SiteURL, p.UserID, p.Ciphertext).Scan(&id)
	} else {
		_, e = tx.ExecContext(ctx, `UPDATE new_api_site_authorizations SET access_token_ciphertext=$1,revision=revision+1,updated_at=NOW() WHERE id=$2`, p.Ciphertext, id)
	}
	if e != nil {
		return e
	}
	// Every rotation invalidates snapshots for other bindings on this profile.
	rows, e := tx.QueryContext(ctx, `UPDATE accounts a SET extra=COALESCE(a.extra,'{}'::jsonb)-'upstream_billing_probe',updated_at=NOW() FROM new_api_account_bindings b WHERE a.id=b.account_id AND b.authorization_id=$1 RETURNING a.id`, id)
	if e != nil {
		return e
	}
	changed := []int64{}
	for rows.Next() {
		var aid int64
		if e = rows.Scan(&aid); e != nil {
			_ = rows.Close()
			return e
		}
		changed = append(changed, aid)
	}
	e = rows.Err()
	closeErr := rows.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	for _, b := range ordered {
		_, e = tx.ExecContext(ctx, `INSERT INTO new_api_account_bindings(account_id,authorization_id,upstream_token_id,account_fingerprint,token_group) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id) DO UPDATE SET authorization_id=EXCLUDED.authorization_id,upstream_token_id=EXCLUDED.upstream_token_id,account_fingerprint=EXCLUDED.account_fingerprint,token_group=EXCLUDED.token_group,updated_at=NOW()`, b.Account.ID, id, b.TokenID, service.NewAPIAccountFingerprint(b.Account), b.Group)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE accounts SET extra=(COALESCE(extra,'{}'::jsonb)-'upstream_billing_probe') || jsonb_build_object('upstream_billing_provider','new_api','upstream_billing_site',$2::text,'upstream_billing_user_id',$3::bigint,'upstream_billing_probe_enabled',true,'upstream_billing_rate_sync_enabled',false),updated_at=NOW() WHERE id=$1`, b.Account.ID, p.SiteURL, p.UserID)
		if e != nil {
			return e
		}
		changed = append(changed, b.Account.ID)
	}
	for _, aid := range changed {
		if e = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &aid, nil, nil); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (r *newAPIAuthorizationRepository) Unbind(ctx context.Context, id int64) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)-'upstream_billing_provider'-'upstream_billing_site'-'upstream_billing_user_id'-'upstream_billing_probe',updated_at=NOW() WHERE id=$1 AND deleted_at IS NULL`, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM new_api_account_bindings WHERE account_id=$1`, id); e != nil {
		return e
	}
	if e = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *newAPIAuthorizationRepository) WriteSnapshot(ctx context.Context, a *service.Account, b *service.NewAPIAccountBinding, snap *service.UpstreamBillingProbeSnapshot) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	var revision int64
	if e = tx.QueryRowContext(ctx, `SELECT revision FROM new_api_site_authorizations WHERE id=$1 FOR SHARE`, b.Profile.ID).Scan(&revision); e != nil {
		return e
	}
	if revision != b.Profile.Revision {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	if e = matchNewAPIAccountTx(ctx, tx, a); e != nil {
		return e
	}
	payload, e := json.Marshal(snap)
	if e != nil {
		return e
	}
	old, e := json.Marshal(a.Extra[service.UpstreamBillingProbeExtraKey])
	if e != nil {
		return e
	}
	enabled, e := json.Marshal(a.Extra[service.UpstreamBillingProbeEnabledExtraKey])
	if e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `UPDATE accounts a SET extra=COALESCE(extra,'{}'::jsonb)||jsonb_build_object('upstream_billing_probe',$1::jsonb),updated_at=NOW() WHERE a.id=$2 AND COALESCE(extra->'upstream_billing_probe','null'::jsonb)=$3::jsonb AND COALESCE(extra->'upstream_billing_probe_enabled','null'::jsonb)=$4::jsonb AND EXISTS(SELECT 1 FROM new_api_account_bindings b WHERE b.account_id=a.id AND b.authorization_id=$5 AND b.upstream_token_id=$6 AND b.account_fingerprint=$7)`, string(payload), a.ID, string(old), string(enabled), b.Profile.ID, b.TokenID, b.Fingerprint)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	if e = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &a.ID, nil, nil); e != nil {
		return e
	}
	return tx.Commit()
}
