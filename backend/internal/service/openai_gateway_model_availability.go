package service

import (
	"context"
	"strings"
	"time"
)

// DiagnoseModelAvailabilityForPlatform reports whether the requested model
// is configured to be served by any persistently eligible OpenAI-compatible
// account in the group for the given platform (e.g. PlatformOpenAI,
// PlatformGrok). The platform scopes the candidate pool so distinct
// OpenAI-compatible platforms do not cross-contaminate diagnosis results.
// The query bypasses scheduler snapshots and all expiring observations, including
// upstream model rejections. They still block scheduling until expiry, but do
// not establish that the group's configuration cannot serve the model.
//
// Safe to call on the error path: returns {true,true} on any internal
// failure or when the inputs preclude meaningful diagnosis (empty model,
// nil service), so callers stay on the 503 fallback branch.
func (s *OpenAIGatewayService) DiagnoseModelAvailabilityForPlatform(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	if s.accountRepo == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	platform = NormalizeOpenAICompatiblePlatform(platform)
	queryGroupID := groupID
	if primary, restricted := primaryOnlyGroupFromContext(ctx); restricted {
		if groupID != nil && *groupID != primary {
			return ModelAvailabilityDiagnosis{Complete: true}
		}
		queryGroupID = &primary
	}
	includeGrouped := false
	if simpleModeAccountScope(ctx, s.cfg) {
		queryGroupID = nil
		includeGrouped = true
	}
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(
		ctx,
		queryGroupID,
		[]string{platform},
		includeGrouped,
	)
	if err != nil {
		// Conservative fallback so the caller keeps returning 503; we do not
		// want a transient lookup failure to flip into 404 model_not_found.
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{Complete: true}
	now := time.Now()
	for i := range accounts {
		if !accountAllowedByAPIKeyGroup(ctx, &accounts[i]) {
			continue
		}
		diag.HasAccountsInPool = true
		if isRetiredOpenAISubscriptionModel(&accounts[i], resolveOpenAIAccountUpstreamModelForRequest(&accounts[i], requestedModel, false)) {
			diag.HasRetiredSubscriptionModel = true
		}
		// Only configured support can justify a downstream model_not_found.
		// Keep old negative-cache records in the scheduler, not this verdict:
		// otherwise another relay starts a new 30-minute cache near our expiry.
		diag.recordAccount(&accounts[i], accounts[i].IsModelSupportedInGroup(groupID, requestedModel), now)
	}
	return diag
}
