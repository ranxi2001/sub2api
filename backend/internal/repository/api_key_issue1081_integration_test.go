//go:build integration

package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *APIKeyRepoSuite) TestIssue1081StoredBindingsSurviveToggleAndLegacyEdits() {
	user := s.mustCreateUser("issue1081@test.invalid")
	primary := s.mustCreateGroup("issue1081-primary")
	extra := s.mustCreateGroup("issue1081-extra")
	key := &service.APIKey{UserID: user.ID, Key: "sk-issue1081-fixture", Name: "fixture", Status: service.StatusActive, GroupID: &primary.ID, GroupIDs: []int64{primary.ID, extra.ID}, QuotaUsed: 12, Usage5h: 4}
	s.Require().NoError(s.repo.Create(s.ctx, key))
	// Create intentionally initializes rolling usage to zero; establish real
	// persisted pre-existing usage before testing preservation.
	s.Require().NoError(s.repo.Update(s.ctx, key, service.APIKeyUpdateFields{RateLimitUsage: true}))
	settings := NewSettingRepository(s.client)
	cfg := &config.Config{}
	svc := service.NewAPIKeyService(s.repo, newUserRepositoryWithSQL(s.client, s.repo.sql), newGroupRepositoryWithSQL(s.client, s.repo.sql), nil, nil, nil, cfg)
	svc.SetGroupPolicy(service.NewSettingService(settings, nil))
	for _, enabled := range []string{"false", "true", "false"} {
		s.Require().NoError(settings.Set(s.ctx, service.SettingKeyAPIKeyMultiGroupEnabled, enabled))
		auth, err := svc.GetByKeyUncached(s.ctx, key.Key)
		s.Require().NoError(err)
		expected := []int64{primary.ID}
		if enabled == "true" {
			expected = append(expected, extra.ID)
		}
		s.Equal(expected, auth.ConfiguredGroupIDs())
		name := "renamed"
		updated, err := svc.Update(s.ctx, key.ID, user.ID, service.UpdateAPIKeyRequest{Name: &name, GroupID: &primary.ID})
		s.Require().NoError(err)
		s.Equal([]int64{primary.ID, extra.ID}, updated.GroupIDs)
		saved, err := s.repo.GetByID(s.ctx, key.ID)
		s.Require().NoError(err)
		s.Equal([]int64{primary.ID, extra.ID}, saved.GroupIDs)
		s.Equal(float64(12), saved.QuotaUsed)
		s.Equal(float64(4), saved.Usage5h)
		var count int
		s.Require().NoError(scanSingleRow(s.ctx, s.client, "SELECT COUNT(*) FROM api_key_groups WHERE api_key_id=$1 AND group_id=$2", []any{key.ID, extra.ID}, &count))
		s.Equal(1, count)
	}
	// The production regional source uses the same transaction as settings/keys.
	source := &regionalAuthSource{client: s.client}
	enabled, err := source.APIKeyMultiGroupEnabled(s.ctx)
	s.Require().NoError(err)
	s.False(enabled)
}
