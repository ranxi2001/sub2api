package repository

import (
	"context"
	"database/sql"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	dbproxy "github.com/Wei-Shaw/sub2api/ent/proxy"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.OpenAITurnAdmissionReader = (*accountRepository)(nil)

// GetOpenAITurnAdmission reads only the fields needed to decide whether an
// already-selected OpenAI account may start another upstream turn. In
// particular, admission needs group IDs and their model restrictions, but not
// the complete group entities used by administration and scheduling views.
// Keeping that distinction avoids an otherwise redundant groups query on every
// turn, which is especially costly for a gateway Pod using a remote database.
//
// The transaction remains read-only and repeatable-read: a partial hydration
// must never combine an old account state with newer group membership.
func (r *accountRepository) GetOpenAITurnAdmission(ctx context.Context, id int64) (*service.Account, *service.Account, error) {
	tx, err := r.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	reader := &accountRepository{client: tx.Client()}
	account, err := reader.getOpenAITurnAdmissionAccount(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var parent *service.Account
	if account.IsShadow() {
		parent, err = reader.getOpenAITurnAdmissionAccount(ctx, *account.ParentAccountID)
		if err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return account, parent, nil
}

func (r *accountRepository) getOpenAITurnAdmissionAccount(ctx context.Context, id int64) (*service.Account, error) {
	m, err := observerAccountQuery(ctx, r.client.Account.Query()).Where(dbaccount.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrAccountNotFound, nil)
	}
	account := accountEntityToService(m)
	if account == nil {
		return nil, service.ErrAccountNotFound
	}
	// Keep independent pauses in repeatable-read exports; the service performs
	// another uncached authority read immediately before each actual send.
	paused, err := performancePaused(ctx, r.client, []int64{id})
	if err != nil {
		return nil, err
	}
	account.PerformancePaused = paused[id]

	if m.ProxyID != nil {
		proxies, err := r.client.Proxy.Query().Where(dbproxy.IDEQ(*m.ProxyID)).All(ctx)
		if err != nil {
			return nil, err
		}
		if len(proxies) > 0 {
			account.Proxy = proxyEntityToService(proxies[0])
		}
	}

	groups, err := r.client.AccountGroup.Query().
		Where(dbaccountgroup.AccountIDEQ(id)).
		Order(dbaccountgroup.ByPriority()).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		account.GroupIDs = append(account.GroupIDs, group.GroupID)
		account.AccountGroups = append(account.AccountGroups, service.AccountGroup{
			AccountID:     group.AccountID,
			GroupID:       group.GroupID,
			Priority:      group.Priority,
			AllowedModels: service.NormalizeGroupAllowedModels(group.AllowedModels),
			CreatedAt:     group.CreatedAt,
		})
	}
	return account, nil
}
