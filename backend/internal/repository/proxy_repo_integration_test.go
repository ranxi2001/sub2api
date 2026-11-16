//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/suite"
)

type ProxyRepoSuite struct {
	suite.Suite
	ctx  context.Context
	tx   *dbent.Tx
	repo *proxyRepository
}

func (s *ProxyRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.tx = tx
	s.repo = newProxyRepositoryWithSQL(tx.Client(), tx)
}

func TestProxyRepoSuite(t *testing.T) {
	suite.Run(t, new(ProxyRepoSuite))
}

// --- Create / GetByID / Update / Delete ---

func (s *ProxyRepoSuite) TestCreate() {
	proxy := &service.Proxy{
		Name:     "test-create",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
		Status:   service.StatusActive,
	}

	err := s.repo.Create(s.ctx, proxy)
	s.Require().NoError(err, "Create")
	s.Require().NotZero(proxy.ID, "expected ID to be set")

	got, err := s.repo.GetByID(s.ctx, proxy.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Equal("test-create", got.Name)
}

func (s *ProxyRepoSuite) TestGetByID_NotFound() {
	_, err := s.repo.GetByID(s.ctx, 999999)
	s.Require().Error(err, "expected error for non-existent ID")
}

func (s *ProxyRepoSuite) TestMihomoNodeIdentityPersistsAndIsUniqueUntilDeleted() {
	proxy := &service.Proxy{Name: "Mihomo fixture", Protocol: "http", Host: "127.0.0.1", Port: 32000, Status: service.StatusActive, MihomoNodeID: "node-fixture", FallbackMode: service.FallbackModeNone}
	s.Require().NoError(s.repo.Create(s.ctx, proxy))
	got, err := s.repo.GetByMihomoNodeID(s.ctx, "node-fixture")
	s.Require().NoError(err)
	s.Require().Equal(proxy.ID, got.ID)
	s.Require().Equal("node-fixture", got.MihomoNodeID)
	got.Name = "renamed"
	s.Require().NoError(s.repo.Update(s.ctx, got))
	rows, _, err := s.repo.ListWithFiltersAndAccountCount(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "renamed")
	s.Require().NoError(err)
	s.Require().Len(rows, 1)
	s.Require().Equal("node-fixture", rows[0].MihomoNodeID)
	_, err = s.tx.ExecContext(s.ctx, "SAVEPOINT duplicate_static_node")
	s.Require().NoError(err)
	duplicate := *proxy
	duplicate.ID = 0
	s.Require().Error(s.repo.Create(s.ctx, &duplicate), "the node identity must be unique")
	_, err = s.tx.ExecContext(s.ctx, "ROLLBACK TO SAVEPOINT duplicate_static_node")
	s.Require().NoError(err)
	s.Require().NoError(s.repo.Delete(s.ctx, proxy.ID))
	got, err = s.repo.GetByMihomoNodeID(s.ctx, "node-fixture")
	s.Require().NoError(err)
	s.Require().Nil(got)
	s.Require().NoError(s.repo.Create(s.ctx, &duplicate), "a deleted node proxy can be added again")
	s.Require().NotEqual(proxy.ID, duplicate.ID)
}

func (s *ProxyRepoSuite) TestUpdate() {
	proxy := &service.Proxy{
		Name:     "original",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
		Status:   service.StatusActive,
	}
	s.Require().NoError(s.repo.Create(s.ctx, proxy))

	proxy.Name = "updated"
	err := s.repo.Update(s.ctx, proxy)
	s.Require().NoError(err, "Update")

	got, err := s.repo.GetByID(s.ctx, proxy.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("updated", got.Name)
}

func (s *ProxyRepoSuite) TestDelete() {
	proxy := &service.Proxy{
		Name:     "to-delete",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
		Status:   service.StatusActive,
	}
	s.Require().NoError(s.repo.Create(s.ctx, proxy))

	err := s.repo.Delete(s.ctx, proxy.ID)
	s.Require().NoError(err, "Delete")

	_, err = s.repo.GetByID(s.ctx, proxy.ID)
	s.Require().Error(err, "expected error after delete")
}

// --- List / ListWithFilters ---

func (s *ProxyRepoSuite) TestList() {
	s.mustCreateProxy(&service.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})
	s.mustCreateProxy(&service.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: service.StatusActive})

	proxies, page, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err, "List")
	s.Require().Len(proxies, 2)
	s.Require().Equal(int64(2), page.Total)
}

func (s *ProxyRepoSuite) TestListWithFilters_Protocol() {
	s.mustCreateProxy(&service.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})
	s.mustCreateProxy(&service.Proxy{Name: "p2", Protocol: "socks5", Host: "127.0.0.1", Port: 8081, Status: service.StatusActive})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "socks5", "", "")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Equal("socks5", proxies[0].Protocol)
}

func (s *ProxyRepoSuite) TestListWithFilters_Status() {
	s.mustCreateProxy(&service.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})
	s.mustCreateProxy(&service.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: service.StatusDisabled})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", service.StatusDisabled, "")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Equal(service.StatusDisabled, proxies[0].Status)
}

func (s *ProxyRepoSuite) TestListWithFilters_Search() {
	s.mustCreateProxy(&service.Proxy{Name: "production-proxy", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})
	s.mustCreateProxy(&service.Proxy{Name: "dev-proxy", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: service.StatusActive})

	proxies, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "prod")
	s.Require().NoError(err)
	s.Require().Len(proxies, 1)
	s.Require().Contains(proxies[0].Name, "production")
}

// --- ListActive ---

func (s *ProxyRepoSuite) TestListActive() {
	s.mustCreateProxy(&service.Proxy{Name: "active1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})
	s.mustCreateProxy(&service.Proxy{Name: "inactive1", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: service.StatusDisabled})

	proxies, err := s.repo.ListActive(s.ctx)
	s.Require().NoError(err, "ListActive")
	s.Require().Len(proxies, 1)
	s.Require().Equal("active1", proxies[0].Name)
}

// --- ExistsByHostPortAuth ---

func (s *ProxyRepoSuite) TestExistsByHostPortAuth() {
	s.mustCreateProxy(&service.Proxy{
		Name:     "p1",
		Protocol: "http",
		Host:     "1.2.3.4",
		Port:     8080,
		Username: "user",
		Password: "pass",
		Status:   service.StatusActive,
	})

	exists, err := s.repo.ExistsByHostPortAuth(s.ctx, "1.2.3.4", 8080, "user", "pass")
	s.Require().NoError(err, "ExistsByHostPortAuth")
	s.Require().True(exists)

	notExists, err := s.repo.ExistsByHostPortAuth(s.ctx, "1.2.3.4", 8080, "wrong", "creds")
	s.Require().NoError(err)
	s.Require().False(notExists)
}

func (s *ProxyRepoSuite) TestExistsByHostPortAuth_NoAuth() {
	s.mustCreateProxy(&service.Proxy{
		Name:     "p-noauth",
		Protocol: "http",
		Host:     "5.6.7.8",
		Port:     8081,
		Username: "",
		Password: "",
		Status:   service.StatusActive,
	})

	exists, err := s.repo.ExistsByHostPortAuth(s.ctx, "5.6.7.8", 8081, "", "")
	s.Require().NoError(err)
	s.Require().True(exists)
}

// --- CountAccountsByProxyID ---

func (s *ProxyRepoSuite) TestCountAccountsByProxyID() {
	proxy := s.mustCreateProxy(&service.Proxy{Name: "p-count", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})
	s.mustInsertAccount("a1", &proxy.ID)
	s.mustInsertAccount("a2", &proxy.ID)
	s.mustInsertAccount("a3", nil) // no proxy

	count, err := s.repo.CountAccountsByProxyID(s.ctx, proxy.ID)
	s.Require().NoError(err, "CountAccountsByProxyID")
	s.Require().Equal(int64(2), count)
}

func (s *ProxyRepoSuite) TestCountAccountsByProxyID_Zero() {
	proxy := s.mustCreateProxy(&service.Proxy{Name: "p-zero", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})

	count, err := s.repo.CountAccountsByProxyID(s.ctx, proxy.ID)
	s.Require().NoError(err)
	s.Require().Zero(count)
}

// --- GetAccountCountsForProxies ---

func (s *ProxyRepoSuite) TestGetAccountCountsForProxies() {
	p1 := s.mustCreateProxy(&service.Proxy{Name: "p1", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive})
	p2 := s.mustCreateProxy(&service.Proxy{Name: "p2", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: service.StatusActive})

	s.mustInsertAccount("a1", &p1.ID)
	s.mustInsertAccount("a2", &p1.ID)
	s.mustInsertAccount("a3", &p2.ID)

	counts, err := s.repo.GetAccountCountsForProxies(s.ctx)
	s.Require().NoError(err, "GetAccountCountsForProxies")
	s.Require().Equal(int64(2), counts[p1.ID])
	s.Require().Equal(int64(1), counts[p2.ID])
}

func (s *ProxyRepoSuite) TestGetAccountCountsForProxies_Empty() {
	counts, err := s.repo.GetAccountCountsForProxies(s.ctx)
	s.Require().NoError(err)
	s.Require().Empty(counts)
}

func (s *ProxyRepoSuite) TestConfiguredAccountReferencesIncludeBPSGroupsOnce() {
	p1 := s.mustCreateProxy(&service.Proxy{Name: "configured-one", Protocol: "http", Host: "192.0.2.11", Port: 8080, Status: service.StatusActive})
	p2 := s.mustCreateProxy(&service.Proxy{Name: "configured-two", Protocol: "http", Host: "192.0.2.22", Port: 8080, Status: service.StatusActive})
	s.mustInsertAccount("traditional", &p1.ID)
	insert := func(name, platform, accountType string, proxyID *int64, extra map[string]any, deleted bool) {
		data, err := json.Marshal(extra)
		s.Require().NoError(err)
		var deletedAt any
		if deleted {
			deletedAt = time.Now()
		}
		_, err = s.tx.ExecContext(s.ctx, `INSERT INTO accounts (name, platform, type, proxy_id, extra, deleted_at) VALUES ($1,$2,$3,$4,$5,$6)`, name, platform, accountType, proxyID, string(data), deletedAt)
		s.Require().NoError(err)
	}
	group := func(enabled bool, ids ...int64) map[string]any {
		return map[string]any{"openai_excel_bps": enabled, service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: ids}
	}
	insert("both-fields", service.PlatformOpenAI, service.AccountTypeOAuth, &p1.ID, group(true, p1.ID, p1.ID, p2.ID), false)
	insert("saved-disabled-bps", service.PlatformOpenAI, service.AccountTypeOAuth, nil, group(false, p1.ID), false)
	insert("removed-account", service.PlatformOpenAI, service.AccountTypeOAuth, &p1.ID, group(true, p1.ID), true)
	insert("other-platform-extra", service.PlatformAnthropic, service.AccountTypeOAuth, nil, group(true, p1.ID), false)
	insert("other-type-extra", service.PlatformOpenAI, service.AccountTypeAPIKey, nil, group(true, p1.ID), false)
	insert("session-mode", service.PlatformOpenAI, service.AccountTypeOAuth, nil, map[string]any{service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeSession, service.ExcelBPSProxyIDsKey: []int64{p1.ID}}, false)
	insert("invalid-scalar", service.PlatformOpenAI, service.AccountTypeOAuth, nil, map[string]any{service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: "invalid"}, false)
	insert("invalid-elements", service.PlatformOpenAI, service.AccountTypeOAuth, nil, map[string]any{service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: []any{nil, "not-an-id", -1, 0, 1.5, json.Number("9223372036854775808")}}, false)

	accounts, err := s.repo.ListAccountSummariesByProxyID(s.ctx, p1.ID)
	s.Require().NoError(err)
	names := make([]string, 0, len(accounts))
	for _, account := range accounts {
		names = append(names, account.Name)
	}
	s.Require().ElementsMatch([]string{"traditional", "both-fields", "saved-disabled-bps"}, names)
	count, err := s.repo.CountAccountsByProxyID(s.ctx, p1.ID)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), count)
	counts, err := s.repo.GetAccountCountsForProxies(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(3), counts[p1.ID])
	s.Require().Equal(int64(1), counts[p2.ID])
	rows, _, err := s.repo.ListWithFiltersAndAccountCount(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10, SortBy: "account_count", SortOrder: "desc"}, "", "", "configured-")
	s.Require().NoError(err)
	s.Require().Len(rows, 2)
	s.Require().Equal(p1.ID, rows[0].ID)
	s.Require().Equal(int64(3), rows[0].AccountCount)
	s.Require().Equal(int64(1), rows[1].AccountCount)
}

func (s *ProxyRepoSuite) TestBPSConfiguredReferenceProtectsSingleAndBatchDeletion() {
	p := s.mustCreateProxy(&service.Proxy{Name: "saved-bps-group", Protocol: "http", Host: "192.0.2.31", Port: 8080, Status: service.StatusActive})
	extra, err := json.Marshal(map[string]any{"openai_excel_bps": false, service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: []int64{p.ID}})
	s.Require().NoError(err)
	_, err = s.tx.ExecContext(s.ctx, `INSERT INTO accounts (name, platform, type, extra) VALUES ($1,$2,$3,$4)`, "saved-group-only", service.PlatformOpenAI, service.AccountTypeOAuth, string(extra))
	s.Require().NoError(err)
	admin := service.NewAdminService(
		nil, nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	s.Require().ErrorIs(admin.DeleteProxy(s.ctx, p.ID), service.ErrProxyInUse)
	batch, err := admin.BatchDeleteProxies(s.ctx, []int64{p.ID})
	s.Require().NoError(err)
	s.Require().Empty(batch.DeletedIDs)
	s.Require().Len(batch.Skipped, 1)
	s.Require().Equal(p.ID, batch.Skipped[0].ID)
	s.Require().Equal(service.ErrProxyInUse.Error(), batch.Skipped[0].Reason)
	_, err = s.repo.GetByID(s.ctx, p.ID)
	s.Require().NoError(err, "a saved group keeps its proxy configuration intact")
	_, err = s.tx.ExecContext(s.ctx, `UPDATE accounts SET extra = '{"openai_excel_bps_proxy_mode":"session","openai_excel_bps_proxy_ids":[]}'::jsonb WHERE name = $1`, "saved-group-only")
	s.Require().NoError(err)
	s.Require().NoError(admin.DeleteProxy(s.ctx, p.ID), "removing the saved group releases deletion protection")
}

func (s *ProxyRepoSuite) TestOpenAIConfiguredReferencesPreferExplicitMode() {
	newGroup := s.mustCreateProxy(&service.Proxy{Name: "new-group", Protocol: "http", Host: "192.0.2.61", Port: 8080, Status: service.StatusActive})
	legacyGroup := s.mustCreateProxy(&service.Proxy{Name: "legacy-group", Protocol: "http", Host: "192.0.2.62", Port: 8080, Status: service.StatusActive})
	for _, mode := range []string{service.OpenAIProxyModeSingle, service.OpenAIProxyModeRequest} {
		extra, err := json.Marshal(map[string]any{service.OpenAIProxyModeKey: mode, service.OpenAIProxyIDsKey: []int64{newGroup.ID, newGroup.ID}, service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: []int64{legacyGroup.ID}})
		s.Require().NoError(err)
		_, err = s.tx.ExecContext(s.ctx, `INSERT INTO accounts (name, platform, type, extra) VALUES ($1,$2,$3,$4)`, "explicit-"+mode, service.PlatformOpenAI, service.AccountTypeOAuth, string(extra))
		s.Require().NoError(err)
	}
	count, err := s.repo.CountAccountsByProxyID(s.ctx, newGroup.ID)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), count)
	count, err = s.repo.CountAccountsByProxyID(s.ctx, legacyGroup.ID)
	s.Require().NoError(err)
	s.Require().Zero(count, "explicit single/request both override retained legacy group")
	accounts, err := s.repo.ListAccountSummariesByProxyID(s.ctx, newGroup.ID)
	s.Require().NoError(err)
	s.Require().Len(accounts, 1)
	s.Require().Equal("explicit-request", accounts[0].Name)
	counts, err := s.repo.GetAccountCountsForProxies(s.ctx)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), counts[newGroup.ID])
	s.Require().Zero(counts[legacyGroup.ID])
}

func (s *ProxyRepoSuite) TestNonBPSCredentialsIgnoreRetainedGroupsButKeepLegacyProxy() {
	groupOnly := s.mustCreateProxy(&service.Proxy{Name: "non-bps-group-only", Protocol: "http", Host: "192.0.2.41", Port: 8080, Status: service.StatusActive})
	batchOnly := s.mustCreateProxy(&service.Proxy{Name: "non-bps-batch-only", Protocol: "http", Host: "192.0.2.42", Port: 8080, Status: service.StatusActive})
	legacy := s.mustCreateProxy(&service.Proxy{Name: "non-bps-legacy", Protocol: "http", Host: "192.0.2.43", Port: 8080, Status: service.StatusActive})
	extra, err := json.Marshal(map[string]any{"openai_excel_bps": true, service.ExcelBPSProxyModeKey: service.ExcelBPSProxyModeRequest, service.ExcelBPSProxyIDsKey: []int64{groupOnly.ID, batchOnly.ID, legacy.ID, legacy.ID}})
	s.Require().NoError(err)
	credentials := []map[string]any{
		{"auth_mode": " \tAGENTidentity\n"},
		{"auth_mode": "\tPersonalAccessToken \r"},
		{"auth_mode": " PERSONAL_ACCESS_TOKEN "},
		{"auth_mode": "oauth", "openai_auth_mode": " PeRsOnAlAcCeSsToKeN "},
		{"openai_auth_mode": "\u00a0personal_access_token\u3000"},
	}
	for i, credential := range credentials {
		data, marshalErr := json.Marshal(credential)
		s.Require().NoError(marshalErr)
		_, err = s.tx.ExecContext(s.ctx, `INSERT INTO accounts (name, platform, type, proxy_id, credentials, extra) VALUES ($1,$2,$3,$4,$5,$6)`, fmt.Sprintf("non-bps-credentials-%d", i), service.PlatformOpenAI, service.AccountTypeOAuth, legacy.ID, string(data), string(extra))
		s.Require().NoError(err)
	}
	for _, id := range []int64{groupOnly.ID, batchOnly.ID} {
		accounts, listErr := s.repo.ListAccountSummariesByProxyID(s.ctx, id)
		s.Require().NoError(listErr)
		s.Require().Empty(accounts, "non-BPS retained groups must not be configured references")
		count, countErr := s.repo.CountAccountsByProxyID(s.ctx, id)
		s.Require().NoError(countErr)
		s.Require().Zero(count)
	}
	accounts, err := s.repo.ListAccountSummariesByProxyID(s.ctx, legacy.ID)
	s.Require().NoError(err)
	s.Require().Len(accounts, 5, "the traditional field remains one reference per account")
	counts, err := s.repo.GetAccountCountsForProxies(s.ctx)
	s.Require().NoError(err)
	s.Require().Zero(counts[groupOnly.ID])
	s.Require().Zero(counts[batchOnly.ID])
	s.Require().Equal(int64(5), counts[legacy.ID])
	admin := service.NewAdminService(
		nil, nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	s.Require().NoError(admin.DeleteProxy(s.ctx, groupOnly.ID))
	batch, err := admin.BatchDeleteProxies(s.ctx, []int64{batchOnly.ID, legacy.ID})
	s.Require().NoError(err)
	s.Require().Equal([]int64{batchOnly.ID}, batch.DeletedIDs)
	s.Require().Len(batch.Skipped, 1)
	s.Require().Equal(legacy.ID, batch.Skipped[0].ID)
	s.Require().Equal(service.ErrProxyInUse.Error(), batch.Skipped[0].Reason)
	s.Require().ErrorIs(admin.DeleteProxy(s.ctx, legacy.ID), service.ErrProxyInUse)
}

// --- ListActiveWithAccountCount ---

func (s *ProxyRepoSuite) TestListActiveWithAccountCount() {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	p1 := s.mustCreateProxyWithTimes("p1", service.StatusActive, base.Add(-1*time.Hour))
	p2 := s.mustCreateProxyWithTimes("p2", service.StatusActive, base)
	s.mustCreateProxyWithTimes("p3-inactive", service.StatusDisabled, base.Add(1*time.Hour))

	s.mustInsertAccount("a1", &p1.ID)
	s.mustInsertAccount("a2", &p1.ID)
	s.mustInsertAccount("a3", &p2.ID)

	withCounts, err := s.repo.ListActiveWithAccountCount(s.ctx)
	s.Require().NoError(err, "ListActiveWithAccountCount")
	s.Require().Len(withCounts, 2, "expected 2 active proxies")

	// Sorted by created_at DESC, so p2 first
	s.Require().Equal(p2.ID, withCounts[0].ID)
	s.Require().Equal(int64(1), withCounts[0].AccountCount)
	s.Require().Equal(p1.ID, withCounts[1].ID)
	s.Require().Equal(int64(2), withCounts[1].AccountCount)
}

// --- Combined original test ---

func (s *ProxyRepoSuite) TestExistsByHostPortAuth_And_AccountCountAggregates() {
	p1 := s.mustCreateProxy(&service.Proxy{Name: "p1", Protocol: "http", Host: "1.2.3.4", Port: 8080, Username: "u", Password: "p", Status: service.StatusActive})
	p2 := s.mustCreateProxy(&service.Proxy{Name: "p2", Protocol: "http", Host: "5.6.7.8", Port: 8081, Username: "", Password: "", Status: service.StatusActive})

	exists, err := s.repo.ExistsByHostPortAuth(s.ctx, "1.2.3.4", 8080, "u", "p")
	s.Require().NoError(err, "ExistsByHostPortAuth")
	s.Require().True(exists, "expected proxy to exist")

	s.mustInsertAccount("a1", &p1.ID)
	s.mustInsertAccount("a2", &p1.ID)
	s.mustInsertAccount("a3", &p2.ID)

	count1, err := s.repo.CountAccountsByProxyID(s.ctx, p1.ID)
	s.Require().NoError(err, "CountAccountsByProxyID")
	s.Require().Equal(int64(2), count1, "expected 2 accounts for p1")

	counts, err := s.repo.GetAccountCountsForProxies(s.ctx)
	s.Require().NoError(err, "GetAccountCountsForProxies")
	s.Require().Equal(int64(2), counts[p1.ID])
	s.Require().Equal(int64(1), counts[p2.ID])

	withCounts, err := s.repo.ListActiveWithAccountCount(s.ctx)
	s.Require().NoError(err, "ListActiveWithAccountCount")
	s.Require().Len(withCounts, 2, "expected 2 proxies")
	for _, pc := range withCounts {
		switch pc.ID {
		case p1.ID:
			s.Require().Equal(int64(2), pc.AccountCount, "p1 count mismatch")
		case p2.ID:
			s.Require().Equal(int64(1), pc.AccountCount, "p2 count mismatch")
		default:
			s.Require().Fail("unexpected proxy id", pc.ID)
		}
	}
}

func (s *ProxyRepoSuite) mustCreateProxy(p *service.Proxy) *service.Proxy {
	s.T().Helper()
	s.Require().NoError(s.repo.Create(s.ctx, p), "create proxy")
	return p
}

func (s *ProxyRepoSuite) mustCreateProxyWithTimes(name, status string, createdAt time.Time) *service.Proxy {
	s.T().Helper()

	// Use the repository create for standard fields, then update timestamps via raw SQL to keep deterministic ordering.
	p := s.mustCreateProxy(&service.Proxy{
		Name:     name,
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
		Status:   status,
	})
	_, err := s.tx.ExecContext(s.ctx, "UPDATE proxies SET created_at = $1, updated_at = $1 WHERE id = $2", createdAt, p.ID)
	s.Require().NoError(err, "update proxy timestamps")
	return p
}

func (s *ProxyRepoSuite) mustInsertAccount(name string, proxyID *int64) {
	s.T().Helper()
	var pid any
	if proxyID != nil {
		pid = *proxyID
	}
	_, err := s.tx.ExecContext(
		s.ctx,
		"INSERT INTO accounts (name, platform, type, proxy_id) VALUES ($1, $2, $3, $4)",
		name,
		service.PlatformAnthropic,
		service.AccountTypeOAuth,
		pid,
	)
	s.Require().NoError(err, "insert account")
}
