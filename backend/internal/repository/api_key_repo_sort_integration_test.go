//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *APIKeyRepoSuite) TestListByUserID_SortByUsageBeforePagination() {
	user := s.mustCreateUser("sort-usage@example.com")
	low := s.mustCreateApiKey(user.ID, "sk-usage-low", "low", nil)
	high := s.mustCreateApiKey(user.ID, "sk-usage-high", "high", nil)
	zero := s.mustCreateApiKey(user.ID, "sk-usage-zero", "zero", nil)
	accountID := mustCreateAPIKeyRepoAccount(s.T(), s.ctx, s.client, "usage-sort")
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	for i, log := range []struct {
		keyID int64
		cost  float64
		at    time.Time
	}{
		{low.ID, 2, start},
		{high.ID, 5, start},
		{low.ID, 100, end},
	} {
		_, err := s.client.UsageLog.Create().SetUserID(user.ID).SetAPIKeyID(log.keyID).
			SetAccountID(accountID).SetRequestID([]string{"usage-low", "usage-high", "usage-outside"}[i]).
			SetModel("test-model").SetActualCost(log.cost).SetCreatedAt(log.at).Save(s.ctx)
		s.Require().NoError(err)
	}
	for _, order := range []string{"asc", "desc"} {
		want := []int64{zero.ID, low.ID, high.ID}
		if order == "desc" {
			want = []int64{high.ID, low.ID, zero.ID}
		}
		for page, id := range want {
			keys, result, err := s.repo.ListByUserID(s.ctx, user.ID, pagination.PaginationParams{
				Page: page + 1, PageSize: 1, SortBy: "usage", SortOrder: order,
			}, service.APIKeyListFilters{UsageStartTime: start, UsageEndTime: end})
			s.Require().NoError(err)
			s.Require().EqualValues(3, result.Total)
			s.Require().Len(keys, 1)
			s.Require().Equal(id, keys[0].ID)
		}
	}
}

func (s *APIKeyRepoSuite) TestListByUserID_SortByNameAsc() {
	user := s.mustCreateUser("sort-name@example.com")
	s.mustCreateApiKey(user.ID, "sk-z", "z-key", nil)
	s.mustCreateApiKey(user.ID, "sk-a", "a-key", nil)

	keys, _, err := s.repo.ListByUserID(s.ctx, user.ID, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "name",
		SortOrder: "asc",
	}, service.APIKeyListFilters{})
	s.Require().NoError(err)
	s.Require().Len(keys, 2)
	s.Require().Equal("a-key", keys[0].Name)
	s.Require().Equal("z-key", keys[1].Name)
}

func (s *APIKeyRepoSuite) TestListByUserID_SortByID() {
	user := s.mustCreateUser("sort-id@example.com")
	first := s.mustCreateApiKey(user.ID, "sk-id-a", "a-key", nil)
	second := s.mustCreateApiKey(user.ID, "sk-id-b", "b-key", nil)

	keys, _, err := s.repo.ListByUserID(s.ctx, user.ID, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "id",
		SortOrder: "desc",
	}, service.APIKeyListFilters{})
	s.Require().NoError(err)
	s.Require().Len(keys, 2)
	s.Require().Equal(second.ID, keys[0].ID)
	s.Require().Equal(first.ID, keys[1].ID)
}
