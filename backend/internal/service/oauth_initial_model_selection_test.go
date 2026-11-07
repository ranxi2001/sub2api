package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Keep this regression source-compatible with production so the remote RED
// checkout fails on admission assertions, rather than on a new Go symbol.
func TestOAuthInitialModelSelectionAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, selection string
		sol, luna       bool
	}{
		{"legacy", "null", true, true},
		{"selected", `["gpt-6.1-sol"]`, true, false},
		{"none", `[]`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultOAuthAutoConfig()
			c.Enabled, c.GroupIDs, c.ModelMappings = true, []int64{2}, nil
			raw, err := json.Marshal(c)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			var selected any
			require.NoError(t, json.Unmarshal([]byte(tc.selection), &selected))
			fields["selected_models"] = selected
			raw, err = json.Marshal(fields)
			require.NoError(t, err)
			svc := &adminServiceImpl{settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil), groupRepo: autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}}
			input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}}
			require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
			// Real JSON persistence round trip, matching stored account credentials.
			stored, err := json.Marshal(input.Credentials)
			require.NoError(t, err)
			var credentials map[string]any
			require.NoError(t, json.Unmarshal(stored, &credentials))
			account := &Account{Platform: input.Platform, Type: input.Type, Credentials: credentials}
			require.Equal(t, tc.sol, account.IsModelSupported("gpt-6.1-sol"), "selected model admission")
			require.Equal(t, tc.luna, account.IsModelSupported("gpt-6-luna"), "excluded model admission")
		})
	}
}
