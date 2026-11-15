//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSiteQueryDetailedOutcomesAndFilters(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, status, code string
		provider           ChannelStatusProvider
	}{
		{"ok", "succeeded", "", channelStatusProviderFunc(func(context.Context) (*ChannelStatusReport, error) { return channelStatusFixture(), nil })},
		{"disabled", "unavailable", "monitor_disabled", channelStatusProviderFunc(func(context.Context) (*ChannelStatusReport, error) { return nil, ErrChannelStatusDisabled })},
		{"failure", "failed", "provider_failed", channelStatusProviderFunc(func(context.Context) (*ChannelStatusReport, error) { return nil, errors.New("private-provider-secret") })},
	} {
		s.SetChannelStatusProvider(tc.provider)
		result, e := s.querySiteInfoDetailed(ctx, c, Job{}, siteQuery{Kind: "channel_status", Page: 1})
		require.NoError(t, e)
		require.Equal(t, tc.status, result.Decision.Status)
		require.Equal(t, tc.code, result.Decision.ErrorCode)
		require.NotContains(t, result.Text, "private-provider-secret")
		_, e = db.Exec("INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,status,payload) VALUES($1,'app',$1,'GROUP_AT_MESSAGE_CREATE','group','member','completed','{}')", tc.name)
		require.NoError(t, e)
		j := Job{ID: tc.name}
		require.NoError(t, s.recordSiteQuery(ctx, &j, result.Decision))
	}
	page, err := s.ListJobs(ctx, JobFilter{QueryKind: "channel_status", QueryStatus: "failed", Page: 1, PageSize: 1})
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, "failure", page.Items[0].ID)
	require.Equal(t, "completed", page.Items[0].Status)
	require.NotContains(t, string(page.Items[0].SiteQuery), "private-provider-secret")
	_, err = s.ListJobs(ctx, JobFilter{QueryKind: "transfer", Page: 1, PageSize: 1})
	require.Error(t, err)
	result, err := s.querySiteInfoDetailed(ctx, c, Job{MemberID: "unbound"}, siteQuery{Kind: "model_price", Model: "missing"})
	require.NoError(t, err)
	require.Equal(t, "unavailable", result.Decision.Status)
	s.SetModelPriceProvider(siteModelPriceFunc(func(context.Context, string, int64, time.Time) ([]string, error) { return nil, nil }))
	result, err = s.querySiteInfoDetailed(ctx, c, Job{MemberID: "unbound"}, siteQuery{Kind: "model_price", Model: "missing"})
	require.NoError(t, err)
	require.Equal(t, "not_found", result.Decision.Status)
}

func TestSiteQueryFollowupIsolationExpiryAndInterruption(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	at := time.Now().UTC()
	raw, _ := json.Marshal(SiteQueryDecision{Kind: "model_price", Model: "private-model", Page: 1, Status: "succeeded"})
	insert := func(id, app, group, member, status string, when time.Time, query string) {
		_, e := db.Exec("INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,status,created_at,payload,site_query) VALUES($1,$2,$1,'GROUP_AT_MESSAGE_CREATE',$3,$4,$5,$6,'{}',$7)", id, app, group, member, status, when, query)
		require.NoError(t, e)
	}
	insert("previous", "app", "group", "member", "completed", at.Add(-time.Minute), string(raw))
	j := Job{ID: "current", GroupID: "group", MemberID: "member", CreatedAt: at}
	q, err := s.resolveSiteQueryFollowup(ctx, c, j, "下一页")
	require.NoError(t, err)
	require.NotNil(t, q)
	require.Equal(t, 2, q.Page)
	for _, other := range []Job{{ID: "x", GroupID: "group", MemberID: "other", CreatedAt: at}, {ID: "x", GroupID: "other", MemberID: "member", CreatedAt: at}, {ID: "x", GroupID: "group", MemberID: "member", CreatedAt: at.Add(11 * time.Minute)}} {
		q, err = s.resolveSiteQueryFollowup(ctx, c, other, "下一页")
		require.NoError(t, err)
		require.Nil(t, q)
	}
	different := c
	different.AppID = "different"
	q, err = s.resolveSiteQueryFollowup(ctx, different, j, "下一页")
	require.NoError(t, err)
	require.Nil(t, q)
	insert("ordinary", "app", "group", "member", "completed", at.Add(-30*time.Second), "{}")
	_, err = db.Exec("UPDATE qq_bot_jobs SET event_type='INTERACTION_CREATE' WHERE id='ordinary'")
	require.NoError(t, err)
	q, err = s.resolveSiteQueryFollowup(ctx, c, j, "下一页")
	require.NoError(t, err)
	require.Nil(t, q)
	_, err = db.Exec("UPDATE qq_bot_jobs SET event_type='GROUP_AT_MESSAGE_CREATE' WHERE id='ordinary'")
	require.NoError(t, err)
	q, err = s.resolveSiteQueryFollowup(ctx, c, j, "下一页")
	require.NoError(t, err)
	require.Nil(t, q)
	insert("undelivered", "app", "group", "member", "delivery_failed", at.Add(-10*time.Second), string(raw))
	q, err = s.resolveSiteQueryFollowup(ctx, c, j, "再查一次")
	require.NoError(t, err)
	require.Nil(t, q)
}
