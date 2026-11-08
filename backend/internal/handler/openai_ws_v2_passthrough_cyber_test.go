package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIWSPassthroughHandlerHarness struct {
	handler        *OpenAIGatewayHandler
	clientConn     *coderws.Conn
	handlerDone    <-chan struct{}
	wsURL          string
	moderationRepo *contentModerationHandlerTestRepo
	gatewayCache   service.GatewayCache
	apiKey         *service.APIKey
	finalHeaders   <-chan http.Header
	usageLogs      <-chan *service.UsageLog
}

func newOpenAIWSPassthroughHandlerHarness(t *testing.T, upstreamURL string, settings ...map[string]string) *openAIWSPassthroughHandlerHarness {
	extraSettings := map[string]string{}
	for _, overrides := range settings {
		for key, value := range overrides {
			extraSettings[key] = value
		}
	}
	return newOpenAIWSPassthroughHandlerHarnessWithOptions(t, upstreamURL, http.Header{"Session_id": []string{"ws-test-session"}}, extraSettings)
}

func newOpenAIWSPassthroughHandlerHarnessWithOptions(t *testing.T, upstreamURL string, clientHeaders http.Header, extraSettings map[string]string, configure ...func(*service.Account, *config.Config, *service.HTTPUpstream, *service.AccountRepository)) *openAIWSPassthroughHandlerHarness {
	return newOpenAIWSPassthroughHandlerHarnessWithCache(t, upstreamURL, clientHeaders, extraSettings, nil, configure...)
}

func newOpenAIWSPassthroughHandlerHarnessWithCache(t *testing.T, upstreamURL string, clientHeaders http.Header, extraSettings map[string]string, gatewayCache service.GatewayCache, configure ...func(*service.Account, *config.Config, *service.HTTPUpstream, *service.AccountRepository)) *openAIWSPassthroughHandlerHarness {
	t.Helper()
	return newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstreamURL, clientHeaders, extraSettings, gatewayCache, nil, configure...)
}

func newOpenAIWSPassthroughHandlerHarnessWithKey(t *testing.T, upstreamURL string, clientHeaders http.Header, extraSettings map[string]string, gatewayCache service.GatewayCache, configureKey func(*service.APIKey), configure ...func(*service.Account, *config.Config, *service.HTTPUpstream, *service.AccountRepository)) *openAIWSPassthroughHandlerHarness {
	t.Helper()
	if gatewayCache == nil {
		gatewayCache = testutil.NewRedisGatewayCache(t)
	}

	settings := map[string]string{
		service.SettingKeyRiskControlEnabled:          "true",
		service.SettingKeyCyberSessionBlockEnabled:    "true",
		service.SettingKeyCyberSessionBlockTTLSeconds: "60",
	}
	for key, value := range extraSettings {
		settings[key] = value
	}
	settingRepo := &contentModerationHandlerSettingRepo{values: settings}
	moderationRepo := &contentModerationHandlerTestRepo{}
	moderationSvc := service.NewContentModerationService(settingRepo, moderationRepo, nil, nil, nil, nil, nil, nil)
	settingSvc := service.NewSettingService(settingRepo, nil)

	groupID := int64(4301)
	account := service.Account{
		ID:          9951,
		Name:        "openai-ws-passthrough-cyber",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": upstreamURL},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
		},
	}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 3

	account.GroupIDs = []int64{groupID}
	var accountRepo service.AccountRepository
	var httpUpstream service.HTTPUpstream
	for _, apply := range configure {
		apply(&account, cfg, &httpUpstream, &accountRepo)
	}
	if httpUpstream == nil {
		httpUpstream = testutil.NewRealHTTPUpstream(cfg)
	}
	if accountRepo == nil {
		accountRepo = &openAIWSUsageHandlerAccountRepoStub{account: account}
	}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 8)}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo, nil, usageRepo, nil, nil, nil, nil, gatewayCache, cfg, nil, nil,
		service.NewBillingService(cfg, nil), service.NewRateLimitService(accountRepo, nil, cfg, nil, nil), billingCacheSvc, httpUpstream, &service.DeferredService{},
		nil, nil, nil, nil, nil, settingSvc, nil,
	)
	t.Cleanup(gatewaySvc.CloseOpenAIWSPool)
	concurrencyCache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := &OpenAIGatewayHandler{
		gatewayService:           gatewaySvc,
		billingCacheService:      billingCacheSvc,
		apiKeyService:            &service.APIKeyService{},
		contentModerationService: moderationSvc,
		maxAccountSwitches:       cfg.Gateway.MaxAccountSwitches,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(concurrencyCache), SSEPingFormatNone, time.Second),
	}

	apiKey := &service.APIKey{
		ID:      1851,
		UserID:  1751,
		Name:    "ws-cyber-key",
		Key:     "sk-handler-cyber-test",
		GroupID: &groupID,
		User:    &service.User{ID: 1751, Status: service.StatusActive},
	}
	if configureKey != nil {
		configureKey(apiKey)
	}
	finalHeaders := make(chan http.Header, 4)
	handlerDone := make(chan struct{}, 4)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Next()
	})
	router.GET("/openai/v1/responses", func(c *gin.Context) {
		h.ResponsesWebSocket(c)
		finalHeaders <- c.Request.Header.Clone()
		handlerDone <- struct{}{}
	})
	handlerServer := httptest.NewServer(router)
	t.Cleanup(handlerServer.Close)

	wsURL := "ws" + strings.TrimPrefix(handlerServer.URL, "http") + "/openai/v1/responses"
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, wsURL, &coderws.DialOptions{HTTPHeader: clientHeaders})
	cancelDial()
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.CloseNow() })

	return &openAIWSPassthroughHandlerHarness{
		handler:        h,
		clientConn:     clientConn,
		handlerDone:    handlerDone,
		wsURL:          wsURL,
		moderationRepo: moderationRepo,
		gatewayCache:   gatewayCache,
		apiKey:         apiKey,
		finalHeaders:   finalHeaders,
		usageLogs:      usageRepo.created,
	}
}

func TestOpenAIResponsesWebSocketV2PassthroughCyberMarkIsConsumedAfterTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamDone := make(chan struct{})
	secondUpstreamFrame := make(chan []byte, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, _, err = conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		failed := []byte(`{"type":"response.failed","response":{"id":"resp_cyber_handler","model":"gpt-5.1","error":{"code":"cyber_policy","message":"blocked by upstream policy"},"usage":{"input_tokens":11,"output_tokens":3}}}`)
		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, failed)
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead = context.WithTimeout(r.Context(), 3*time.Second)
		_, second, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			return
		}
		secondUpstreamFrame <- append([]byte(nil), second...)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp_cyber_handler_turn_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
		writeCtx, cancelWrite = context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, completed)
		cancelWrite()
		require.NoError(t, err)
	}))
	defer upstreamServer.Close()
	harness := newOpenAIWSPassthroughHandlerHarness(t, upstreamServer.URL)

	requestPayload := `{"type":"response.create","model":"gpt-5.1","prompt_cache_key":"cyber-session-1","input":"test"}`
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err := harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(requestPayload))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "response.failed", gjson.GetBytes(event, "type").String())

	require.Eventually(t, func() bool {
		logs := harness.moderationRepo.logSnapshot()
		return len(logs) == 1 && logs[0].Action == service.ContentModerationActionCyberPolicy &&
			strings.Contains(logs[0].Error, "upstream_usage=in:11,out:3")
	}, 3*time.Second, 10*time.Millisecond, "handler AfterTurn must call recordCyberPolicyIfMarked and write the risk-control event")

	keyCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	keyCtx.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(requestPayload))
	keyCtx.Request.Header.Set("session_id", "ws-test-session")
	blockKey := service.CyberSessionExplicitBlockKey(harness.apiKey.ID, keyCtx, []byte(requestPayload))
	require.NotEmpty(t, blockKey)
	store, ok := harness.gatewayCache.(service.CyberSessionBlockStore)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		matched, findErr := store.FindCyberSessionBlocked(context.Background(), []string{blockKey})
		return findErr == nil && matched == blockKey
	}, 3*time.Second, 10*time.Millisecond, "handler AfterTurn must write the cyber session block table")

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","prompt_cache_key":"cyber-session-1","input":"follow-up"}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = harness.clientConn.Read(readCtx)
	cancelRead()
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	// closeOpenAIClientWS caps close reasons at 120 bytes; passthrough must expose
	// the same client-visible prefix rather than dropping the close frame.
	require.Equal(t, "该会话已被网络安全策略屏蔽，请开启新会话 / This session is blocked by cyber-security policy, please ", closeErr.Reason)
	select {
	case <-harness.handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("websocket handler did not exit")
	}
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream websocket did not exit")
	}
	select {
	case second := <-secondUpstreamFrame:
		t.Fatalf("blocked follow-up reached upstream: %s", second)
	default:
	}
}

func TestOpenAIResponsesWebSocketV2PassthroughNonCyberTurnAllowsFollowup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamDone := make(chan struct{})
	secondUpstreamFrame := make(chan []byte, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, _, err = conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		firstCompleted := []byte(`{"type":"response.completed","response":{"id":"resp_non_cyber_handler_turn_1","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`)
		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, firstCompleted)
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead = context.WithTimeout(r.Context(), 3*time.Second)
		_, second, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)
		secondUpstreamFrame <- append([]byte(nil), second...)

		secondCompleted := []byte(`{"type":"response.completed","response":{"id":"resp_non_cyber_handler_turn_2","model":"gpt-5.1","usage":{"input_tokens":3,"output_tokens":1}}}`)
		writeCtx, cancelWrite = context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, coderws.MessageText, secondCompleted)
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead = context.WithTimeout(r.Context(), 3*time.Second)
		_, _, _ = conn.Read(readCtx)
		cancelRead()
	}))
	defer upstreamServer.Close()
	harness := newOpenAIWSPassthroughHandlerHarness(t, upstreamServer.URL)

	firstPayload := `{"type":"response.create","model":"gpt-5.1","prompt_cache_key":"non-cyber-session-1","input":"first"}`
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err := harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(firstPayload))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, firstEvent, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "resp_non_cyber_handler_turn_1", gjson.GetBytes(firstEvent, "response.id").String())

	secondPayload := `{"type":"response.create","model":"gpt-5.1","prompt_cache_key":"non-cyber-session-1","input":"follow-up"}`
	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(secondPayload))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, secondEvent, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "resp_non_cyber_handler_turn_2", gjson.GetBytes(secondEvent, "response.id").String())
	require.Empty(t, harness.moderationRepo.logSnapshot())

	keyCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	keyCtx.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(firstPayload))
	keyCtx.Request.Header.Set("session_id", "ws-test-session")
	blockKey := service.CyberSessionExplicitBlockKey(harness.apiKey.ID, keyCtx, []byte(firstPayload))
	require.NotEmpty(t, blockKey)
	store, ok := harness.gatewayCache.(service.CyberSessionBlockStore)
	require.True(t, ok)
	matched, findErr := store.FindCyberSessionBlocked(context.Background(), []string{blockKey})
	require.NoError(t, findErr)
	require.Empty(t, matched)

	require.NoError(t, harness.clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-harness.handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("non-cyber websocket handler did not exit")
	}
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("non-cyber upstream websocket did not exit")
	}
	select {
	case second := <-secondUpstreamFrame:
		require.JSONEq(t, secondPayload, string(second))
	default:
		t.Fatal("non-cyber follow-up did not reach upstream")
	}
}

func TestOpenAIResponsesWebSocketV2InheritsFirstBodyIdentityForLaterCyberHit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var upstreamConnections atomic.Int32
	upstreamDone := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamConnections.Add(1) > 1 {
			t.Errorf("blocked reconnect unexpectedly reached upstream")
			http.Error(w, "unexpected reconnect", http.StatusBadGateway)
			return
		}
		defer close(upstreamDone)
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, first, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)
		require.Equal(t, "thread-body-bound", gjson.GetBytes(first, "response.client_metadata.thread_id").String())

		completed := []byte(`{"type":"response.completed","response":{"id":"resp_bound_turn_1","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`)
		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		require.NoError(t, conn.Write(writeCtx, coderws.MessageText, completed))
		cancelWrite()

		readCtx, cancelRead = context.WithTimeout(r.Context(), 3*time.Second)
		_, second, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(second, "response.client_metadata").Exists(), "follow-up intentionally omits identity")

		failed := []byte(`{"type":"response.failed","response":{"id":"resp_bound_turn_2","model":"gpt-5.1","error":{"code":"cyber_policy","message":"blocked by upstream policy"},"usage":{"input_tokens":7,"output_tokens":1}}}`)
		writeCtx, cancelWrite = context.WithTimeout(r.Context(), 3*time.Second)
		require.NoError(t, conn.Write(writeCtx, coderws.MessageText, failed))
		cancelWrite()
	}))
	defer upstreamServer.Close()

	harness := newOpenAIWSPassthroughHandlerHarnessWithOptions(t, upstreamServer.URL, nil, map[string]string{
		service.SettingKeyCyberSessionIdentityStrictEnabled: "true",
	})

	firstPayload := `{"type":"response.create","model":"gpt-5.1","response":{"client_metadata":{"thread_id":"thread-body-bound"},"input":"first"}}`
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(firstPayload)))
	cancelWrite()

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, firstEvent, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "resp_bound_turn_1", gjson.GetBytes(firstEvent, "response.id").String())

	secondPayload := `{"type":"response.create","model":"gpt-5.1","response":{"input":"follow-up without identity"}}`
	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, harness.clientConn.Write(writeCtx, coderws.MessageText, []byte(secondPayload)))
	cancelWrite()

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, secondEvent, err := harness.clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "response.failed", gjson.GetBytes(secondEvent, "type").String())

	keyCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	keyCtx.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(firstPayload))
	blockKey := service.CyberSessionExplicitBlockKey(harness.apiKey.ID, keyCtx, []byte(firstPayload))
	require.NotEmpty(t, blockKey)
	store, ok := harness.gatewayCache.(service.CyberSessionBlockStore)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		matched, findErr := store.FindCyberSessionBlocked(context.Background(), []string{blockKey})
		return findErr == nil && matched == blockKey
	}, 3*time.Second, 10*time.Millisecond, "a later identity-less turn must write the connection-bound block key")

	require.NoError(t, harness.clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-harness.handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("first websocket handler did not exit")
	}
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream websocket did not exit")
	}

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	reconnect, _, err := coderws.Dial(dialCtx, harness.wsURL, nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = reconnect.CloseNow() }()

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, reconnect.Write(writeCtx, coderws.MessageText, []byte(firstPayload)))
	cancelWrite()

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, blockedEvent, err := reconnect.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "session_blocked_by_cyber_policy", gjson.GetBytes(blockedEvent, "error.code").String())

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = reconnect.Read(readCtx)
	cancelRead()
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)

	select {
	case <-harness.handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked reconnect handler did not exit")
	}
	require.Equal(t, int32(1), upstreamConnections.Load())
}
