//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUpstreamChannelEntityHTTPAccountProfitPersistence(t *testing.T) {
	ctx := context.Background()
	db, client := repository.UpstreamChannelsTestDatabase(t)
	userRepo := repository.NewUserRepository(client, db)
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "entity-isolated-test", ExpireHour: 1}}
	var uid, regular int64
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash,role) VALUES('entity-admin@test.invalid','fixture','admin') RETURNING id`).Scan(&uid))
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash,role) VALUES('entity-user@test.invalid','fixture','user') RETURNING id`).Scan(&regular))
	auth := service.NewAuthService(client, userRepo, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	au, e := userRepo.GetByID(ctx, uid)
	require.NoError(t, e)
	token, e := auth.GenerateToken(ctx, au)
	require.NoError(t, e)
	ru, e := userRepo.GetByID(ctx, regular)
	require.NoError(t, e)
	userToken, e := auth.GenerateToken(ctx, ru)
	require.NoError(t, e)
	channelService := service.NewChannelService(repository.NewChannelRepository(db), nil, nil, nil, nil)
	ch := admin.NewChannelHandler(channelService, nil, nil)
	adminService := service.NewAdminService(cfg, userRepo, repository.NewAdminGroupRepository(client, db), repository.NewAdminAccountRepository(client, db, nil), nil, nil, nil, nil, nil, nil, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ah := admin.NewAccountHandler(adminService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ah.SetUpstreamChannelService(channelService)
	audit := service.NewAuditLogService(repository.NewAuditLogRepository(db), nil)
	audit.StartWriter()
	t.Cleanup(audit.Stop)
	r := gin.New()
	g := r.Group("/api/v1/admin")
	g.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, service.NewUserService(userRepo, nil, nil, nil), nil, nil)))
	g.Use(gin.HandlerFunc(middleware.NewAuditLogMiddleware(audit)))
	g.Use(ah.AuthorizeObserver)
	g.GET("/upstream-channels", ch.ListUpstreamChannels)
	g.POST("/upstream-channels", ch.MutateUpstreamChannel)
	g.GET("/upstream-channels/:id", ch.GetUpstreamChannel)
	g.PUT("/upstream-channels/:id", ch.MutateUpstreamChannel)
	g.DELETE("/upstream-channels/:id", ch.MutateUpstreamChannel)
	g.GET("/upstream-channels/:id/members", ch.UpstreamChannelMembers)
	g.GET("/upstream-channel-bindings", ch.ReadUpstreamChannelBindings)
	g.PUT("/upstream-channel-bindings", ch.BindUpstreamChannelAccounts)
	g.POST("/accounts", ah.Create)
	g.GET("/accounts/:id", ah.GetByID)
	g.GET("/accounts", ah.List)
	g.GET("/profit-operations/snapshot", ch.ProfitOperations)
	request := func(method, path string, body any, credential string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, "/api/v1/admin"+path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Admin-UI", "1")
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, out any) {
		t.Helper()
		require.Equal(t, 200, w.Code, w.Body.String())
		var b struct{ Data json.RawMessage }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &b))
		require.NoError(t, json.Unmarshal(b.Data, out))
	}
	create := func(code, name string) service.UpstreamChannel {
		var c service.UpstreamChannel
		decode(request("POST", "/upstream-channels", service.UpstreamChannelMutation{Code: code, Name: name, Remark: "实体备注", Confirm: true}, token), &c)
		require.Positive(t, c.ID)
		return c
	}
	get := func(id int64) service.UpstreamChannel {
		var c service.UpstreamChannel
		decode(request("GET", fmt.Sprintf("/upstream-channels/%d", id), nil, token), &c)
		return c
	}
	require.Equal(t, 400, request("POST", "/upstream-channels", service.UpstreamChannelMutation{Code: "unconfirmed", Name: "Unconfirmed"}, token).Code)
	a := create("supplier-a", "供应商A")
	b := create("supplier-b", "供应商B")
	require.Equal(t, 409, request("POST", "/upstream-channels", service.UpstreamChannelMutation{Code: a.Code, Name: "另一个名字", Confirm: true}, token).Code)
	require.Equal(t, 409, request("POST", "/upstream-channels", service.UpstreamChannelMutation{Code: "unique-code", Name: a.Name, Confirm: true}, token).Code)
	for _, path := range []string{"/upstream-channels", "/upstream-channel-bindings?account_ids=1"} {
		require.Equal(t, 401, request("GET", path, nil, "").Code)
		require.Equal(t, 403, request("GET", path, nil, userToken).Code)
	}
	require.Equal(t, 403, request("PUT", "/upstream-channel-bindings", service.UpstreamChannelBindingsMutation{}, userToken).Code)
	var key int64
	require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,'entity-fixture-key','fixture') RETURNING id`, uid).Scan(&key))
	rawAccounts := []struct{ platform, kind, url string }{{"openai", "apikey", "https://same.example/v1"}, {"openai", "apikey", "https://different.example:8443/Tenant/v2"}, {"gemini", "oauth", ""}, {"openai", "apikey", "https://name:private@secret.example/v1?token=secret-query"}, {"openai", "apikey", "https://same.example/v1"}, {"openai", "apikey", "https://fallback.example/v1"}, {"gemini", "oauth", ""}}
	ids := []int64{}
	for i, fixture := range rawAccounts {
		credentials, _ := json.Marshal(map[string]string{"base_url": fixture.url, "api_key": "do-not-return"})
		var id int64
		require.NoError(t, db.QueryRow(`INSERT INTO accounts(name,platform,type,credentials,extra) VALUES($1,$2,$3,$4::jsonb,'{"cost_multiplier":0.25}') RETURNING id`, fmt.Sprintf("entity-account-%d", i), fixture.platform, fixture.kind, string(credentials)).Scan(&id))
		ids = append(ids, id)
	}
	bindings := func(accounts []int64) []service.UpstreamChannelBinding {
		parts := []string{}
		for _, id := range accounts {
			parts = append(parts, strconv.FormatInt(id, 10))
		}
		var result struct {
			Items []service.UpstreamChannelBinding
		}
		decode(request("GET", "/upstream-channel-bindings?account_ids="+strings.Join(parts, ","), nil, token), &result)
		require.Len(t, result.Items, len(accounts))
		return result.Items
	}
	bind := func(target int64, accounts []int64) {
		t.Helper()
		m := service.UpstreamChannelBindingsMutation{ChannelID: target, Bindings: bindings(accounts), Confirm: true}
		var result struct {
			Items []service.UpstreamChannelBinding
		}
		decode(request("PUT", "/upstream-channel-bindings", m, token), &result)
		require.Len(t, result.Items, len(accounts))
		for _, saved := range bindings(accounts) {
			require.Equal(t, target, saved.ChannelID, "explicit binding must persist independently of URL eligibility")
		}
	}
	bind(a.ID, ids[:4])
	bind(b.ID, ids[4:5])
	for i, id := range ids {
		_, e = db.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,account_stats_cost,account_rate_multiplier,created_at) VALUES($1,$2,$3,$4,'entity-model',10,4,8,999,clock_timestamp())`, uid, key, id, fmt.Sprintf("entity-request-%d", i))
		require.NoError(t, e)
		_, e = db.Exec(`INSERT INTO ops_error_logs(account_id,model,error_phase,error_type,status_code,is_count_tokens,created_at) VALUES($1,'entity-model','upstream','http_error',502,false,clock_timestamp())`, id)
		require.NoError(t, e)
	}
	var usageBefore string
	require.NoError(t, db.QueryRow(`SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM usage_logs u`).Scan(&usageBefore))
	var now time.Time
	require.NoError(t, db.QueryRow(`SELECT clock_timestamp() + interval '1 second'`).Scan(&now))
	now = now.UTC()
	require.Greater(t, get(a.ID).Revision, a.Revision, "membership changes invalidate multi-account edit confirmation")
	a = get(a.ID)
	b = get(b.ID)
	profit := func(upstream string, page int) service.ProfitOperationsSnapshot {
		path := fmt.Sprintf("/profit-operations/snapshot?from=%s&until=%s&model=entity-model&upstream=%s&page=%d&page_size=1&sort=name&direction=asc", now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339), upstream, page)
		var out service.ProfitOperationsSnapshot
		decode(request("GET", path, nil, token), &out)
		raw, _ := json.Marshal(out)
		for _, secret := range []string{"name:private", "secret-query", "do-not-return"} {
			require.NotContains(t, string(raw), secret)
		}
		return out
	}
	type row struct {
		Key, Name, Assignment string
		ChannelID             int64 `json:"channel_id"`
		Requests              int
		Profit                float64 `json:"estimated_profit"`
	}
	all := map[string]row{}
	for page := 1; page <= 4; page++ {
		out := profit("", page)
		var data struct {
			Rows  []row
			Total int
		}
		require.NoError(t, json.Unmarshal(out.Usage, &data))
		require.Equal(t, 4, data.Total)
		require.Len(t, data.Rows, 1)
		all[data.Rows[0].Key] = data.Rows[0]
	}
	aKey := fmt.Sprintf("channel:%d", a.ID)
	bKey := fmt.Sprintf("channel:%d", b.ID)
	selected := profit(aKey, 1)
	require.Equal(t, a.ID, selected.Scope.ChannelID, "public endpoint must normalize channel:<id> into the explicit FK scope")
	var selectedData struct {
		Rows  []row
		Total int
	}
	require.NoError(t, json.Unmarshal(selected.Usage, &selectedData))
	require.Equal(t, 1, selectedData.Total, "drill-down filters by explicit foreign key rather than URL text")
	require.Equal(t, 4, selectedData.Rows[0].Requests)
	require.Equal(t, "ready", selected.Errors.Status)
	var selectedErrors struct{ Records int }
	require.NoError(t, json.Unmarshal(selected.Errors.Data, &selectedErrors))
	require.Equal(t, 4, selectedErrors.Records, "explicit entity filters retained errors to the same account cohort")

	require.Equal(t, 4, all[aKey].Requests, "explicit ownership groups different, unknown and unsupported URLs")
	require.Equal(t, 6.0, all[aKey].Profit, "confirmed formula: saved total_cost × current account multiplier only")
	require.Equal(t, "explicit", all[aKey].Assignment)
	require.Equal(t, 1, all[bKey].Requests, "same URL can belong to another explicit channel")
	require.Equal(t, "unbound_url", all["https://fallback.example/v1"].Assignment)
	require.Equal(t, "unbound_unknown", all["unknown:oauth_dynamic_endpoint"].Assignment)
	// Rename and URL changes never change the internal aggregation key or bindings.
	a = get(a.ID)
	renamed := service.UpstreamChannelMutation{Code: a.Code, Name: "供应商A更名", Remark: "共享说明已改", Revision: &a.Revision, Confirm: true}
	var updated service.UpstreamChannel
	decode(request("PUT", fmt.Sprintf("/upstream-channels/%d", a.ID), renamed, token), &updated)
	require.Equal(t, a.ID, updated.ID)
	require.Equal(t, 409, request("PUT", fmt.Sprintf("/upstream-channels/%d", a.ID), renamed, token).Code)
	_, e = db.Exec(`UPDATE accounts SET name='renamed',credentials='{"base_url":"https://changed.example:9443/New"}' WHERE id=$1`, ids[0])
	require.NoError(t, e)
	var afterRename struct{ Rows []row }
	out := profit(aKey, 1)
	require.NoError(t, json.Unmarshal(out.Usage, &afterRename))
	require.Equal(t, aKey, afterRename.Rows[0].Key)
	require.Equal(t, "供应商A更名", afterRename.Rows[0].Name)
	require.Equal(t, 4, afterRename.Rows[0].Requests)
	// Initial account binding uses the real management API and one transaction.
	initial := map[string]any{"name": "created-with-channel", "platform": "anthropic", "type": "apikey", "credentials": map[string]string{"api_key": "synthetic-fixture", "base_url": "https://initial.example"}, "upstream_channel_id": a.ID, "confirm_upstream_channel_binding": true, "confirm_mixed_channel_risk": true}
	var created struct{ ID int64 }
	decode(request("POST", "/accounts", initial, token), &created)
	require.Positive(t, created.ID)
	require.Equal(t, a.ID, bindings([]int64{created.ID})[0].ChannelID)
	accountRead := request("GET", fmt.Sprintf("/accounts/%d", created.ID), nil, token)
	require.Equal(t, 200, accountRead.Code)
	require.Contains(t, accountRead.Body.String(), `"upstream_channel_binding"`)
	require.Contains(t, accountRead.Body.String(), fmt.Sprintf(`"channel_id":%d`, a.ID))
	initial["name"] = "must-rollback"
	initial["upstream_channel_id"] = int64(999999999)
	require.Equal(t, 404, request("POST", "/accounts", initial, token).Code)
	var absent int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts WHERE name='must-rollback'`).Scan(&absent))
	require.Zero(t, absent, "invalid channel rolls back the new account")
	// Cannot delete a bound channel or silently detach it, even with confirmation.
	a = get(a.ID)
	require.Equal(t, 409, request("DELETE", fmt.Sprintf("/upstream-channels/%d", a.ID), service.UpstreamChannelMutation{Revision: &a.Revision, Confirm: true}, token).Code)
	require.Equal(t, a.ID, bindings(ids[:1])[0].ChannelID)
	// Stale bind has no partial effect; bounded explicit detach returns URL fallback.
	stale := bindings(ids[:2])
	bind(b.ID, ids[:1])
	m := service.UpstreamChannelBindingsMutation{ChannelID: 0, Bindings: stale, Confirm: true}
	require.Equal(t, 409, request("PUT", "/upstream-channel-bindings", m, token).Code)
	require.Equal(t, a.ID, bindings(ids[1:2])[0].ChannelID)
	bind(a.ID, ids[:1])
	raceBindings := bindings(ids[:1])
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, target := range []int64{0, b.ID} {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- request("PUT", "/upstream-channel-bindings", service.UpstreamChannelBindingsMutation{ChannelID: target, Bindings: raceBindings, Confirm: true}, token).Code
		}()
	}
	wg.Wait()
	close(codes)
	got := []int{}
	for code := range codes {
		got = append(got, code)
	}
	require.ElementsMatch(t, []int{200, 409}, got)
	bind(0, append(append([]int64{}, ids[:4]...), created.ID))
	a = get(a.ID)
	require.Zero(t, a.Members)
	require.Equal(t, 400, request("DELETE", fmt.Sprintf("/upstream-channels/%d", a.ID), service.UpstreamChannelMutation{Revision: &a.Revision}, token).Code)
	decode(request("DELETE", fmt.Sprintf("/upstream-channels/%d", a.ID), service.UpstreamChannelMutation{Revision: &a.Revision, Confirm: true}, token), &map[string]any{})
	require.Equal(t, 404, request("GET", fmt.Sprintf("/upstream-channels/%d", a.ID), nil, token).Code)
	var accountCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&accountCount))
	require.Equal(t, len(ids)+1, accountCount)
	var usageAfter string
	require.NoError(t, db.QueryRow(`SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM usage_logs u`).Scan(&usageAfter))
	require.Equal(t, usageBefore, usageAfter, "bindings and delete never rewrite usage or financial rows")
	require.Equal(t, 0, int(bindings(ids[1:2])[0].ChannelID))
	var fallback struct{ Rows []row }
	out = profit("https://different.example:8443/Tenant/v2", 1)
	require.NoError(t, json.Unmarshal(out.Usage, &fallback))
	require.Empty(t, fallback.Rows, "unlinking today must not move historical income to the URL bucket")
	out = profit(aKey, 1)
	require.NoError(t, json.Unmarshal(out.Usage, &afterRename))
	require.Len(t, afterRename.Rows, 1)
	require.Equal(t, 4, afterRename.Rows[0].Requests)
	require.Equal(t, 6.0, afterRename.Rows[0].Profit)
	// Ordinary account deletion is soft: retain explicit ownership for historical usage.
	beforeDeleteRevision := get(b.ID).Revision
	require.NoError(t, repository.NewAdminAccountRepository(client, db, nil).Delete(ctx, ids[4]))
	require.Equal(t, beforeDeleteRevision, get(b.ID).Revision)
	var retainedUsage int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM usage_logs WHERE account_id=$1`, ids[4]).Scan(&retainedUsage))
	require.Equal(t, 1, retainedUsage)
	var bindingCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM upstream_channel_bindings WHERE account_id=$1`, ids[4]).Scan(&bindingCount))
	require.Equal(t, 1, bindingCount, "soft deletion must retain explicit channel ownership")
	retainedBinding := bindings(ids[4:5])[0]
	require.Equal(t, b.ID, retainedBinding.ChannelID)
	require.True(t, retainedBinding.Deleted)
	retainedProfit := profit(bKey, 1)
	var retained struct{ Rows []row }
	require.NoError(t, json.Unmarshal(retainedProfit.Usage, &retained))
	require.Equal(t, bKey, retained.Rows[0].Key)

	audit.Stop()
	var auditRaw string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(a))::text,'[]') FROM audit_logs a WHERE action LIKE 'admin.upstream_channel.%'`).Scan(&auditRaw))
	require.Contains(t, auditRaw, "filter_hash")
	var createdAudits int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='admin.upstream_channel.post' AND status_code=200 AND (extra->>'target_channel_id')::bigint IN ($1,$2)`, a.ID, b.ID).Scan(&createdAudits))
	require.Equal(t, 2, createdAudits, "creation audit must identify assigned immutable channel IDs")

	require.NotContains(t, auditRaw, "实体备注")
	require.NotContains(t, auditRaw, "secret-query")
}
