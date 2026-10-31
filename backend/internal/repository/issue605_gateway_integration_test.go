//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This fixture is a custom native API-key endpoint, not an official-host
// authorization projection. Keep its final transport restricted to loopback.
type issue605LoopbackInference struct {
	client *http.Client
	host   string
}

func (u issue605LoopbackInference) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if r.URL == nil || r.URL.Scheme != "http" || r.URL.Host != u.host {
		return nil, fmt.Errorf("unexpected isolated upstream")
	}
	return u.client.Do(r)
}
func (u issue605LoopbackInference) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}

// Full authenticated handler -> actual scheduler/service/bridge -> loopback SSE,
// with actual SQL usage/balance settlement and Redis turn-slot readback.
func TestSmartProtocolGatewayBillingAndQuotaIntegration(t *testing.T) {
	f := newConnectionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	redis := repository.Issue594TestRedis()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, f.db)))
	user, err := client.User.Create().SetEmail(fmt.Sprintf("smart-protocol-%d@example.invalid", f.accountID)).SetPasswordHash("synthetic").SetRole(service.RoleUser).SetStatus(service.StatusActive).SetBalance(1000).SetConcurrency(2).Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().SetName(fmt.Sprintf("smart-protocol-%d", f.accountID)).SetPlatform(service.PlatformOpenAI).SetSubscriptionType(service.SubscriptionTypeStandard).SetStatus(service.StatusActive).SetRateMultiplier(1).Save(ctx)
	require.NoError(t, err)
	keys := repository.NewAPIKeyRepository(client, f.db)
	key := &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: fmt.Sprintf("sk-smart-synthetic-%d", f.accountID), Name: "synthetic", Status: service.StatusActive, ConcurrencyLimit: 2}
	require.NoError(t, keys.Create(ctx, key))
	_, err = f.db.Exec(`INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, f.accountID, group.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM usage_logs WHERE account_id=$1`, f.accountID)
		_, _ = f.db.Exec(`DELETE FROM accounts WHERE id=$1`, f.accountID)
		_, _ = f.db.Exec(`DELETE FROM api_keys WHERE id=$1`, key.ID)
		_, _ = f.db.Exec(`DELETE FROM groups WHERE id=$1`, group.ID)
		_, _ = f.db.Exec(`DELETE FROM users WHERE id=$1`, user.ID)
	})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEqual(t, "websocket", strings.ToLower(r.Header.Get("Upgrade")))
		n := calls.Add(1)
		raw, e := io.ReadAll(r.Body)
		require.NoError(t, e)
		if n == 2 {
			require.Equal(t, "resp_smart_1", gjson.GetBytes(raw, "previous_response_id").String())
			require.Equal(t, "second", gjson.GetBytes(raw, "input").String())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if gjson.GetBytes(raw, "input").String() == "partial" {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_smart_partial\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
			return
		}
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_smart_%d\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_smart_%d\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":7}}}\n\n", n, n)
	}))
	defer upstream.Close()
	credentials, _ := json.Marshal(map[string]any{"api_key": "synthetic-native", "base_url": upstream.URL})
	_, err = f.db.Exec(`UPDATE accounts SET type='apikey',credentials=$2::jsonb,extra='{"openai_responses_supported":true,"responses_websockets_v2_enabled":true}'::jsonb WHERE id=$1`, f.accountID, string(credentials))
	require.NoError(t, err)
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.SmartProtocol = config.GatewayOpenAIWSSmartProtocolConfig{Enabled: true, Mode: "http"}
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 2 * time.Second
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	users := repository.NewUserRepository(client, f.db)
	groups := repository.NewGroupRepository(client, f.db)
	slots := repository.NewConcurrencyCache(redis, 1, 60)
	concurrency := service.NewConcurrencyService(slots)
	billing := service.NewBillingCacheService(repository.NewBillingCache(redis), users, nil, keys, nil, nil, cfg, nil)
	defer billing.Stop()
	deferred := service.NewDeferredService(f.accounts, nil, time.Hour)
	prices := service.NewBillingService(cfg, nil)
	transport := issue605LoopbackInference{client: upstream.Client(), host: strings.TrimPrefix(upstream.URL, "http://")}
	gateway := service.NewOpenAIGatewayService(f.accounts, nil, repository.NewUsageLogRepository(client, f.db), repository.NewUsageBillingRepository(client, f.db), users, nil, nil, repository.NewGatewayCache(redis), cfg, nil, concurrency, prices, nil, billing, transport, deferred, nil, nil, nil, nil, nil, nil, nil)
	defer gateway.StopOpenAICodexTicketHarvester()
	defer gateway.CloseOpenAIWSPool()
	keyService := service.NewAPIKeyService(keys, users, groups, nil, nil, nil, cfg)
	h := handler.NewOpenAIGatewayHandler(gateway, concurrency, billing, keyService, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keyService, nil, cfg)))
	router.GET("/v1/responses", h.ResponsesWebSocket)
	ingress := httptest.NewServer(router)
	defer ingress.Close()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(ingress.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + key.Key}}})
	require.NoError(t, err)
	defer func(c *coderws.Conn) { _ = c.CloseNow() }(conn)
	send := func(input, previous string) {
		raw, _ := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.4", "input": input, "previous_response_id": previous, "store": true})
		require.NoError(t, conn.Write(ctx, coderws.MessageText, raw))
	}
	terminal := func() {
		for {
			_, raw, e := conn.Read(ctx)
			require.NoError(t, e)
			if gjson.GetBytes(raw, "type").String() == "response.completed" {
				return
			}
		}
	}
	send("first", "")
	terminal()
	send("second", "resp_smart_1")
	terminal()
	send("partial", "resp_smart_2")
	for {
		_, _, e := conn.Read(ctx)
		if e != nil {
			break
		}
	}

	require.Eventually(t, func() bool {
		var n int
		_ = f.db.QueryRow(`SELECT count(*) FROM usage_logs WHERE account_id=$1`, f.accountID).Scan(&n)
		return n == 3
	}, 5*time.Second, 20*time.Millisecond)
	var balance, cost float64
	require.NoError(t, f.db.QueryRow(`SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.NoError(t, f.db.QueryRow(`SELECT COALESCE(SUM(actual_cost),0) FROM usage_logs WHERE account_id=$1`, f.accountID).Scan(&cost))
	require.Greater(t, cost, 0.0)
	require.InDelta(t, cost, 1000-balance, 1e-9)
	// Authoritative account quota changes are checked before a later send.
	_, err = f.db.Exec(`UPDATE accounts SET rate_limited_at=NOW(),rate_limit_reset_at=NOW()+INTERVAL '5 minutes' WHERE id=$1`, f.accountID)
	require.NoError(t, err)
	conn, _, err = coderws.Dial(ctx, "ws"+strings.TrimPrefix(ingress.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + key.Key}}})
	require.NoError(t, err)
	defer func(c *coderws.Conn) { _ = c.CloseNow() }(conn)
	send("blocked", "")
	for {
		_, _, e := conn.Read(ctx)
		if e != nil {
			break
		}
	}
	require.EqualValues(t, 3, calls.Load(), "partial usage is settled once; quota rejection cannot submit or replay")
	require.Eventually(t, func() bool { n, e := slots.GetAccountConcurrency(ctx, f.accountID); return e == nil && n == 0 }, 5*time.Second, 20*time.Millisecond)
	var rows int
	require.NoError(t, f.db.QueryRow(`SELECT count(*) FROM usage_logs WHERE account_id=$1`, f.accountID).Scan(&rows))
	require.Equal(t, 3, rows)
}
