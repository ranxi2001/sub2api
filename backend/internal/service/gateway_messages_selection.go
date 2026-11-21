package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type gatewaySelectionKey struct{}

type gatewaySelection struct {
	groupID     int64
	model       string
	publicModel string
	family      domain.PlatformGateway
	target      string
}

func withGatewaySelection(ctx context.Context, publicModel, model string, family domain.PlatformGateway) context.Context {
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if group == nil {
		return ctx
	}
	target := group.Platform
	if resolved, ok := ResolvedTargetPlatformFromContext(ctx); ok {
		target = resolved
	}
	if forced, _ := ctx.Value(ctxkey.ForcePlatform).(string); forced != "" {
		if resolved, ok := ResolvedTargetPlatformFromContext(ctx); ok && resolved != forced {
			return ctx
		}
		target = forced
	}
	return context.WithValue(ctx, gatewaySelectionKey{}, gatewaySelection{groupID: group.ID, publicModel: publicModel, model: model, family: family, target: target})
}

func WithGatewayMessagesSelection(ctx context.Context, model string) context.Context {
	family := domain.PlatformGatewayAnthropic
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	target, _ := ResolvedTargetPlatformFromContext(ctx)
	if (group != nil && group.Platform == PlatformGemini) || target == PlatformGemini {
		family = domain.PlatformGatewayGemini
	}
	return withGatewaySelection(ctx, model, model, family)
}

func WithGatewayOpenAISelection(ctx context.Context, publicModel, model string) context.Context {
	return withGatewaySelection(ctx, publicModel, model, domain.PlatformGatewayOpenAI)
}

func WithGatewayGeminiSelection(ctx context.Context, model string) context.Context {
	return withGatewaySelection(ctx, model, model, domain.PlatformGatewayGemini)
}

// GatewayRequestCandidateReader is deliberately separate from shared snapshot
// queries: a neutral account must never enter a platform bucket or group zero.
// Membership and group policy must come from the same primary SQL snapshot.
type GatewayRequestCandidateReader interface {
	ListGatewayRequestCandidates(context.Context, int64, string) ([]Account, *Group, error)
}

func requestGatewaySelection(ctx context.Context, groupID *int64, platform string) (gatewaySelection, bool) {
	selection, ok := ctx.Value(gatewaySelectionKey{}).(gatewaySelection)
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if !ok || selection.model == "" || groupID == nil || *groupID <= 0 || group == nil || group.ID != *groupID || selection.groupID != *groupID ||
		!group.IsActive() || group.RequireOAuthOnly || selection.target != platform ||
		(group.Platform != platform && group.Platform != PlatformComposite) {
		return gatewaySelection{}, false
	}
	if target, ok := ResolvedTargetPlatformFromContext(ctx); ok && target != platform {
		return gatewaySelection{}, false
	}
	if forced, _ := ctx.Value(ctxkey.ForcePlatform).(string); forced != "" && forced != platform {
		return gatewaySelection{}, false
	}
	spec, concrete := domain.LookupPlatform(platform)
	nativeAntigravity := platform == PlatformAntigravity && (selection.family == domain.PlatformGatewayAnthropic || selection.family == domain.PlatformGatewayGemini)
	return selection, concrete && (spec.Gateway == selection.family || nativeAntigravity)
}

func (s *GatewayService) appendGatewayMessagesCandidates(ctx context.Context, groupID *int64, platform string, accounts []Account) ([]Account, error) {
	return appendGatewayCandidates(ctx, s.accountRepo, s.schedulerSnapshot, groupID, platform, accounts), nil
}

func appendGatewayCandidates(ctx context.Context, repo AccountRepository, snapshot *SchedulerSnapshotService, groupID *int64, platform string, accounts []Account) []Account {
	selection, enabled := requestGatewaySelection(ctx, groupID, platform)
	if !enabled {
		return accounts
	}
	reader, _ := repo.(GatewayRequestCandidateReader)
	if snapshot != nil && snapshot.localSnapshotReader != nil {
		reader, _ = snapshot.localSnapshotReader.(GatewayRequestCandidateReader)
	}
	if reader == nil {
		return accounts
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	candidates, group, err := reader.ListGatewayRequestCandidates(ctx, *groupID, platform)
	if err != nil {
		slog.Warn("gateway candidate read unavailable", "group_id", *groupID, "error", err)
		return accounts
	}
	// An invalidated group contributes no gateway candidates. Legacy candidates
	// retain their existing scheduler policy, independent of this capability.
	if group == nil || (group.Platform != platform && group.Platform != PlatformComposite) {
		return accounts
	}
	seen := make(map[int64]bool, len(accounts))
	accounts = append([]Account(nil), accounts...)
	for i := range accounts {
		seen[accounts[i].ID] = true
	}
	for i := range candidates {
		candidate := candidates[i]
		view, err := gatewayProtocolAccountFromSnapshot(ctx, &candidate, &candidate, group, selection.family, selection.model)
		if err != nil || seen[candidate.ID] {
			continue
		}
		if selection.family == domain.PlatformGatewayOpenAI {
			// Run the normal provider-specific scheduler predicates on a local
			// projection; the handler receives the neutral account for accounting.
			view.Platform = platform
			accounts = append(accounts, *view)
			seen[candidate.ID] = true
			continue
		}
		candidate.gatewaySelectionTarget = platform
		candidate.gatewayMessagesView = view
		accounts = append(accounts, candidate)
		seen[candidate.ID] = true
	}
	return accounts
}

func (s *GatewayService) refreshGatewayMessagesSelection(ctx context.Context, account *Account) (*Account, error) {
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if group == nil {
		return nil, ErrGatewayProtocolsPending
	}
	scope, _ := ctx.Value(gatewaySelectionKey{}).(gatewaySelection)
	selection, enabled := requestGatewaySelection(ctx, &group.ID, scope.target)
	if !enabled {
		return nil, ErrGatewayProtocolsPending
	}
	view, err := s.gatewayProtocolAccount(ctx, account, selection.family, selection.model)
	if err != nil {
		return nil, err
	}
	neutral := *view.gatewayProtocolSource
	neutral.gatewaySelectionTarget = selection.target
	neutral.gatewayMessagesView = view
	return &neutral, nil
}

// Rebuild the scheduling projection from current membership and credentials.
// Only HTTP text entry points put this capability in their request context.
func (s *OpenAIGatewayService) openAIGatewaySelectionView(ctx context.Context, account *Account) (*Account, error) {
	if account == nil || (!account.IsGateway() && account.gatewayProtocolSource == nil) {
		return account, nil
	}
	if account.gatewayProtocolSource != nil {
		account = account.gatewayProtocolSource
	}
	scope, _ := ctx.Value(gatewaySelectionKey{}).(gatewaySelection)
	selection, ok := requestGatewaySelection(ctx, &scope.groupID, scope.target)
	if !ok || selection.family != domain.PlatformGatewayOpenAI {
		return nil, ErrGatewayProtocolsPending
	}
	view, err := s.gatewayProtocolAccount(ctx, account, selection.family, selection.model)
	if err != nil {
		return nil, err
	}
	view.Platform = selection.target
	return view, nil
}
