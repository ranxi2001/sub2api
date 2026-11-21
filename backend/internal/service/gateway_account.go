package service

import (
	"context"
	"maps"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// PlatformGateway is reserved for account storage and is excluded from the catalog.
const PlatformGateway = domain.AccountPlatformGateway
const GatewayUpstreamKindCredentialKey = "upstream_kind"

var ErrGatewayAccountIdentity = infraerrors.BadRequest("INVALID_GATEWAY_ACCOUNT", "gateway accounts require platform/type gateway, upstream_kind new_api or sub2api, a site URL and an API key")
var ErrGatewayAccountGroup = infraerrors.BadRequest("INVALID_GATEWAY_GROUP", "gateway accounts require an existing concrete or composite group that does not require OAuth")
var ErrGatewayProtocolsPending = infraerrors.BadRequest("GATEWAY_PROTOCOLS_PENDING", "gateway scheduling and forwarding are not available yet")
var ErrGatewayAtomicUpdateRequired = infraerrors.BadRequest("GATEWAY_ATOMIC_UPDATE_REQUIRED", "gateway updates require transactional account and group writes")

type gatewayAccountTransactionKey struct{}

func gatewayAccountTransactionActive(ctx context.Context) bool {
	active, _ := ctx.Value(gatewayAccountTransactionKey{}).(bool)
	return active
}

func withinGatewayAccountTransaction(ctx context.Context, repo AccountRepository, id int64, groups []int64, run func(context.Context) error) error {
	writer, ok := repo.(GatewayAccountTransactionRepository)
	if !ok {
		return ErrGatewayAtomicUpdateRequired
	}
	return writer.WithinGatewayAccountTransaction(ctx, id, groups, func(txctx context.Context) error {
		return run(context.WithValue(txctx, gatewayAccountTransactionKey{}, true))
	})
}

// ValidateGatewayAccountIdentity reserves an account-only identity. Legacy
// records are neither reclassified nor subjected to new credential validation.
func ValidateGatewayAccountIdentity(platform, accountType string, credentials map[string]any) error {
	if platform != PlatformGateway && accountType != AccountTypeGateway {
		return nil
	}
	if platform != PlatformGateway || accountType != AccountTypeGateway {
		return ErrGatewayAccountIdentity
	}
	kind, _ := credentials[GatewayUpstreamKindCredentialKey].(string)
	key, _ := credentials["api_key"].(string)
	base, _ := credentials["base_url"].(string)
	u, err := url.Parse(base)
	if (kind != "new_api" && kind != "sub2api") || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") ||
		err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.TrimSpace(base) != base {
		return ErrGatewayAccountIdentity
	}
	return nil
}

// ValidateGatewayAccountGroup adds only the new type's binding policy. Existing
// account rules remain with their existing callers. No implicit membership is
// created by this check; group IDs must be supplied by the administrator.
func ValidateGatewayAccountGroup(a *Account, group *Group) error {
	if a == nil || (!a.IsGateway() && a.Platform != PlatformGateway) {
		return nil
	}
	if !a.IsGateway() || a.Platform != PlatformGateway || group == nil || group.ID <= 0 ||
		!domain.IsGroupPlatform(group.Platform) || group.RequireOAuthOnly {
		return ErrGatewayAccountGroup
	}
	return nil
}

func validateGatewayAccountGroups(ctx context.Context, a *Account, repo GroupRepository, ids []int64) error {
	if a == nil || (!a.IsGateway() && a.Platform != PlatformGateway) || len(ids) == 0 {
		return nil
	}
	if repo == nil {
		return ErrGatewayAccountGroup
	}
	for _, id := range ids {
		if id <= 0 {
			return ErrGatewayAccountGroup
		}
		group, err := repo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if err := ValidateGatewayAccountGroup(a, group); err != nil {
			return err
		}
	}
	return nil
}

func validateGatewayAccountUpdate(a *Account, input *UpdateAccountInput) error {
	platform, kind := a.Platform, a.Type
	if input.Platform != "" {
		platform = input.Platform
	}
	if input.Type != "" {
		kind = input.Type
	}
	if a.IsGateway() || a.Platform == PlatformGateway || platform == PlatformGateway || kind == AccountTypeGateway {
		// Conversion needs a new account, not a platform edit that loses bindings
		// or reuses credentials with different ownership/protocol semantics.
		if platform != a.Platform || kind != a.Type {
			return ErrGatewayAccountIdentity
		}
		credentials := mergeGatewayCredentials(a.Credentials, input.Credentials)
		return ValidateGatewayAccountIdentity(platform, kind, credentials)
	}
	return nil
}

func mergeGatewayCredentials(existing, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(incoming))
	maps.Copy(out, existing)
	maps.Copy(out, incoming)
	return out
}

// IsGateway identifies the dedicated cross-platform account type. Existing
// apikey/upstream credentials and probe settings never opt an account into it.
func (a *Account) IsGateway() bool {
	return a != nil && a.Type == AccountTypeGateway
}

func (a *Account) gatewayProtocolsPending() bool {
	return a != nil && (a.IsGateway() || a.Platform == PlatformGateway)
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
