package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type ipPoolProxyRepoStub struct {
	ProxyRepository
	proxies []Proxy
	err     error
	calls   int
}

func (r *ipPoolProxyRepoStub) ListActive(context.Context) ([]Proxy, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return append([]Proxy(nil), r.proxies...), nil
}

func ipPoolTestProxies() []Proxy {
	expired := time.Now().Add(-time.Hour)
	return []Proxy{
		{Name: "东京", Protocol: "http", Host: "a.example.com", Port: 8080, Username: "u", Password: "secret"},
		{Name: " 大阪 ", Protocol: "socks5h", Host: "b.example.com", Port: 1080},
		{Name: "过期", Protocol: "http", Host: "expired.example.com", Port: 8080, ExpiresAt: &expired},
	}
}

func ipPoolTestService(t *testing.T, harvestProxy string, repo *ipPoolProxyRepoStub) *OpenAIGatewayService {
	t.Helper()
	settings := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{
		SettingKeyOpenAICodexTicketHarvestProxyURL: harvestProxy,
	}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}}, nil)
	svc.settingService = NewSettingService(settings, &config.Config{})
	svc.proxyRepo = repo
	return svc
}

func TestCodexTicketIPPoolSentinelValidatesAndSurvivesMasking(t *testing.T) {
	require.NoError(t, ValidateOpenAICodexTicketHarvestProxyURL(OpenAICodexTicketHarvestIPPoolURL))
	require.Equal(t, OpenAICodexTicketHarvestIPPoolURL, MaskProxyURL(OpenAICodexTicketHarvestIPPoolURL))
	require.False(t, IsMaskedProxyURL(OpenAICodexTicketHarvestIPPoolURL), "saving the pool must not be mistaken for an unchanged masked URL")
	require.Error(t, ValidateOpenAICodexTicketHarvestProxyURL("ippool://other"))
}

func TestCodexTicketIPPoolResolvesToActiveUnexpiredMembers(t *testing.T) {
	repo := &ipPoolProxyRepoStub{proxies: ipPoolTestProxies()}
	svc := ipPoolTestService(t, OpenAICodexTicketHarvestIPPoolURL, repo)
	ctx := context.Background()

	require.True(t, svc.openAICodexTicketHarvestIPPoolEnabled(ctx))
	require.Empty(t, svc.openAICodexTicketHarvestFixedProxyURL(ctx), "business requests must not rotate through the pool")
	members := []string{"http://u:secret@a.example.com:8080", "socks5h://b.example.com:1080"}
	require.Contains(t, members, svc.openAICodexTicketHarvestProxyURLContext(ctx))

	seen := map[string]string{}
	for i := 0; i < 4; i++ {
		exit, ok := svc.pickHarvestIPPoolExit(ctx, nil)
		require.True(t, ok)
		seen[exit.url] = exit.name
	}
	require.Equal(t, map[string]string{members[0]: "东京", members[1]: "大阪"}, seen, "rotation covers every active member and skips expired ones")
	require.Equal(t, 1, repo.calls, "membership is cached between attempts")

	tried := map[string]bool{members[0]: true}
	exit, ok := svc.pickHarvestIPPoolExit(ctx, tried)
	require.True(t, ok)
	require.Equal(t, members[1], exit.url)
	tried[members[1]] = true
	_, ok = svc.pickHarvestIPPoolExit(ctx, tried)
	require.False(t, ok)
}

func TestCodexTicketIPPoolKeepsMembershipOnRefreshErrorAndEmptiesWhenPoolEmpty(t *testing.T) {
	repo := &ipPoolProxyRepoStub{proxies: ipPoolTestProxies()}
	svc := ipPoolTestService(t, OpenAICodexTicketHarvestIPPoolURL, repo)
	ctx := context.Background()
	_, ok := svc.pickHarvestIPPoolExit(ctx, nil)
	require.True(t, ok)

	repo.err = errors.New("database unavailable")
	svc.harvestIPPoolSyncedAt = time.Time{}
	_, ok = svc.pickHarvestIPPoolExit(ctx, nil)
	require.True(t, ok, "a transient listing error keeps the previous pool")

	repo.err = nil
	repo.proxies = nil
	svc.harvestIPPoolSyncedAt = time.Time{}
	_, ok = svc.pickHarvestIPPoolExit(ctx, nil)
	require.False(t, ok)
	require.Empty(t, svc.openAICodexTicketHarvestProxyURLContext(ctx), "an empty pool never falls back to a direct connection")
}

func TestCodexTicketStaticHarvestProxyIsNotAffectedByPool(t *testing.T) {
	repo := &ipPoolProxyRepoStub{proxies: ipPoolTestProxies()}
	svc := ipPoolTestService(t, "http://static.example.com:8080", repo)
	ctx := context.Background()
	require.False(t, svc.openAICodexTicketHarvestIPPoolEnabled(ctx))
	require.Equal(t, "http://static.example.com:8080", svc.openAICodexTicketHarvestFixedProxyURL(ctx))
	require.Equal(t, "http://static.example.com:8080", svc.openAICodexTicketHarvestProxyURLContext(ctx))
	require.Zero(t, repo.calls)
}

func TestCodexTicketIPPoolRefreshPrefersTicketExit(t *testing.T) {
	repo := &ipPoolProxyRepoStub{proxies: ipPoolTestProxies()}
	svc := ipPoolTestService(t, OpenAICodexTicketHarvestIPPoolURL, repo)
	ctx := context.Background()
	account := ticketTestAccount(61)
	pinned := "socks5h://b.example.com:1080"
	require.NoError(t, svc.storeOpenAICodexTicket(ctx, account, &openAICodexTicket{
		Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292,
		ExpiresAt: time.Now().Add(time.Minute), HarvestProxyURL: pinned,
	}))

	for i := 0; i < 3; i++ {
		exit, ok := svc.pickHarvestIPPoolExitFor(ctx, account, "gpt-6-astra", map[string]bool{})
		require.True(t, ok)
		require.Equal(t, pinned, exit.url)
	}
	exit, ok := svc.pickHarvestIPPoolExitFor(ctx, account, "gpt-6-astra", map[string]bool{pinned: true})
	require.True(t, ok)
	require.Equal(t, "http://u:secret@a.example.com:8080", exit.url)

	repo.proxies = repo.proxies[:1]
	svc.harvestIPPoolSyncedAt = time.Time{}
	exit, ok = svc.pickHarvestIPPoolExitFor(ctx, account, "gpt-6-astra", map[string]bool{})
	require.True(t, ok)
	require.Equal(t, "http://u:secret@a.example.com:8080", exit.url, "a removed exit is not reused")
}

func TestHarvestIPPoolAttemptBudgetAndNodeName(t *testing.T) {
	controls := CodexHarvestControls{Speed: CodexHarvestSpeed{MaxNodeAttempts: 3}}
	require.Equal(t, 1, harvestMaxNodeAttempts(controls, false))
	require.Equal(t, 3, harvestMaxNodeAttempts(controls, true))
	controls.NodeMemoryEnabled = true
	require.Equal(t, 3, harvestMaxNodeAttempts(controls, false))
	require.Equal(t, 1, harvestMaxNodeAttempts(CodexHarvestControls{}, true))

	require.Equal(t, "IP池 · 东京", harvestIPPoolNodeName(harvestIPPoolExit{url: "http://u:secret@a.example.com:8080", name: "东京"}))
	require.Equal(t, "IP池", harvestIPPoolNodeName(harvestIPPoolExit{url: "http://u:secret@a.example.com:8080"}))
}

func TestExcelBPSProxySourceFollowsSessionProxyToggle(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		"openai_excel_bps": true, ExcelBPSProxySourceKey: ExcelBPSProxySourceIPPool,
	}}
	require.Empty(t, account.ExcelBPSProxySource(), "the source is inert until the session proxy is on")
	account.Extra["openai_excel_bps_mihomo"] = true
	require.Equal(t, ExcelBPSProxySourceIPPool, account.ExcelBPSProxySource())
	account.Extra[ExcelBPSProxySourceKey] = "unknown"
	require.Equal(t, ExcelBPSProxySourceMihomo, account.ExcelBPSProxySource())
	delete(account.Extra, ExcelBPSProxySourceKey)
	require.Equal(t, ExcelBPSProxySourceMihomo, account.ExcelBPSProxySource())
}
