//go:build integration

package qqbot

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func observedBinding(t *testing.T, s *Service, uid int64, member string) UnbindRequest {
	t.Helper()
	b, _, err := s.Binding(context.Background(), "app", uid)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.Equal(t, member, b.MemberID)
	return UnbindRequest{AppID: b.AppID, MemberID: b.MemberID, CreatedAt: b.CreatedAt}
}

func TestQQBotAdminBindingListSearchAndPagination(t *testing.T) {
	s, _, c, _, uid := emailFixture(t)
	ctx := context.Background()
	bind(t, s.db, uid, "alice-member")
	_, err := s.db.Exec(`UPDATE qq_bot_bindings SET nickname='Alice 昵称' WHERE user_id=$1`, uid)
	require.NoError(t, err)
	other := user(t, s.db)
	bind(t, s.db, other, "second-member")
	_, err = s.db.Exec(`UPDATE users SET email='second_%@example.com',status='disabled',deleted_at=NOW() WHERE id=$1`, other)
	require.NoError(t, err)
	c.Enabled = false
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	page, err := s.ListBindings(ctx, "", 1, 1)
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.Len(t, page.Items, 1)
	next, err := s.ListBindings(ctx, "", 2, 1)
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	require.NotEqual(t, page.Items[0].UserID, next.Items[0].UserID)
	for _, search := range []string{"ALICE@", "昵称", "alice-member", strconv.FormatInt(uid, 10)} {
		p, err := s.ListBindings(ctx, search, 1, 20)
		require.NoError(t, err)
		require.Equal(t, 1, p.Total)
		require.Equal(t, uid, p.Items[0].UserID)
	}
	p, err := s.ListBindings(ctx, "_%", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, p.Total)
	require.True(t, p.Items[0].UserDeleted)
	require.Equal(t, "disabled", p.Items[0].UserStatus)
	p, err = s.ListBindings(ctx, "group", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 2, p.Total)
	p, err = s.ListBindings(ctx, "' OR 1=1 --", 1, 20)
	require.NoError(t, err)
	require.Empty(t, p.Items)
}

func TestQQBotAdminUnbindRevokesRequestsAndPreservesRewards(t *testing.T) {
	s, m, c, e, uid := emailFixture(t)
	ctx := context.Background()
	requestBindingEmail(s, ctx, c, e, "alice@example.com")
	token := m.token(t)
	bind(t, s.db, uid, "member")
	code, err := s.CreateBindingCode(ctx, uid)
	require.NoError(t, err)
	s.claimBinding(ctx, c, e, code.Code)
	other := user(t, s.db)
	otherCode, err := s.CreateBindingCode(ctx, other)
	require.NoError(t, err)
	s.claimBinding(ctx, c, e, otherCode.Code)
	_, err = s.reward(ctx, c, Job{AppID: c.AppID, MemberID: "member", GroupID: "group"}, "checkin")
	require.NoError(t, err)
	req := observedBinding(t, s, uid, "member")
	wrong := req
	wrong.CreatedAt = wrong.CreatedAt.Add(-time.Second)
	require.ErrorIs(t, s.Unbind(ctx, uid, wrong), ErrBindingChanged)
	require.ErrorIs(t, s.Unbind(ctx, other, req), ErrBindingChanged)
	require.NoError(t, s.Unbind(ctx, uid, req))
	require.ErrorIs(t, s.Unbind(ctx, uid, req), ErrBindingChanged)
	_, err = s.PreviewEmailBinding(ctx, token)
	require.Error(t, err)
	require.Error(t, s.ConfirmBinding(ctx, uid, "member"))
	require.Error(t, s.ConfirmBinding(ctx, other, "member"))
	var pending int
	require.NoError(t, s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM qq_bot_bind_requests)+(SELECT COUNT(*) FROM qq_bot_email_bind_requests)`).Scan(&pending))
	require.Zero(t, pending)
	var balance float64
	require.NoError(t, s.db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, c.CheckinReward, balance)
	var records int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&records))
	require.Equal(t, 1, records)
	// Same QQ on another website account cannot claim the same day's reward again.
	bind(t, s.db, other, "member")
	text, err := s.reward(ctx, c, Job{AppID: c.AppID, MemberID: "member", GroupID: "group"}, "checkin")
	require.NoError(t, err)
	require.Contains(t, text, "今日已签到")
	require.NoError(t, s.db.QueryRow(`SELECT balance FROM users WHERE id=$1`, other).Scan(&balance))
	require.Zero(t, balance)
	// Stale removal must not delete even an identical account/member pair rebound later.
	require.NoError(t, s.Unbind(ctx, other, observedBinding(t, s, other, "member")))
	bind(t, s.db, uid, "member")
	require.ErrorIs(t, s.Unbind(ctx, uid, req), ErrBindingChanged)
	b, _, err := s.Binding(ctx, c.AppID, uid)
	require.NoError(t, err)
	require.NotNil(t, b)
}

func TestQQBotAdminUnbindInviteRewardCannotBeReset(t *testing.T) {
	s, _, c, _, uid := emailFixture(t)
	ctx := context.Background()
	inviter := user(t, s.db)
	bind(t, s.db, inviter, "inviter")
	bind(t, s.db, uid, "member")
	_, err := s.db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at) VALUES('app','group','member','inviter',NOW()-INTERVAL '1 minute',NOW())`)
	require.NoError(t, err)
	j := Job{AppID: c.AppID, GroupID: "group", MemberID: "member"}
	_, err = s.reward(ctx, c, j, "invite")
	require.NoError(t, err)
	require.NoError(t, s.Unbind(ctx, uid, observedBinding(t, s, uid, "member")))
	other := user(t, s.db)
	bind(t, s.db, other, "member")
	text, err := s.reward(ctx, c, j, "invite")
	require.NoError(t, err)
	require.Contains(t, text, "该邀请奖励已发放")
	var balance float64
	require.NoError(t, s.db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, c.InviteReward, balance)
}

func TestQQBotAdminUnbindRaceWithEmailConfirmation(t *testing.T) {
	s, m, c, e, uid := emailFixture(t)
	ctx := context.Background()
	requestBindingEmail(s, ctx, c, e, "alice@example.com")
	token := m.token(t)
	// Simulate a still-pending older email alongside an existing binding.
	bind(t, s.db, uid, "member")
	req := observedBinding(t, s, uid, "member")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); require.NoError(t, s.Unbind(ctx, uid, req)) }()
	go func() { defer wg.Done(); _, err := s.ConfirmEmailBinding(ctx, token, "member"); require.Error(t, err) }()
	wg.Wait()
	b, _, err := s.Binding(ctx, c.AppID, uid)
	require.NoError(t, err)
	require.Nil(t, b)
}
