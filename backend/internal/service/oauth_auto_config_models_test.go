package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthModelMappingLegacyAndRoundTrip(t *testing.T) {
	repo := &accountOpsSettingsStub{raw: "{\"enabled\":false,\"platform\":\"openai\"}"}
	c, err := GetOAuthAutoConfig(t.Context(), repo)
	require.NoError(t, err)
	require.Nil(t, c.ModelMappings, "legacy settings do not change account routing before an explicit save")
	require.Equal(t, []OAuthModelMappingRule{{From: "gpt-5.4", To: "gpt-5.5"}}, DefaultOAuthAutoConfig().ModelMappings)
	c.ModelMappings = []OAuthModelMappingRule{{From: "custom", To: "custom-upstream"}}
	svc := NewAccountOpsService(repo, nil, nil)
	svc.autoGroups = autoConfigGroups{}
	c, err = svc.SaveOAuthAutoConfig(t.Context(), c)
	require.NoError(t, err)
	loaded, err := GetOAuthAutoConfig(t.Context(), repo)
	require.NoError(t, err)
	require.Equal(t, c.ModelMappings, loaded.ModelMappings, "removed example rules must not reappear")
	c.ModelMappings = []OAuthModelMappingRule{}
	c, err = svc.SaveOAuthAutoConfig(t.Context(), c)
	require.NoError(t, err)
	loaded, err = GetOAuthAutoConfig(t.Context(), repo)
	require.NoError(t, err)
	require.Empty(t, loaded.ModelMappings)
	c.ModelMappings = nil
	saved, err := svc.SaveOAuthAutoConfig(t.Context(), c)
	require.NoError(t, err)
	require.NotNil(t, saved.ModelMappings, "JSON responses must contain an empty array, not null")
}

func TestOAuthModelMappingValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules []OAuthModelMappingRule
		valid bool
	}{
		{"example", []OAuthModelMappingRule{{"gpt-5.4", "gpt-5.5"}}, true},
		{"trailing wildcard", []OAuthModelMappingRule{{"gpt-5.4*", "gpt-5.5"}}, true},
		{"trim and identity", []OAuthModelMappingRule{{" gpt-5.5 ", "gpt-5.5 "}}, true},
		{"empty", nil, true},
		{"empty source", []OAuthModelMappingRule{{" ", "gpt-5.5"}}, false},
		{"empty target", []OAuthModelMappingRule{{"gpt-5.4", ""}}, false},
		{"duplicate source", []OAuthModelMappingRule{{"gpt-5.4", "one"}, {" gpt-5.4 ", "two"}}, false},
		{"wildcard target", []OAuthModelMappingRule{{"gpt-*", "gpt-*"}}, false},
		{"middle wildcard", []OAuthModelMappingRule{{"gpt-*-latest", "gpt-5.5"}}, false},
		{"multiple wildcards", []OAuthModelMappingRule{{"gpt-**", "gpt-5.5"}}, false},
		{"whitespace", []OAuthModelMappingRule{{"gpt 5.4", "gpt-5.5"}}, false},
		{"control character", []OAuthModelMappingRule{{"gpt-5.4", "gpt-\x005.5"}}, false},
		{"long source", []OAuthModelMappingRule{{strings.Repeat("a", 257), "gpt-5.5"}}, false},
		{"long target", []OAuthModelMappingRule{{"gpt-5.4", strings.Repeat("a", 257)}}, false},
		{"UTF-8 byte limit", []OAuthModelMappingRule{{strings.Repeat("模", 100), "gpt-5.5"}}, false},
		{"too many", make([]OAuthModelMappingRule, 101), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultOAuthAutoConfig()
			c.ModelMappings = tc.rules
			if tc.valid {
				require.NoError(t, ValidateOAuthAutoConfig(c))
			} else {
				require.Error(t, ValidateOAuthAutoConfig(c))
			}
		})
	}
}

func TestOAuthModelMappingScopeAndImportedRules(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind string
		enabled, apply       bool
	}{
		{"new OAuth", PlatformOpenAI, AccountTypeOAuth, true, true},
		{"disabled", PlatformOpenAI, AccountTypeOAuth, false, false},
		{"API key", PlatformOpenAI, AccountTypeAPIKey, true, false},
		{"other platform", PlatformAnthropic, AccountTypeOAuth, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultOAuthAutoConfig()
			// Exercise mapping and allowlist behavior with an available OAuth target.
			c.ModelMappings = []OAuthModelMappingRule{{From: "gpt-5.4", To: "gpt-6.1-sol"}}
			c.Enabled, c.Revision, c.GroupIDs = tc.enabled, "mapping-revision", []int64{2}
			raw, err := json.Marshal(c)
			require.NoError(t, err)
			svc := &adminServiceImpl{settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil), groupRepo: autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}}
			originalMapping := map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}
			originalCredentials := map[string]any{"access_token": "fake-token", "model_mapping": originalMapping}
			input := &CreateAccountInput{Platform: tc.platform, Type: tc.kind, Credentials: originalCredentials, Priority: 13, Concurrency: 7, GroupIDs: []int64{9}}
			require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
			account := &Account{Platform: input.Platform, Type: input.Type, Credentials: input.Credentials}
			if tc.apply {
				require.Equal(t, "gpt-6.1-sol", account.GetMappedModel("gpt-5.4"))
				require.True(t, account.IsModelSupported("gpt-5.4"))
				require.True(t, account.IsModelSupported("gpt-6.1-sol"))
				require.False(t, account.IsModelSupported("gpt-other"), "retain existing allowlist semantics")
				require.Equal(t, c.Revision, input.Extra["auto_config_initial_revision"])
				require.Equal(t, c.Priority, input.Priority)
				require.Equal(t, c.Concurrency, input.Concurrency)
				require.Equal(t, c.GroupIDs, input.GroupIDs)
			} else {
				require.NotContains(t, input.Credentials["model_mapping"], "gpt-5.4")
				require.NotContains(t, input.Extra, "auto_config_initial_revision")
				require.Equal(t, 13, input.Priority)
				require.Equal(t, 7, input.Concurrency)
				require.Equal(t, []int64{9}, input.GroupIDs)
			}
			require.Equal(t, "fake-token", input.Credentials["access_token"])
			require.NotContains(t, originalMapping, "gpt-5.4", "never mutate caller-owned credentials")
		})
	}
	for _, existing := range []any{map[string]any{"gpt-5.4": "custom"}, map[string]string{"gpt-5.4": "custom"}, "invalid"} {
		rules := defaultOAuthModelMappings(PlatformOpenAI)
		input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": existing}}
		require.False(t, applyOAuthModelMappings(input, rules))
		require.Equal(t, existing, input.Credentials["model_mapping"])
	}
}

func TestOAuthModelMappingEmptyCredentialsAndIsolation(t *testing.T) {
	rules := defaultOAuthModelMappings(PlatformOpenAI)
	first := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	second := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.True(t, applyOAuthModelMappings(first, rules))
	require.True(t, applyOAuthModelMappings(second, rules))
	firstMapping, ok := first.Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	secondMapping, ok := second.Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	firstMapping["gpt-5.4"] = "changed"
	require.Equal(t, "gpt-5.5", secondMapping["gpt-5.4"])
	require.Equal(t, "gpt-5.5", rules[0].To)
}

func TestOAuthModelMappingCRSNewAccount(t *testing.T) {
	c := DefaultOAuthAutoConfig()
	c.Enabled, c.GroupIDs = true, []int64{2}
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	policy := &adminServiceImpl{settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil), groupRepo: autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}}
	repo := &autoConfigAccountRepo{}
	syncer := &CRSSyncService{accountRepo: repo, autoConfigure: policy.ApplyOAuthAutoConfig}
	originalMapping := map[string]any{"gpt-5.4": "gpt-5.4", "gpt-5.5": "gpt-5.5"}
	original := map[string]any{"access_token": "fake-token", "model_mapping": originalMapping}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: original, Concurrency: 7}
	require.NoError(t, syncer.createSyncedAccount(t.Context(), account))
	require.Equal(t, "gpt-5.5", repo.created.GetMappedModel("gpt-5.4"))
	require.Equal(t, "fake-token", repo.created.Credentials["access_token"])
	require.Equal(t, "gpt-5.4", originalMapping["gpt-5.4"])
	require.Equal(t, "gpt-5.5", repo.created.GetMappedModel("gpt-5.5"))
	require.Equal(t, c.Concurrency, repo.created.Concurrency)
	require.Equal(t, c.GroupIDs, repo.groups)
}

func TestOAuthModelMappingIdentityImportScope(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind, want string
		enabled                    bool
	}{
		{"enabled OAuth", PlatformOpenAI, AccountTypeOAuth, "gpt-5.5", true},
		{"disabled", PlatformOpenAI, AccountTypeOAuth, "gpt-5.4", false},
		{"API key", PlatformOpenAI, AccountTypeAPIKey, "gpt-5.4", true},
		{"other platform", PlatformAnthropic, AccountTypeOAuth, "gpt-5.4", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultOAuthAutoConfig()
			c.Enabled, c.GroupIDs = tc.enabled, []int64{2}
			raw, err := json.Marshal(c)
			require.NoError(t, err)
			svc := &adminServiceImpl{settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil), groupRepo: autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}}}
			originalMapping := map[string]any{"gpt-5.4": "gpt-5.4", "gpt-5.5": "gpt-5.5", "custom": "custom-upstream"}
			input := &CreateAccountInput{Platform: tc.platform, Type: tc.kind, Credentials: map[string]any{"access_token": "test-token", "model_mapping": originalMapping}}
			require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
			account := &Account{Platform: tc.platform, Type: tc.kind, Credentials: input.Credentials}
			require.Equal(t, tc.want, account.GetMappedModel("gpt-5.4"))
			require.Equal(t, "gpt-5.5", account.GetMappedModel("gpt-5.5"))
			require.Equal(t, "custom-upstream", account.GetMappedModel("custom"))
			require.Equal(t, "test-token", input.Credentials["access_token"])
			require.Equal(t, "gpt-5.4", originalMapping["gpt-5.4"], "do not mutate shared import credentials")
		})
	}
}

func TestOAuthModelMappingIdentityPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping any
		target  string
		want    any
		applied bool
	}{
		{"decoded identity", map[string]any{"gpt-5.4": "gpt-5.4"}, "gpt-5.5", "gpt-5.5", true},
		{"typed identity", map[string]string{"gpt-5.4": "gpt-5.4"}, "gpt-5.5", "gpt-5.5", true},
		{"custom mapping", map[string]any{"gpt-5.4": "custom-upstream"}, "gpt-5.5", "custom-upstream", false},
		{"already matches template", map[string]any{"gpt-5.4": "gpt-5.5"}, "gpt-5.5", "gpt-5.5", false},
		{"identity template no-op", map[string]any{"gpt-5.4": "gpt-5.4"}, "gpt-5.4", "gpt-5.4", false},
		{"non-string target", map[string]any{"gpt-5.4": []string{"invalid"}}, "gpt-5.5", []string{"invalid"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.mapping)
			require.NoError(t, err)
			input := &CreateAccountInput{Credentials: map[string]any{"model_mapping": tc.mapping}}
			applied := applyOAuthModelMappings(input, []OAuthModelMappingRule{{From: "gpt-5.4", To: tc.target}})
			require.Equal(t, tc.applied, applied)
			mapping, ok := input.Credentials["model_mapping"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, tc.want, mapping["gpt-5.4"])
			after, err := json.Marshal(tc.mapping)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "caller-owned mapping must be unchanged")
		})
	}
}
