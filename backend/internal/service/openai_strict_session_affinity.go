package service

import (
	"context"
	"fmt"
	"strings"
)

func (s *OpenAIGatewayService) strictOpenAISessionAffinity(groupID *int64, platform string, imageCapability OpenAIImagesCapability) bool {
	if s == nil || s.cfg == nil || groupID == nil || (platform != PlatformOpenAI && platform != PlatformQwen) || imageCapability != "" {
		return false
	}
	for _, id := range s.cfg.Gateway.OpenAIScheduler.StrictSessionAffinityGroupIDs {
		if id == *groupID {
			return true
		}
	}
	return false
}

func strictSessionUnavailable(reason string) error {
	return fmt.Errorf("%w: strict session affinity %s", ErrNoAvailableAccounts, reason)
}

// Strict affinity is an opt-in group policy. Ownership outlives a temporary
// failure or capacity shortage; only expiry of the existing sticky TTL starts
// a new session assignment. A failed request never searches a second account.
func (s *defaultOpenAIAccountScheduler) selectStrictSessionAccount(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{}
	if strings.TrimSpace(req.SessionHash) == "" || s.service.cache == nil {
		return nil, decision, strictSessionUnavailable("requires a session identity and shared cache")
	}
	snapshot := openAIStickyBindingSnapshotFromContext(ctx, req.GroupID, req.SessionHash)
	if snapshot == nil || !snapshot.known {
		return nil, decision, strictSessionUnavailable("binding lookup failed")
	}
	ownerID := req.StickyAccountID
	if previousID := strings.TrimSpace(req.PreviousResponseID); previousID != "" {
		store := s.service.getOpenAIWSStateStore()
		if store == nil {
			return nil, decision, strictSessionUnavailable("response ownership is unavailable")
		}
		previousOwner, err := store.GetResponseAccount(ctx, derefGroupID(req.GroupID), previousID)
		if err != nil || previousOwner <= 0 || ownerID > 0 && previousOwner != ownerID {
			return nil, decision, strictSessionUnavailable("response ownership is unknown or conflicts with the session")
		}
		ownerID = previousOwner
	}
	if req.GuardianParentAccountID > 0 {
		if ownerID > 0 && ownerID != req.GuardianParentAccountID {
			return nil, decision, strictSessionUnavailable("task ownership conflicts with the session")
		}
		ownerID = req.GuardianParentAccountID
	}
	if _, excluded := req.ExcludedIDs[ownerID]; excluded || ownerID == 0 && len(req.ExcludedIDs) > 0 {
		return nil, decision, strictSessionUnavailable("owner failed; cross-account failover is disabled")
	}
	req.StickyWeighted = false
	req.DisableStickyEscape = true
	req.PreserveStickyBinding = true
	req.PreviousResponseCanMove = false
	req.StickyAccountID = ownerID
	var selection *AccountSelectionResult
	var err error
	if ownerID > 0 {
		selection, _, err = s.selectBySessionHash(ctx, req)
		decision.Layer = openAIAccountScheduleLayerSessionSticky
		decision.StickySessionHit = true
	} else {
		selection, decision.CandidateCount, decision.TopK, decision.LoadSkew, err = s.selectByLoadBalance(ctx, req)
		decision.Layer = openAIAccountScheduleLayerLoadBalance
	}
	if err != nil {
		return nil, decision, err
	}
	if selection == nil || selection.Account == nil {
		return nil, decision, strictSessionUnavailable("owner is unavailable")
	}
	reject := func(reason string) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return nil, OpenAIAccountScheduleDecision{}, strictSessionUnavailable(reason)
	}
	if ownerID > 0 && selection.Account.ID != ownerID {
		return reject("selected account differs from the owner")
	}
	// Claim before returning even a WaitPlan. Concurrent first turns must not
	// enqueue/dispatch to different accounts. Production uses the existing Redis
	// compare-and-swap against the original observation; a losing turn fails.
	if err := s.service.claimStrictSessionAccount(ctx, req, selection.Account.ID); err != nil {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return nil, OpenAIAccountScheduleDecision{}, err
	}
	decision.SelectedAccountID = selection.Account.ID
	decision.SelectedAccountType = selection.Account.Type
	return selection, decision, nil
}

func (s *OpenAIGatewayService) claimStrictSessionAccount(ctx context.Context, req OpenAIAccountScheduleRequest, accountID int64) error {
	if strings.TrimSpace(req.SessionHash) == "" || s.cache == nil {
		return strictSessionUnavailable("requires a stable session identity and shared cache")
	}
	snapshot := openAIStickyBindingSnapshotFromContext(ctx, req.GroupID, req.SessionHash)
	if snapshot == nil || !snapshot.known {
		return strictSessionUnavailable("binding lookup failed")
	}
	if err := s.setStickySessionAccountIDIfUnchanged(ctx, req.GroupID, req.SessionHash, accountID, s.openAIWSSessionStickyTTL()); err != nil {
		return strictSessionUnavailable("binding could not be saved")
	}
	boundID, err := s.getStickySessionAccountID(ctx, req.GroupID, req.SessionHash)
	if err != nil || boundID != accountID {
		return strictSessionUnavailable("another request owns the session")
	}
	return nil
}
