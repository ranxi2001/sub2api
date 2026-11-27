package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Constructed only by the publication transaction, never with the primary pool.
type regionalAuthSource struct {
	client *dbent.Client
	sql    *sql.Tx
}

func (r *regionalAuthSource) ListRegionalAuth(ctx context.Context, allowed []int64) ([]service.RegionalAuthRecord, error) {
	out := make([]service.RegionalAuthRecord, 0)
	if len(allowed) == 0 {
		return out, nil
	}
	// Omit revoked/deleted records. An empty replacement is itself a revocation.
	keys, err := r.client.APIKey.Query().Where(apikey.UserIDIn(allowed...), apikey.DeletedAtIsNil(), apikey.StatusEQ(service.StatusActive), apikey.HasUserWith(user.DeletedAtIsNil(), user.StatusEQ(service.StatusActive))).
		Order(dbent.Asc(apikey.FieldID)).Limit(4097).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(keys) > 4096 {
		return nil, errors.New("regional auth assignment exceeds key limit")
	}
	repo := newAPIKeyRepositoryWithSQL(r.client, r.sql)
	permissions := &userGroupRateRepository{sql: r.sql}
	now := time.Now()
	for _, entity := range keys {
		// Use the canonical auth projection to keep all group/model constraints.
		key, err := repo.GetByKeyForAuth(ctx, entity.Key)
		if err != nil {
			return nil, err
		}
		key.Key = entity.Key
		if key.User == nil {
			return nil, errors.New("regional auth user edge missing")
		}
		record := service.RegionalAuthRecord{APIKey: key}
		if key.ExpiresAt != nil && !now.Before(*key.ExpiresAt) {
			continue
		}
		if key.GroupID != nil {
			exists, err := r.client.Group.Query().Where(group.IDEQ(*key.GroupID), group.DeletedAtIsNil(), group.StatusEQ(service.StatusActive)).Exist(ctx)
			if err != nil {
				return nil, err
			}
			if !exists {
				continue
			}
			key.User.UserGroupDeniedModels, err = permissions.GetDeniedModelsByUserAndGroup(ctx, key.UserID, *key.GroupID)
			if err != nil {
				return nil, err
			}
			key.User.UserGroupRPMOverride, err = permissions.GetRPMOverrideByUserAndGroup(ctx, key.UserID, *key.GroupID)
			if err != nil {
				return nil, err
			}
			if key.Group.IsSubscriptionType() {
				var grant service.RegionalAuthSubscription
				err := r.sql.QueryRowContext(ctx, `SELECT starts_at, expires_at FROM user_subscriptions WHERE user_id=$1 AND group_id=$2 AND status='active' AND deleted_at IS NULL AND starts_at <= $3 AND expires_at > $3`, key.UserID, *key.GroupID, now).Scan(&grant.StartsAt, &grant.ExpiresAt)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return nil, err
				}
				record.Subscription = &grant
			}
		}
		out = append(out, record)
	}
	return out, nil
}

// Uses the same Ent transaction as keys and grants, never the primary pool.
func (r *regionalAuthSource) APIKeyMultiGroupEnabled(ctx context.Context) (bool, error) {
	return service.NewSettingService(NewSettingRepository(r.client), nil).APIKeyMultiGroupEnabled(ctx)
}
