package service

import (
	"context"
	"fmt"
)

// Ticket admission needs credentials and persisted tickets that sched:meta
// deliberately omits. Hydrate only gated candidates, before TopK filtering;
// looking solely at the metadata would reject even a valid warm-memory ticket
// because its identity cannot match the stripped credentials.
func (s *OpenAIGatewayService) listSchedulableAccountsForRequest(
	ctx context.Context, groupID *int64, platform, requestedModel string,
	requireCompact bool, excludedIDs map[int64]struct{},
) ([]Account, error) {
	accounts, err := s.listSchedulableAccounts(ctx, groupID, platform)
	if err != nil || s.schedulerSnapshot == nil || len(accounts) == 0 {
		return accounts, err
	}
	if !s.openAICodexTicketEnabledContext(ctx) || !s.openAICodexTicketConfig().FailClosed {
		return accounts, nil
	}
	ids := make([]int64, 0, len(accounts))
	hydrate := make(map[int64]struct{})
	for i := range accounts {
		account := &accounts[i]
		if _, excluded := excludedIDs[account.ID]; excluded {
			continue
		}
		if !account.IsSchedulable() || !isOpenAICodexTicketAccount(account) {
			continue
		}
		model := s.openAICodexTicketOutboundModel(account, requestedModel, requireCompact)
		if !isOpenAICodexTicketAccount(account, model) || !s.openAICodexTicketGatedModel(model) {
			continue
		}
		if _, exists := hydrate[account.ID]; !exists {
			ids = append(ids, account.ID)
			hydrate[account.ID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return accounts, nil
	}
	full, err := s.schedulerSnapshot.GetAccounts(ctx, ids)
	if err != nil {
		// Do not turn an unavailable authority into "no ticket", or fail open.
		return nil, fmt.Errorf("codex ticket scheduling state unavailable: %w", err)
	}
	result := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if _, needed := hydrate[account.ID]; needed {
			account = full[account.ID]
			if account == nil || !s.openAIAccountMatchesRequestGroup(ctx, account, groupID) {
				continue
			}
		}
		result = append(result, *account)
	}
	return result, nil
}
