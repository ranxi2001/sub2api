package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type gatewayMessagesSelectionKey struct{}

// WithGatewayMessagesSelection enables only the native Messages handler's
// concrete Anthropic selection. Other protocols and composite selection remain
// closed until their scheduler/forward/usage contracts are covered together.
func WithGatewayMessagesSelection(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, gatewayMessagesSelectionKey{}, model)
}

// GatewayRequestCandidateReader is deliberately separate from shared snapshot
// queries: a neutral account must never enter a platform bucket or group zero.
// Membership and group policy must come from the same primary SQL snapshot.
type GatewayRequestCandidateReader interface {
	ListGatewayRequestCandidates(context.Context, int64, string) ([]Account, *Group, error)
}

func gatewayMessagesSelectionModel(ctx context.Context, groupID *int64, platform string) (string, bool) {
	model, _ := ctx.Value(gatewayMessagesSelectionKey{}).(string)
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if model == "" || groupID == nil || *groupID <= 0 || platform != PlatformAnthropic ||
		group == nil || group.ID != *groupID || group.Platform != PlatformAnthropic || !group.IsActive() || group.RequireOAuthOnly {
		return "", false
	}
	if target, ok := ResolvedTargetPlatformFromContext(ctx); ok && target != PlatformAnthropic {
		return "", false
	}
	if forced, _ := ctx.Value(ctxkey.ForcePlatform).(string); forced != "" && forced != PlatformAnthropic {
		return "", false
	}
	return model, true
}

func (s *GatewayService) appendGatewayMessagesCandidates(ctx context.Context, groupID *int64, platform string, accounts []Account) ([]Account, error) {
	model, enabled := gatewayMessagesSelectionModel(ctx, groupID, platform)
	if !enabled || (s.schedulerSnapshot != nil && s.schedulerSnapshot.localSnapshotReader != nil) {
		return accounts, nil
	}
	reader, ok := s.accountRepo.(GatewayRequestCandidateReader)
	if !ok {
		return accounts, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	candidates, group, err := reader.ListGatewayRequestCandidates(ctx, *groupID, platform)
	if err != nil {
		slog.Warn("gateway candidate read unavailable", "group_id", *groupID, "error", err)
		return accounts, nil
	}
	// An invalidated group contributes no gateway candidates. Legacy candidates
	// retain their existing scheduler policy, independent of this capability.
	if group == nil || group.Platform != PlatformAnthropic {
		return accounts, nil
	}
	seen := make(map[int64]bool, len(accounts))
	accounts = append([]Account(nil), accounts...)
	for i := range accounts {
		seen[accounts[i].ID] = true
	}
	for i := range candidates {
		candidate := candidates[i]
		view, err := gatewayProtocolAccountFromSnapshot(ctx, &candidate, &candidate, group, "anthropic", model)
		if err != nil || seen[candidate.ID] {
			continue
		}
		candidate.gatewayMessagesView = view
		accounts = append(accounts, candidate)
		seen[candidate.ID] = true
	}
	return accounts, nil
}

func (s *GatewayService) refreshGatewayMessagesSelection(ctx context.Context, account *Account) (*Account, error) {
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if group == nil {
		return nil, ErrGatewayProtocolsPending
	}
	model, enabled := gatewayMessagesSelectionModel(ctx, &group.ID, PlatformAnthropic)
	if !enabled || (s.schedulerSnapshot != nil && s.schedulerSnapshot.localSnapshotReader != nil) {
		return nil, ErrGatewayProtocolsPending
	}
	view, err := s.gatewayProtocolAccount(ctx, account, "anthropic", model)
	if err != nil {
		return nil, err
	}
	neutral := *view.gatewayProtocolSource
	neutral.gatewayMessagesView = view
	return &neutral, nil
}
