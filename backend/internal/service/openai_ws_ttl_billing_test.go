//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise a real downstream WS, the production passthrough adapter and the
// same service's tariff resolution, usage persistence and debit boundary.
func TestOpenAIWSPassthroughCacheWriteTTLReachesBillingOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
	cfg := svc.cfg
	cfg.Gateway.OpenAIWS = passthroughLifecycleConfig().Gateway.OpenAIWS
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	var pricing ChannelModelPricing
	require.NoError(t, json.Unmarshal([]byte(`{"platform":"openai","models":["gpt-6-luna"],"billing_mode":"token","input_price":0.00000065,"output_price":0.00000325,"cache_write_price":0.0000008125,"cache_write_12h_price":0.000001625,"cache_read_price":0.000000065}`), &pricing))
	const groupID int64 = 777
	channel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{groupID}, ModelPricing: []ChannelModelPricing{pricing}, ApplyPricingToAccountStats: true}
	svc.channelService = &ChannelService{}
	svc.channelService.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{groupID: PlatformOpenAI}))
	svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
	upstream := newStagedPassthroughConn()
	svc.httpUpstream = &httpUpstreamRecorder{}
	svc.cache = &stubGatewayCache{}
	svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
	svc.toolCorrector = NewCodexToolCorrector()
	svc.openaiWSPassthroughDialer = &stagedPassthroughDialer{conn: upstream}
	account := passthroughLifecycleAccount()
	account.GroupIDs = []int64{groupID}
	// Keep the production pre-send admission guard enabled and provide its
	// authoritative reader, rather than bypassing it in a billing fixture.
	svc.accountRepo = &stubOpenAIAccountRepo{accounts: []Account{*account}}
	apiKey := &APIKey{ID: 1, Status: StatusActive, GroupID: i64p(groupID), Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 0.5}}
	billingResults := make(chan error, 4)
	hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
		if err != nil {
			billingResults <- err
			return
		}
		billingResults <- svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result: result,
			APIKey: apiKey,
			User:   &User{ID: 1}, Account: account,
			ChannelUsageFields: ChannelUsageFields{OriginalModel: "gpt-6-luna", BillingModelSource: BillingModelSourceRequested},
		})
	}}
	controlCtx, cancelControl := context.WithCancel(context.Background())
	server, done := startPassthroughLifecycleServerWithHooks(t, controlCtx, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
		c.Set("api_key", apiKey)
		return hooks
	})
	var client *coderws.Conn
	joined := false
	t.Cleanup(func() {
		cancelControl()
		if client != nil {
			_ = client.CloseNow()
		}
		_ = upstream.Close()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("TTL proxy did not join during cleanup")
			}
		}
		server.Close()
	})
	client = dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-6-luna","input":[]}`)
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_ttl_ws","model":"gpt-6-luna","status":"completed","usage":{"input_tokens":2402,"output_tokens":5,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":2399,"cache_write_tokens_details":{"ttl_30m_tokens":9,"ttl_12h_tokens":2390}}}}}`)
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
	select {
	case err := <-done:
		joined = true
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("TTL relay did not stop")
	}
	require.Len(t, billingResults, 1)
	require.NoError(t, <-billingResults)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	log := usageRepo.lastLog
	require.NotNil(t, log)
	require.Equal(t, 9, log.CacheCreation30mTokens)
	require.Equal(t, 2390, log.CacheCreation12hTokens)
	require.Equal(t, 3, log.InputTokens)
	cacheCost := (9*0.8125 + 2390*1.625) / 1e6
	totalCost := cacheCost + 3*0.65/1e6 + 5*3.25/1e6
	require.InDelta(t, cacheCost, log.CacheCreationCost, 1e-12)
	require.InDelta(t, totalCost, log.TotalCost, 1e-12)
	require.Equal(t, QuantizeUsageBillingAmount(totalCost*0.5), log.ActualCost)
	require.Equal(t, log.ActualCost, userRepo.lastAmount)
	require.NotNil(t, log.AccountStatsCost)
	require.InDelta(t, totalCost, *log.AccountStatsCost, 1e-12)
}
