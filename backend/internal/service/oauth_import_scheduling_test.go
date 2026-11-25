package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthImportScheduling(t *testing.T) {
	for _, jsonOn := range []bool{false, true} {
		for _, twoFAOn := range []bool{false, true} {
			t.Run(fmt.Sprintf("json=%t/2fa=%t", jsonOn, twoFAOn), func(t *testing.T) {
				cfg := DefaultOAuthAutoConfig()
				cfg.Enabled, cfg.GroupIDs, cfg.Concurrency = true, []int64{2}, 8
				cfg.JSONImportAutoSchedule = map[string]bool{PlatformOpenAI: jsonOn}
				cfg.TwoFALoginAutoSchedule = map[string]bool{PlatformOpenAI: twoFAOn}
				raw, err := json.Marshal(cfg)
				require.NoError(t, err)
				repo := &accountOpsSettingsStub{raw: string(raw)}
				svc := &adminServiceImpl{settingService: NewSettingService(repo, nil), groupRepo: autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}}
				for source, want := range map[OAuthImportSource]bool{OAuthImportJSON: jsonOn, OAuthImportTwoFA: twoFAOn, "": true} {
					input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, OAuthImportSource: source, Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{"schedulable": true}}
					require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
					account, err := buildAccountForCreate(input, input.Extra)
					require.NoError(t, err)
					require.Equal(t, want, account.Schedulable, "source %q", source)
					require.Equal(t, 8, account.Concurrency)
					require.Equal(t, cfg.GroupIDs, input.GroupIDs)
					require.Equal(t, "fixture", account.Credentials["access_token"])
				}
				cfg.OpenAITwoFAAdmission.Enabled, cfg.OpenAITwoFAAdmission.IsolationGroupID = true, 2
				raw, err = json.Marshal(cfg)
				require.NoError(t, err)
				repo.raw = string(raw)
				input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, OAuthImportSource: OAuthImportTwoFA, Credentials: map[string]any{"access_token": "fixture"}}
				require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
				require.NoError(t, svc.applyOpenAITwoFAAdmission(t.Context(), input))
				state, ok := input.Extra[OpenAITwoFAAdmissionExtraKey].(OpenAITwoFAAdmissionState)
				require.True(t, ok)
				require.Equal(t, !twoFAOn, state.KeepSchedulingOnPass)
				account, err := buildAccountForCreate(input, input.Extra)
				require.NoError(t, err)
				require.False(t, account.Schedulable, "admission starts pending even when auto scheduling is enabled")
			})
		}
	}
}

func TestOAuthJSONImportMissingCredentialStaysPaused(t *testing.T) {
	cfg := DefaultOAuthAutoConfig()
	input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, OAuthImportSource: OAuthImportJSON,
		Credentials: map[string]any{"email": "fixture@example.invalid", "access_token": "  "}}
	require.False(t, *cfg.importScheduling(input))
	input.Credentials["refresh_token"] = "fixture"
	require.True(t, *cfg.importScheduling(input), "legacy settings allow imports with a refresh credential")
}
