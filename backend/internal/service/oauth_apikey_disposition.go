package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/accounttimer"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type OAuthAPIKeyDispositionConfig = accounttimer.OAuthDispositionConfig

func DefaultOAuthAPIKeyDispositionConfig() OAuthAPIKeyDispositionConfig {
	return OAuthAPIKeyDispositionConfig{Scope: "accounts", Action: "pause", DelayMinutes: 5, PauseMinutes: 180, Timezone: "Asia/Shanghai", AccountIDs: []int64{}, GroupIDs: []int64{}}
}

func (s *AccountOpsService) validateOAuthAPIKeyDisposition(ctx context.Context, c OAuthAutoConfig) error {
	p := c.APIKeyDisposition
	if !p.Enabled {
		return nil
	}
	bad := func() error {
		return infraerrors.BadRequest("OAUTH_APIKEY_DISPOSITION_INVALID", "select API Key accounts or active groups for the OAuth platform")
	}
	if p.Scope == "groups" {
		for _, id := range p.GroupIDs {
			g, err := s.autoGroups.GetByID(ctx, id)
			if err != nil {
				return err
			}
			if g == nil || g.Platform != c.Platform || g.Status != StatusActive {
				return bad()
			}
		}
	} else {
		if s.accounts == nil {
			return bad()
		}
		accounts, err := s.accounts.GetByIDs(ctx, p.AccountIDs)
		if err != nil {
			return err
		}
		valid := map[int64]bool{}
		for _, a := range accounts {
			if a != nil && a.Type == AccountTypeAPIKey && a.Platform == c.Platform && !a.IsShadow() {
				valid[a.ID] = true
			}
		}
		for _, id := range p.AccountIDs {
			if !valid[id] {
				return bad()
			}
		}
	}
	return nil
}
