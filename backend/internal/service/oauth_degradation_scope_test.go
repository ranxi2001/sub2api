//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationAuditDoesNotOverrideNativeModelMapping(t *testing.T) {
	for _, scope := range []string{`["gpt-6-luna"]`, `[]`, `null`} {
		t.Run(scope, func(t *testing.T) {
			extra := map[string]any{}
			require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"models":`+scope+`,"concurrency_cap":10}}`), &extra))
			a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-luna": "gpt-6-luna"}}, Extra: extra}
			require.False(t, a.IsModelSupported("gpt-6.1-sol"), "the native model mapping still limits admission")
			require.True(t, a.IsModelSupported("gpt-6-luna"))
			a.Credentials["model_mapping"] = map[string]any{"gpt-6-luna": "gpt-6.1-sol"}
			require.True(t, a.IsModelSupported("gpt-6-luna"), "an explicit native mapping edit takes effect despite historical action data")
			a.Credentials["model_mapping"] = map[string]any{}
			require.True(t, a.IsModelSupported("gpt-6.1-sol"), "clearing native mapping retains the ordinary default model policy")
		})
	}
}

func TestOAuthDegradationPublicModelsFollowNativeMappingEdits(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-luna": "gpt-6-luna"}}, Extra: map[string]any{}}
	require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"models":["gpt-6-luna"],"concurrency_cap":10}}`), &a.Extra))
	body, err := projectAccountModelAliases([]byte(`{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6.1-sol"}]}`), a, nil, false)
	require.NoError(t, err)
	require.Contains(t, string(body), "gpt-6-luna")
	require.NotContains(t, string(body), "gpt-6.1-sol", "discovery follows the current native whitelist")
	a.Credentials["model_mapping"] = map[string]any{"gpt-6-luna": "gpt-6-luna", "gpt-6.1-sol": "gpt-6.1-sol"}
	body, err = projectAccountModelAliases([]byte(`{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6.1-sol","description":"restored"}]}`), a, nil, false)
	require.NoError(t, err)
	require.Contains(t, string(body), "gpt-6.1-sol", "restoring a native model also restores its discovery")
	require.Contains(t, string(body), `"description":"restored"`)
}

func TestOAuthDegradationAuditDoesNotOverrideCompactOrPassthroughEdits(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"compact_model_mapping": map[string]any{"gpt-6-luna": "gpt-6.1-sol"}}, Extra: map[string]any{}}
	require.NoError(t, json.Unmarshal([]byte(`{"oauth_degradation_action":{"models":["gpt-6-luna"],"concurrency_cap":10}}`), &a.Extra))
	require.True(t, supportsOpenAIRequestModel(a, "gpt-6-luna", false))
	require.True(t, supportsOpenAIRequestModel(a, "gpt-6-luna", true), "native compact mapping determines the outbound model")
	a.Extra["openai_passthrough"] = true
	require.True(t, a.IsModelSupported("gpt-6.1-sol"), "an explicit native passthrough setting must take effect")
	require.True(t, a.IsModelSupported("gpt-6-luna"))
}

func TestOAuthDegradationNativeModelEditRestoresQualityChecks(t *testing.T) {
	a := &Account{ID: 325, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-luna": "gpt-6-luna"}},
		Extra:       map[string]any{OAuthDegradationActionExtraKey: map[string]any{"models": []string{"gpt-6-luna"}, "concurrency_cap": 10}},
	}
	svc := NewScheduledTestService(nil, nil)
	svc.accountTests = &AccountTestService{accountRepo: &qualitySupportAccounts{account: a}}
	plan := multiModelQualityPlan(t, `["gpt-6-astra"]`)
	plan.AccountID = a.ID
	require.ErrorContains(t, svc.validateQualityAccountModels(context.Background(), plan), "账号检测策略不允许所选模型")
	a.Credentials["model_mapping"] = map[string]any{"gpt-6-luna": "gpt-6-luna", "gpt-6-astra": "gpt-6-astra"}
	require.NoError(t, svc.validateQualityAccountModels(context.Background(), plan))
	allowed, blocked, err := svc.eligibleQualityModels(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-6-astra"}, allowed)
	require.Empty(t, blocked, "a restored model must no longer be skipped by scheduled quality checks")
	require.True(t, a.HasOAuthDegradationAction(), "restoration retains the audit record and one-time action marker")
}

func TestOAuthDegradationNativeOAuthWhitelistDoesNotInjectProviderModels(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformAntigravity, PlatformGemini, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			a := &Account{Platform: platform, Type: AccountTypeOAuth,
				Credentials: map[string]any{OpenAIModelMappingModeKey: "whitelist", "model_mapping": map[string]any{"custom-allowed": "custom-allowed"}},
				Extra:       map[string]any{OAuthDegradationActionExtraKey: map[string]any{"models": []string{"custom-allowed"}, "native_controls": true}},
			}
			require.Equal(t, map[string]string{"custom-allowed": "custom-allowed"}, a.GetModelMapping(), "an explicit native whitelist must not gain provider defaults")
			require.True(t, a.IsModelSupported("custom-allowed"))
			require.False(t, a.IsModelSupported("gemini-3-flash"))
			require.False(t, a.IsModelSupported("grok-imagine-image"), "native OAuth whitelists must not inherit media fallbacks")
			a.Credentials["model_mapping"] = map[string]any{"custom-allowed": "custom-allowed", "custom-restored": "custom-restored"}
			require.True(t, a.IsModelSupported("custom-restored"), "native edits must restore models on every OAuth platform")
		})
	}
}

func TestOAuthDegradationNativeWhitelistModeInvalidatesCachedProviderDefaults(t *testing.T) {
	a := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"custom-allowed": "custom-allowed"}}}
	require.Greater(t, len(a.GetModelMapping()), 1, "ordinary Antigravity mappings retain their compatibility defaults")
	a.Credentials[OpenAIModelMappingModeKey] = "whitelist"
	require.Equal(t, map[string]string{"custom-allowed": "custom-allowed"}, a.GetModelMapping(), "an explicit mode edit must invalidate cached provider defaults")
}
func TestOAuthDegradationImportedMarkerCannotFabricateAction(t *testing.T) {
	svc := &adminServiceImpl{}
	input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"oauth_degradation_action": map[string]any{"models": []string{}, "concurrency_cap": 10}, "manual": "keep"}}
	require.NoError(t, svc.ApplyOAuthAutoConfig(context.Background(), input))
	require.NotContains(t, input.Extra, "oauth_degradation_action", "new imports must not fabricate persisted degradation action")
	require.Equal(t, "keep", input.Extra["manual"])
}
