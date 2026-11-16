package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOAuthAPIKeyDispositionScopeAndSnapshot(t *testing.T) {
	for _, scope := range []string{"new", "disabled", "apikey", "other_platform", "observer"} {
		t.Run(scope, func(t *testing.T) {
			cfg := DefaultOAuthAutoConfig()
			cfg.Enabled = scope != "disabled"
			cfg.GroupIDs = []int64{5}
			cfg.APIKeyDisposition.Enabled = true
			cfg.APIKeyDisposition.AccountIDs = []int64{7}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			svc := &adminServiceImpl{settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil), groupRepo: autoConfigGroups{group: &Group{Platform: PlatformOpenAI, Status: StatusActive}}}
			in := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, GroupIDs: []int64{5}, InitialAPIKeyDisposition: &cfg.APIKeyDisposition}
			if scope == "apikey" {
				in.Type = AccountTypeAPIKey
			}
			if scope == "other_platform" {
				in.Platform = PlatformGemini
			}
			ctx := t.Context()
			if scope == "observer" {
				ctx = WithObserverScope(ctx, []int64{5})
			}
			require.NoError(t, svc.ApplyOAuthAutoConfig(ctx, in))
			if scope == "new" {
				require.NotNil(t, in.InitialAPIKeyDisposition)
				require.Equal(t, 180, in.InitialAPIKeyDisposition.PauseMinutes)
				require.Equal(t, []int64{7}, in.InitialAPIKeyDisposition.AccountIDs)
			} else {
				require.Nil(t, in.InitialAPIKeyDisposition)
			}
		})
	}
}
func TestOAuthAPIKeyDispositionValidationAndLegacy(t *testing.T) {
	cfg, err := GetOAuthAutoConfig(t.Context(), &accountOpsSettingsStub{raw: `{"platform":"openai"}`})
	require.NoError(t, err)
	require.False(t, cfg.APIKeyDisposition.Enabled)
	cfg.APIKeyDisposition.Enabled = true
	require.Error(t, ValidateOAuthAutoConfig(cfg))
	cfg.APIKeyDisposition.AccountIDs = []int64{7}
	require.NoError(t, ValidateOAuthAutoConfig(cfg))
	cfg.APIKeyDisposition.DelayMinutes = -1
	require.Error(t, ValidateOAuthAutoConfig(cfg))
	cfg.APIKeyDisposition.Enabled = false
	require.NoError(t, ValidateOAuthAutoConfig(cfg))
}
