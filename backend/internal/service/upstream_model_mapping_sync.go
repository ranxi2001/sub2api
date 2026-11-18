package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

const (
	UpstreamModelSyncExtraKey = "upstream_model_sync"
	upstreamModelSyncInterval = 5 * time.Minute
	upstreamModelSyncTimeout  = time.Minute
)

// UpstreamModelMappingSyncRepository keeps automatic reads bounded across
// processes and protects an in-flight discovery from overwriting a credential
// rotation or a concurrent model-mapping edit.
type UpstreamModelMappingSyncRepository interface {
	ClaimAutomaticUpstreamModelSync(ctx context.Context, accountID int64, now, next time.Time) (bool, error)
	UpdateUpstreamModelMappingIfUnchanged(ctx context.Context, expected *Account, mapping map[string]string) (bool, error)
}

type UpstreamModelSyncOptions struct {
	Apply         bool `json:"apply"`
	Automatic     bool `json:"automatic"`
	WhitelistOnly bool `json:"whitelist_only"`
}

// Validate rejects combinations that could silently turn a mapping-only edit
// into metadata discovery or an automatic refresh window.
func (options UpstreamModelSyncOptions) Validate() error {
	if options.WhitelistOnly && (!options.Apply || options.Automatic) {
		return newUpstreamModelSyncConfigError("Whitelist-only sync requires an explicit manual apply", nil)
	}
	if options.Automatic && !options.Apply {
		return newUpstreamModelSyncConfigError("Automatic model sync requires apply=true", nil)
	}
	return nil
}

// SyncUpstreamModelCatalogWithOptions preserves the original discovery-only API
// unless Apply is explicitly requested. An automatic attempt claims its window
// before any upstream request; failures therefore cannot cause a polling storm.
func (s *AccountTestService) SyncUpstreamModelCatalogWithOptions(ctx context.Context, account *Account, options UpstreamModelSyncOptions) (*UpstreamModelCatalog, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if !options.Apply {
		return s.SyncUpstreamModelCatalog(ctx, account)
	}
	if _, observer := ObserverGroupIDs(ctx); observer {
		return nil, ErrObserverScope
	}
	if account == nil || account.ID <= 0 {
		return nil, newUpstreamModelSyncConfigError("A saved account is required to apply upstream models", nil)
	}
	// Credential shadows and explicit OAuth alias mode have a narrower account
	// policy than their parent's full catalog; do not broaden that policy.
	if account.IsCredentialShadow() || account.IsOpenAIModelMappingAliases() || account.hasOAuthInitialModelSelection() {
		return skippedUpstreamModelSync("account_policy", nil), nil
	}
	// Automatic catalog refresh must not broaden an explicit OAuth whitelist.
	// An administrator can still deliberately replace it with an explicit apply.
	if options.Automatic && account.Type == AccountTypeOAuth && account.GetCredential(OpenAIModelMappingModeKey) == "whitelist" && len(rawUpstreamModelMapping(account)) > 0 {
		return skippedUpstreamModelSync("account_policy", nil), nil
	}
	if options.Automatic && (account.Status != StatusActive || !account.Schedulable) {
		return skippedUpstreamModelSync("inactive", nil), nil
	}
	repo, ok := s.accountRepo.(UpstreamModelMappingSyncRepository)
	if !ok {
		return nil, newUpstreamModelSyncInternalError("Upstream model mapping persistence is not configured", nil)
	}

	var nextSyncAt *time.Time
	if options.Automatic {
		now := time.Now().UTC()
		// The persisted gate has whole-second resolution. Round up so another
		// instance cannot admit an attempt fractionally before five minutes.
		next := time.Unix(now.Add(upstreamModelSyncInterval).Unix()+1, 0).UTC()
		claimed, err := repo.ClaimAutomaticUpstreamModelSync(ctx, account.ID, now, next)
		if err != nil {
			return nil, newUpstreamModelSyncInternalError("Failed to claim automatic model sync", err)
		}
		if !claimed {
			latest, err := s.accountRepo.GetByID(ctx, account.ID)
			if err != nil {
				return nil, newUpstreamModelSyncInternalError("Failed to read automatic model sync state", err)
			}
			return skippedUpstreamModelSync("throttled", upstreamModelNextSyncAt(latest)), nil
		}
		nextSyncAt = &next
	}

	ctx, cancel := context.WithTimeout(ctx, upstreamModelSyncTimeout)
	defer cancel()
	var catalog *UpstreamModelCatalog
	var err error
	if options.WhitelistOnly {
		catalog, err = s.fetchWhitelistCatalog(ctx, account)
	} else {
		catalog, err = s.SyncUpstreamModelCatalog(ctx, account)
	}
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return skippedUpstreamModelSync("empty", nextSyncAt), nil
	}
	catalog.NextSyncAt = nextSyncAt
	if catalog.Skipped != "" {
		return catalog, nil
	}
	if catalog.DiscoverySource != "upstream" {
		catalog.Skipped = "unverified_catalog"
		return catalog, nil
	}
	models := dedupeAndSortModelIDs(catalog.Models)
	if len(models) == 0 {
		catalog.Skipped = "empty"
		return catalog, nil
	}
	mapping := reconciledUpstreamModelMapping(rawUpstreamModelMapping(account), models)
	if len(mapping) == 0 {
		catalog.Skipped = "empty"
		return catalog, nil
	}
	if reflect.DeepEqual(rawUpstreamModelMapping(account), mapping) {
		catalog.Applied = true
		return catalog, nil
	}
	applied, err := repo.UpdateUpstreamModelMappingIfUnchanged(ctx, account, mapping)
	if err != nil {
		return nil, newUpstreamModelSyncInternalError("Failed to apply upstream model mapping", err)
	}
	if !applied {
		catalog.Skipped = "account_changed"
		return catalog, nil
	}
	catalog.Applied = true
	catalog.Changed = true
	return catalog, nil
}

// Mapping-only edits never refresh capability snapshots, observations, quality
// state or the automatic cooldown. Use only the authenticated live listing.
func (s *AccountTestService) fetchWhitelistCatalog(ctx context.Context, account *Account) (*UpstreamModelCatalog, error) {
	if account.IsOpenAIAgentIdentity() {
		// The non-BPS request builder can register and persist an identity task.
		// Whitelist-only edits never own that credential lifecycle.
		return nil, newUpstreamModelSyncUnsupportedError("Read-only model discovery does not support agent identity accounts", nil)
	}
	if account.Type == AccountTypeOAuth && (account.Platform == PlatformAntigravity || account.Platform == PlatformGemini) {
		// These providers can refresh credentials, backfill project IDs and
		// update cooldowns while obtaining a token. They have no read-only
		// catalog policy, so reject before entering their credential lifecycle.
		return nil, newUpstreamModelSyncUnsupportedError("Read-only model discovery does not support Gemini or Antigravity OAuth accounts", nil)
	}
	var models []string
	var body []byte
	var err error
	if account.IsExcelBPSEnabled() && s.openaiGatewayService != nil {
		// The display catalog tolerates one source failing. A replacement
		// whitelist needs both native and BPS sources when both routes apply.
		response, fetchErr := s.openaiGatewayService.fetchExcelBPSAccountModelsWithPolicy(ctx, account, true)
		if fetchErr != nil {
			return nil, newUpstreamModelSyncUpstreamError("Excel BPS model discovery failed", fetchErr)
		}
		body = response.Body
		models, _, err = extractUpstreamModelCatalog(body, false)
	} else {
		models, body, err = s.fetchUpstreamModelList(ctx, account)
	}
	if err != nil {
		return nil, err
	}
	// Vertex's list reader consumes every page and returns no raw envelope.
	// Do not replace an entire mapping with a partial single-page catalog.
	var page struct {
		HasMore       bool   `json:"has_more"`
		NextPageToken string `json:"nextPageToken"`
	}
	if len(body) > 0 && json.Unmarshal(body, &page) == nil && (page.HasMore || page.NextPageToken != "") {
		return skippedUpstreamModelSync("unverified_catalog", nil), nil
	}
	return &UpstreamModelCatalog{DiscoverySource: "upstream", Models: visibleUpstreamModelIDs(account, models)}, nil
}

func skippedUpstreamModelSync(reason string, next *time.Time) *UpstreamModelCatalog {
	return &UpstreamModelCatalog{Models: []string{}, Skipped: reason, NextSyncAt: next}
}

func upstreamModelNextSyncAt(account *Account) *time.Time {
	if account == nil {
		return nil
	}
	state, _ := account.Extra[UpstreamModelSyncExtraKey].(map[string]any)
	var seconds int64
	switch value := state["next_attempt_unix"].(type) {
	case float64:
		seconds = int64(value)
	case int64:
		seconds = value
	case json.Number:
		seconds, _ = value.Int64()
	}
	if seconds <= 0 {
		return nil
	}
	next := time.Unix(seconds, 0).UTC()
	return &next
}

// Read the stored declaration, not GetModelMapping's generated compatibility
// aliases or provider defaults. Only administrator-authored redirects survive.
func rawUpstreamModelMapping(account *Account) map[string]string {
	mapping := make(map[string]string)
	switch raw := account.Credentials["model_mapping"].(type) {
	case map[string]any:
		for source, value := range raw {
			if target, ok := value.(string); ok {
				mapping[source] = target
			}
		}
	case map[string]string:
		for source, target := range raw {
			mapping[source] = target
		}
	}
	return mapping
}

func reconciledUpstreamModelMapping(existing map[string]string, models []string) map[string]string {
	result := make(map[string]string, len(models))
	live := make(map[string]bool, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" && !strings.Contains(model, "*") {
			result[model] = model
			live[model] = true
		}
	}
	for source, target := range existing {
		if source == target || strings.TrimSpace(source) == "" {
			continue
		}
		if live[target] {
			result[source] = target
		}
	}
	return result
}
