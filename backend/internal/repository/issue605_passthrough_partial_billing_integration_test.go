//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Actual authenticated production handler, service, native WS transport, PG
// settlement and Redis slots. No aggregate relay result or fake billing port.
func TestSmartProtocolPassthroughPartialBilling(t *testing.T) {
	for _, failure := range []string{"eof", "client_cancel", "drain_timeout", "control_cancel", "missing_usage", "normal_terminal", "terminal_error"} {
		priors := []bool{false}
		if failure == "eof" || failure == "client_cancel" || failure == "drain_timeout" || failure == "control_cancel" {
			priors = append(priors, true)
		}
		for _, prior := range priors {
			t.Run(fmt.Sprintf("%s/prior_%t", failure, prior), func(t *testing.T) { runPassthroughPartialBilling(t, failure, prior) })
		}
	}
}
func runPassthroughPartialBilling(t *testing.T, failure string, prior bool) {
	f := newConnectionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	redis := repository.Issue594TestRedis()
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, f.db)))
	user, err := client.User.Create().SetEmail(fmt.Sprintf("p1-%d@example.invalid", f.accountID)).SetPasswordHash("synthetic").SetRole(service.RoleUser).SetStatus(service.StatusActive).SetBalance(1000).SetConcurrency(2).Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().SetName(fmt.Sprintf("p1-%d", f.accountID)).SetPlatform(service.PlatformOpenAI).SetSubscriptionType(service.SubscriptionTypeStandard).SetStatus(service.StatusActive).SetRateMultiplier(1).Save(ctx)
	require.NoError(t, err)
	keys := repository.NewAPIKeyRepository(client, f.db)
	key := &service.APIKey{UserID: user.ID, GroupID: &group.ID, Key: fmt.Sprintf("sk-p1-synthetic-%d", f.accountID), Name: "synthetic", Status: service.StatusActive, ConcurrencyLimit: 2}
	require.NoError(t, keys.Create(ctx, key))
	_, err = f.db.Exec(`INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, f.accountID, group.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, q := range []struct {
			sql string
			id  int64
		}{{"DELETE FROM usage_logs WHERE account_id=$1", f.accountID}, {"DELETE FROM accounts WHERE id=$1", f.accountID}, {"DELETE FROM api_keys WHERE id=$1", key.ID}, {"DELETE FROM groups WHERE id=$1", group.ID}, {"DELETE FROM users WHERE id=$1", user.ID}} {
			_, e := f.db.Exec(q.sql, q.id)
			require.NoError(t, e)
		}
	})
	var calls atomic.Int32
	var active atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := coderws.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		active.Add(1)
		defer active.Add(-1)
		write := func(raw string) bool { return conn.Write(ctx, coderws.MessageText, []byte(raw)) == nil }
		for {
			_, raw, e := conn.Read(ctx)
			if e != nil {
				return
			}
			calls.Add(1)
			marker := gjson.GetBytes(raw, "input").String()
			id := "resp_partial"
			if marker == "completed" {
				id = "resp_completed"
			}
			if !write(fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"model":"gpt-5.4"}}`, id)) {
				return
			}
			if marker == "completed" {
				if !write(`{"type":"response.completed","response":{"id":"resp_completed","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":7}}}`) {
					return
				}
				continue
			}
			if failure == "missing_usage" {
				return
			}
			if !write(`{"type":"response.in_progress","response":{"id":"resp_partial","model":"gpt-5.4","usage":{"input_tokens":3,"output_tokens":2}}}`) {
				return
			}
			switch failure {
			case "eof":
				return
			case "client_cancel":
				select {
				case <-release:
				case <-ctx.Done():
				}
				return
			case "normal_terminal":
				write(`{"type":"response.completed","response":{"id":"resp_partial","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2}}}`)
			case "terminal_error":
				write(`{"type":"response.failed","response":{"id":"resp_partial","model":"gpt-5.4","status":"failed","error":{"code":"invalid_request_error","message":"synthetic rejection"},"usage":{"input_tokens":3,"output_tokens":2}}}`)
			}
			_, _, _ = conn.Read(ctx)
			return
		}
	}))
	defer upstream.Close()
	credentials, _ := json.Marshal(map[string]any{"api_key": "synthetic-native", "base_url": upstream.URL})
	_, err = f.db.Exec(`UPDATE accounts SET type='apikey',credentials=$2::jsonb,extra='{"openai_responses_supported":true,"responses_websockets_v2_enabled":true,"openai_apikey_responses_websockets_v2_mode":"passthrough"}'::jsonb WHERE id=$1`, f.accountID, string(credentials))
	require.NoError(t, err)
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = service.OpenAIWSIngressModePassthrough
	cfg.Gateway.OpenAIWS.SmartProtocol = config.GatewayOpenAIWSSmartProtocolConfig{Enabled: true, Mode: "ws"}
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 2 * time.Second
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	users := repository.NewUserRepository(client, f.db)
	groups := repository.NewGroupRepository(client, f.db)
	slots := repository.NewConcurrencyCache(redis, 1, 60)
	concurrency := service.NewConcurrencyService(slots)
	billing := service.NewBillingCacheService(repository.NewBillingCache(redis), users, nil, keys, nil, nil, cfg, nil)
	defer billing.Stop()
	prices := service.NewBillingService(cfg, nil)
	pricing, err := prices.GetModelPricing("gpt-5.4")
	require.NoError(t, err)
	gateway := service.NewOpenAIGatewayService(f.accounts, nil, repository.NewUsageLogRepository(client, f.db), repository.NewUsageBillingRepository(client, f.db), users, nil, nil, repository.NewGatewayCache(redis), cfg, nil, concurrency, prices, nil, billing, issue605LoopbackInference{client: upstream.Client(), host: strings.TrimPrefix(upstream.URL, "http://")}, service.NewDeferredService(f.accounts, nil, time.Hour), nil, nil, nil, nil, nil, nil, nil)
	defer gateway.StopOpenAICodexTicketHarvester()
	defer gateway.CloseOpenAIWSPool()
	keyService := service.NewAPIKeyService(keys, users, groups, nil, nil, nil, cfg)
	h := handler.NewOpenAIGatewayHandler(gateway, concurrency, billing, keyService, nil, nil, nil, nil, cfg)
	handlerDone := make(chan struct{})
	router := gin.New()
	router.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keyService, nil, cfg)))
	router.GET("/v1/responses", func(c *gin.Context) { defer close(handlerDone); h.ResponsesWebSocket(c) })
	ingress := httptest.NewUnstartedServer(router)
	ingress.Config.BaseContext = func(net.Listener) context.Context { return appCtx }
	ingress.Start()
	defer ingress.Close()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(ingress.URL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + key.Key}}})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	send := func(marker, previous string) {
		raw, _ := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.4", "input": marker, "previous_response_id": previous, "store": true})
		require.NoError(t, conn.Write(ctx, coderws.MessageText, raw))
	}
	readType := func(want string) {
		for {
			_, raw, e := conn.Read(ctx)
			require.NoError(t, e)
			if gjson.GetBytes(raw, "type").String() == want {
				return
			}
		}
	}
	if prior {
		send("completed", "")
		readType("response.completed")
	}
	send("partial", "")
	if failure != "missing_usage" {
		readType("response.in_progress")
	}
	switch failure {
	case "client_cancel":
		_ = conn.CloseNow()
		releaseOnce.Do(func() { close(release) })
	case "drain_timeout":
		_ = conn.CloseNow()
	case "control_cancel":
		cancelApp()
	case "normal_terminal":
		readType("response.completed")
		_ = conn.CloseNow()
	case "terminal_error":
		readType("response.failed")
		_ = conn.CloseNow()
	default:
		for {
			_, _, e := conn.Read(ctx)
			if e != nil {
				break
			}
		}
	}
	select {
	case <-handlerDone:
	case <-time.After(7 * time.Second):
		t.Fatal("P1_HANDLER_CLEANUP_TIMEOUT")
	}
	require.Eventually(t, func() bool {
		a, e := slots.GetAccountConcurrency(ctx, f.accountID)
		u, ue := slots.GetUserConcurrency(ctx, user.ID)
		k, ke := concurrency.GetAPIKeyConcurrencyBatch(ctx, []int64{key.ID})
		return e == nil && ue == nil && ke == nil && a == 0 && u == 0 && k[key.ID] == 0
	}, 4*time.Second, 10*time.Millisecond, "P1_SLOTS_LEAKED")
	expectedRows := 1
	if failure == "missing_usage" {
		expectedRows = 0
	}
	if prior {
		expectedRows++
	}
	require.Eventually(t, func() bool {
		var n int
		_ = f.db.QueryRow(`SELECT count(*) FROM usage_logs WHERE account_id=$1`, f.accountID).Scan(&n)
		return n == expectedRows
	}, 4*time.Second, 10*time.Millisecond, "P1_AUTHORITATIVE_USAGE_NOT_BILLED")
	var count, input, output int
	var cost float64
	require.NoError(t, f.db.QueryRow(`SELECT count(*),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(actual_cost),0) FROM usage_logs WHERE account_id=$1`, f.accountID).Scan(&count, &input, &output, &cost))
	wantInput, wantOutput := 3, 2
	if failure == "missing_usage" {
		wantInput, wantOutput = 0, 0
	}
	if prior {
		wantInput += 5
		wantOutput += 7
	}
	require.Equal(t, expectedRows, count, "P1_DUPLICATE_SETTLEMENT")
	require.Equal(t, wantInput, input, "P1_PRIOR_USAGE_REBILLED")
	require.Equal(t, wantOutput, output, "P1_PRIOR_USAGE_REBILLED")
	expectedCost := float64(wantInput)*pricing.InputPricePerToken + float64(wantOutput)*pricing.OutputPricePerToken
	require.InDelta(t, expectedCost, cost, 1e-10, "P1_EXACT_COST_MISMATCH")
	var balance float64
	require.NoError(t, f.db.QueryRow(`SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
	require.InDelta(t, 1000-expectedCost, balance, 1e-9, "P1_EXACT_BALANCE_MISMATCH")
	wantCalls := int32(1)
	if prior {
		wantCalls++
	}
	require.Equal(t, wantCalls, calls.Load(), "P1_UPSTREAM_REPLAY")
	gateway.CloseOpenAIWSPool()
	require.Eventually(t, func() bool { return active.Load() == 0 }, 2*time.Second, 10*time.Millisecond, "P1_UPSTREAM_NOT_CLOSED")
}
