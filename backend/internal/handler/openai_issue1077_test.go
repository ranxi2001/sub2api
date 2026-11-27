//go:build unit && issue1077

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/regional/ledger"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	ws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type issue1077Repo struct {
	openAIWSFailoverHandlerAccountRepoStub
	stateWrites []int64
}

func (r *issue1077Repo) SetError(_ context.Context, id int64, _ string) error {
	r.stateWrites = append(r.stateWrites, id)
	return nil
}
func (r *issue1077Repo) SetTempUnschedulable(_ context.Context, id int64, _ time.Time, _ string) error {
	r.stateWrites = append(r.stateWrites, id)
	return nil
}
func (r *issue1077Repo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

// Records the exact identity used by each durable boundary. A canceled A hold
// cannot be started or completed as B's request.
type issue1077Ledger struct {
	mu                sync.Mutex
	reserved          []ledger.Reservation
	started, canceled []string
	events            []ledger.Event
	cancelErr         error
}

func (l *issue1077Ledger) Reserve(_ context.Context, r ledger.Reservation) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reserved = append(l.reserved, r)
	return true, nil
}
func (l *issue1077Ledger) Start(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = append(l.started, id)
	return nil
}
func (l *issue1077Ledger) CancelBeforeStart(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.canceled = append(l.canceled, id)
	return l.cancelErr
}
func (l *issue1077Ledger) Complete(_ context.Context, e ledger.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
	return nil
}

func TestIssue1077HandlerSamePOST(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, refusal, extra                                                             string
		status, switches                                                                 int
		prewarm, metered, cancelFail, cancelClient, committed, failB, wantB, streamQuota bool
	}{
		{name: "stream_quota", switches: 1, streamQuota: true},
		{name: "credential", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, switches: 1, wantB: true},
		{name: "quota", status: 429, refusal: `{"error":{"type":"usage_limit_reached"}}`, switches: 1, wantB: true},
		{name: "function", status: 401, refusal: `{"error":{"code":"token_revoked"}}`, extra: `,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]`, switches: 1, wantB: true},
		{name: "prewarm", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, switches: 1, prewarm: true, wantB: true},
		{name: "metered", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, switches: 1, metered: true, wantB: true},
		{name: "cancel_hold_unknown", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, switches: 1, metered: true, cancelFail: true},
		{name: "budget_zero", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`},
		{name: "B_rejected", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, switches: 1, failB: true, wantB: true},
		{name: "client_cancel", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, switches: 1, cancelClient: true},
		{name: "committed", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, switches: 1, committed: true},
		{name: "relay", status: 401, refusal: `{"error":{"type":"authentication_error"}}`, switches: 1},
		{name: "bare", status: 401, refusal: `Unauthorized`, switches: 1},
		{name: "proxy", status: 401, refusal: `{"error":{"code":"proxy_authentication_required","type":"authentication_error"}}`, switches: 1},
		{name: "quota_conflict", status: 429, refusal: `{"error":{"code":"rate_limit_exceeded","type":"usage_limit_reached"}}`, switches: 1},
		{name: "bound_vector_store", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, extra: `,"tools":[{"type":"file_search","vector_store_ids":["vs_A"]}]`, switches: 1},
		{name: "bound_container", status: 401, refusal: `{"error":{"code":"invalid_api_key"}}`, extra: `,"tools":[{"type":"code_interpreter","container":"container_A"}]`, switches: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var aHandshakes, bHandshakes, aGenerations, bGenerations atomic.Int32
			var mu sync.Mutex
			var bWire [][]byte
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				isA := r.Header.Get("Authorization") == "Bearer fixture-A"
				if isA {
					aHandshakes.Add(1)
				}
				if isA && !tc.streamQuota {
					if tc.cancelClient {
						cancel()
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.refusal))
					return // no upgraded socket: A receives zero prewarm/generation frames
				}
				if !isA {
					bHandshakes.Add(1)
				}
				if tc.failB {
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key"}}`))
					return
				}
				conn, err := ws.Accept(w, r, &ws.AcceptOptions{InsecureSkipVerify: true})
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for {
					_, raw, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					if tc.streamQuota {
						aGenerations.Add(1)
						_ = conn.Write(r.Context(), ws.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_quota1077"}}`))
						_ = conn.Write(r.Context(), ws.MessageText, []byte(`{"type":"error","status_code":429,"error":{"type":"usage_limit_reached","message":"private fixture upstream body"}}`))
						return
					}
					if gjson.GetBytes(raw, "generate").Type != gjson.False {
						bGenerations.Add(1)
						mu.Lock()
						bWire = append(bWire, append([]byte(nil), raw...))
						mu.Unlock()
					}
					if err := conn.Write(r.Context(), ws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_issue1077","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":2}}}`)); err != nil {
						return
					}
				}
			}))
			t.Cleanup(peer.Close)
			target, err := url.Parse(peer.URL)
			require.NoError(t, err)
			transport := &http.Transport{}
			t.Cleanup(transport.CloseIdleConnections)
			previous := http.DefaultClient
			http.DefaultClient = &http.Client{Transport: incompleteRegionalTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "chatgpt.com" {
					return nil, errors.New("fixture refuses external traffic")
				}
				local := r.Clone(r.Context())
				local.URL.Scheme, local.URL.Host, local.Host = target.Scheme, target.Host, target.Host
				return transport.RoundTrip(local)
			})}
			t.Cleanup(func() { http.DefaultClient = previous })
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Runtime.Role = config.RuntimeRoleGateway
			cfg.Default.RateMultiplier = 1
			cfg.Gateway.OpenAILegacyCompatibilityMode = true
			cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.OAuthEnabled = true, true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2, cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true, true
			cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = tc.prewarm
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 2
			groupID := int64(777)
			a := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"access_token": "fixture-A"}, Extra: map[string]any{"openai_oauth_responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": service.OpenAIWSIngressModeCtxPool, "openai_oauth_ws_sse_acceleration": true}}
			b := a
			b.ID = 2
			b.Priority = 1
			b.Credentials = map[string]any{"access_token": "fixture-B"}
			repo := &issue1077Repo{openAIWSFailoverHandlerAccountRepoStub: openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{a, b}}}
			usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 4)}
			meterLedger := &issue1077Ledger{}
			if tc.cancelFail {
				meterLedger.cancelErr = errors.New("cancel ACK unknown")
			}
			var options []service.OpenAIGatewayOption
			if tc.metered {
				options = append(options, service.WithRegionalMetering(&service.RegionalMetering{Ledger: meterLedger, Readiness: func(context.Context) error { return nil }, Admit: func(_ context.Context, k *service.APIKey, a *service.Account, _ []byte) (ledger.Reservation, ledger.Price, error) {
					return ledger.Reservation{RequestID: fmt.Sprintf("same-post-account-%d", a.ID), UserID: k.UserID, APIKeyID: k.ID, AccountID: a.ID, AdmittedAt: time.Now().Add(-time.Millisecond)}, ledger.Price{Model: "gpt-5.4", Input: 1_000_000_000, Output: 2_000_000_000, MaxInput: 1000, MaxOutput: 100}, nil
				}}))
			}
			up := &incompleteRegionalHTTP{}
			gateway := service.NewOpenAIGatewayService(repo, nil, usage, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), service.NewRateLimitService(repo, nil, cfg, nil, nil), nil, up, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, options...)
			t.Cleanup(gateway.CloseOpenAIWSPool)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			h.maxAccountSwitches = tc.switches
			key := &service.APIKey{ID: 7772, UserID: 7773, AllowLongContext: true, GroupID: &groupID, User: &service.User{ID: 7773, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}}
			router := gin.New()
			if tc.streamQuota {
				setupOpsErrorLogTestQueue(t, 4)
				ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router.Use(OpsErrorLoggerMiddleware(ops))
			}
			var posts atomic.Int32
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID})
				if tc.committed {
					c.Writer.WriteHeaderNow()
				}
			})
			router.POST("/v1/responses", func(c *gin.Context) { posts.Add(1); h.Responses(c) })
			body := `{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic text"}],"max_output_tokens":10,"stream":true` + tc.extra + `}`
			if tc.metered {
				body = `{"model":"gpt-5.4","input":"synthetic text","max_output_tokens":10,"stream":true}`
			}
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, request)
			require.EqualValues(t, 1, posts.Load())
			t.Logf("fixture wire result: status=%d A=%d B=%d body=%s", rec.Code, aHandshakes.Load(), bHandshakes.Load(), rec.Body.String())
			if tc.name == "bound_vector_store" || tc.name == "bound_container" {
				require.Zero(t, bHandshakes.Load())
				require.Zero(t, bGenerations.Load())
				return
			}
			require.EqualValues(t, 1, aHandshakes.Load(), "ISSUE1077_HANDLER_SAME_POST")
			if tc.streamQuota {
				require.EqualValues(t, 1, aGenerations.Load())
				require.Zero(t, bHandshakes.Load(), "sent quota request cannot replay")
				require.Equal(t, 200, rec.Code)
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.failed"`))
				require.NotContains(t, rec.Body.String(), `"type":"error"`)
				require.NotContains(t, rec.Body.String(), "private fixture")
				require.Equal(t, int64(1), OpsErrorLogQueueLength())
				job := <-opsErrorLogQueue
				require.Equal(t, 429, job.entry.StatusCode)
				require.Equal(t, "upstream_transport", job.entry.ErrorSource)
				require.Equal(t, "provider", job.entry.ErrorOwner)
				require.Nil(t, job.entry.UpstreamStatusCode, "WS semantic 429 is not an HTTP handshake status")
				require.Equal(t, int64(200), gjson.Get(job.entry.ErrorBody, "error.wire_status").Int())
				require.Equal(t, int64(429), gjson.Get(job.entry.ErrorBody, "error.status").Int())
				return
			}
			require.Zero(t, aGenerations.Load(), "A handshake refusal receives zero generation/prewarm frames")
			if !tc.wantB {
				if tc.name == "relay" || tc.name == "bare" || tc.name == "proxy" {
					require.Empty(t, repo.stateWrites, "relay/proxy refusal must not mutate selected credential state")
				}
				if tc.metered {
					require.Len(t, meterLedger.reserved, 1)
					require.Equal(t, []string{"same-post-account-1"}, meterLedger.canceled)
					require.Empty(t, meterLedger.started)
					require.Empty(t, meterLedger.events)
				}
				require.Zero(t, bHandshakes.Load(), "unsafe/budget-limited request must not reach B")
				require.Zero(t, bGenerations.Load())
				return
			}
			require.EqualValues(t, 1, bHandshakes.Load(), "ISSUE1077_HANDLER_SAME_POST: B must be reached in the original POST")
			if tc.status == 401 {
				require.Contains(t, repo.stateWrites, int64(1), "explicit credential rejection applies the existing account policy")
			}
			if tc.failB {
				require.Zero(t, bGenerations.Load())
				return
			}
			require.EqualValues(t, 1, bGenerations.Load())
			require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.completed"`))
			require.NotContains(t, rec.Body.String(), "response.failed")
			mu.Lock()
			require.Len(t, bWire, 1)
			if tc.metered {
				require.Equal(t, "synthetic text", gjson.GetBytes(bWire[0], "input.0.content").String())
			} else {
				require.JSONEq(t, gjson.Get(body, "input").Raw, gjson.GetBytes(bWire[0], "input").Raw)
			}
			if tc.extra != "" {
				require.JSONEq(t, gjson.Get(body, "tools").Raw, gjson.GetBytes(bWire[0], "tools").Raw)
			}
			mu.Unlock()
			if tc.metered {
				require.Len(t, meterLedger.reserved, 2)
				require.Equal(t, []string{"same-post-account-1"}, meterLedger.canceled)
				require.Equal(t, []string{"same-post-account-2"}, meterLedger.started)
				require.Len(t, meterLedger.events, 1, "B settles exactly once")
				require.Equal(t, "same-post-account-2", meterLedger.events[0].Usage.EventID)
				require.Equal(t, int64(2), meterLedger.events[0].Usage.AccountID)
				require.Equal(t, int64(7), meterLedger.events[0].InputTokens)
				require.Equal(t, int64(2), meterLedger.events[0].OutputTokens)
				require.Equal(t, int64(2), meterLedger.reserved[1].AccountID)
				require.Empty(t, usage.created, "legacy usage must not double-bill a metered result")
			} else {
				select {
				case log := <-usage.created:
					require.Equal(t, int64(2), log.AccountID)
					require.Equal(t, 7, log.InputTokens)
					require.Equal(t, 2, log.OutputTokens)
				case <-time.After(3 * time.Second):
					t.Fatal("missing B usage")
				}
				require.Empty(t, usage.created, "A must not create usage")
			}
		})
	}
}

type issue1077BridgeHTTP struct {
	service.HTTPUpstream
	mu       sync.Mutex
	accounts []int64
	bodies   [][]byte
	control  string
	onSecond func(context.Context)
}

func (u *issue1077BridgeHTTP) Do(r *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.accounts = append(u.accounts, accountID)
	u.bodies = append(u.bodies, append([]byte(nil), raw...))
	if len(u.accounts) == 2 {
		if u.onSecond != nil {
			u.onSecond(r.Context())
		}
		if u.control == "after_output" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" + "data: {\"type\":\"error\",\"status_code\":401,\"error\":{\"code\":\"invalid_api_key\",\"type\":\"authentication_error\"}}\n\n"))}, nil
		}
		refusal := `{"error":{"code":"invalid_api_key","type":"authentication_error"}}`
		if u.control == "relay" {
			refusal = `{"error":{"type":"authentication_error"}}`
		}
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(refusal))}, nil
	}
	id := "resp_first1077"
	output := `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first-ok"}]},{"type":"function_call","call_id":"call1077","name":"lookup","arguments":"{}","status":"completed"}]`
	if len(u.accounts) > 2 {
		id = "resp_second1077"
		output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second-ok"}]}]`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"id":"` + id + `","status":"completed","output":` + output + `,"usage":{"input_tokens":7,"output_tokens":2}}}` + "\n\n"))}, nil
}
func (u *issue1077BridgeHTTP) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, a, c)
}

func TestIssue1077HandlerBridgeLater401(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, control := range []string{"success", "bound_tool", "bound_reference", "orphan_tool", "after_output", "cancel", "relay"} {
			t.Run(fmt.Sprintf("automatic=%v/%s", automatic, control), func(t *testing.T) {
				up := &issue1077BridgeHTTP{control: control}
				repo := &issue1077Repo{}
				headers := http.Header{"Session-Id": {"fixture-bridge1077"}}
				h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, "http://fixture.invalid", headers,
					map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI400OmitEncryptedHistoryEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "false"}, nil,
					func(k *service.APIKey) { k.AllowLongContext = true },
					func(a *service.Account, cfg *config.Config, transport *service.HTTPUpstream, ar *service.AccountRepository) {
						a.ID = 1
						a.Priority = 0
						a.Type = service.AccountTypeOAuth
						a.Credentials = map[string]any{"access_token": "fixture-A"}
						a.Extra = map[string]any{"openai_oauth_responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": service.OpenAIWSIngressModeHTTPBridge}
						if automatic {
							a.Extra["openai_oauth_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeCtxPool
						}
						cfg.Gateway.OpenAIWS.OAuthEnabled = true
						cfg.Gateway.OpenAILegacyCompatibilityMode = true
						cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
						cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
						cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 15 << 20
						cfg.Gateway.OpenAIWS.ClientReadLimitBytes = 64 << 20
						cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 30
						cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 30
						cfg.Gateway.MaxAccountSwitches = 1
						b := *a
						b.ID = 2
						b.Priority = 1
						b.Credentials = map[string]any{"access_token": "fixture-B"}
						repo.accounts = []service.Account{*a, b}
						*ar = repo
						*transport = up
					})
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				defer cancel()
				text := "first"
				if automatic {
					text = strings.Repeat("x", 24<<20)
				}
				first, _ := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.4", "input": []any{map[string]any{"role": "user", "content": text}}, "tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}})
				if control == "bound_tool" {
					var value map[string]any
					require.NoError(t, json.Unmarshal(first, &value))
					value["tools"] = []any{map[string]any{"type": "file_search", "vector_store_ids": []string{"vs_A"}}}
					first, _ = json.Marshal(value)
				}
				require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, first))
				readCompleted := func(id string) {
					for i := 0; i < 10; i++ {
						_, raw, err := h.clientConn.Read(ctx)
						require.NoError(t, err, "ISSUE1077_BRIDGE_LATER_401")
						require.NotEqual(t, "error", gjson.GetBytes(raw, "type").String(), string(raw))
						if gjson.GetBytes(raw, "type").String() == "response.completed" {
							require.Equal(t, id, gjson.GetBytes(raw, "response.id").String())
							return
						}
					}
					t.Fatal("missing completion")
				}
				readCompleted("resp_first1077")
				second := `{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp_first1077","input":[{"type":"function_call_output","call_id":"call1077","output":"second"}]}`
				if control == "orphan_tool" {
					second = strings.ReplaceAll(second, "call1077", "missing_call")
				}
				if control == "bound_reference" {
					second = `{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp_first1077","input":[{"type":"item_reference","id":"private_A"},{"role":"user","content":"second"}]}`
				}
				if control == "cancel" {
					reached := make(chan struct{})
					up.mu.Lock()
					up.onSecond = func(upstream context.Context) { close(reached); <-upstream.Done() }
					up.mu.Unlock()
					require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(second)))
					select {
					case <-reached:
					case <-ctx.Done():
						t.Fatal("second HTTP request not reached")
					}
					_ = h.clientConn.CloseNow()
				} else {
					require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(second)))
					if control == "success" {
						readCompleted("resp_second1077")
					} else {
						failed := false
						for i := 0; i < 10; i++ {
							_, raw, err := h.clientConn.Read(ctx)
							if err != nil {
								failed = true
								break
							}
							kind := gjson.GetBytes(raw, "type").String()
							require.NotEqual(t, "response.completed", kind, "unsafe second turn must never complete on B")
							if kind == "error" || kind == "response.failed" {
								failed = true
								break
							}
						}
						require.True(t, failed, "unsafe continuation must be explicitly refused")
					}
				}
				issue600Finish(t, h, headers)
				up.mu.Lock()
				defer up.mu.Unlock()
				if control != "success" {
					require.NotContains(t, up.accounts, int64(2), "bound/tool/output/canceled/relay request cannot reach B")
					require.LessOrEqual(t, len(up.accounts), 2)
					return
				}
				require.Equal(t, []int64{1, 1, 2}, up.accounts, "only current turn may execute on B once")
				require.False(t, gjson.GetBytes(up.bodies[2], "previous_response_id").Exists())
				require.JSONEq(t, gjson.GetBytes(first, "tools").Raw, gjson.GetBytes(up.bodies[2], "tools").Raw, "B must receive the original client function declarations")
				input := gjson.GetBytes(up.bodies[2], "input").Array()
				require.Len(t, input, 4)
				require.Equal(t, text, input[0].Get("content").String())
				require.Equal(t, "assistant", input[1].Get("role").String())
				require.Equal(t, "function_call", input[2].Get("type").String())
				require.Equal(t, "function_call_output", input[3].Get("type").String())
				require.Equal(t, "call1077", input[2].Get("call_id").String())
				require.Equal(t, "call1077", input[3].Get("call_id").String())
			})
		}
	}
}
