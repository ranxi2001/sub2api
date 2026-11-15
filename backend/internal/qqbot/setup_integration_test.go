//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type setupSiteFake struct {
	prepared, revoked int
	keyID             int64
	chatProtocol      string
}

func (p *setupSiteFake) Options(context.Context, int64, Config) (SiteAIOptions, error) {
	return SiteAIOptions{}, nil
}
func (p *setupSiteFake) Prepare(_ context.Context, _ *sql.Tx, owner, group int64, _ float64, old Config) (SiteAIConnection, error) {
	p.prepared++
	p.chatProtocol = old.ChatProtocol
	if old.AIConnection == "site" && old.SiteGroupID == group && old.SiteAPIKeyID > 0 {
		return SiteAIConnection{BaseURL: "http://127.0.0.1:8080/v1", Key: old.APIKey, KeyID: old.SiteAPIKeyID, OwnerID: old.SiteOwnerID, GroupID: group}, nil
	}
	p.keyID++
	return SiteAIConnection{BaseURL: "http://127.0.0.1:8080/v1", Key: "dedicated-secret", KeyID: p.keyID, OwnerID: owner, GroupID: group, Created: true}, nil
}
func (p *setupSiteFake) Revoke(context.Context, int64, int64) error { p.revoked++; return nil }
func setupRequest(c Config, sections ...string) SetupRequest {
	return SetupRequest{Config: c.Public(), Versions: configVersions(c), Sections: sections, SiteQuota: 10}
}
func TestSetupAtomicSaveAndManagedSecretLifecycle(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	provider := &setupSiteFake{keyID: 100}
	s.SetSiteAIProvider(provider)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	req := setupRequest(c, "chat")
	req.Config.AIConnection = "site"
	req.Config.SiteGroupID = 2
	saved, err := s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	require.Empty(t, saved.APIKey)
	require.True(t, saved.APIKeySet)
	require.Equal(t, int64(101), saved.SiteAPIKeyID)
	var raw string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key=$1`, configKey).Scan(&raw))
	require.Contains(t, raw, "encrypted:dedicated-secret")
	c, err = s.Config(ctx)
	require.NoError(t, err)
	_, err = s.SaveSetup(ctx, 7, setupRequest(c, "chat"))
	require.NoError(t, err)
	require.Equal(t, int64(101), provider.keyID)
	// Advanced section cannot redirect a managed secret.
	draft := c.Public()
	draft.BaseURL = "https://attacker.invalid/v1"
	_, err = s.SaveConfigSection(ctx, "chat", sectionVersion(c, "chat"), draft)
	require.Error(t, err)
	req = setupRequest(c, "chat")
	req.Config.AIConnection = "external"
	req.Config.BaseURL = "https://external.invalid/v1"
	_, err = s.SaveSetup(ctx, 7, req)
	require.Error(t, err)
	require.Zero(t, provider.revoked)
	req.Config.APIKey = "external-secret"
	_, err = s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	require.Equal(t, 1, provider.revoked)
	// Validation failure revokes only the newly created key and does not alter config.
	c, err = s.Config(ctx)
	require.NoError(t, err)
	req = setupRequest(c, "chat")
	req.Config.AIConnection = "site"
	req.Config.SiteGroupID = 3
	req.Config.MaxOutputTokens = -1
	_, err = s.SaveSetup(ctx, 7, req)
	require.Error(t, err)
	require.Equal(t, 2, provider.revoked)
	current, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, "external-secret", current.APIKey)
	history, err := s.ConfigHistory(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	b, _ := json.Marshal(history)
	require.NotContains(t, string(b), "secret")
}

func TestSetupManagedProviderReceivesRequestedChatProtocol(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	provider := &setupSiteFake{keyID: 100}
	s.SetSiteAIProvider(provider)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.ChatProtocol = "responses"
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	for _, protocol := range []string{"chat_completions", "responses"} {
		c, err = s.Config(ctx)
		require.NoError(t, err)
		req := setupRequest(c, "chat")
		req.Config.AIConnection, req.Config.SiteGroupID, req.Config.ChatProtocol = "site", 2, protocol
		saved, err := s.SaveSetup(ctx, 7, req)
		require.NoError(t, err)
		require.Equal(t, protocol, provider.chatProtocol)
		require.Equal(t, protocol, saved.ChatProtocol)
	}
	require.Equal(t, int64(101), provider.keyID, "changing protocol must reuse the managed key")
	require.Zero(t, provider.revoked)
}

func TestSetupManagedProtocolCannotBypassProviderValidation(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	provider := &setupSiteFake{keyID: 100}
	s.SetSiteAIProvider(provider)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	req := setupRequest(c, "chat")
	req.Config.AIConnection, req.Config.SiteGroupID, req.Config.ChatProtocol = "site", 2, "chat_completions"
	_, err = s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	for _, mode := range []string{"section", "basic", "full"} {
		c, err = s.Config(ctx)
		require.NoError(t, err)
		draft := c.Public()
		draft.ChatProtocol = "responses"
		switch mode {
		case "section":
			_, err = s.SaveConfigSection(ctx, "chat", sectionVersion(c, "chat"), draft)
		case "basic":
			_, err = s.SaveBasicConfig(ctx, draft)
		case "full":
			_, err = s.SaveConfig(ctx, draft)
		}
		require.ErrorContains(t, err, "本站 AI 凭据和接口由快速配置管理", mode)
		current, err := s.Config(ctx)
		require.NoError(t, err)
		require.Equal(t, "chat_completions", current.ChatProtocol)
		require.Equal(t, c.SiteAPIKeyID, current.SiteAPIKeyID)
		require.Equal(t, c.APIKey, current.APIKey)
	}
	for _, mode := range []string{"section", "basic"} {
		c, err = s.Config(ctx)
		require.NoError(t, err)
		draft := c.Public()
		draft.SystemPrompt = "只修改人设：" + mode
		var saved Config
		if mode == "section" {
			saved, err = s.SaveConfigSection(ctx, "chat", sectionVersion(c, "chat"), draft)
		} else {
			saved, err = s.SaveBasicConfig(ctx, draft)
		}
		require.NoError(t, err)
		require.Equal(t, draft.SystemPrompt, saved.SystemPrompt)
	}
	require.Equal(t, 1, provider.prepared)
	require.Zero(t, provider.revoked)
}
func TestSetupActivationRequiresCurrentProbesButLiveEditsRemainPossible(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	req := setupRequest(c, "limits")
	req.Config.TotalDailyChatLimit = 100
	_, err = s.SaveSetup(ctx, 1, req)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	c.Enabled = false
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	req = setupRequest(c, "connection")
	req.Config.Enabled = true
	_, err = s.SaveSetup(ctx, 1, req)
	require.Error(t, err)
	state := setupProbeState{Fingerprint: probeFingerprint(c), Results: map[string]SetupProbe{}}
	for _, kind := range requiredSetupProbes(c) {
		state.Results[kind] = SetupProbe{Kind: kind, Status: "ready", At: time.Now()}
	}
	raw, _ := json.Marshal(state)
	_, err = db.Exec(`INSERT INTO settings(key,value) VALUES($1,$2)`, setupProbeKey, string(raw))
	require.NoError(t, err)
	saved, err := s.SaveSetup(ctx, 1, req)
	require.NoError(t, err)
	require.True(t, saved.Enabled)
}
func TestSetupProbeSingleFlightStaleConfigAndSecretRedaction(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		http.Error(w, "operator-key upstream error", 500)
	}))
	defer server.Close()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.BaseURL = server.URL
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() { _, err := s.SetupProbe(ctx, "chat"); result <- err }()
	<-started
	_, err = s.SetupProbe(ctx, "chat")
	require.ErrorContains(t, err, "已有能力测试")
	c, err = s.Config(ctx)
	require.NoError(t, err)
	c.ChatModel = "changed"
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	close(release)
	require.ErrorIs(t, <-result, ErrConfigConflict)
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "operator-key upstream error", 500) }))
	defer server2.Close()
	c, err = s.Config(ctx)
	require.NoError(t, err)
	c.BaseURL = server2.URL
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	probe, err := s.SetupProbe(ctx, "chat")
	require.NoError(t, err)
	require.Equal(t, "blocked", probe.Status)
	require.NotContains(t, probe.Message, "operator-key")
}
func TestRewardHardBudgetSerializesAcrossMembersAndPreservesEntitlements(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	budget := .3
	c.RewardBudgetDaily = &budget
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	for i := range 12 {
		uid := user(t, db)
		bind(t, db, uid, fmt.Sprint("member", i))
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.reward(ctx, c, Job{GroupID: "group", MemberID: fmt.Sprint("member", i)}, "checkin")
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var amount float64
	var count int
	require.NoError(t, db.QueryRow(`SELECT SUM(amount),COUNT(*) FROM qq_bot_rewards`).Scan(&amount, &count))
	require.Equal(t, .3, amount)
	require.Equal(t, 3, count)
	uid := user(t, db)
	bind(t, db, uid, "pending")
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = grantActivityReward(ctx, tx, c, Job{GroupID: "group", MemberID: "pending"}, uid, "budget-growth", "growth", "growth", GrowthReward{Type: "balance", Amount: .1}, time.Now())
	require.ErrorIs(t, err, ErrRewardBudget)
	require.NoError(t, tx.Rollback())
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes WHERE source_key='budget-growth'`).Scan(&count))
	require.Zero(t, count)
}
func TestRewardBudgetRejectsWholeLotteryBatchWithoutConsumingChances(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, _ := lotteryFixture(t, 30)
	budget := 1.0
	c.RewardBudgetDaily = &budget
	_, err := s.SaveConfig(ctx, c)
	require.NoError(t, err)
	j := Job{ID: "budget-batch", GroupID: "group", MemberID: "member"}
	text, err := s.drawLotteryCount(ctx, c, j, 2)
	require.NoError(t, err)
	require.Contains(t, text, "预算")
	lotteryBatchTotals(t, db, uid, 0, 0)
	_, err = s.drawLotteryCount(ctx, c, j, 1)
	require.NoError(t, err)
	lotteryBatchTotals(t, db, uid, 1, 1)
	replay, err := s.drawLotteryCount(ctx, c, j, 1)
	require.NoError(t, err)
	require.NotContains(t, replay, "预算")
	lotteryBatchTotals(t, db, uid, 1, 1)
}
func TestActivityTemplateAtomicVersionAndConfirmation(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	bundles, err := s.ActivityTemplates(ctx)
	require.NoError(t, err)
	budget := 2.0
	req := ActivityTemplateRequest{Template: "active", Version: bundles["current"].Version, Budget: &budget}
	_, err = s.ApplyActivityTemplate(ctx, req)
	require.Error(t, err)
	req.Confirmed = true
	saved, err := s.ApplyActivityTemplate(ctx, req)
	require.NoError(t, err)
	require.Equal(t, budget, *saved.RewardBudgetDaily)
	current, err := s.ActivityTemplates(ctx)
	require.NoError(t, err)
	require.Equal(t, .05, current["current"].Invite.InviteReward)
	require.True(t, current["current"].Lottery.Enabled)
	require.True(t, current["current"].Ranking.RewardsEnabled)
	_, err = s.ApplyActivityTemplate(ctx, req)
	require.True(t, errors.Is(err, ErrConfigConflict))
	req.Version = current["current"].Version
	req.Template = "light"
	_, err = s.ApplyActivityTemplate(ctx, req)
	require.NoError(t, err)
	current, err = s.ActivityTemplates(ctx)
	require.NoError(t, err)
	require.False(t, current["current"].Invite.Enabled)
	require.False(t, current["current"].Lottery.Enabled)
}

func TestSetupExplicitDedicatedKeyReplacement(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	provider := &setupSiteFake{keyID: 100}
	s.SetSiteAIProvider(provider)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	req := setupRequest(c, "chat")
	req.Config.AIConnection = "site"
	req.Config.SiteGroupID = 2
	_, err = s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	req = setupRequest(c, "chat")
	req.RegenerateSiteKey = true
	saved, err := s.SaveSetup(ctx, 7, req)
	require.NoError(t, err)
	require.Equal(t, int64(102), saved.SiteAPIKeyID)
	require.Equal(t, 1, provider.revoked)
}

func TestLegacyLimitsClientCannotClearDailyBudgetByOmission(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	budget := 2.0
	c.RewardBudgetDaily = &budget
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	draft := c.Public()
	draft.RewardBudgetDaily = nil
	draft.DailyChatLimit = 40
	saved, err := s.SaveConfigSection(ctx, "limits", sectionVersion(c, "limits"), draft, true)
	require.NoError(t, err)
	require.Equal(t, budget, *saved.RewardBudgetDaily)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	_, err = s.SaveConfigSection(ctx, "limits", sectionVersion(c, "limits"), draft, false)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	require.Nil(t, c.RewardBudgetDaily)
}
