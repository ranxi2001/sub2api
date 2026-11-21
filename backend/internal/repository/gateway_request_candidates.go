package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.GatewayRequestCandidateReader = (*accountRepository)(nil)

// This request-only SELECT never expands the platform bucket queries. It reads
// policy, explicit membership, credentials and runtime state in one SQL snapshot.
func (r *accountRepository) ListGatewayRequestCandidates(ctx context.Context, groupID int64, target string) ([]service.Account, *service.Group, error) {
	if groupID <= 0 || !domain.IsConcretePlatform(target) {
		return nil, nil, nil
	}
	return withGatewayReadSnapshot(ctx, r, func(ctx context.Context, reader *accountRepository) ([]service.Account, *service.Group, error) {
		return reader.gatewayRequestCandidates(ctx, groupID, target)
	})
}

func (r *accountRepository) gatewayRequestCandidates(ctx context.Context, groupID int64, target string) ([]service.Account, *service.Group, error) {
	g, err := r.client.Group.Get(ctx, groupID)
	if err != nil {
		return nil, nil, translatePersistenceError(err, service.ErrGroupNotFound, nil)
	}
	group := groupEntityToService(g)
	if g.DeletedAt != nil || !group.IsActive() || (group.Platform != target && group.Platform != service.PlatformComposite) || group.RequireOAuthOnly {
		return nil, group, nil
	}
	ids, err := observerAccountQuery(ctx, r.client.Account.Query()).Where(
		dbaccount.PlatformEQ(service.PlatformGateway), dbaccount.TypeEQ(service.AccountTypeGateway),
		dbaccount.StatusEQ(service.StatusActive), dbaccount.SchedulableEQ(true),
		dbaccount.HasAccountGroupsWith(dbaccountgroup.GroupIDEQ(groupID)),
	).Order(dbent.Asc(dbaccount.FieldPriority), dbent.Asc(dbaccount.FieldID)).IDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	accounts := make([]service.Account, 0, len(ids))
	for _, id := range ids {
		account, _, err := r.gatewayRequestAccount(ctx, id, groupID)
		if err != nil {
			return nil, nil, err
		}
		accounts = append(accounts, *account)
	}
	return accounts, group, nil
}
