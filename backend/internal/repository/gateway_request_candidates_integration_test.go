//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *AccountRepoSuite) TestGatewayRequestCandidatesRequireLiveExplicitAnthropicGroup() {
	group, err := s.client.Group.Create().SetName("gateway-candidates").SetPlatform(service.PlatformAnthropic).Save(s.ctx)
	s.Require().NoError(err)
	a := &service.Account{Name: "gateway-candidate", Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
		Status: service.StatusActive, Concurrency: 2,
		Credentials: map[string]any{"upstream_kind": "new_api", "base_url": "https://gateway.example", "api_key": "synthetic-only"}}
	s.Require().NoError(s.repo.CreateWithAccountGroups(s.ctx, a, []service.AccountGroup{{GroupID: group.ID}}))
	_, err = s.client.Account.UpdateOneID(a.ID).SetSchedulable(true).Save(s.ctx)
	s.Require().NoError(err)
	accounts, live, err := s.repo.ListGatewayRequestCandidates(s.ctx, group.ID, service.PlatformAnthropic)
	s.Require().NoError(err)
	s.Require().Len(accounts, 1)
	s.Equal(a.ID, accounts[0].ID)
	s.Equal(service.PlatformGateway, accounts[0].Platform)
	s.Equal([]int64{group.ID}, accounts[0].GroupIDs)
	s.False(accounts[0].IsSchedulable(), "SELECT returns a neutral account, never a globally eligible view")
	s.Equal(service.PlatformAnthropic, live.Platform)
	legacyBucket, err := s.repo.ListSchedulableByGroupIDAndPlatform(s.ctx, group.ID, service.PlatformAnthropic)
	s.Require().NoError(err)
	s.Empty(legacyBucket, "shared snapshot queries still exclude gateway")
	for _, target := range []string{service.PlatformOpenAI, service.PlatformGemini, service.PlatformAntigravity, service.PlatformGrok, service.PlatformKimi, service.PlatformComposite} {
		accounts, _, err = s.repo.ListGatewayRequestCandidates(s.ctx, group.ID, target)
		s.Require().NoError(err)
		s.Empty(accounts)
	}
	s.Require().NoError(s.repo.BindGroups(s.ctx, a.ID, nil))
	accounts, _, err = s.repo.ListGatewayRequestCandidates(s.ctx, group.ID, service.PlatformAnthropic)
	s.Require().NoError(err)
	s.Empty(accounts, "warm prior reads cannot retain removed membership")
	_, err = s.client.Group.UpdateOneID(group.ID).SetRequireOauthOnly(true).Save(s.ctx)
	s.Require().NoError(err)
	accounts, live, err = s.repo.ListGatewayRequestCandidates(s.ctx, group.ID, service.PlatformAnthropic)
	s.Require().NoError(err)
	s.Empty(accounts)
	s.True(live.RequireOAuthOnly)
}
