package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthAutoConfigDisableSchedulingOn429Scope(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind, templatePlatform string
		enabled, disable, observer             bool
		want                                   any
	}{
		{name: "new OpenAI OAuth", enabled: true, disable: true, want: true},
		{name: "unchecked overrides import", enabled: true, want: false},
		{name: "initialization disabled", disable: true},
		{name: "API key", enabled: true, disable: true, kind: AccountTypeAPIKey},
		{name: "another platform", enabled: true, disable: true, platform: PlatformAnthropic},
		{name: "matching non-OpenAI platform", enabled: true, disable: true, platform: PlatformAnthropic, templatePlatform: PlatformAnthropic},
		{name: "observer preserves explicit configuration", enabled: true, disable: true, observer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultOAuthAutoConfig()
			cfg.Enabled, cfg.OpenAIDisableSchedulingOn429, cfg.GroupIDs = tc.enabled, tc.disable, []int64{2}
			if tc.templatePlatform != "" {
				cfg.Platform = tc.templatePlatform
			}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			svc := &adminServiceImpl{
				settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil),
				groupRepo:      autoConfigGroups{group: &Group{ID: 2, Platform: cfg.Platform, Status: StatusActive}},
			}
			original := map[string]any{"keep": "unchanged"}
			if tc.want != nil {
				original[OpenAIDisableSchedulingOn429ExtraKey] = !tc.disable
			}
			input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, GroupIDs: []int64{25}, Extra: original}
			if tc.platform != "" {
				input.Platform = tc.platform
			}
			if tc.kind != "" {
				input.Type = tc.kind
			}
			ctx := t.Context()
			if tc.observer {
				ctx = WithObserverScope(ctx, []int64{25})
			}
			require.NoError(t, svc.ApplyOAuthAutoConfig(ctx, input))
			require.Equal(t, tc.want, input.Extra[OpenAIDisableSchedulingOn429ExtraKey])
			require.Equal(t, "unchanged", input.Extra["keep"])
			if tc.want != nil {
				require.Equal(t, !tc.disable, original[OpenAIDisableSchedulingOn429ExtraKey], "template must not mutate the imported map")
			} else {
				require.NotContains(t, original, OpenAIDisableSchedulingOn429ExtraKey)
			}
		})
	}
}

func TestOAuthAutoConfigDisableSchedulingOn429LegacyAndRoundTrip(t *testing.T) {
	repo := &accountOpsSettingsStub{raw: `{"enabled":false,"platform":"openai"}`}
	cfg, err := GetOAuthAutoConfig(t.Context(), repo)
	require.NoError(t, err)
	require.False(t, cfg.OpenAIDisableSchedulingOn429, "old settings must not opt in")
	svc := NewAccountOpsService(repo, nil, nil)
	svc.autoGroups = autoConfigGroups{}
	for _, rotate := range []bool{false, true} {
		for _, disable := range []bool{false, true} {
			cfg.OpenAITOTPRotateOn429, cfg.OpenAIDisableSchedulingOn429 = rotate, disable
			cfg, err = svc.SaveOAuthAutoConfig(t.Context(), cfg)
			require.NoError(t, err)
			loaded, err := GetOAuthAutoConfig(t.Context(), repo)
			require.NoError(t, err)
			require.Equal(t, disable, loaded.OpenAIDisableSchedulingOn429)
			require.Equal(t, rotate, loaded.OpenAITOTPRotateOn429, "the two policies are independent")
		}
	}
}
