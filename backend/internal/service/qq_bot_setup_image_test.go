package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/stretchr/testify/require"
)

func TestQQBotSiteImageOptionsKeepImagesRoutesAndIndependentQuota(t *testing.T) {
	groups := qqSetupGroups{}
	allowed := []int64{}
	for _, spec := range domain.Platforms() {
		id := int64(len(groups.groups) + 1)
		groups.groups = append(groups.groups, Group{ID: id, Name: spec.ID, Platform: spec.ID, Status: StatusActive, AllowImageGeneration: true})
		allowed = append(allowed, id)
	}
	groups.groups = append(groups.groups, Group{ID: 100, Platform: PlatformComposite, Status: StatusActive, AllowImageGeneration: true})
	allowed = append(allowed, 100)
	// These would otherwise use the supported Images route; access and the image
	// gate must still remove them from the image selector.
	groups.groups = append(groups.groups,
		Group{ID: 101, Platform: PlatformOpenAI, Status: StatusActive},
		Group{ID: 102, Platform: PlatformGrok, Status: StatusDisabled, AllowImageGeneration: true},
		Group{ID: 103, Platform: PlatformOpenAI, Status: StatusActive, AllowImageGeneration: true, IsExclusive: true},
		Group{ID: 104, Platform: PlatformGrok, Status: StatusActive, AllowImageGeneration: true, SubscriptionType: SubscriptionTypeSubscription},
		Group{ID: 105, Platform: PlatformOpenAI, Status: StatusActive, AllowImageGeneration: true},
	)
	allowed = append(allowed, 101, 102, 104)
	users := qqSetupUsers{user: &User{ID: 7, Status: StatusActive, RestrictPublicGroups: true, AllowedGroups: allowed}}
	settings := qqSetupSettings{}
	p := &qqBotSiteAI{
		users: users, groups: groups, accounts: qqSetupAccounts{},
		keys: &APIKeyService{userRepo: users, groupRepo: groups, apiKeyRepo: qqSetupKeys{keys: map[int64]*APIKey{
			11: {ID: 11, UserID: 7, Quota: 20, QuotaUsed: 5},
			12: {ID: 12, UserID: 7, Quota: 40, QuotaUsed: 9},
		}}},
		settings: &SettingService{settingRepo: settings, cfg: &config.Config{}},
		email:    &EmailService{settingRepo: settings},
	}
	options, err := p.Options(context.Background(), 7, qqbot.Config{SiteAPIKeyID: 11, SiteOwnerID: 7, ImageSiteAPIKeyID: 12, ImageSiteOwnerID: 7})
	require.NoError(t, err)
	require.Equal(t, float64(20), options.CurrentQuota)
	require.Equal(t, float64(5), options.CurrentUsed)
	require.Equal(t, float64(40), options.CurrentImageQuota)
	require.Equal(t, float64(9), options.CurrentImageUsed)
	platforms := []string{}
	for _, group := range options.ImageGroups {
		platforms = append(platforms, group.Platform)
		require.NotEmpty(t, group.Models)
		for _, model := range group.Models {
			require.True(t, IsGPTImageGenerationModel(model) || isGrokImageGenerationModel(model), model)
		}
	}
	require.ElementsMatch(t, []string{PlatformOpenAI, PlatformGrok, PlatformComposite}, platforms)
	options, err = p.Options(context.Background(), 7, qqbot.Config{ImageSiteAPIKeyID: 12, ImageSiteOwnerID: 99})
	require.NoError(t, err)
	require.Zero(t, options.CurrentImageQuota)
	require.Zero(t, options.CurrentImageUsed)
}

func TestQQBotSiteImageModelCandidatesKeepActualImagesAndAllowlist(t *testing.T) {
	g := Group{Platform: PlatformComposite, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{
		"gpt-image-2", "grok-imagine-image", "gemini-3-pro-image", "picture-alias", "gpt-image-route", "gpt-image-empty", "chat-model", "gpt-image-*",
	}}, ModelRoutingEnabled: true, ModelRouting: map[string][]int64{"gpt-image-route": {1}, "gpt-image-empty": {}, "chat-model": {1}}}
	accounts := []Account{{Platform: PlatformGrok, Credentials: map[string]any{"model_mapping": map[string]any{
		"picture-alias": "gpt-image-2", "blocked-picture": "gpt-image-2", "chat-model": "grok-4.7", "gpt-image-*": "gpt-image-2",
	}}}}
	models := qqBotSiteImageModelCandidates(g, accounts)
	require.Contains(t, models, "gpt-image-2")
	require.Contains(t, models, "grok-imagine-image")
	require.Contains(t, models, "gemini-3-pro-image")
	require.Contains(t, models, "picture-alias")
	require.Contains(t, models, "gpt-image-route")
	require.NotContains(t, models, "chat-model")
	require.NotContains(t, models, "blocked-picture")
	require.NotContains(t, models, "gpt-image-*")
	for _, model := range models {
		require.True(t, g.ModelAllowlist.Allows(model))
	}
	// No image defaults should be injected when the group only allows chat.
	g.ModelAllowlist.Models = []string{"grok-4.7", "claude-opus-5"}
	require.Empty(t, qqBotSiteImageModelCandidates(g, accounts))
	// The OpenAI parser rejects arbitrary public aliases before account mapping.
	g.Platform = PlatformOpenAI
	g.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"picture-alias", "gpt-image-mapped"}}
	accounts[0].Platform = PlatformOpenAI
	accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"picture-alias": "gpt-image-2", "gpt-image-mapped": "gpt-image-2"}}
	require.Equal(t, []string{"gpt-image-mapped"}, qqBotSiteImageModelCandidates(g, accounts))
}

func TestQQBotManagedImageKeyOnlySupportedPlatformsReuseIndependentKey(t *testing.T) {
	platforms := append(domain.ConcretePlatformIDs(), PlatformComposite, "unsupported-provider")
	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			groupID := int64(2)
			key := &APIKey{ID: 12, UserID: 7, GroupID: &groupID, Key: APIKeyStoredValue("image-secret"), Status: StatusActive, Quota: 40, QuotaUsed: 9}
			p := setupProvider(key)
			groups, ok := p.groups.(qqSetupGroups)
			require.True(t, ok)
			group := groups.group
			group.Platform, group.AllowImageGeneration = platform, true
			old := qqbot.Config{AIConnection: "site", SiteAPIKeyID: 11, SiteGroupID: groupID, SiteOwnerID: 999, APIKey: "different-chat-secret", ChatProtocol: "responses",
				ImageAIConnection: "site", ImageSiteAPIKeyID: 12, ImageSiteGroupID: groupID, ImageSiteOwnerID: 7, ImageAPIKey: "image-secret"}
			conn, err := p.PrepareImage(context.Background(), nil, 7, groupID, 40, old)
			if platform != PlatformOpenAI && platform != PlatformGrok && platform != PlatformComposite {
				require.ErrorContains(t, err, "允许生图")
				require.Zero(t, conn.KeyID)
				return
			}
			require.NoError(t, err)
			require.Equal(t, int64(12), conn.KeyID)
			require.Equal(t, "image-secret", conn.Key)
			require.Equal(t, float64(9), key.QuotaUsed)
			require.False(t, conn.Created)
		})
	}
}

func TestQQBotManagedImageKeyCreatesSeparateKeyInCallerTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{})
	require.NoError(t, err)
	mock.ExpectQuery("INSERT INTO api_keys").WithArgs(int64(7), sqlmock.AnyArg(), "QQ bot image app", int64(2), StatusActive, float64(40)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(12))
	p := setupProvider(nil)
	groups, ok := p.groups.(qqSetupGroups)
	require.True(t, ok)
	groups.group.AllowImageGeneration = true
	conn, err := p.PrepareImage(context.Background(), tx, 7, 2, 40, qqbot.Config{AppID: "app", AIConnection: "site", SiteAPIKeyID: 11, SiteGroupID: 2, SiteOwnerID: 7, APIKey: "chat-secret"})
	require.NoError(t, err)
	require.Equal(t, int64(12), conn.KeyID)
	require.NotEmpty(t, conn.Key)
	require.NotEqual(t, "chat-secret", conn.Key)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQQBotManagedImageKeyKeepsAccessIdentityAndSpendingChecks(t *testing.T) {
	for _, mode := range []string{"gate-disabled", "inactive", "exclusive", "subscription", "quota-change", "disabled-key", "expired-key", "exhausted-key", "rotated-key", "wrong-owner"} {
		t.Run(mode, func(t *testing.T) {
			groupID := int64(2)
			key := &APIKey{ID: 12, UserID: 7, GroupID: &groupID, Key: APIKeyStoredValue("image-secret"), Status: StatusActive, Quota: 40, QuotaUsed: 9}
			p := setupProvider(key)
			groups, ok := p.groups.(qqSetupGroups)
			require.True(t, ok)
			group := groups.group
			group.AllowImageGeneration = true
			quota := float64(40)
			switch mode {
			case "gate-disabled":
				group.AllowImageGeneration = false
			case "inactive":
				group.Status = StatusDisabled
			case "exclusive":
				group.IsExclusive = true
			case "subscription":
				group.SubscriptionType = SubscriptionTypeSubscription
			case "quota-change":
				quota++
			case "disabled-key":
				key.Status = StatusDisabled
			case "expired-key":
				expired := time.Now().Add(-time.Hour)
				key.ExpiresAt = &expired
			case "exhausted-key":
				key.QuotaUsed = 40
			case "rotated-key":
				key.Key = APIKeyStoredValue("rotated")
			case "wrong-owner":
				key.UserID = 99
			}
			old := qqbot.Config{ImageAIConnection: "site", ImageSiteAPIKeyID: 12, ImageSiteGroupID: groupID, ImageSiteOwnerID: 7, ImageAPIKey: "image-secret"}
			_, err := p.PrepareImage(context.Background(), nil, 7, groupID, quota, old)
			require.Error(t, err)
		})
	}
}

func TestQQBotManagedImageKeyCountsUncommittedChatKeyAgainstLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	p := setupProvider(nil)
	groups, ok := p.groups.(qqSetupGroups)
	require.True(t, ok)
	groups.group.AllowImageGeneration = true
	p.keys.cfg.APIKeyCreate.MaxActivePerUser = 1
	// The repository stub always sees zero committed keys. The caller's shared
	// transaction sees the chat key before attempting the separate image key.
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM api_keys").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO api_keys").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
	_, err = p.Prepare(context.Background(), tx, 7, 2, 20, qqbot.Config{})
	require.NoError(t, err)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM api_keys").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	_, err = p.PrepareImage(context.Background(), tx, 7, 2, 40, qqbot.Config{})
	require.ErrorIs(t, err, ErrAPIKeyCountExceeded)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}
