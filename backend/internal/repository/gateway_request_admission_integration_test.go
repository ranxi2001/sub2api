//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *AccountRepoSuite) TestGatewayRequestAdmissionReloadsGroupMembershipAndStatus() {
	group, err := s.client.Group.Create().SetName("gateway-request-admission").SetPlatform(service.PlatformComposite).Save(s.ctx)
	s.Require().NoError(err)
	a := &service.Account{Name: "gateway-admission", Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
		Status: service.StatusActive, Concurrency: 2,
		Credentials: map[string]any{"upstream_kind": "sub2api", "base_url": "https://gateway.example", "api_key": "synthetic-only"}}
	s.Require().NoError(s.repo.CreateWithAccountGroups(s.ctx, a, []service.AccountGroup{{GroupID: group.ID, Priority: 1}}))
	current, liveGroup, err := s.repo.GatewayRequestAccount(s.ctx, a.ID, group.ID)
	s.Require().NoError(err)
	s.Equal([]int64{group.ID}, current.GroupIDs)
	s.False(liveGroup.RequireOAuthOnly)
	s.Require().NoError(s.repo.BindGroups(s.ctx, a.ID, nil))
	_, err = s.client.Group.UpdateOneID(group.ID).SetRequireOauthOnly(true).Save(s.ctx)
	s.Require().NoError(err)
	current, liveGroup, err = s.repo.GatewayRequestAccount(s.ctx, a.ID, group.ID)
	s.Require().NoError(err)
	s.Empty(current.GroupIDs, "reader cannot reuse the pre-removal membership snapshot")
	s.True(liveGroup.RequireOAuthOnly, "terminal admission sees the current group policy")
	_, err = s.client.Account.UpdateOneID(a.ID).SetStatus(service.StatusError).Save(s.ctx)
	s.Require().NoError(err)
	current, _, err = s.repo.GatewayRequestAccount(s.ctx, a.ID, group.ID)
	s.Require().NoError(err)
	s.Equal(service.StatusError, current.Status)
}
