package service

import (
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
)

type bulkOpenAISettings struct {
	requestTimezone         bool
	excelBPS                bool
	longContextBilling      bool
	endpointCapabilities    bool
	responsesMode           bool
	capabilitiesIncludeChat bool
	forcedResponsesMode     bool
}

func (s bulkOpenAISettings) any() bool {
	return s.requestTimezone || s.excelBPS || s.longContextBilling || s.endpointCapabilities || s.responsesMode
}

func normalizeBulkOpenAISettings(input *BulkUpdateAccountsInput) (bulkOpenAISettings, error) {
	var settings bulkOpenAISettings
	if input == nil {
		return settings, nil
	}
	if _, exists := input.Extra[openAIRequestTimezoneExtraKey]; exists {
		settings.requestTimezone = true
		if err := ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, input.Extra); err != nil {
			return settings, err
		}
	}
	var err error
	settings.excelBPS, err = normalizeBulkExcelBPSExtra(input.Extra)
	if err != nil {
		return settings, err
	}

	if _, exists := input.Extra[openAILongContextBillingEnabledKey]; exists {
		settings.longContextBilling = true
		if err := ValidateOpenAILongContextBillingExtra(PlatformOpenAI, input.Extra); err != nil {
			return settings, err
		}
	}

	if raw, exists := input.Credentials[openAIEndpointCapabilitiesCredentialKey]; exists {
		settings.endpointCapabilities = true
		capabilities, includeChat, err := normalizeBulkOpenAIEndpointCapabilities(raw)
		if err != nil {
			return settings, err
		}
		settings.capabilitiesIncludeChat = includeChat
		input.Credentials[openAIEndpointCapabilitiesCredentialKey] = capabilities
	}

	if raw, exists := input.Extra[openai_compat.ExtraKeyResponsesMode]; exists {
		settings.responsesMode = true
		mode, forced, err := normalizeBulkOpenAIResponsesMode(raw)
		if err != nil {
			return settings, err
		}
		settings.forcedResponsesMode = forced
		input.Extra[openai_compat.ExtraKeyResponsesMode] = mode
	}

	if settings.endpointCapabilities && !settings.capabilitiesIncludeChat {
		if settings.forcedResponsesMode {
			return settings, infraerrors.BadRequest(
				"OPENAI_RESPONSES_MODE_INVALID",
				"a forced Responses route requires the chat_completions endpoint capability",
			)
		}
		if input.Extra == nil {
			input.Extra = make(map[string]any, 1)
		}
		input.Extra[openai_compat.ExtraKeyResponsesMode] = nil
		settings.responsesMode = true
	}

	return settings, nil
}

// A nil model scope removes the key (all models); an empty list selects no models.
func normalizeBulkExcelBPSExtra(extra map[string]any) (bool, error) {
	changed := false
	if _, exists := extra[ExcelBPS403RecoveryIntervalMinutesKey]; exists {
		changed = true
		if err := validateExcelBPS403RecoveryExtra(extra); err != nil {
			return true, err
		}
	}
	for _, key := range []string{"openai_excel_bps", ExcelBPSIgnoreEncryptedContentKey, "openai_excel_bps_cache_creation_as_input", "openai_excel_bps_auto_disable_on_403", ExcelBPSAutoRecoverOn403Key, ExcelBPSAutoMoveOn403Key, "openai_excel_bps_mihomo"} {
		if raw, exists := extra[key]; exists {
			changed = true
			if _, ok := raw.(bool); !ok {
				return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", key+" must be a boolean")
			}
		}
	}
	if raw, exists := extra[ExcelBPS403TargetGroupIDKey]; exists {
		changed = true
		if _, ok := excelBPS403GroupID(raw); raw != nil && !ok {
			return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", ExcelBPS403TargetGroupIDKey+" must be a nonnegative integer")
		}
	}
	if raw, exists := extra["openai_excel_bps_models"]; exists {
		changed = true
		if raw != nil {
			models := make([]string, 0)
			switch values := raw.(type) {
			case []string:
				models = append(models, values...)
			case []any:
				for _, value := range values {
					model, ok := value.(string)
					if !ok {
						return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", "openai_excel_bps_models must be an array of strings or null")
					}
					models = append(models, model)
				}
			default:
				return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", "openai_excel_bps_models must be an array of strings or null")
			}
			normalized := make([]string, 0, len(models))
			seen := make(map[string]bool, len(models))
			for _, model := range models {
				model = strings.TrimSpace(model)
				if model != "" && !seen[model] {
					normalized = append(normalized, model)
					seen[model] = true
				}
			}
			extra["openai_excel_bps_models"] = normalized
		}
	}
	if enabled, exists := extra["openai_excel_bps"].(bool); exists && !enabled {
		extra["openai_excel_bps_models"] = nil
		if _, exists := extra["openai_excel_bps_mihomo"]; exists {
			extra["openai_excel_bps_mihomo"] = false
		}
		extra["openai_excel_bps_cache_creation_as_input"] = false
		if _, exists := extra[ExcelBPSAutoRecoverOn403Key]; exists {
			extra[ExcelBPSAutoRecoverOn403Key] = false
		}
		if _, exists := extra[ExcelBPSIgnoreEncryptedContentKey]; exists {
			extra[ExcelBPSIgnoreEncryptedContentKey] = false
		}
		if _, exists := extra["openai_excel_bps_auto_disable_on_403"]; exists {
			extra["openai_excel_bps_auto_disable_on_403"] = false
		}
		if _, exists := extra[ExcelBPSAutoMoveOn403Key]; exists {
			extra[ExcelBPSAutoMoveOn403Key] = false
		}
		if _, exists := extra[ExcelBPS403TargetGroupIDKey]; exists {
			extra[ExcelBPS403TargetGroupIDKey] = nil
		}
	}
	return changed, nil
}

func normalizeBulkOpenAIEndpointCapabilities(raw any) (any, bool, error) {
	if raw == nil {
		return nil, true, nil
	}

	values := make([]string, 0, 2)
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, false, invalidBulkOpenAIEndpointCapabilities()
			}
			values = append(values, value)
		}
	case []string:
		values = append(values, typed...)
	default:
		return nil, false, invalidBulkOpenAIEndpointCapabilities()
	}

	selected := make(map[OpenAIEndpointCapability]bool, 4)
	for _, value := range values {
		capability := OpenAIEndpointCapability(value)
		switch capability {
		case OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityEmbeddings,
			OpenAIEndpointCapabilitySeedance, OpenAIEndpointCapabilityVideos:
			selected[capability] = true
		default:
			return nil, false, invalidBulkOpenAIEndpointCapabilities()
		}
	}
	if len(selected) == 0 {
		return nil, false, invalidBulkOpenAIEndpointCapabilities()
	}

	includeChat := selected[OpenAIEndpointCapabilityChatCompletions]
	if len(selected) == 2 && includeChat && selected[OpenAIEndpointCapabilityEmbeddings] {
		return nil, true, nil
	}
	capabilities := make([]string, 0, len(selected))
	for _, capability := range []OpenAIEndpointCapability{
		OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityEmbeddings,
		OpenAIEndpointCapabilitySeedance, OpenAIEndpointCapabilityVideos,
	} {
		if selected[capability] {
			capabilities = append(capabilities, string(capability))
		}
	}
	return capabilities, includeChat, nil
}

func invalidBulkOpenAIEndpointCapabilities() error {
	return infraerrors.BadRequest(
		"OPENAI_ENDPOINT_CAPABILITIES_INVALID",
		"openai_capabilities must contain at least one of chat_completions, embeddings, seedance, or videos",
	)
}

func normalizeBulkOpenAIResponsesMode(raw any) (any, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	mode, ok := raw.(string)
	if !ok {
		return nil, false, invalidBulkOpenAIResponsesMode()
	}
	switch openai_compat.ResponsesSupportMode(mode) {
	case openai_compat.ResponsesSupportModeAuto:
		return nil, false, nil
	case openai_compat.ResponsesSupportModeForceResponses,
		openai_compat.ResponsesSupportModeForceChatCompletions:
		return mode, true, nil
	default:
		return nil, false, invalidBulkOpenAIResponsesMode()
	}
}

func invalidBulkOpenAIResponsesMode() error {
	return infraerrors.BadRequest(
		"OPENAI_RESPONSES_MODE_INVALID",
		"openai_responses_mode must be auto, force_responses, force_chat_completions, or null",
	)
}

func validateBulkOpenAISettingsTargets(
	input *BulkUpdateAccountsInput,
	settings bulkOpenAISettings,
	targetsByID map[int64]*Account,
) (int, error) {
	if input == nil || !settings.any() {
		return 0, nil
	}

	inheritedCount := 0
	for _, accountID := range input.AccountIDs {
		account, ok := targetsByID[accountID]
		if !ok || account == nil {
			return 0, invalidBulkOpenAITarget(accountID, "account does not exist")
		}

		if settings.requestTimezone {
			if err := ValidateOpenAIRequestTimezoneExtra(account.Platform, input.Extra); err != nil {
				return 0, err
			}
		}
		if settings.excelBPS && (account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth ||
			account.IsShadow() || account.IsOpenAIAgentIdentity() || account.IsOpenAIPersonalAccessToken()) {
			return 0, invalidBulkOpenAITarget(accountID, "Excel / BPS requires a regular ChatGPT OAuth account")
		}

		if settings.longContextBilling {
			if account.Platform != PlatformOpenAI || !supportsOpenAILongContextBilling(account.Type) {
				return 0, invalidBulkOpenAITarget(accountID, "long-context billing requires an OpenAI OAuth, setup-token, or API-key account")
			}
			if account.IsShadow() {
				inheritedCount++
			}
		}

		if settings.endpointCapabilities || settings.responsesMode {
			if account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey {
				return 0, invalidBulkOpenAITarget(accountID, "endpoint capabilities and Responses routing require an OpenAI API-key account")
			}
		}

		if settings.forcedResponsesMode && !settings.capabilitiesIncludeChat &&
			!settings.endpointCapabilities &&
			!account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions) {
			return 0, invalidBulkOpenAITarget(accountID, "a forced Responses route requires the chat_completions endpoint capability")
		}
	}

	if settings.longContextBilling && inheritedCount == len(input.AccountIDs) && bulkUpdateOnlyChangesLongContext(input) {
		return 0, infraerrors.BadRequest(
			"OPENAI_LONG_CONTEXT_PARENT_REQUIRED",
			"long-context billing is owned by parent accounts; select at least one parent account",
		)
	}
	return inheritedCount, nil
}

func supportsOpenAILongContextBilling(accountType string) bool {
	switch accountType {
	case AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey:
		return true
	default:
		return false
	}
}

func invalidBulkOpenAITarget(accountID int64, message string) error {
	return infraerrors.BadRequest(
		"OPENAI_BULK_TARGET_INVALID",
		fmt.Sprintf("account %d: %s", accountID, message),
	).WithMetadata(map[string]string{"account_id": strconv.FormatInt(accountID, 10)})
}

func bulkUpdateOnlyChangesLongContext(input *BulkUpdateAccountsInput) bool {
	if input == nil || input.Name != "" || input.ProxyID != nil || input.Concurrency != nil ||
		input.Priority != nil || input.RateMultiplier != nil || input.LoadFactor != nil ||
		input.Status != "" || input.Schedulable != nil || input.GroupIDs != nil ||
		len(input.Credentials) != 0 || input.ProbeEnabled != nil {
		return false
	}
	if len(input.Extra) != 1 {
		return false
	}
	_, ok := input.Extra[openAILongContextBillingEnabledKey]
	return ok
}
