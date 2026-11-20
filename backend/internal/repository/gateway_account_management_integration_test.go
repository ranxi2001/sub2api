//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *AccountRepoSuite) TestGatewayManagementAtomicMembershipAndOAuthExclusion() {
	openai, err := s.client.Group.Create().SetName("gateway-openai").SetPlatform(service.PlatformOpenAI).Save(s.ctx)
	s.Require().NoError(err)
	anthropic, err := s.client.Group.Create().SetName("gateway-anthropic").SetPlatform(service.PlatformAnthropic).Save(s.ctx)
	s.Require().NoError(err)
	composite, err := s.client.Group.Create().SetName("gateway-composite").SetPlatform(service.PlatformComposite).Save(s.ctx)
	s.Require().NoError(err)
	oauth, err := s.client.Group.Create().SetName("gateway-oauth-only").SetPlatform(service.PlatformComposite).SetRequireOauthOnly(true).Save(s.ctx)
	s.Require().NoError(err)
	a := &service.Account{Name: "gateway-account", Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
		Status: service.StatusActive, Schedulable: true, Concurrency: 2,
		Credentials: map[string]any{"upstream_kind": "new_api", "base_url": "https://gateway.example", "api_key": "synthetic-key"}}
	s.Require().NoError(s.repo.CreateWithAccountGroups(s.ctx, a, []service.AccountGroup{
		{GroupID: openai.ID, Priority: 1}, {GroupID: anthropic.ID, Priority: 2}, {GroupID: composite.ID, Priority: 3},
	}))
	stored, err := s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	s.False(stored.Schedulable)
	s.Equal("new_api", stored.Credentials["upstream_kind"])
	s.ElementsMatch([]int64{openai.ID, anthropic.ID, composite.ID}, stored.GroupIDs)
	// A manual/stale flag cannot leak this incomplete type into any eligible pool.
	s.Require().NoError(s.repo.SetSchedulable(s.ctx, a.ID, true))
	candidates, err := s.repo.ListSchedulableByGroupID(s.ctx, openai.ID)
	s.Require().NoError(err)
	s.Empty(candidates)
	candidates, err = s.repo.ListSchedulable(s.ctx)
	s.Require().NoError(err)
	for _, candidate := range candidates {
		s.NotEqual(a.ID, candidate.ID)
	}
	s.Error(s.repo.BindGroups(s.ctx, a.ID, []int64{openai.ID, oauth.ID}))
	s.Error(s.repo.AddToGroup(s.ctx, a.ID, oauth.ID, 1))
	groups := newGroupRepositoryWithSQL(s.client, s.repo.sql)
	s.Error(groups.BindAccountsToGroup(s.ctx, oauth.ID, []int64{a.ID}))
	rejected := *a
	rejected.ID = 0
	rejected.Name = "rejected-gateway"
	s.Error(s.repo.CreateWithAccountGroups(s.ctx, &rejected, []service.AccountGroup{{GroupID: oauth.ID, Priority: 1}}))
	s.Zero(rejected.ID, "rejected group policy is checked before inserting the account")
	stored, err = s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	s.ElementsMatch([]int64{openai.ID, anthropic.ID, composite.ID}, stored.GroupIDs, "rejection preserves prior explicit memberships")
	s.Require().NoError(s.repo.BindGroups(s.ctx, a.ID, []int64{anthropic.ID}))
	stored, err = s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	s.Equal([]int64{anthropic.ID}, stored.GroupIDs, "removal does not infer other platform memberships")
	group, err := groups.GetByID(s.ctx, anthropic.ID)
	s.Require().NoError(err)
	group.RequireOAuthOnly = true
	s.ErrorIs(groups.Update(s.ctx, group), service.ErrGatewayAccountGroup, "group edits cannot strand a gateway in an OAuth-only group")
}
