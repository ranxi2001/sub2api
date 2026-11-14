package service

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
)

const (
	ModerationUpstreamAll      = "all"
	ModerationUpstreamSelected = "selected"
)

// Bindings are server-owned snapshots, never accepted from an update DTO.
// Keeping membership as well as ID prevents account moves from widening scope.
type ModerationUpstreamBinding struct {
	Platform string  `json:"platform"`
	Type     string  `json:"type"`
	GroupIDs []int64 `json:"group_ids"`
}

func moderationUpstreamBinding(a *Account) ModerationUpstreamBinding {
	groups := append([]int64(nil), a.GroupIDs...)
	slices.Sort(groups)
	return ModerationUpstreamBinding{Platform: a.Platform, Type: a.Type, GroupIDs: slices.Compact(groups)}
}

func validModerationUpstreamAccount(a *Account) bool {
	if a == nil || a.ID <= 0 {
		return false
	}
	switch a.Platform {
	case PlatformAnthropic:
		return a.Type == AccountTypeOAuth || a.Type == AccountTypeSetupToken || a.Type == AccountTypeAPIKey || a.Type == AccountTypeBedrock || a.Type == AccountTypeUpstream
	case PlatformOpenAI:
		return a.Type == AccountTypeOAuth || a.Type == AccountTypeSetupToken || a.Type == AccountTypeAPIKey || a.Type == AccountTypeUpstream
	case PlatformGemini:
		return a.Type == AccountTypeOAuth || a.Type == AccountTypeAPIKey || a.Type == AccountTypeServiceAccount || a.Type == AccountTypeUpstream
	case PlatformAntigravity, PlatformGrok:
		return a.Type == AccountTypeOAuth || a.Type == AccountTypeAPIKey
	case PlatformDeepseek, PlatformKimi, PlatformZhipu, PlatformMiniMax, PlatformQwen, PlatformTypeSafe:
		return a.Type == AccountTypeAPIKey
	default:
		return false
	}
}

func (s *ContentModerationService) updateUpstreamScope(ctx context.Context, cfg *ContentModerationConfig, input UpdateContentModerationConfigInput) error {
	if input.UpstreamAccountScope != nil {
		if *input.UpstreamAccountScope != ModerationUpstreamAll && *input.UpstreamAccountScope != ModerationUpstreamSelected {
			return infraerrors.BadRequest("INVALID_MODERATION_UPSTREAM_SCOPE", "Unknown upstream account scope")
		}
		cfg.UpstreamAccountScope = *input.UpstreamAccountScope
	}
	if cfg.UpstreamAccountScope == "" {
		cfg.UpstreamAccountScope = ModerationUpstreamAll
	}
	if cfg.UpstreamAccountScope != ModerationUpstreamAll && cfg.UpstreamAccountScope != ModerationUpstreamSelected {
		return infraerrors.BadRequest("INVALID_MODERATION_UPSTREAM_SCOPE", "Unknown upstream account scope")
	}
	if input.UpstreamAccountIDs != nil {
		for _, id := range *input.UpstreamAccountIDs {
			if id <= 0 {
				return infraerrors.BadRequest("INVALID_MODERATION_UPSTREAM_ACCOUNT", "Account IDs must be positive")
			}
		}
		cfg.UpstreamAccountIDs = normalizeInt64IDs(*input.UpstreamAccountIDs)
	}
	if len(cfg.UpstreamAccountIDs) > 10000 {
		return infraerrors.BadRequest("INVALID_MODERATION_UPSTREAM_ACCOUNT", "At most 10000 accounts may be selected")
	}
	bindings := make(map[int64]ModerationUpstreamBinding, len(cfg.UpstreamAccountIDs))
	for _, id := range cfg.UpstreamAccountIDs {
		if s.accountRepo == nil {
			return infraerrors.BadRequest("INVALID_MODERATION_UPSTREAM_ACCOUNT", "Account validation unavailable")
		}
		a, err := s.accountRepo.GetByID(ctx, id)
		if err != nil || !validModerationUpstreamAccount(a) {
			return infraerrors.BadRequest("INVALID_MODERATION_UPSTREAM_ACCOUNT", fmt.Sprintf("Upstream account %d is missing or has an unsupported platform/type", id))
		}
		binding := moderationUpstreamBinding(a)
		if input.UpstreamAccountIDs == nil {
			previous, ok := cfg.UpstreamAccountBindings[id]
			if !ok || previous.Platform != binding.Platform || previous.Type != binding.Type || !slices.Equal(previous.GroupIDs, binding.GroupIDs) {
				return infraerrors.BadRequest("STALE_MODERATION_UPSTREAM_ACCOUNT", fmt.Sprintf("Review and reselect changed upstream account %d", id))
			}
		}
		bindings[id] = binding
	}
	cfg.UpstreamAccountBindings = bindings
	return nil
}

// UpstreamModerationConfig returns an owned snapshot for one logical request or
// WS turn. A failover must reuse that snapshot and original content/identity.
func (s *ContentModerationService) UpstreamModerationConfig(ctx context.Context) (*ContentModerationConfig, error) {
	if s == nil || s.settingRepo == nil {
		return nil, nil
	}
	snapshot, err := s.loadRuntimeSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	cfg := snapshot.config
	if !snapshot.riskControlEnabled || !cfg.Enabled || cfg.Mode == ContentModerationModeOff || cfg.UpstreamAccountScope == ModerationUpstreamAll || cfg.UpstreamAccountScope == "" {
		return nil, nil
	}
	return cloneContentModerationConfig(cfg), nil
}

// UpstreamModerationApplies checks every actual selection, including on a cached
// audit. Stale scheduler data is never sufficient to validate the binding.
func (s *ContentModerationService) UpstreamModerationApplies(ctx context.Context, cfg *ContentModerationConfig, input ContentModerationCheckInput) (bool, error) {
	if cfg == nil {
		return false, nil
	}
	if cfg.UpstreamAccountScope != ModerationUpstreamSelected {
		return false, fmt.Errorf("unknown upstream account scope %q", cfg.UpstreamAccountScope)
	}
	if !cfg.includesGroup(input.GroupID) || !cfg.includesModel(input.Model) {
		return false, nil
	}
	a := input.UpstreamAccount
	if a == nil || !slices.Contains(cfg.UpstreamAccountIDs, a.ID) {
		return false, nil
	}
	binding, ok := cfg.UpstreamAccountBindings[a.ID]
	if !ok || s.accountRepo == nil {
		return false, fmt.Errorf("missing upstream binding for account %d", a.ID)
	}
	current, err := s.accountRepo.GetByID(ctx, a.ID)
	if err != nil || !validModerationUpstreamAccount(current) {
		return false, fmt.Errorf("upstream account %d is unavailable", a.ID)
	}
	for _, candidate := range []*Account{a, current} {
		actual := moderationUpstreamBinding(candidate)
		if actual.Platform != binding.Platform || actual.Type != binding.Type || !slices.Equal(actual.GroupIDs, binding.GroupIDs) {
			return false, fmt.Errorf("upstream account %d binding changed; review scope", a.ID)
		}
	}
	return true, nil
}

func moderationUpstreamUnavailable(input ContentModerationCheckInput, err error) *ContentModerationDecision {
	var accountID int64
	if input.UpstreamAccount != nil {
		accountID = input.UpstreamAccount.ID
	}
	slog.Error("content_moderation.upstream_scope_unavailable", "account_id", accountID, "request_id", input.RequestID, "error", err)
	return &ContentModerationDecision{Blocked: true, Action: ContentModerationActionBlock, StatusCode: 503, Message: "Upstream moderation scope is unavailable; administrator review required"}
}

const upstreamModerationGateKey = "content_moderation.selected_upstream_gate"

type upstreamModerationGate func(context.Context, *Account) error

func SetUpstreamModerationGate(c *gin.Context, gate func(context.Context, *Account) error) {
	if c != nil {
		c.Set(upstreamModerationGateKey, upstreamModerationGate(gate))
	}
}

// CheckUpstreamModeration runs after scheduling/admission and before any provider
// generation write. Protocol adapters may nest; the registered gate deduplicates.
func CheckUpstreamModeration(ctx context.Context, c *gin.Context, account *Account) error {
	if c != nil {
		if value, ok := c.Get(upstreamModerationGateKey); ok {
			if gate, ok := value.(upstreamModerationGate); ok && gate != nil {
				return gate(ctx, account)
			}
		}
	}
	return nil
}
