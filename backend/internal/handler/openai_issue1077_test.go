//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
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
		name, refusal, extra                                                string
		status, switches                                                    int
		prewarm, metered, cancelFail, cancelClient, committed, failB, wantB bool
	}{
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
			var aHandshakes, bHandshakes, bGenerations atomic.Int32
			var mu sync.Mutex
			var bWire [][]byte
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer fixture-A" {
					aHandshakes.Add(1)
					if tc.cancelClient {
						cancel()
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.refusal))
					return // no upgraded socket: A receives zero prewarm/generation frames
				}
				bHandshakes.Add(1)
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
			a := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Credentials: map[string]any{"access_token": "fixture-A"}, Extra: map[string]any{"openai_oauth_responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": service.OpenAIWSIngressModeCtxPool, "openai_oauth_ws_sse_acceleration": true}}
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
				options = append(options, service.WithRegionalMetering(&service.RegionalMetering{Ledger: meterLedger, Admit: func(_ context.Context, k *service.APIKey, a *service.Account, _ []byte) (ledger.Reservation, ledger.Price, error) {
					return ledger.Reservation{RequestID: fmt.Sprintf("same-post-account-%d", a.ID), UserID: k.UserID, APIKeyID: k.ID, AccountID: a.ID, AdmittedAt: time.Now().Add(-time.Millisecond)}, ledger.Price{Model: "gpt-5.4", Input: 1_000_000_000, Output: 2_000_000_000, MaxInput: 1000, MaxOutput: 100}, nil
				}}))
			}
			up := &incompleteRegionalHTTP{}
			gateway := service.NewOpenAIGatewayService(repo, nil, usage, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, nil, up, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, options...)
			t.Cleanup(gateway.CloseOpenAIWSPool)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			h.maxAccountSwitches = tc.switches
			key := &service.APIKey{ID: 7772, UserID: 7773, GroupID: &groupID, User: &service.User{ID: 7773, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}}
			router := gin.New()
			var posts atomic.Int32
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID})
				if tc.committed {
					c.Writer.WriteHeaderNow()
				}
			})
			router.POST("/v1/responses", func(c *gin.Context) { posts.Add(1); h.Responses(c) })
			body := `{"model":"gpt-5.4","input":"synthetic text","max_output_tokens":10,"stream":true` + tc.extra + `}`
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, request)
			require.EqualValues(t, 1, posts.Load())
			require.EqualValues(t, 1, aHandshakes.Load(), "ISSUE1077_HANDLER_SAME_POST")
			if !tc.wantB {
				require.Zero(t, bHandshakes.Load(), "unsafe/budget-limited request must not reach B")
				require.Zero(t, bGenerations.Load())
				return
			}
			require.EqualValues(t, 1, bHandshakes.Load(), "ISSUE1077_HANDLER_SAME_POST: B must be reached in the original POST")
			if tc.failB {
				require.Zero(t, bGenerations.Load())
				return
			}
			require.EqualValues(t, 1, bGenerations.Load())
			require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.completed"`))
			require.NotContains(t, rec.Body.String(), "response.failed")
			mu.Lock()
			require.Len(t, bWire, 1)
			require.Equal(t, gjson.Get(body, "input").Raw, gjson.GetBytes(bWire[0], "input").Raw)
			if tc.extra != "" {
				require.JSONEq(t, gjson.Get(body, "tools").Raw, gjson.GetBytes(bWire[0], "tools").Raw)
			}
			mu.Unlock()
			if tc.metered {
				require.Len(t, meterLedger.reserved, 2)
				require.Equal(t, []string{"same-post-account-1"}, meterLedger.canceled)
				require.Equal(t, []string{"same-post-account-2"}, meterLedger.started)
				require.Len(t, meterLedger.events, 1, "B settles exactly once")
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
