package service

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gocache "github.com/patrickmn/go-cache"
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
				extra["openai_passthrough"] = true
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

func TestOAuthInitialSelectionMixedCatalogKeepsExistingAccountModels(t *testing.T) {
	restricted := Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []string{}, "model_mapping": map[string]any{}}}
	require.Empty(t, supplementUnmappedOpenAIModels([]Account{restricted}, nil, []string{}), "all-restricted group remains authoritatively empty")
	existing := Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.Contains(t, supplementUnmappedOpenAIModels([]Account{restricted, existing}, nil, []string{}), "gpt-6.1-sol", "new restrictions cannot hide an existing unrestricted account's catalog")
}

func TestOAuthInitialSelectionDoesNotApplyCodexNormalizationToOtherPlatforms(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		account := &Account{Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []string{"gpt-6"}, "model_mapping": map[string]any{"gpt-6": "gpt-6"}}}
		require.True(t, account.IsModelSupported("gpt-6"), platform)
	}
}

func TestOAuthInitialSelectionAdminCreatePersistsPolicy(t *testing.T) {
	svc, _ := initialSelectionFixture(t, PlatformGemini, []string{"gemini-3.5-flash"})
	repo := &autoConfigAccountRepo{}
	svc.accountRepo = repo
	created, err := svc.CreateAccount(t.Context(), &CreateAccountInput{Name: "fixture-create", Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}, Concurrency: 1, SkipDefaultGroupBind: true, SkipMixedChannelCheck: true})
	require.NoError(t, err)
	require.Same(t, repo.created, created)
	require.Equal(t, []int64{2}, repo.groups)
	require.True(t, created.IsModelSupported("gemini-3.5-flash"))
	require.False(t, created.IsModelSupported("gemini-excluded"))
}

func TestOAuthInitialSelectionAllPlatformCatalogsRemainEmpty(t *testing.T) {
	groupID := int64(2)
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		account := Account{ID: 3, Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []string{}, "model_mapping": map[string]any{}}}
		repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {account}}}
		svc := &GatewayService{accountRepo: repo, modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute}
		for count := 0; count < 2; count++ {
			models := svc.GetAvailableModels(t.Context(), &groupID, platform)
			require.NotNil(t, models, platform)
			require.Empty(t, models, platform)
			require.Empty(t, ModelListingSource(platform, models, DefaultModelIDsForPlatform(platform)), platform)
		}
	}
}

func TestOAuthInitialSelectionTemplateAliasWithoutImportScope(t *testing.T) {
	input := &CreateAccountInput{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture"}}
	applyOAuthInitialModels(input, []OAuthModelMappingRule{{From: "custom", To: "gemini-3.6-flash"}}, []string{"custom", "gemini-3.6-flash"})
	account := &Account{Platform: input.Platform, Type: input.Type, Credentials: input.Credentials}
	require.True(t, account.IsModelSupported("custom"))
	require.Equal(t, "gemini-3.6-flash", account.GetMappedModel("custom"))
}

type initialSelectionEditRepo struct {
	AccountRepository
	account *Account
}

func (r *initialSelectionEditRepo) GetByID(context.Context, int64) (*Account, error) {
	copy := *r.account
	return &copy, nil
}
func (r *initialSelectionEditRepo) Update(_ context.Context, a *Account) error {
	r.account = a
	return nil
}
func TestOAuthInitialSelectionExistingAccountEditDoesNotReapplyTemplate(t *testing.T) {
	for _, selected := range [][]string{{}, {"gpt-6.1-sol"}} {
		policy, _ := initialSelectionFixture(t, PlatformOpenAI, selected)
		input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		require.NoError(t, policy.ApplyOAuthAutoConfig(t.Context(), input))
		repo := &initialSelectionEditRepo{account: &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "before", Credentials: input.Credentials, Extra: map[string]any{}}}
		policy.accountRepo = repo
		edited := maps.Clone(input.Credentials)
		if len(selected) == 0 {
			delete(edited, "model_mapping")
		}
		updated, err := policy.UpdateAccount(t.Context(), 3, &UpdateAccountInput{Name: "after", Credentials: edited})
		require.NoError(t, err)
		require.Equal(t, "after", updated.Name)
		require.True(t, updated.hasOAuthInitialModelSelection())
		require.False(t, updated.IsModelSupported("gpt-6-luna"))
		edited = maps.Clone(updated.Credentials)
		edited["model_mapping"] = map[string]any{"gpt-6-luna": "gpt-6-luna"}
		updated, err = policy.UpdateAccount(t.Context(), 3, &UpdateAccountInput{Credentials: edited})
		require.NoError(t, err)
		require.False(t, updated.hasOAuthInitialModelSelection())
		require.True(t, updated.IsModelSupported("gpt-6-luna"), "manual mapping takes ownership even with a globally enabled template")
	}
}

func TestOAuthInitialSelectionMixedCatalogAllPlatforms(t *testing.T) {
	groupID := int64(2)
	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformGrok} {
		restricted := Account{ID: 1, Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []string{}, "model_mapping": map[string]any{}}}
		existing := Account{ID: 2, Platform: platform, Type: AccountTypeOAuth}
		repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {restricted, existing}}}
		svc := &GatewayService{accountRepo: repo}
		require.NotEmpty(t, svc.GetAvailableModels(t.Context(), &groupID, platform), "a new restricted account must not hide the old account's models: %s", platform)
	}
}

func TestOAuthInitialSelectionAntigravityFinalThinkingTarget(t *testing.T) {
	body := []byte(`{"model":"custom","messages":[{"role":"user","content":"fixture"}],"thinking":{"type":"enabled","budget_tokens":1024}}`)
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []string{"custom", "claude-sonnet-4-5"}, "model_mapping": map[string]any{"custom": "claude-sonnet-4-5"}}}
	svc := &AntigravityGatewayService{} // nil token provider proves rejection precedes upstream work
	for _, compat := range []bool{false, true} {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodPost, "/fixture", nil)
		if compat {
			_, err := svc.prepareAntigravityCompatCall(t.Context(), c, account, antigravityCompatRequest{originalModel: "custom", claudeBody: body})
			require.Error(t, err)
		} else {
			_, err := svc.Forward(t.Context(), c, account, body, false)
			require.Error(t, err)
		}
		require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "not in whitelist")
	}
}

func TestOAuthInitialSelectionSharedMultiGroupCatalog(t *testing.T) {
	const groupID int64 = 2
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		restricted := Account{ID: 1, Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{OAuthInitialModelSelectionKey: []string{}, "model_mapping": map[string]any{}}}
		repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{groupID: {restricted}}}
		svc := &GatewayService{accountRepo: repo}
		for _, catalogPlatform := range []string{platform, PlatformComposite} {
			models, err := svc.GetGroupModelCatalog(t.Context(), groupID, catalogPlatform)
			require.NoError(t, err)
			require.NotNil(t, models)
			require.Empty(t, models, "strict group catalogs must preserve an explicitly empty initial scope")
		}
	}
}
