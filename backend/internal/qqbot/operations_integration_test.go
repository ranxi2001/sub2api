//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScopedConfigCASAndSecretFreeHistory(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	initial, err := s.Config(ctx)
	require.NoError(t, err)
	a, b := initial.Public(), initial.Public()
	a.ChatModel = "new-chat"
	b.ImageModel = "new-image"
	saved, err := s.SaveConfigSection(ctx, "chat", a.SectionVersions["chat"], a)
	require.NoError(t, err)
	require.Empty(t, saved.APIKey)
	_, err = s.SaveConfigSection(ctx, "images", b.SectionVersions["images"], b)
	require.NoError(t, err, "unrelated modules should not conflict")
	_, err = s.SaveConfigSection(ctx, "chat", a.SectionVersions["chat"], a)
	require.ErrorIs(t, err, ErrConfigConflict)
	_, err = s.SaveConfigSection(ctx, "chat", "", a)
	require.ErrorIs(t, err, ErrConfigConflict)
	now, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, "new-chat", now.ChatModel)
	require.Equal(t, "new-image", now.ImageModel)
	require.Equal(t, initial.APIKey, now.APIKey)
	h, err := s.ConfigHistory(ctx)
	require.NoError(t, err)
	require.Len(t, h, 2)
	raw, err := json.Marshal(h)
	require.NoError(t, err)
	require.NotContains(t, string(raw), initial.APIKey)
	require.NotContains(t, string(raw), "api_key")
	// A forged full object on a module endpoint cannot change other settings.
	draft := now.Public()
	draft.Enabled = false
	draft.TransferEnabled = true
	draft.ChatModel = "another"
	_, err = s.SaveConfigSection(ctx, "chat", draft.SectionVersions["chat"], draft)
	require.NoError(t, err)
	now, err = s.Config(ctx)
	require.NoError(t, err)
	require.True(t, now.Enabled)
	require.False(t, now.TransferEnabled)
}
func TestGroupQuotaSerializesAndChatCanBeDisabled(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	limit := 2
	c.GroupPolicies = []GroupPolicy{{GroupID: "group", ChatDailyLimit: &limit}}
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- s.reserveUsage(ctx, c, Job{ID: fmt.Sprint(i), GroupID: "group", MemberID: fmt.Sprint(i)}, "chat")
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else {
			require.ErrorIs(t, e, errUsageLimit)
		}
	}
	require.Equal(t, 2, success)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count FROM qq_bot_daily_usage WHERE app_id='app' AND scope='all' AND kind='chat'`).Scan(&count))
	require.Equal(t, 2, count, "failed group reservation must roll back global and personal counters")
	c.ChatDisabled = true
	c.BaseURL = ""
	c.APIKey = ""
	c.ClearAPIKey = true
	c.ChatModel = ""
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	require.ErrorIs(t, s.reserveUsage(ctx, c, Job{GroupID: "other-group", MemberID: "another"}, "chat"), errFeatureUnavailable)
}
func TestOperationsFilterBeforePaginationAndOverview(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	for i := 0; i < 70; i++ {
		kind := "chat"
		status := "completed"
		group := "other-group"
		if i == 0 {
			kind = "health_notification"
			status = "delivery_failed"
			group = "group"
		}
		_, err := s.db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,kind,status,created_at,payload) VALUES($1,'app',$1,'GROUP_AT_MESSAGE_CREATE',$2,'member',$3,$4,$5,'{}')`, fmt.Sprintf("job-%03d", i), group, kind, status, time.Now().Add(time.Duration(i-70)*time.Second))
		require.NoError(t, err)
	}
	p, err := s.ListJobs(ctx, JobFilter{Kind: "notifications", GroupID: "group", Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, 1, p.Total)
	require.Equal(t, "job-000", p.Items[0].ID)
	o, err := s.Overview(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, o.Failed)
	require.Equal(t, 500, o.ChatRemaining)
	_, err = s.ListJobs(ctx, JobFilter{Page: 0, PageSize: 20})
	require.Error(t, err)
	_, err = s.ListJobs(ctx, JobFilter{Page: 1, PageSize: 20, From: "bad"})
	require.Error(t, err)
}
func TestActivityCASRejectsStaleWriters(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	c, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	version := ConfigVersion(c)
	c.MinAmount = 0.2
	c.MaxAmount = 0.3
	require.NoError(t, s.SaveCheckinConfig(WithConfigVersion(ctx, version), c))
	c.MaxAmount = 1
	require.ErrorIs(t, s.SaveCheckinConfig(WithConfigVersion(ctx, version), c), ErrConfigConflict)
	tr, err := s.TransferConfig(ctx)
	require.NoError(t, err)
	v := ConfigVersion(tr)
	tr.TransferFeePercent = 2
	require.NoError(t, s.SaveTransferConfig(WithConfigVersion(ctx, v), tr))
	tr.TransferFeePercent = 9
	require.ErrorIs(t, s.SaveTransferConfig(WithConfigVersion(ctx, v), tr), ErrConfigConflict)
}
func TestCompletedTransferHasOneAtomicReceipt(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	raw, err := migrations.FS.ReadFile("355_qq_bot_transfer_requests.sql")
	require.NoError(t, err)
	_, err = s.db.Exec(string(raw))
	require.NoError(t, err)
	require.NoError(t, s.SaveTransferConfig(ctx, TransferConfig{TransferEnabled: true}))
	from, to := user(t, s.db), user(t, s.db)
	_, err = s.db.Exec(`UPDATE users SET balance=10 WHERE id=$1`, from)
	require.NoError(t, err)
	token := strings.Repeat("a", 48)
	_, err = s.db.Exec(`INSERT INTO qq_bot_transfer_requests(token_hash,app_id,from_member_id,from_user_id,to_user_id,from_email,to_email,amount,fee,net_amount,expires_at) VALUES($1,'app','member',$2,$3,'from@test.invalid','to@test.invalid',5,1,4,NOW()+INTERVAL '10 minutes')`, codeHash(token), from, to)
	require.NoError(t, err)
	require.NoError(t, s.ConfirmTransfer(ctx, token))
	require.Error(t, s.ConfirmTransfer(ctx, token))
	p, err := s.TransferHistory(ctx, "", 1)
	require.NoError(t, err)
	require.Equal(t, 1, p.Total)
	require.Equal(t, "5.00000000", p.Items[0].Amount)
	require.Equal(t, "1.00000000", p.Items[0].Fee)
	require.Equal(t, "4.00000000", p.Items[0].NetAmount)
	var total string
	require.NoError(t, s.db.QueryRow(`SELECT SUM(balance)::text FROM users WHERE id IN ($1,$2)`, from, to).Scan(&total))
	require.Equal(t, "9.00000000", total)
}

func TestActivityPreviewCannotSettleRewards(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	require.NoError(t, s.SaveCheckinConfig(ctx, CheckinConfig{Enabled: true, MinAmount: 1, MaxAmount: 3, Growth: GrowthConfig{BindingChances: 2}}))
	result, err := s.PreviewActivities(ctx, uid)
	require.NoError(t, err)
	require.True(t, result.Bound)
	require.True(t, result.CheckinEligible)
	require.InDelta(t, 1.7749999955, result.CheckinExpected, 1e-9)
	require.NotEmpty(t, result.Tasks)
	checkin, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	checkin.RewardDistribution = &CheckinRewardDistribution{0, 0, 10000}
	require.NoError(t, s.SaveCheckinConfig(ctx, checkin))
	result, err = s.PreviewActivities(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 2.75, result.CheckinExpected)
	for _, table := range []string{"qq_bot_rewards", "qq_bot_reward_codes", "qq_bot_lottery_chance_grants"} {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
		require.Zero(t, n, "preview wrote to "+table)
	}
}
func TestCheckinSectionsPreserveConcurrentDrafts(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	original, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	first := original
	first.MinAmount = 0.2
	first.MaxAmount = 0.3
	first.RewardDistribution = &CheckinRewardDistribution{1234, 4321, 4445}
	require.NoError(t, s.SaveCheckinConfig(WithCheckinSection(WithConfigVersion(ctx, CheckinSectionVersion(original, "checkin")), "checkin"), first))
	second := original
	second.Growth.Tasks = []GrowthTask{}
	require.NoError(t, s.SaveCheckinConfig(WithCheckinSection(WithConfigVersion(ctx, CheckinSectionVersion(original, "tasks")), "tasks"), second))
	got, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 0.2, got.MinAmount)
	require.Equal(t, 0.3, got.MaxAmount)
	require.Equal(t, first.RewardDistribution, got.RewardDistribution)
	require.Empty(t, got.Growth.Tasks)
	require.ErrorIs(t, s.SaveCheckinConfig(WithCheckinSection(WithConfigVersion(ctx, CheckinSectionVersion(original, "checkin")), "checkin"), original), ErrConfigConflict)
}

func TestModerationRecordsFilterBeforePagination(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 65; i++ {
		id, group, status := fmt.Sprintf("incident-%03d", i), "other-group", "succeeded"
		if i < 2 {
			group, status = "group", "failed"
		}
		_, err := s.db.Exec(`INSERT INTO qq_bot_moderation_incidents(id,app_id,sandbox,group_id,member_id,policy_hash,reason,mute_seconds,created_at) VALUES($1,'app',false,$2,'member','hash','repeat',300,$3)`, id, group, now.Add(time.Duration(i-65)*time.Second))
		require.NoError(t, err)
		_, err = s.db.Exec(`INSERT INTO qq_bot_moderation_operations(incident_id,kind,expires_at,status) VALUES($1,'mute',NOW(),$2),($1,'recall',NOW(),'succeeded')`, id, status)
		require.NoError(t, err)
	}
	f := JobFilter{GroupID: "group", MemberID: "member", Status: "failed", From: now.Add(-time.Hour).Format(time.RFC3339), To: now.Format(time.RFC3339), Page: 1, PageSize: 1}
	p, err := s.ListModerationRecords(ctx, f)
	require.NoError(t, err)
	require.Equal(t, 2, p.Total)
	require.Len(t, p.Items, 1)
	require.Equal(t, "incident-001", p.Items[0].ID)
	require.Equal(t, "failed", p.Items[0].MuteStatus)
	require.EqualValues(t, 1, p.Items[0].RecallSucceeded, "filter must not remove sibling operations from incident details")
	f.Page = 2
	p, err = s.ListModerationRecords(ctx, f)
	require.NoError(t, err)
	require.Equal(t, "incident-000", p.Items[0].ID)
	f.From, f.To = f.To, f.From
	_, err = s.ListModerationRecords(ctx, f)
	require.Error(t, err)
	_, err = s.ListJobs(ctx, f)
	require.Error(t, err)
}

func TestCapabilityProbesUseRealImageAndNeverExecuteTools(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, kind := range []string{"vision", "tools"} {
			t.Run(protocol+"/"+kind, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					require.Contains(t, []string{"/v1/chat/completions", "/v1/responses"}, r.URL.Path)
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					if kind == "vision" {
						raw, err := json.Marshal(body)
						require.NoError(t, err)
						require.Contains(t, string(raw), probeImage(), "vision must send image bytes, not a text-only capability question")
						if protocol == "responses" {
							_, _ = w.Write([]byte(`{"status":"completed","output_text":"A blue square"}`))
						} else {
							_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"A blue square"}}]}`))
						}
					} else {
						require.Equal(t, "required", body["tool_choice"])
						_, _ = w.Write(textToolTestJSON(t, protocol, "OK"))
					}
				}))
				defer server.Close()
				s := savedBot(t, testDB(t))
				c, err := s.Config(context.Background())
				require.NoError(t, err)
				c.BaseURL, c.ChatProtocol = server.URL+"/v1", protocol
				_, err = s.SaveConfig(context.Background(), c)
				require.NoError(t, err)
				_, err = s.Probe(context.Background(), kind)
				require.NoError(t, err)
				require.Equal(t, 1, calls, "probe must not retry or generate an image")
			})
		}
	}
}
