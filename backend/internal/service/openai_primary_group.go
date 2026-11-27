package service

import (
	"context"
	"strings"
)

func (s *OpenAIGatewayService) openAIAccountMatchesRequestGroup(ctx context.Context, account *Account, groupID *int64) bool {
	if primary, restricted := primaryOnlyGroupFromContext(ctx); restricted {
		return (groupID == nil || *groupID == primary) && accountAllowedByAPIKeyGroup(ctx, account)
	}
	return s.openAIAccountMatchesSchedulingGroup(account, groupID)
}

// A primary-only request cannot move an existing response to another account.
func (s *OpenAIGatewayService) checkPrimaryOnlyPreviousResponseOwner(ctx context.Context, groupID *int64, responseID string) error {
	responseID = strings.TrimSpace(responseID)
	if _, restricted := primaryOnlyGroupFromContext(ctx); !restricted || responseID == "" {
		return nil
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return denyOpenAITurn("group_membership_changed")
	}
	id, err := store.GetResponseAccount(ctx, derefGroupID(groupID), responseID)
	if err != nil || id <= 0 {
		return denyOpenAITurn("group_membership_changed")
	}
	account, err := s.getSchedulableAccount(ctx, id)
	if err != nil || !s.openAIAccountMatchesRequestGroup(ctx, account, groupID) {
		return denyOpenAITurn("group_membership_changed")
	}
	return nil
}
