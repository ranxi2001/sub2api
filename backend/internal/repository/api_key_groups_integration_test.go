//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *APIKeyRepoSuite) TestAdditionalGroupReplacementAndPrimaryRemoval() {
	user := s.mustCreateUser("replace-multi@test.invalid")
	first := s.mustCreateGroup("multi-first")
	second := s.mustCreateGroup("multi-second")
	third := s.mustCreateGroup("multi-third")
	key := &service.APIKey{UserID: user.ID, Key: "sk-multi-ctes", Name: "multi", Status: service.StatusActive, GroupID: &first.ID, GroupIDs: []int64{first.ID, second.ID}}
	s.Require().NoError(s.repo.Create(s.ctx, key))
	affected, err := s.repo.UpdateGroupIDByUserAndGroup(s.ctx, user.ID, second.ID, third.ID)
	s.Require().NoError(err)
	s.Equal(int64(1), affected)
	got, err := s.repo.GetByKeyForAuth(s.ctx, key.Key)
	s.Require().NoError(err)
	s.Equal([]int64{first.ID, third.ID}, got.GroupIDs)
	_, err = s.repo.ClearGroupIDByGroupID(s.ctx, first.ID)
	s.Require().NoError(err)
	got, err = s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err)
	s.Require().NotNil(got.GroupID)
	s.Equal(third.ID, *got.GroupID)
	s.Equal([]int64{third.ID}, got.GroupIDs)
}

func (s *APIKeyRepoSuite) TestGroupDeletionRemovesAdditionalKeyBindings() {
	for _, guarded := range []bool{false, true} {
		s.Run(map[bool]string{false: "cascade", true: "if-empty"}[guarded], func() {
			user := s.mustCreateUser("delete-extra-" + map[bool]string{false: "cascade", true: "empty"}[guarded] + "@test.invalid")
			primary := s.mustCreateGroup("keep-primary-" + user.Email)
			deleted := s.mustCreateGroup("delete-extra-" + user.Email)
			other := s.mustCreateGroup("keep-extra-" + user.Email)
			key := &service.APIKey{UserID: user.ID, Key: "sk-delete-extra-" + user.Email, Name: "multi", Status: service.StatusActive, GroupID: &primary.ID, GroupIDs: []int64{primary.ID, deleted.ID, other.ID}}
			s.Require().NoError(s.repo.Create(s.ctx, key))
			groups := newGroupRepositoryWithSQL(s.client, s.repo.sql)
			var err error
			if guarded {
				_, err = groups.DeleteCascadeIfEmpty(s.ctx, deleted.ID)
			} else {
				_, err = groups.DeleteCascade(s.ctx, deleted.ID)
			}
			s.Require().NoError(err)
			var count int
			s.Require().NoError(scanSingleRow(s.ctx, s.client, "SELECT COUNT(*) FROM api_key_groups WHERE api_key_id=$1 AND group_id=$2", []any{key.ID, deleted.ID}, &count))
			s.Zero(count)
			got, err := s.repo.GetByID(s.ctx, key.ID)
			s.Require().NoError(err)
			s.Require().NotNil(got.GroupID)
			s.Equal(primary.ID, *got.GroupID)
			s.Equal([]int64{primary.ID, other.ID}, got.GroupIDs)
		})
	}
}
