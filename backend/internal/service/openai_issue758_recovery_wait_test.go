package service

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func issue758WaitFixture(t *testing.T, cache GatewayCache) (*OpenAIGatewayService, *defaultOpenAIAccountScheduler, context.Context, OpenAIAccountScheduleRequest, *Account) {
	t.Helper()
	accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{715}, Credentials: map[string]any{"api_key": "fixture"}}}
	service := &OpenAIGatewayService{cfg: &config.Config{}, cache: cache, accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
	issue715LearnRoute(t, service, &accounts[0], issue715OpaqueBody)
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	ctx := service.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
	require.NotNil(t, openAIAgentRecoveryFromContext(ctx).owner)
	request := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: int64Ptr(715), SessionHash: "fixture-session", RequestedModel: "gpt-6-astra"}
	return service, &defaultOpenAIAccountScheduler{service: service}, ctx, request, &accounts[0]
}

func TestIssue758CooldownUsesLastResetAndPreservesRoute(t *testing.T) {
	for _, test := range []struct {
		name                 string
		rateAfter, loadAfter time.Duration
		reason               string
		status               int
	}{
		{"rate_only", 5 * time.Hour, 0, "owner_rate_limited", http.StatusTooManyRequests},
		{"overload_only", 0, 3 * time.Hour, "owner_overloaded", http.StatusServiceUnavailable},
		{"overload_later", 2 * time.Hour, 5 * time.Hour, "owner_overloaded", http.StatusServiceUnavailable},
		{"rate_later", 5 * time.Hour, 2 * time.Hour, "owner_rate_limited", http.StatusTooManyRequests},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, scheduler, ctx, request, account := issue758WaitFixture(t, nil)
			now := time.Now()
			var expected time.Time
			if test.rateAfter > 0 {
				reset := now.Add(test.rateAfter)
				account.RateLimitResetAt, expected = &reset, reset
			}
			if test.loadAfter > 0 {
				reset := now.Add(test.loadAfter)
				account.OverloadUntil = &reset
				if reset.After(expected) {
					expected = reset
				}
			}
			var refused *OpenAIAgentRecoveryError
			require.ErrorAs(t, scheduler.checkOpenAIAgentRecoveryCooldown(ctx, request, openAIAgentRecoveryFromContext(ctx)), &refused)
			require.Equal(t, test.reason, refused.Reason)
			require.Equal(t, test.status, refused.HTTPStatus())
			require.Equal(t, expected, refused.RetryAt)
			owner := openAIAgentRecoveryFromContext(ctx).owner
			for _, binding := range service.agentMessageRecovery.bindings {
				require.True(t, sameOpenAIAgentRecoveryRoute(binding, *owner))
				require.WithinDuration(t, expected.Add(service.openAIWSSessionStickyTTL()), binding.expiresAt, time.Second)
			}
			// A stale snapshot cannot force the new diagnostic path: the current
			// authoritative account reset has elapsed, so ordinary admission resumes.
			past := now.Add(-time.Minute)
			account.RateLimitResetAt, account.OverloadUntil = &past, &past
			require.NoError(t, scheduler.checkOpenAIAgentRecoveryCooldown(ctx, request, openAIAgentRecoveryFromContext(ctx)))
		})
	}
}

func TestIssue758CooldownCannotDisguiseAdmissionRestrictions(t *testing.T) {
	for _, change := range []string{"group_removed", "model_removed", "group_model_removed", "transport", "manual_pause", "auth_cooldown", "hard_quota", "excluded", "guardian"} {
		t.Run(change, func(t *testing.T) {
			service, scheduler, ctx, request, account := issue758WaitFixture(t, nil)
			resetAt := time.Now().Add(5 * time.Hour)
			account.RateLimitResetAt = &resetAt
			switch change {
			case "group_removed":
				account.GroupIDs = []int64{716}
			case "model_removed":
				account.Credentials["model_mapping"] = map[string]any{"other-model": "other-model"}
			case "group_model_removed":
				account.AccountGroups = []AccountGroup{{GroupID: 715, AllowedModels: []string{"other-model"}}}
			case "transport":
				request.RequiredTransport = OpenAIUpstreamTransportResponsesWebsocketV2
			case "manual_pause":
				account.Schedulable = false
			case "auth_cooldown":
				account.TempUnschedulableUntil = &resetAt
			case "hard_quota":
				account.Extra = map[string]any{"quota_limit": float64(10), "quota_used": float64(10)}
			case "excluded":
				request.ExcludedIDs = map[int64]struct{}{account.ID: {}}
			case "guardian":
				request.GuardianParentAccountID = account.ID + 1
			}
			before := make(map[[32]byte]openAIAgentRecoveryBinding)
			for key, binding := range service.agentMessageRecovery.bindings {
				before[key] = binding
			}
			var refused *OpenAIAgentRecoveryError
			require.ErrorAs(t, scheduler.checkOpenAIAgentRecoveryCooldown(ctx, request, openAIAgentRecoveryFromContext(ctx)), &refused)
			require.Equal(t, http.StatusConflict, refused.HTTPStatus(), "a quota reset cannot restore a revoked or incompatible route")
			require.True(t, refused.RetryAt.IsZero())
			require.Equal(t, before, service.agentMessageRecovery.bindings, "hard refusal must not extend compatibility evidence")
		})
	}
}

func TestIssue758WSOAuthPreviousResponseStillWaits(t *testing.T) {
	for _, reason := range []string{"owner_rate_limited", "owner_overloaded"} {
		t.Run(reason, func(t *testing.T) {
			accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, GroupIDs: []int64{715}, Credentials: map[string]any{"access_token": "fixture-oauth"}}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
			issue715LearnRoute(t, svc, &accounts[0], issue715OpaqueBody)
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(issue715OpaqueBody), "fixture-session", "gpt-6-astra")
			recovery := openAIAgentRecoveryFromContext(ctx)
			require.NotNil(t, recovery.owner)
			require.Zero(t, recovery.userID, "WS ingress has no HTTP authorization marker")
			request := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: int64Ptr(715), SessionHash: "fixture-session", RequestedModel: "gpt-6-astra", PreviousResponseID: "resp_ws758"}
			require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, 715, request.PreviousResponseID, 1, time.Hour))
			resetAt := time.Now().Add(5 * time.Hour)
			if reason == "owner_rate_limited" {
				accounts[0].RateLimitResetAt = &resetAt
			} else {
				accounts[0].OverloadUntil = &resetAt
			}
			var refused *OpenAIAgentRecoveryError
			scheduler := &defaultOpenAIAccountScheduler{service: svc}
			require.ErrorAs(t, scheduler.checkOpenAIAgentRecoveryCooldown(ctx, request, recovery), &refused)
			require.Equal(t, reason, refused.Reason)
			require.Equal(t, resetAt, refused.RetryAt)
			for _, binding := range svc.agentMessageRecovery.bindings {
				require.WithinDuration(t, resetAt.Add(svc.openAIWSSessionStickyTTL()), binding.expiresAt, time.Second)
			}
		})
	}
}

type issue758RetainingOwners struct {
	*issue715SharedOwners
	err   error
	ttl   time.Duration
	calls int
}

func (c *issue758RetainingOwners) RefreshOpenAIAgentRecovery(ctx context.Context, key, route string, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.ttl = ttl
	if c.err != nil {
		return false, c.err
	}
	return c.values[key] == route, ctx.Err()
}

func TestIssue758SharedRetentionFailsClosedWithoutRecreatingEvidence(t *testing.T) {
	for _, state := range []string{"proven", "unsupported", "error", "miss", "changed", "pending", "failed", "conflict"} {
		t.Run(state, func(t *testing.T) {
			base := &issue715SharedOwners{}
			retainer := &issue758RetainingOwners{issue715SharedOwners: base}
			var cache GatewayCache = retainer
			if state == "unsupported" {
				cache = base
			}
			_, scheduler, ctx, request, account := issue758WaitFixture(t, cache)
			recovery := openAIAgentRecoveryFromContext(ctx)
			var key string
			for digest := range recovery.known {
				key = hex.EncodeToString(digest[:])
			}
			require.NotEmpty(t, key)
			switch state {
			case "error":
				retainer.err = errors.New("fixture Redis unavailable")
			case "miss":
				delete(base.values, key)
			case "changed":
				other := *recovery.owner
				other.accountID++
				base.values[key] = encodeOpenAIAgentRecoveryBinding(other)
			case "pending":
				base.values[key] = "pending:fixture-other-producer:" + base.values[key]
			case "failed", "conflict":
				base.values[key] = state
			}
			before := make(map[string]string)
			for key, value := range base.values {
				before[key] = value
			}
			resetAt := time.Now().Add(5 * time.Hour)
			account.RateLimitResetAt = &resetAt
			var refused *OpenAIAgentRecoveryError
			require.ErrorAs(t, scheduler.checkOpenAIAgentRecoveryCooldown(ctx, request, recovery), &refused)
			wantReason := "owner_unavailable"
			switch state {
			case "proven":
				wantReason = "owner_rate_limited"
				require.WithinDuration(t, resetAt, refused.RetryAt, time.Second)
				require.InDelta(t, (6 * time.Hour).Seconds(), retainer.ttl.Seconds(), 2)
			case "unsupported", "error":
				wantReason = "store_unavailable"
			}
			require.Equal(t, wantReason, refused.Reason)
			require.Equal(t, before, base.values, "retention failure must neither recreate a missing grant nor replace another producer's state")
			if state == "unsupported" {
				require.Zero(t, retainer.calls)
			} else {
				require.Equal(t, 1, retainer.calls)
			}
		})
	}
}

func TestIssue758LocalRetentionDoesNotResurrectOrShortenEvidence(t *testing.T) {
	for _, state := range []string{"longer", "miss", "expired", "changed", "pending", "failed", "conflict"} {
		t.Run(state, func(t *testing.T) {
			service, _, ctx, _, _ := issue758WaitFixture(t, nil)
			recovery := openAIAgentRecoveryFromContext(ctx)
			var key [32]byte
			for digest := range recovery.known {
				key = digest
			}
			binding := service.agentMessageRecovery.bindings[key]
			switch state {
			case "longer":
				binding.expiresAt = time.Now().Add(8 * time.Hour)
			case "expired":
				binding.expiresAt = time.Now().Add(-time.Second)
			case "changed":
				binding.accountID++
			case "pending":
				binding.pending = "fixture-other-producer"
			case "failed":
				binding.failed = true
			case "conflict":
				binding.conflicted = true
			}
			service.agentMessageRecovery.bindings[key] = binding
			if state == "miss" {
				delete(service.agentMessageRecovery.bindings, key)
			}
			err := service.retainOpenAIAgentRecovery(ctx, recovery, 6*time.Hour)
			if state == "longer" {
				require.NoError(t, err)
			} else {
				var refused *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &refused)
				require.Equal(t, "owner_unavailable", refused.Reason)
			}
			if state == "miss" {
				require.Empty(t, service.agentMessageRecovery.bindings)
			} else {
				require.Equal(t, binding, service.agentMessageRecovery.bindings[key])
			}
		})
	}
}
