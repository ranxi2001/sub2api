package service

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func initialSelectionFixture(t *testing.T, platform string, selected []string) (*adminServiceImpl, OAuthAutoConfig) {
	t.Helper()
	c := DefaultOAuthAutoConfig()
	c.Enabled, c.Platform, c.GroupIDs, c.SelectedModels, c.ModelMappings = true, platform, []int64{2}, selected, nil
	c.Priority, c.LoadFactor, c.Concurrency = 4, 9, 8
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	return &adminServiceImpl{settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil), groupRepo: autoConfigGroups{group: &Group{ID: 2, Platform: platform, Status: StatusActive}}}, c
}

func TestOAuthInitialSelectionImportedMappings(t *testing.T) {
	for _, typed := range []bool{false, true} {
		for _, passthrough := range []bool{false, true} {
			svc, _ := initialSelectionFixture(t, PlatformOpenAI, []string{"gpt-6.1-sol", "gpt-6-luna"})
			var mapping any = map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}
			if typed {
				mapping = map[string]string{"gpt-6.1-sol": "gpt-6.1-sol"}
			}
			credentials := map[string]any{"access_token": "fixture", "model_mapping": mapping}
			extra := map[string]any{}
			if passthrough {
				extra["openai_passthrough_enabled"] = true
			}
			input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: credentials, Extra: extra}
			require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
			account := &Account{Platform: input.Platform, Type: input.Type, Credentials: input.Credentials, Extra: extra}
			require.True(t, account.IsModelSupported("gpt-6.1-sol"))
			require.False(t, account.IsModelSupported("gpt-6-luna"), "selection cannot broaden an imported whitelist, including typed imports and passthrough")
			require.Equal(t, mapping, credentials["model_mapping"], "caller import is immutable")
		}
	}
	for _, mode := range []string{"whitelist", "aliases"} {
		svc, _ := initialSelectionFixture(t, PlatformOpenAI, []string{"gpt-6.1-sol", "custom", "other"})
		input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping_mode": mode, "model_mapping": map[string]any{"gpt-*": "gpt-6-luna", "custom": "gpt-6.1-sol", "other": "gpt-6-luna"}, "compact_model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6-luna"}}}
		require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
		account := &Account{Platform: input.Platform, Type: input.Type, Credentials: input.Credentials}
		require.True(t, account.IsModelSupported("custom"), "selected custom redirect is preserved")
		require.False(t, account.IsModelSupported("other"), "excluded target is removed")
		require.False(t, account.IsModelSupported("gpt-6-luna"))
		require.False(t, account.IsModelSupported("gpt-6.1-sol"), "wildcard target must be selected")
		require.Empty(t, input.Credentials["compact_model_mapping"])
	}
}

func TestOAuthInitialSelectionScopeAndPlatforms(t *testing.T) {
	for platform, model := range map[string]string{PlatformOpenAI: "gpt-6.1-sol", PlatformAnthropic: "claude-sonnet-4-6", PlatformGemini: "gemini-3.5-flash", PlatformAntigravity: "gemini-3.5-flash", PlatformGrok: "grok-4.6"} {
		svc, c := initialSelectionFixture(t, platform, []string{model})
		input := &CreateAccountInput{Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{model: model, "excluded": "excluded"}}, Priority: 88, Concurrency: 1, GroupIDs: []int64{7}}
		require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
		account := &Account{Platform: platform, Type: AccountTypeOAuth, Credentials: input.Credentials}
		require.True(t, account.IsModelSupported(model), platform)
		require.False(t, account.IsModelSupported("excluded"), platform)
		require.Equal(t, c.Priority, input.Priority)
		require.Equal(t, c.Concurrency, input.Concurrency)
		require.Equal(t, c.LoadFactor, *input.LoadFactor)
		require.Equal(t, c.GroupIDs, input.GroupIDs)
		gateway := &GatewayService{}
		require.False(t, gateway.isModelSupportedByAccountWithContext(t.Context(), account, "excluded"))
		require.False(t, gateway.isModelSupportedByAccount(account, "excluded"))
	}
	for _, scope := range []string{"disabled", "other platform", "API key", "observer"} {
		svc, c := initialSelectionFixture(t, PlatformOpenAI, []string{})
		input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Priority: 88, Concurrency: 7, GroupIDs: []int64{7}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6-luna": "gpt-6-luna"}}}
		ctx := t.Context()
		switch scope {
		case "disabled":
			c.Enabled = false
			raw, _ := json.Marshal(c)
			svc.settingService = NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil)
		case "other platform":
			input.Platform = PlatformGemini
		case "API key":
			input.Type = AccountTypeAPIKey
		case "observer":
			ctx = WithObserverScope(ctx, []int64{7})
		}
		require.NoError(t, svc.ApplyOAuthAutoConfig(ctx, input))
		require.NotContains(t, input.Credentials, OAuthInitialModelSelectionKey, scope)
		require.Equal(t, 88, input.Priority, scope)
		require.Equal(t, 7, input.Concurrency, scope)
	}
}

func TestOAuthInitialSelectionTargetsAndDefaults(t *testing.T) {
	for _, model := range []string{"gpt-6", "gpt-5.6", "gpt-5"} {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []string{model}, "model_mapping": map[string]any{model: model}}}
		require.False(t, account.IsModelSupported(model), "Codex normalized target must also be selected: %s", model)
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []string{"gpt-6.1-sol"}, "model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}, "compact_model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6-luna"}}}
	require.True(t, account.IsModelSupported("gpt-6.1-sol"))
	require.False(t, supportsOpenAIRequestModel(account, "gpt-6.1-sol", true))
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		empty := &Account{Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []any{}, "model_mapping": map[string]any{}}}
		require.False(t, empty.IsModelSupported("gemini-3.5-flash"), platform)
		require.False(t, empty.IsModelSupported("grok-imagine-image"), platform)
		require.Empty(t, empty.GetModelMapping(), platform)
		require.Empty(t, mapAntigravityModel(empty, "gemini-3.5-flash"), platform)
	}
}

func TestOAuthInitialSelectionPersistenceRefreshAndManualOwnership(t *testing.T) {
	for _, models := range [][]string{{}, {"gpt-6.1-sol"}} {
		svc, _ := initialSelectionFixture(t, PlatformOpenAI, models)
		input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture-old"}}
		require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
		raw, err := json.Marshal(input.Credentials)
		require.NoError(t, err)
		var stored map[string]any
		require.NoError(t, json.Unmarshal(raw, &stored))
		refreshed := MergeCredentials(stored, map[string]any{"access_token": "fixture-new"})
		require.Equal(t, stored[OAuthInitialModelSelectionKey], refreshed[OAuthInitialModelSelectionKey], "token refresh and reauth merge preserve scope")
		ordinary := maps.Clone(stored)
		delete(ordinary, "access_token")
		if len(models) == 0 {
			delete(ordinary, "model_mapping")
		}
		merged := MergePreservingSensitiveCreds(stored, ordinary)
		require.Equal(t, stored[OAuthInitialModelSelectionKey], merged[OAuthInitialModelSelectionKey], "name-only editor save preserves empty and nonempty scopes")
		for _, change := range []map[string]any{{"model_mapping": map[string]any{"gpt-6-luna": "gpt-6-luna"}}, {"model_mapping_mode": "aliases"}, {"compact_model_mapping": map[string]any{"custom": "gpt-6-luna"}}, {OAuthInitialModelSelectionKey: nil}} {
			edited := maps.Clone(stored)
			maps.Copy(edited, change)
			updated := MergePreservingSensitiveCreds(stored, edited)
			require.NotContains(t, updated, OAuthInitialModelSelectionKey, "manual scope edits take ownership")
		}
	}
}

func TestOAuthInitialSelectionAutomaticSyncAndCRS(t *testing.T) {
	svc, repo, upstream := modelMappingSyncFixture(http.StatusOK, `{"data":[{"id":"gpt-6-luna"}]}`)
	repo.account.Type = AccountTypeOAuth
	repo.account.Credentials[OAuthInitialModelSelectionKey] = []string{}
	for _, automatic := range []bool{true, false} {
		catalog, err := svc.SyncUpstreamModelCatalogWithOptions(t.Context(), repo.account, UpstreamModelSyncOptions{Apply: true, Automatic: automatic})
		require.NoError(t, err)
		require.Equal(t, "account_policy", catalog.Skipped)
	}
	require.Empty(t, upstream.requests)
	require.Zero(t, repo.applyCalls)
	policy, _ := initialSelectionFixture(t, PlatformOpenAI, []string{"gpt-6.1-sol"})
	crsRepo := &autoConfigAccountRepo{}
	syncer := &CRSSyncService{accountRepo: crsRepo, autoConfigure: policy.ApplyOAuthAutoConfig}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}}
	require.NoError(t, syncer.createSyncedAccount(context.Background(), account))
	require.True(t, crsRepo.created.IsModelSupported("gpt-6.1-sol"))
	require.False(t, crsRepo.created.IsModelSupported("gpt-6-luna"))
}

func TestOAuthInitialSelectionCatalog(t *testing.T) {
	for _, selected := range [][]string{{}, {"gpt-6.1-sol"}} {
		svc, _ := initialSelectionFixture(t, PlatformOpenAI, selected)
		input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: input.Credentials}
		projected, err := projectAccountModelAliases([]byte(`{"data":[{"id":"gpt-6.1-sol"},{"id":"gpt-6-luna"}]}`), account, nil, false)
		require.NoError(t, err)
		var catalog struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(projected, &catalog))
		require.Len(t, catalog.Data, len(selected))
	}
}
