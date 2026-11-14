package service

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type qqSetupUsers struct {
	UserRepository
	user *User
}

func (r qqSetupUsers) GetByID(context.Context, int64) (*User, error) { return r.user, nil }

type qqSetupGroups struct {
	GroupRepository
	group  *Group
	groups []Group
}

func (r qqSetupGroups) GetByID(_ context.Context, id int64) (*Group, error) {
	if r.group != nil && r.group.ID == id {
		return r.group, nil
	}
	for i := range r.groups {
		if r.groups[i].ID == id {
			return &r.groups[i], nil
		}
	}
	return nil, ErrGroupNotFound
}
func (r qqSetupGroups) ListActive(context.Context) ([]Group, error) { return r.groups, nil }

type qqSetupAccounts struct {
	AccountRepository
	accounts map[int64][]Account
}

func (r qqSetupAccounts) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	return r.accounts[groupID], nil
}

type qqSetupSettings struct{ SettingRepository }

func (qqSetupSettings) GetValue(context.Context, string) (string, error) { return "", nil }
func (qqSetupSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type qqSetupKeys struct {
	APIKeyRepository
	key  *APIKey
	keys map[int64]*APIKey
}

func (r qqSetupKeys) GetByID(_ context.Context, id int64) (*APIKey, error) {
	if r.keys != nil {
		if key := r.keys[id]; key != nil {
			return key, nil
		}
		return nil, ErrAPIKeyNotFound
	}
	return r.key, nil
}
func (r qqSetupKeys) CountByUserID(context.Context, int64) (int64, error) {
	return int64(len(r.keys)), nil
}
func setupProvider(key *APIKey) *qqBotSiteAI {
	users := qqSetupUsers{user: &User{ID: 7, Status: StatusActive}}
	groups := qqSetupGroups{group: &Group{ID: 2, Status: StatusActive, Platform: PlatformOpenAI}}
	keys := &APIKeyService{userRepo: users, groupRepo: groups, apiKeyRepo: qqSetupKeys{key: key}, cfg: &config.Config{}}
	return &qqBotSiteAI{users: users, groups: groups, keys: keys, baseURL: "http://127.0.0.1:8080/v1"}
}
func TestQQBotManagedKeyReuseNeverResetsSpending(t *testing.T) {
	group := int64(2)
	key := &APIKey{ID: 11, UserID: 7, GroupID: &group, Key: APIKeyStoredValue("raw-secret"), Status: StatusActive, Quota: 20, QuotaUsed: 5}
	p := setupProvider(key)
	old := qqbot.Config{AIConnection: "site", SiteGroupID: 2, SiteAPIKeyID: 11, SiteOwnerID: 7, APIKey: "raw-secret"}
	conn, err := p.Prepare(context.Background(), nil, 7, 2, 20, old)
	require.NoError(t, err)
	require.Equal(t, "raw-secret", conn.Key)
	require.Equal(t, int64(11), conn.KeyID)
	require.False(t, conn.Created)
	require.Equal(t, float64(5), key.QuotaUsed)
	_, err = p.Prepare(context.Background(), nil, 7, 2, 21, old)
	require.Error(t, err)
	expired := time.Now().Add(-time.Hour)
	key.ExpiresAt = &expired
	_, err = p.Prepare(context.Background(), nil, 7, 2, 20, old)
	require.Error(t, err)
	key.ExpiresAt = nil
	key.Key = APIKeyStoredValue("rotated")
	_, err = p.Prepare(context.Background(), nil, 7, 2, 20, old)
	require.Error(t, err)
}
func TestQQBotManagedKeyCreatesInsideCallerTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{})
	require.NoError(t, err)
	mock.ExpectQuery("INSERT INTO api_keys").WithArgs(int64(7), sqlmock.AnyArg(), "QQ bot app", int64(2), StatusActive, float64(20)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(12))
	p := setupProvider(nil)
	conn, err := p.Prepare(context.Background(), tx, 7, 2, 20, qqbot.Config{AppID: "app"})
	require.NoError(t, err)
	require.NotEmpty(t, conn.Key)
	require.Equal(t, int64(12), conn.KeyID)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func qqSetupCompatiblePlatforms() []string {
	platforms := []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformComposite}
	for _, spec := range domain.Platforms() {
		if spec.Gateway == domain.PlatformGatewayOpenAI && spec.ID != PlatformOpenAI {
			platforms = append(platforms, spec.ID)
		}
	}
	return platforms
}

func TestQQBotSiteOptionsExposeCompatiblePlatformsAndRespectAccess(t *testing.T) {
	groups := qqSetupGroups{}
	platforms := qqSetupCompatiblePlatforms()
	allowedGroups := []int64{}
	accounts := map[int64][]Account{}
	for i, platform := range platforms {
		id := int64(i + 1)
		groups.groups = append(groups.groups, Group{ID: id, Name: platform, Platform: platform, Status: StatusActive})
		allowedGroups = append(allowedGroups, id)
		accounts[id] = []Account{{Platform: platform, Credentials: map[string]any{"model_mapping": map[string]any{platform + "-mapped": "upstream-model"}}}}
	}
	filteredID := int64(len(platforms) + 1)
	groups.groups = append(groups.groups,
		Group{ID: filteredID, Platform: PlatformAnthropic, Status: StatusActive},
		Group{ID: filteredID + 1, Platform: PlatformGemini, Status: StatusActive, IsExclusive: true},
		Group{ID: filteredID + 2, Platform: PlatformAntigravity, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
		Group{ID: filteredID + 3, Platform: PlatformOpenAI, Status: StatusDisabled},
		Group{ID: filteredID + 4, Platform: "unsupported-provider", Status: StatusActive},
		Group{ID: filteredID + 5, Platform: PlatformTypeSafe, Status: StatusActive},
	)
	allowedGroups = append(allowedGroups, filteredID+2, filteredID+3, filteredID+4, filteredID+5)
	groups.groups[0].ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"listed-model", "route-alias", "mapped-alias", "wild-*"}}
	groups.groups[0].ModelRoutingEnabled = true
	groups.groups[0].ModelRouting = map[string][]int64{"route-alias": {1}, "denied-alias": {1}, "empty-alias": {}, "wild-*": {1}}
	accounts[1] = []Account{{Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"mapped-alias": "upstream-model", "blocked-model": "upstream-model"}}}}
	users := qqSetupUsers{user: &User{ID: 7, Status: StatusActive, RestrictPublicGroups: true, AllowedGroups: allowedGroups}}
	settings := qqSetupSettings{}
	p := &qqBotSiteAI{
		users: users, groups: groups,
		keys:     &APIKeyService{userRepo: users, groupRepo: groups},
		accounts: qqSetupAccounts{accounts: accounts},
		settings: &SettingService{settingRepo: settings, cfg: &config.Config{}},
		email:    &EmailService{settingRepo: settings},
	}
	options, err := p.Options(context.Background(), 7, qqbot.Config{})
	require.NoError(t, err)
	require.Len(t, options.Groups, len(platforms))
	for i, platform := range platforms {
		group := options.Groups[i]
		require.Equal(t, platform, group.Platform)
		if platform == PlatformGemini {
			require.Equal(t, []string{"chat_completions"}, group.ChatProtocols)
		} else {
			require.Equal(t, []string{"chat_completions", "responses"}, group.ChatProtocols)
		}
		require.NotEmpty(t, group.Models)
		switch platform {
		case PlatformGrok:
			require.Contains(t, group.Models, "grok-4.7")
		case PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformCommandCode, PlatformCline:
			// No catalog exists for these providers: configured aliases must not be
			// mixed with the shared catalog's unrelated Claude fallback.
			require.Equal(t, []string{platform + "-mapped"}, group.Models)
		}
	}
	require.Equal(t, []string{"listed-model", "mapped-alias", "route-alias"}, options.Groups[0].Models)
}

func TestQQBotManagedKeyCompatibleProtocolsReuseExistingKey(t *testing.T) {
	for _, platform := range qqSetupCompatiblePlatforms() {
		protocols := []string{"chat_completions", "responses"}
		if platform == PlatformGemini {
			protocols = []string{"chat_completions"}
		}
		for _, protocol := range protocols {
			t.Run(platform+"/"+protocol, func(t *testing.T) {
				groupID := int64(2)
				key := &APIKey{ID: 11, UserID: 7, GroupID: &groupID, Key: APIKeyStoredValue("raw-secret"), Status: StatusActive, Quota: 20, QuotaUsed: 5}
				p := setupProvider(key)
				groups, ok := p.groups.(qqSetupGroups)
				require.True(t, ok)
				groups.group.Platform = platform
				old := qqbot.Config{AIConnection: "site", SiteGroupID: groupID, SiteAPIKeyID: key.ID, SiteOwnerID: 7, APIKey: "raw-secret", ChatProtocol: protocol}
				conn, err := p.Prepare(context.Background(), nil, 7, groupID, 20, old)
				require.NoError(t, err)
				require.Equal(t, key.ID, conn.KeyID)
				require.Equal(t, "raw-secret", conn.Key)
				require.Equal(t, float64(5), key.QuotaUsed)
				require.False(t, conn.Created)
			})
		}
	}
}

func TestQQBotManagedKeyRejectsUnsupportedProtocolBeforeProvisioning(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint("reuse=", reuse), func(t *testing.T) {
			groupID := int64(2)
			key := &APIKey{ID: 11, UserID: 7, GroupID: &groupID, Key: APIKeyStoredValue("raw-secret"), Status: StatusActive, Quota: 20, QuotaUsed: 5}
			p := setupProvider(key)
			groups, ok := p.groups.(qqSetupGroups)
			require.True(t, ok)
			groups.group.Platform = PlatformGemini
			old := qqbot.Config{ChatProtocol: "responses"}
			if reuse {
				old.AIConnection, old.SiteGroupID, old.SiteAPIKeyID, old.SiteOwnerID, old.APIKey = "site", groupID, key.ID, key.UserID, "raw-secret"
			}
			// No transaction is supplied: a rejected protocol must never reach key insertion.
			conn, err := p.Prepare(context.Background(), nil, 7, groupID, 20, old)
			require.ErrorContains(t, err, "不支持聊天接口 responses")
			require.Empty(t, conn.Key)
			require.Zero(t, conn.KeyID)
			require.Equal(t, StatusActive, key.Status)
			require.Equal(t, float64(5), key.QuotaUsed)
			require.Equal(t, APIKeyStoredValue("raw-secret"), key.Key)
		})
	}
}

func TestQQBotManagedKeyExpandedGroupsKeepAccessAndStatusChecks(t *testing.T) {
	for _, mode := range []string{"inactive", "exclusive", "subscription", "unsupported", "systemone-only"} {
		t.Run(mode, func(t *testing.T) {
			p := setupProvider(nil)
			groups, ok := p.groups.(qqSetupGroups)
			require.True(t, ok)
			group := groups.group
			group.Platform = PlatformAnthropic
			switch mode {
			case "inactive":
				group.Status = StatusDisabled
			case "exclusive":
				group.IsExclusive = true
			case "subscription":
				group.SubscriptionType = SubscriptionTypeSubscription
			case "unsupported":
				group.Platform = "unsupported-provider"
			case "systemone-only":
				group.Platform = PlatformTypeSafe
			}
			_, err := p.Prepare(context.Background(), nil, 7, group.ID, 20, qqbot.Config{ChatProtocol: "chat_completions"})
			require.Error(t, err)
		})
	}
}
