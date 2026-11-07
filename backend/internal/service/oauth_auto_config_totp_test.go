package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthAutoConfigTOTPRotationScope(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind string
		enabled, rotate      bool
		applied              bool
	}{
		{"new OpenAI OAuth", PlatformOpenAI, AccountTypeOAuth, true, true, true},
		{"rotation disabled", PlatformOpenAI, AccountTypeOAuth, true, false, false},
		{"initialization disabled", PlatformOpenAI, AccountTypeOAuth, false, true, false},
		{"API key", PlatformOpenAI, AccountTypeAPIKey, true, true, false},
		{"another OAuth platform", PlatformAnthropic, AccountTypeOAuth, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultOAuthAutoConfig()
			cfg.Enabled, cfg.OpenAITOTPRotateOn429, cfg.GroupIDs = tc.enabled, tc.rotate, []int64{2}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			svc := &adminServiceImpl{
				settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil),
				groupRepo:      autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}},
			}
			original := map[string]any{"keep": "unchanged"}
			input := &CreateAccountInput{Platform: tc.platform, Type: tc.kind, Extra: original}
			require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
			enabled, _ := input.Extra[OpenAITOTPRotateOn429ExtraKey].(bool)
			require.Equal(t, tc.applied, enabled)
			require.NotContains(t, original, OpenAITOTPRotateOn429ExtraKey, "do not mutate imported extras in place")
			require.Equal(t, "unchanged", input.Extra["keep"])
			if tc.enabled && tc.platform == PlatformOpenAI && tc.kind == AccountTypeOAuth {
				// The saved template also overrides an imported opt-in when unchecked.
				input.Extra = map[string]any{OpenAITOTPRotateOn429ExtraKey: !tc.rotate}
				require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
				require.Equal(t, tc.rotate, input.Extra[OpenAITOTPRotateOn429ExtraKey])
			}
		})
	}
}

func TestOAuthAutoConfigTOTPRotationLegacyAndRoundTrip(t *testing.T) {
	repo := &accountOpsSettingsStub{raw: `{"enabled":false,"platform":"openai"}`}
	cfg, err := GetOAuthAutoConfig(t.Context(), repo)
	require.NoError(t, err)
	require.False(t, cfg.OpenAITOTPRotateOn429, "legacy settings must not opt in")
	svc := NewAccountOpsService(repo, nil, nil)
	svc.autoGroups = autoConfigGroups{}
	cfg.OpenAITOTPRotateOn429 = true
	_, err = svc.SaveOAuthAutoConfig(t.Context(), cfg)
	require.NoError(t, err)
	loaded, err := GetOAuthAutoConfig(t.Context(), repo)
	require.NoError(t, err)
	require.True(t, loaded.OpenAITOTPRotateOn429)
}

func TestOAuthInitialModelsAndTOTPRotation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		models         []string
		allowsChosen   bool
		allowsExcluded bool
	}{
		{"legacy", nil, true, true},
		{"selected", []string{"gpt-6.1-sol"}, true, false},
		{"none", []string{}, false, false},
	} {
		for _, rotate := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/rotation-off", true: "/rotation-on"}[rotate], func(t *testing.T) {
				cfg := DefaultOAuthAutoConfig()
				cfg.Enabled, cfg.GroupIDs = true, []int64{2}
				cfg.SelectedModels, cfg.ModelMappings = tc.models, nil
				cfg.OpenAITOTPRotateOn429 = rotate
				raw, err := json.Marshal(cfg)
				require.NoError(t, err)
				repo := &accountOpsSettingsStub{raw: string(raw)}
				loaded, err := GetOAuthAutoConfig(t.Context(), repo)
				require.NoError(t, err)
				require.Equal(t, tc.models, loaded.SelectedModels)
				require.Equal(t, rotate, loaded.OpenAITOTPRotateOn429)
				svc := &adminServiceImpl{
					settingService: NewSettingService(repo, nil),
					groupRepo:      autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}},
				}
				input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
				require.Equal(t, rotate, input.Extra[OpenAITOTPRotateOn429ExtraKey])
				account := &Account{Platform: input.Platform, Type: input.Type, Credentials: input.Credentials}
				require.Equal(t, tc.allowsChosen, account.IsModelSupported("gpt-6.1-sol"))
				require.Equal(t, tc.allowsExcluded, account.IsModelSupported("gpt-6-luna"))
			})
		}
	}
}
