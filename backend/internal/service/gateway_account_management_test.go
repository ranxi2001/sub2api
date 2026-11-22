//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func gatewayManagementFixture() *Account {
	return &Account{ID: 959, Name: "Gateway fixture", Platform: PlatformGateway, Type: AccountTypeGateway,
		Status: StatusActive, Schedulable: true, Concurrency: 2,
		Credentials: map[string]any{"upstream_kind": "new_api", "base_url": "https://gateway.example/tenant", "api_key": "synthetic-key"}}
}

type gatewayManagementAtomicRepo struct {
	*accountRepoStubForBulkUpdate
	transactions   int
	transactionErr error
}

func (r *gatewayManagementAtomicRepo) WithinGatewayAccountTransaction(ctx context.Context, _ int64, _ []int64, run func(context.Context) error) error {
	r.transactions++
	if r.transactionErr != nil {
		return r.transactionErr
	}
	return run(ctx)
}

func TestGatewayManagementIdentityIsAccountOnly(t *testing.T) {
	a := gatewayManagementFixture()
	require.NoError(t, ValidateGatewayAccountIdentity(a.Platform, a.Type, a.Credentials))
	require.False(t, domain.IsConcretePlatform(PlatformGateway))
	require.False(t, domain.IsGroupPlatform(PlatformGateway))
	for _, kind := range []string{"new_api", "sub2api"} {
		a.Credentials["upstream_kind"] = kind
		require.NoError(t, ValidateGatewayAccountIdentity(a.Platform, a.Type, a.Credentials))
	}
	for _, platform := range []string{PlatformOpenAI, PlatformComposite, ""} {
		require.Error(t, ValidateGatewayAccountIdentity(platform, AccountTypeGateway, a.Credentials))
	}
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeUpstream, AccountTypeOAuth} {
		require.Error(t, ValidateGatewayAccountIdentity(PlatformGateway, kind, a.Credentials))
		require.NoError(t, ValidateGatewayAccountIdentity(PlatformOpenAI, kind, nil), "legacy validation is unchanged")
	}
	for _, field := range []string{"upstream_kind", "base_url", "api_key"} {
		a = gatewayManagementFixture()
		delete(a.Credentials, field)
		require.Error(t, ValidateGatewayAccountIdentity(a.Platform, a.Type, a.Credentials), field)
	}
	for _, address := range []string{"relative/path", "ftp://gateway.example", "https://user:secret@gateway.example", "https://gateway.example?api_key=secret", "https://gateway.example/#secret"} {
		a = gatewayManagementFixture()
		a.Credentials["base_url"] = address
		err := ValidateGatewayAccountIdentity(a.Platform, a.Type, a.Credentials)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret", "invalid configuration must not echo credentials")
	}
}

func TestGatewayManagementGroupsAcceptConcreteAndCompositeRejectOAuthOnly(t *testing.T) {
	a := gatewayManagementFixture()
	for _, platform := range append(domain.ConcretePlatformIDs(), PlatformComposite) {
		g := &Group{ID: 1, Platform: platform}
		require.NoError(t, ValidateGatewayAccountGroup(a, g), platform)
		g.RequireOAuthOnly = true
		require.Error(t, ValidateGatewayAccountGroup(a, g), platform)
	}
	require.Error(t, ValidateGatewayAccountGroup(a, &Group{ID: 1, Platform: PlatformGateway}))
	require.Error(t, ValidateGatewayAccountGroup(a, nil))
	require.NoError(t, ValidateGatewayAccountGroup(&Account{Type: AccountTypeAPIKey}, nil), "do not change legacy policy")
	// A stored/manual schedulable=true cannot bypass the incomplete protocol gate.
	require.False(t, a.IsSchedulable())
}

func TestGatewayManagementCreateValidatesBeforeWritesAndBindsExplicitGroups(t *testing.T) {
	for _, oauthOnly := range []bool{false, true} {
		repo := &accountRepoStubForBulkUpdate{createID: 959}
		groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{
			1: {ID: 1, Platform: PlatformOpenAI}, 2: {ID: 2, Platform: PlatformAnthropic, RequireOAuthOnly: oauthOnly},
			3: {ID: 3, Platform: PlatformComposite},
		}}
		svc := &adminServiceImpl{accountRepo: repo, groupRepo: groups}
		a := gatewayManagementFixture()
		created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Name: a.Name, Platform: a.Platform, Type: a.Type,
			Credentials: a.Credentials, Concurrency: 2, GroupIDs: []int64{1, 2, 3}})
		if oauthOnly {
			require.Error(t, err)
			require.Nil(t, repo.createAccount)
			require.Empty(t, repo.bindGroupsCalls)
		} else {
			require.NoError(t, err)
			require.Equal(t, []int64{1, 2, 3}, repo.bindGroupsByAccount[created.ID])
			require.False(t, created.Schedulable)
		}
	}
	// No implicit default lookup even when no group repository was configured.
	repo := &accountRepoStubForBulkUpdate{createID: 959}
	a := gatewayManagementFixture()
	created, err := (&adminServiceImpl{accountRepo: repo}).CreateAccount(context.Background(), &CreateAccountInput{
		Name: a.Name, Platform: a.Platform, Type: a.Type, Credentials: a.Credentials})
	require.NoError(t, err)
	require.Empty(t, created.GroupIDs)
	require.Empty(t, repo.bindGroupsCalls)
}

func TestGatewayManagementUpdatePreservesKeyAndRejectsIdentityConversion(t *testing.T) {
	a := gatewayManagementFixture()
	a.Credentials["model_mapping"] = map[string]any{"public": "upstream"}
	a.Credentials["unknown_option"] = map[string]any{"keep": true}
	repo := &gatewayManagementAtomicRepo{accountRepoStubForBulkUpdate: &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{959: a}}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), 959, &UpdateAccountInput{
		Name: "Edited gateway", Credentials: map[string]any{"base_url": "https://edited.example"}})
	require.NoError(t, err)
	require.Equal(t, 1, repo.transactions)
	require.Equal(t, "synthetic-key", updated.Credentials["api_key"])
	require.Equal(t, "new_api", updated.Credentials["upstream_kind"])
	require.Equal(t, map[string]any{"public": "upstream"}, updated.Credentials["model_mapping"])
	require.Equal(t, map[string]any{"keep": true}, updated.Credentials["unknown_option"])
	_, err = svc.UpdateAccount(context.Background(), 959, &UpdateAccountInput{Credentials: map[string]any{"api_key": "rotated-synthetic-key"}})
	require.NoError(t, err)
	_, err = svc.UpdateAccount(context.Background(), 959, &UpdateAccountInput{GroupIDs: &[]int64{}})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"public": "upstream"}, a.Credentials["model_mapping"])
	require.Equal(t, map[string]any{"keep": true}, a.Credentials["unknown_option"])
	for _, input := range []*UpdateAccountInput{{Platform: PlatformOpenAI}, {Type: AccountTypeAPIKey}, {Credentials: map[string]any{"upstream_kind": "unknown"}}} {
		repo.updatedAccounts = nil
		_, err := svc.UpdateAccount(context.Background(), 959, input)
		require.Error(t, err)
		require.Empty(t, repo.updatedAccounts)
	}
	legacy, legacyRepo := newPlatformEditService()
	_, err = legacy.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Platform: PlatformGateway, Type: AccountTypeGateway, Credentials: a.Credentials})
	require.Error(t, err)
	require.Empty(t, legacyRepo.updatedAccounts)
}

func TestGatewayManagementUpdateRequiresAtomicCapability(t *testing.T) {
	for _, route := range []string{"admin", "account-service", "bulk"} {
		t.Run(route, func(t *testing.T) {
			a := gatewayManagementFixture()
			repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{a.ID: a}, getByIDsAccounts: []*Account{a}}
			groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{1: {ID: 1, Platform: PlatformOpenAI}}}
			ids := []int64{1}
			admin := &adminServiceImpl{accountRepo: repo, groupRepo: groups}
			switch route {
			case "admin":
				_, err := admin.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{GroupIDs: &ids, Name: "must not save"})
				require.ErrorIs(t, err, ErrGatewayAtomicUpdateRequired)
			case "account-service":
				_, err := NewAccountService(repo, groups).Update(context.Background(), a.ID, UpdateAccountRequest{GroupIDs: &ids})
				require.ErrorIs(t, err, ErrGatewayAtomicUpdateRequired)
			case "bulk":
				result, err := admin.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{a.ID}, GroupIDs: &ids, Name: "must not save", SkipMixedChannelCheck: true})
				require.NoError(t, err)
				require.Equal(t, []int64{a.ID}, result.FailedIDs)
			}
			require.Empty(t, repo.updatedAccounts)
			require.Zero(t, repo.bulkUpdateCalls)
			require.Empty(t, repo.bindGroupsCalls)
		})
	}
}

func TestGatewayManagementMixedBulkRetainsLegacyWritePath(t *testing.T) {
	gateway := gatewayManagementFixture()
	legacy := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	repo := &gatewayManagementAtomicRepo{accountRepoStubForBulkUpdate: &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{legacy, gateway}}, transactionErr: ErrGatewayAccountGroup}
	groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{1: {ID: 1, Platform: PlatformOpenAI}}}
	ids := []int64{1}
	result, err := (&adminServiceImpl{accountRepo: repo, groupRepo: groups}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{legacy.ID, gateway.ID}, GroupIDs: &ids, Name: "fixture edit", SkipMixedChannelCheck: true})
	require.NoError(t, err)
	require.Equal(t, []int64{legacy.ID}, repo.bulkUpdateIDs, "gateway fields must never enter the legacy batch before membership validation")
	require.Equal(t, []int64{legacy.ID}, repo.bindGroupsCalls)
	require.Equal(t, []int64{legacy.ID}, result.SuccessIDs)
	require.Equal(t, []int64{gateway.ID}, result.FailedIDs)
	require.Equal(t, 1, repo.transactions)
}

func TestGatewayManagementAccountServiceRejectsOAuthGroupsBeforeCreate(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{createID: 959}
	groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{1: {ID: 1, Platform: PlatformComposite, RequireOAuthOnly: true}}}
	a := gatewayManagementFixture()
	_, err := NewAccountService(repo, groups).Create(context.Background(), CreateAccountRequest{
		Name: a.Name, Platform: a.Platform, Type: a.Type, Credentials: a.Credentials, GroupIDs: []int64{1}})
	require.Error(t, err)
	require.Nil(t, repo.createAccount)
}

func TestGatewayManagementForwardingRemainsClosed(t *testing.T) {
	a := gatewayManagementFixture()
	ctx := context.Background()
	// Nil services/contexts are deliberate: the guard must run before transport,
	// policy evaluation, model parsing, OAuth replay or any other side effects.
	_, err := (*GatewayService)(nil).Forward(ctx, nil, a, nil)
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	_, err = (*OpenAIGatewayService)(nil).Forward(ctx, nil, a, nil)
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	_, err = (*OpenAIGatewayService)(nil).ForwardAsAnthropic(ctx, nil, a, nil, "", "")
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	_, err = (*OpenAIGatewayService)(nil).ForwardAsChatCompletions(ctx, nil, a, nil, "", "")
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	_, err = (*GeminiMessagesCompatService)(nil).Forward(ctx, nil, a, nil)
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	_, err = (*GeminiMessagesCompatService)(nil).ForwardNative(ctx, nil, a, "fixture", "generateContent", false, nil)
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	_, err = (*AntigravityGatewayService)(nil).Forward(ctx, nil, a, nil, false)
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	_, err = (*AntigravityGatewayService)(nil).ForwardGemini(ctx, nil, a, "fixture", "generateContent", false, nil, false)
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	_, err = (*AntigravityGatewayService)(nil).ForwardUpstream(ctx, nil, a, nil)
	require.ErrorIs(t, err, ErrGatewayProtocolsPending)
	require.ErrorIs(t, (*OpenAIGatewayService)(nil).ForwardCountTokensAsAnthropic(ctx, nil, a, nil, ""), ErrGatewayProtocolsPending)
	require.ErrorIs(t, (*OpenAIGatewayService)(nil).ForwardResponsesInputTokens(ctx, nil, a, nil), ErrGatewayProtocolsPending)
}

func TestGatewayManagementUpdateAndBulkRejectOAuthBindingsBeforeWrites(t *testing.T) {
	for _, path := range []string{"admin", "account-service", "bulk"} {
		t.Run(path, func(t *testing.T) {
			a := gatewayManagementFixture()
			repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{a.ID: a}, getByIDsAccounts: []*Account{a}}
			groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{1: {ID: 1, Platform: PlatformOpenAI, RequireOAuthOnly: true}}}
			ids := []int64{1}
			var err error
			switch path {
			case "admin":
				_, err = (&adminServiceImpl{accountRepo: repo, groupRepo: groups}).UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{GroupIDs: &ids, Name: "must not save"})
			case "account-service":
				_, err = NewAccountService(repo, groups).Update(context.Background(), a.ID, UpdateAccountRequest{GroupIDs: &ids})
			case "bulk":
				_, err = (&adminServiceImpl{accountRepo: repo, groupRepo: groups}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
					AccountIDs: []int64{a.ID}, GroupIDs: &ids, SkipMixedChannelCheck: true, Name: "must not save"})
			}
			require.Error(t, err)
			require.Empty(t, repo.updatedAccounts)
			require.Zero(t, repo.bulkUpdateCalls)
			require.Empty(t, repo.bindGroupsCalls)
		})
	}
}

func TestGatewayManagementBulkRejectsInvalidCredentialMerge(t *testing.T) {
	a := gatewayManagementFixture()
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{a}}
	_, err := (&adminServiceImpl{accountRepo: repo}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{a.ID}, Credentials: map[string]any{"upstream_kind": "unknown"}})
	require.ErrorIs(t, err, ErrGatewayAccountIdentity)
	require.Zero(t, repo.bulkUpdateCalls)
	require.Equal(t, "new_api", a.Credentials["upstream_kind"])
}
