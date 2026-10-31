//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func pr612HTTPConfig() *config.Config {
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIResponseHeaderTimeout = 1
	cfg.Gateway.OpenAIUploadTimeoutSeconds = 10
	cfg.Gateway.OpenAIPreOutputBudgetSeconds = 10
	return cfg
}

// Both schedulable accounts point at synthetic loopback upstreams. Any automatic
// account retry or physical resubmission is visible in the server submission count.
func pr612HTTPHandler(t *testing.T, cfg *config.Config, baseURL, mode string) *OpenAIGatewayHandler {
	t.Helper()
	accounts := make([]service.Account, 2)
	for i := range accounts {
		accounts[i] = service.Account{ID: int64(i + 1), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Priority: i, GroupIDs: []int64{3131},
			Credentials: map[string]any{"api_key": fmt.Sprintf("synthetic-%d", i), "base_url": baseURL},
			Extra:       map[string]any{"openai_passthrough": mode == "passthrough"}}
		if mode == "chat" {
			accounts[i].Extra["openai_responses_supported"] = false
		}
	}
	gateway := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: accounts}, nil, nil, nil, nil, nil, nil, nil,
		cfg, nil, nil, nil, nil, nil, repository.NewHTTPUpstream(cfg), nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	return NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
}

func TestPR612DelayedHeadersFailoverToNextAccount(t *testing.T) {
	for _, mode := range []string{"responses", "chat", "passthrough"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", mode, stream), func(t *testing.T) {
				var submissions atomic.Int32
				paths := make(chan string, 4)
				canceled := make(chan struct{}, 4)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read synthetic submission: %v", err)
						return
					}
					if !gjson.ValidBytes(body) {
						t.Error("upstream did not receive the complete JSON POST")
					}
					submissions.Add(1)
					paths <- r.URL.Path
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					// Headers deliberately remain unsent after the full POST is read.
					<-r.Context().Done()
					canceled <- struct{}{}
				}))
				defer upstream.Close()
				h := pr612HTTPHandler(t, pr612HTTPConfig(), upstream.URL, mode)
				h.maxAccountSwitches = 2
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)
				c.Request.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"model":"gpt-5.1","stream":%v,"input":"synthetic"}`, stream)))
				h.Responses(c)
				require.Equal(t, int32(2), submissions.Load(), "header timeout should fail over to the next account")
				require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
				wantPath := "/v1/responses"
				if mode == "chat" {
					wantPath = "/v1/chat/completions"
				}
				require.Equal(t, wantPath, <-paths, "must exercise the selected production adapter")
				select {
				case <-canceled:
				case <-ctx.Done():
					t.Fatal("timed out transport did not cancel its loopback request")
				}
				require.NoError(t, ctx.Err(), "attempt cancellation must preserve the caller")
			})
		}
	}
}

func TestPR612ExplicitHTTPRefusalRetainsFailover(t *testing.T) {
	var submissions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		submissions.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"synthetic refusal"}}`)
	}))
	defer upstream.Close()
	h := pr612HTTPHandler(t, pr612HTTPConfig(), upstream.URL, "responses")
	h.maxAccountSwitches = 2
	c, _ := newOpenAIResponsesFailoverTestContext(t, t.Context())
	h.Responses(c)
	require.Equal(t, int32(2), submissions.Load(), "explicit HTTP refusals retain account failover")
}

func TestPR612WSHTTPBridgeDelayedHeadersNeverReplay(t *testing.T) {
	var submissions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		submissions.Add(1)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	cfg := pr612HTTPConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2
	account := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "synthetic", "base_url": upstream.URL},
		Extra:       map[string]any{"openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModeHTTPBridge}}
	gateway := service.NewOpenAIGatewayService(&openAIWSUsageHandlerAccountRepoStub{account: account}, nil, nil, nil, nil, nil, nil, nil,
		cfg, nil, nil, nil, nil, nil, repository.NewHTTPUpstream(cfg), nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	finished := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			finished <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r
		finished <- gateway.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, &account, "synthetic",
			[]byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`), nil)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	_, payload, err := client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "error", gjson.GetBytes(payload, "type").String())
	require.Contains(t, string(payload), "automatic replay disabled")
	select {
	case err := <-finished:
		require.Error(t, err)
	case <-ctx.Done():
		t.Fatal("bridge did not return its terminal error")
	}
	require.Equal(t, int32(1), submissions.Load(), "bridge must submit exactly once")
}

func TestPR612NativeUpstreamWSIgnoresHTTPHeaderTimeout(t *testing.T) {
	var submissions atomic.Int32
	upstreamDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		// Both the upstream WS upgrade headers and first WS response exceed the
		// configured one-second HTTP header timer. This path uses its WS limits.
		select {
		case <-time.After(1200 * time.Millisecond):
		case <-ctx.Done():
			upstreamDone <- ctx.Err()
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			upstreamDone <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_, _, err = conn.Read(ctx)
		if err != nil {
			upstreamDone <- err
			return
		}
		submissions.Add(1)
		select {
		case <-time.After(1200 * time.Millisecond):
		case <-ctx.Done():
			upstreamDone <- ctx.Err()
			return
		}
		upstreamDone <- conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_pr612_native_ws","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`))
	}))
	defer server.Close()
	harness := newOpenAIWSPassthroughHandlerHarnessWithOptions(t, server.URL, nil, nil,
		func(_ *service.Account, cfg *config.Config, _ *service.HTTPUpstream, _ *service.AccountRepository) {
			cfg.Gateway.OpenAIResponseHeaderTimeout = 1
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 5
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
		})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started := time.Now()
	require.NoError(t, harness.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)))
	_, payload, err := harness.clientConn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(payload, "type").String())
	require.Equal(t, "resp_pr612_native_ws", gjson.GetBytes(payload, "response.id").String())
	require.GreaterOrEqual(t, time.Since(started), 2*time.Second)
	select {
	case err := <-upstreamDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("native upstream WS did not finish its response")
	}
	require.Equal(t, int32(1), submissions.Load())
}

func TestPR612HeaderTimeoutFailoverSucceedsWithSecondAccount(t *testing.T) {
	var submissions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if submissions.Add(1) == 1 {
			// First account: read the complete POST but never send headers.
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_synthetic","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	h := pr612HTTPHandler(t, pr612HTTPConfig(), upstream.URL, "responses")
	h.maxAccountSwitches = 2
	c, rec := newOpenAIResponsesFailoverTestContext(t, t.Context())
	c.Request.Body = io.NopCloser(strings.NewReader(`{"model":"gpt-5.1","stream":false,"input":"synthetic"}`))
	h.Responses(c)

	require.Equal(t, int32(2), submissions.Load(), "first header timeout should fail over to the second account")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
