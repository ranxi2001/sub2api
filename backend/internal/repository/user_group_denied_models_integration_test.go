//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type userGroupDeniedModelsFixture struct {
	ctx     context.Context
	repo    *userGroupRateRepository
	tx      sqlExecutor
	groupID int64
	userA   int64
	userB   int64
}

func newUserGroupDeniedModelsFixture(t *testing.T) *userGroupDeniedModelsFixture {
	t.Helper()
	// Rate/RPM synchronization owns a real SQL transaction. Use the public
	// constructor and committed, uniquely named rows, then clean up only those
	// rows; an outer ent transaction cannot stand in for the repository's DB.
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	group := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("denied-models-%d", suffix)})
	userA := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("denied-a-%d@example.invalid", suffix)})
	userB := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("denied-b-%d@example.invalid", suffix)})
	t.Cleanup(func() {
		ctx := context.Background()
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM user_group_rate_multipliers WHERE group_id=$1", group.ID)
		require.NoError(t, err)
		require.NoError(t, client.Group.DeleteOneID(group.ID).Exec(ctx))
		require.NoError(t, client.User.DeleteOneID(userA.ID).Exec(ctx))
		require.NoError(t, client.User.DeleteOneID(userB.ID).Exec(ctx))
	})
	return &userGroupDeniedModelsFixture{
		ctx:     context.Background(),
		repo:    NewUserGroupRateRepository(integrationDB).(*userGroupRateRepository),
		tx:      integrationDB,
		groupID: group.ID,
		userA:   userA.ID,
		userB:   userB.ID,
	}
}

func (f *userGroupDeniedModelsFixture) rowCount(t *testing.T) int {
	t.Helper()
	var count int
	require.NoError(t, scanSingleRow(f.ctx, f.tx,
		"SELECT COUNT(*) FROM user_group_rate_multipliers WHERE group_id = $1", []any{f.groupID}, &count))
	return count
}

func TestUserGroupDeniedModels_SyncReadAndClear(t *testing.T) {
	f := newUserGroupDeniedModelsFixture(t)

	require.NoError(t, f.repo.SyncGroupDeniedModels(f.ctx, f.groupID, []service.GroupUserDeniedModelsInput{
		{UserID: f.userA, DeniedModels: []string{"gpt-6-luna", "gpt-6-*"}},
		{UserID: f.userB, DeniedModels: nil},
	}))

	denied, err := f.repo.GetDeniedModelsByUserAndGroup(f.ctx, f.userA, f.groupID)
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-6-luna", "gpt-6-*"}, denied)

	denied, err = f.repo.GetDeniedModelsByUserAndGroup(f.ctx, f.userB, f.groupID)
	require.NoError(t, err)
	require.Nil(t, denied, "清单为空的用户不写行")
	require.Equal(t, 1, f.rowCount(t))

	byGroup, err := f.repo.GetDeniedModelsByUserID(f.ctx, f.userA)
	require.NoError(t, err)
	require.Equal(t, map[int64][]string{f.groupID: {"gpt-6-luna", "gpt-6-*"}}, byGroup)

	entries, err := f.repo.GetByGroupID(f.ctx, f.groupID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, f.userA, entries[0].UserID)
	require.Nil(t, entries[0].RateMultiplier)
	require.Nil(t, entries[0].RPMOverride)
	require.Equal(t, []string{"gpt-6-luna", "gpt-6-*"}, entries[0].DeniedModels)

	// 整组覆盖：没列出的用户恢复为不限制，整行 NULL 后删除
	require.NoError(t, f.repo.SyncGroupDeniedModels(f.ctx, f.groupID, []service.GroupUserDeniedModelsInput{
		{UserID: f.userB, DeniedModels: []string{"claude-opus-*"}},
	}))
	denied, err = f.repo.GetDeniedModelsByUserAndGroup(f.ctx, f.userA, f.groupID)
	require.NoError(t, err)
	require.Nil(t, denied)
	require.Equal(t, 1, f.rowCount(t))

	require.NoError(t, f.repo.ClearGroupDeniedModels(f.ctx, f.groupID))
	require.Equal(t, 0, f.rowCount(t))
}

// 三类覆盖共用一行：清理倍率 / RPM 时不能把只剩禁用模型的行删掉，反过来也一样。
func TestUserGroupDeniedModels_SharedRowSurvivesOtherOverrideCleanup(t *testing.T) {
	f := newUserGroupDeniedModelsFixture(t)
	rate := 0.5
	rpm := 30

	require.NoError(t, f.repo.SyncGroupDeniedModels(f.ctx, f.groupID, []service.GroupUserDeniedModelsInput{
		{UserID: f.userA, DeniedModels: []string{"gpt-6-luna"}},
	}))

	// 专属倍率的整组同步、用户维度同步、RPM 的同步与清空都不能带走禁用模型
	require.NoError(t, f.repo.SyncGroupRateMultipliers(f.ctx, f.groupID, nil))
	require.NoError(t, f.repo.SyncUserGroupRates(f.ctx, f.userA, map[int64]*float64{}))
	require.NoError(t, f.repo.SyncUserGroupRates(f.ctx, f.userA, map[int64]*float64{f.groupID: nil}))
	require.NoError(t, f.repo.SyncGroupRPMOverrides(f.ctx, f.groupID, nil))
	require.NoError(t, f.repo.SyncGroupRPMOverrides(f.ctx, f.groupID, []service.GroupRPMOverrideInput{{UserID: f.userA, RPMOverride: nil}}))
	require.NoError(t, f.repo.ClearGroupRPMOverrides(f.ctx, f.groupID))

	denied, err := f.repo.GetDeniedModelsByUserAndGroup(f.ctx, f.userA, f.groupID)
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-6-luna"}, denied)

	// 同一行再加上倍率与 RPM，然后清掉禁用模型：倍率与 RPM 必须保留
	require.NoError(t, f.repo.SyncGroupRateMultipliers(f.ctx, f.groupID, []service.GroupRateMultiplierInput{{UserID: f.userA, RateMultiplier: rate}}))
	require.NoError(t, f.repo.SyncGroupRPMOverrides(f.ctx, f.groupID, []service.GroupRPMOverrideInput{{UserID: f.userA, RPMOverride: &rpm}}))
	require.NoError(t, f.repo.ClearGroupDeniedModels(f.ctx, f.groupID))
	require.NoError(t, f.repo.SyncGroupDeniedModels(f.ctx, f.groupID, nil))

	gotRate, err := f.repo.GetByUserAndGroup(f.ctx, f.userA, f.groupID)
	require.NoError(t, err)
	require.NotNil(t, gotRate)
	require.InDelta(t, rate, *gotRate, 1e-9)
	gotRPM, err := f.repo.GetRPMOverrideByUserAndGroup(f.ctx, f.userA, f.groupID)
	require.NoError(t, err)
	require.NotNil(t, gotRPM)
	require.Equal(t, rpm, *gotRPM)
	denied, err = f.repo.GetDeniedModelsByUserAndGroup(f.ctx, f.userA, f.groupID)
	require.NoError(t, err)
	require.Nil(t, denied)
	require.Equal(t, 1, f.rowCount(t))
}
