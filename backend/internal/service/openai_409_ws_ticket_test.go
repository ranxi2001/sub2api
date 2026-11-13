package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Only replace the fixed OAuth destination. The production dialer still opens
// a real socket through the selected P or Q HTTP proxy with the final headers.
type readableTicketLoopbackDialer struct {
	delegate openAIWSClientDialer
	p, q     string
}

func (d readableTicketLoopbackDialer) Dial(ctx context.Context, address string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	if address != "wss://chatgpt.com/backend-api/codex/responses" || (proxy != d.p && proxy != d.q) {
		return nil, 0, nil, fmt.Errorf("fixture refuses unrecognized destination or proxy")
	}
	return d.delegate.Dial(ctx, "ws://127.0.0.1:1/v1/responses", headers, proxy)
}

type readableTicketObservation struct {
	proxy  string
	header http.Header
	body   []byte
}

type readableTicketRepo struct{ turnAdmissionRepo }

func (r *readableTicketRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}
func (r *readableTicketRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

func TestOpenAI409WSTicketBoundEgress(t *testing.T) {
	testOpenAI409WSTicketBoundEgress(t, 1)
}

func TestOpenAI409WSTicketRejectsInvalidConcurrency(t *testing.T) {
	testOpenAI409WSTicketBoundEgress(t, 0)
}

func testOpenAI409WSTicketBoundEgress(t *testing.T, concurrency int) {
	t.Helper()
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModeShared, OpenAIWSIngressModeDedicated} {
		t.Run(mode, func(t *testing.T) {
			observed := make(chan readableTicketObservation, 4)
			var proxyRequests atomic.Int64
			proxyServer := func(label string) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					proxyRequests.Add(1)
					conn, err := coderws.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow() //nolint:errcheck // Fixture transport teardown.
					_, body, err := conn.Read(r.Context())
					if err != nil {
						t.Error(err)
						return
					}
					observed <- readableTicketObservation{label, r.Header.Clone(), append([]byte(nil), body...)}
					if err := conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_ticket_B","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`)); err != nil {
						t.Error(err)
					}
					_, _, _ = conn.Read(r.Context()) // consume shutdown/control frames
				}))
			}
			p, q := proxyServer("harvest-P"), proxyServer("ordinary-Q")
			defer p.Close()
			defer q.Close()
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS = config.GatewayOpenAIWSConfig{
				Enabled: true, OAuthEnabled: true, ResponsesWebsocketsV2: true, ModeRouterV2Enabled: true,
				DialTimeoutSeconds: 3, ReadTimeoutSeconds: 3, WriteTimeoutSeconds: 3,
				MaxConnsPerAccount: 1, MaxIdlePerAccount: 1, QueueLimitPerConn: 2,
			}
			cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292, Models: []string{"gpt-6-astra"}, FailClosed: true}
			account := ticketTestAccount(2)
			account.Concurrency = concurrency
			account.GroupIDs = []int64{9}
			account.Extra = map[string]any{"openai_oauth_responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": mode, "codex_fingerprint_mode": "off"}
			decision := NewOpenAIWSProtocolResolver(cfg).Resolve(account)
			if concurrency > 0 {
				require.Equal(t, OpenAIUpstreamTransportResponsesWebsocketV2, decision.Transport)
			} else {
				require.Equal(t, OpenAIUpstreamTransportHTTPSSE, decision.Transport)
				require.Equal(t, "account_concurrency_invalid", decision.Reason)
			}
			qURL, err := url.Parse(q.URL)
			require.NoError(t, err)
			port, err := strconv.Atoi(qURL.Port())
			require.NoError(t, err)
			account.Proxy = &Proxy{ID: 9, Protocol: "http", Host: qURL.Hostname(), Port: port, Status: StatusActive}
			account.ProxyID = &account.Proxy.ID
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(readableTicketLoopbackDialer{newDefaultOpenAIWSClientDialer(), p.URL, q.URL})
			svc := &OpenAIGatewayService{cfg: cfg, accountRepo: &readableTicketRepo{turnAdmissionRepo{account: account}},
				cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPool: pool,
				toolCorrector: NewCodexToolCorrector(), settingService: NewSettingService(&contentModerationTestSettingRepo{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, cfg)}
			defer svc.CloseOpenAIWSPool()
			state := fakeCodexTicketState(292)
			require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
				AccountID: 2, Model: "gpt-6-astra", State: state, Length: 292, Transport: "websocket",
				HarvestProxyURL: p.URL, HarvestSessionID: "fixture-ticket-B-session", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
			}))
			done := make(chan error, 1)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				conn, err := coderws.Accept(w, req, nil)
				if err != nil {
					done <- err
					return
				}
				defer conn.CloseNow() //nolint:errcheck // Fixture teardown.
				_, source, err := conn.Read(req.Context())
				if err != nil {
					done <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = req
				c.Set("api_key", &APIKey{ID: 7, UserID: 8, GroupID: int64Ptr(9)})
				SetOpenAIWSRecoveryPrincipal(c, 8, 7)
				// This test isolates the post-unsent-rebuild B transport; the
				// handler suite establishes the same-client A disable boundary.
				ctx, body, changed, err := svc.RebuildOpenAIWSUnsentTurn(req.Context(), c, source, "fixture-ticket-client", "gpt-6-astra")
				if err != nil || !changed {
					done <- fmt.Errorf("fixture rebuild failed: changed=%v err=%v", changed, err)
					return
				}
				ctx = ResetOpenAIWSReadableProviderSession(ctx, c)
				c.Request = req.WithContext(ctx)
				done <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "tok", body, nil)
			}))
			defer gateway.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(gateway.URL, "http"), &coderws.DialOptions{HTTPHeader: http.Header{"Session_id": {"fixture-ticket-client"}, "Conversation_id": {"fixture-old-A"}, "X-Codex-Turn-State": {"fixture-old-A-state"}}})
			require.NoError(t, err)
			defer client.CloseNow() //nolint:errcheck // Fixture teardown.
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"role":"user","content":"Continue the readable coding task without repeating completed work."}]}`)))
			_, terminal, err := client.Read(ctx)
			if concurrency <= 0 {
				require.Error(t, err, "invalid concurrency must close before generation")
				require.Empty(t, terminal)
				select {
				case gatewayErr := <-done:
					require.EqualError(t, gatewayErr, "websocket ingress requires ws_v2 transport, got=http_sse")
				case <-ctx.Done():
					t.Fatal("rejected ticket fixture gateway did not terminate")
				}
				require.Zero(t, proxyRequests.Load(), "invalid concurrency must not dial either proxy")
				require.Empty(t, observed, "invalid concurrency must not send a B generation")
				return
			}
			if err != nil {
				select {
				case gatewayErr := <-done:
					t.Fatalf("ticket fixture downstream read failed: %v; gateway: %v", err, gatewayErr)
				case <-ctx.Done():
					t.Fatalf("ticket fixture downstream read failed: %v; gateway did not finish", err)
				}
			}
			require.NoError(t, err)
			require.Equal(t, "response.completed", gjson.GetBytes(terminal, "type").String())
			require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("fixture gateway did not terminate")
			}
			select {
			case wire := <-observed:
				require.Equal(t, "harvest-P", wire.proxy, "WS_RESIDUAL_TICKET_EGRESS_LOST: B ticket must never use ordinary-Q")
				require.Equal(t, state, wire.header.Get(openAICodexTurnStateHeader))
				require.Equal(t, "fixture-ticket-B-session", wire.header.Get("session_id"))
				require.Empty(t, wire.header.Get("conversation_id"))
				require.Contains(t, string(wire.body), "Continue the readable coding task")
				require.False(t, gjson.GetBytes(wire.body, "previous_response_id").Exists())
			default:
				t.Fatal("missing actual B wire")
			}
			require.Empty(t, observed, "one B generation only")
		})
	}
}
