package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// IsGateway identifies the dedicated cross-platform account type. Existing
// apikey/upstream credentials and probe settings never opt an account into it.
func (a *Account) IsGateway() bool {
	return a != nil && a.Type == AccountTypeGateway
}

// usagePlatformForRequest preserves the selected account's platform for legacy
// accounts. A gateway has no single provider: use the handler's request snapshot
// (also available after async handoff), then the resolved request/group platform.
// Missing or non-concrete targets stay unknown; never record "composite" or use
// an arbitrary gateway account platform as a concrete request platform.
func usagePlatformForRequest(ctx context.Context, a *Account, key *APIKey, requestPlatform string) string {
	if !a.IsGateway() {
		if a == nil {
			return ""
		}
		return a.Platform
	}
	if domain.IsConcretePlatform(requestPlatform) {
		return requestPlatform
	}
	if platform := QuotaPlatform(ctx, key); domain.IsConcretePlatform(platform) {
		return platform
	}
	return ""
}
