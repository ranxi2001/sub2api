//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/regional/ledger"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Match the regional service fixture: an interrupted started request must
// neither settle partial usage nor cancel its maximum reservation.
type incompleteRegionalLedger struct {
	reservations, starts, completed, canceled int
	reservation                               ledger.Reservation
}

func (l *incompleteRegionalLedger) Reserve(_ context.Context, r ledger.Reservation) (bool, error) {
	l.reservations++
	l.reservation = r
	return true, nil
}

func (l *incompleteRegionalLedger) Start(context.Context, string) error {
	l.starts++
	return nil
}

func (l *incompleteRegionalLedger) CancelBeforeStart(context.Context, string) error {
	l.canceled++
	return nil
}

func (l *incompleteRegionalLedger) Complete(context.Context, ledger.Event) error {
	l.completed++
	return nil
}

type incompleteRegionalTransport func(*http.Request) (*http.Response, error)

func (f incompleteRegionalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type incompleteRegionalHTTP struct {
	calls atomic.Int32
}

func (u *incompleteRegionalHTTP) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls.Add(1)
	return nil, errors.New("unexpected HTTP replay of regional WS request")
}

func (u *incompleteRegionalHTTP) DoWithTLS(r *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, proxy, accountID, concurrency)
}

func TestOpenAIWSSSEIncompleteRegionalHandlerOwnsSingleResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Deliberately serial: OAuth's fixed WS URL is intercepted through the
	// default client, with a transport that can only dial our local peer.
	for _, mode := range []string{"json", "sse"} {
		for _, closeMode := range []string{"eof", "1000"} {
			t.Run(mode+"/"+closeMode, func(t *testing.T) {
				var handshakes, submissions atomic.Int32
				peerDone := make(chan struct{}, 4)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer func() {
						select {
						case peerDone <- struct{}{}:
						default:
						}
					}()
					handshakes.Add(1)
					conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{InsecureSkipVerify: true})
					if err != nil {
						t.Errorf("accept local WS: %v", err)
						return
					}
					defer func() { _ = conn.CloseNow() }()
					_, payload, err := conn.Read(r.Context())
					if err != nil {
						t.Errorf("read response.create: %v", err)
						return
					}
					submissions.Add(1)
					if gjson.GetBytes(payload, "type").String() != "response.create" {
						t.Errorf("expected response.create, got %s", payload)
					}
					events := []string{
						`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":1}}}`,
						`{"type":"codex.response.metadata","metadata":{"synthetic":true}}`,
					}
					if mode == "sse" {
						events = append(events, `{"type":"response.created","sequence_number":7,"response":{"id":"resp_regional_incomplete","model":"gpt-5.1"}}`)
					}
					for _, event := range events {
						if err := conn.Write(r.Context(), coderws.MessageText, []byte(event)); err != nil {
							t.Errorf("write local WS event: %v", err)
							return
						}
					}
					if closeMode == "1000" {
						_ = conn.Close(coderws.StatusNormalClosure, "synthetic close")
					}
				}))
				t.Cleanup(upstream.Close)
				target, err := url.Parse(upstream.URL)
				require.NoError(t, err)
				transport := &http.Transport{}
				t.Cleanup(transport.CloseIdleConnections)
				previousClient := http.DefaultClient
				http.DefaultClient = &http.Client{Transport: incompleteRegionalTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "chatgpt.com" || r.URL.Path != "/backend-api/codex/responses" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
						return nil, errors.New("unexpected non-local WS route")
					}
					local := r.Clone(r.Context())
					local.URL.Scheme, local.URL.Host, local.Host = target.Scheme, target.Host, target.Host
					return transport.RoundTrip(local)
				})}
				t.Cleanup(func() { http.DefaultClient = previousClient })

				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Runtime.Role = config.RuntimeRoleGateway
				cfg.Default.RateMultiplier = 1
				cfg.Gateway.OpenAIWS.Enabled = true
				cfg.Gateway.OpenAIWS.OAuthEnabled = true
				cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
				cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
				cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
				cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 2
				groupID := int64(777)
				account := service.Account{ID: 7771, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
					Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID},
					Credentials: map[string]any{"access_token": "synthetic-oauth"},
					Extra: map[string]any{
						"openai_oauth_responses_websockets_v2_enabled": true,
						"openai_oauth_ws_sse_acceleration":             true,
					}}
				meterLedger := &incompleteRegionalLedger{}
				meter := &service.RegionalMetering{Ledger: meterLedger, Readiness: func(context.Context) error { return nil },
					Admit: func(_ context.Context, key *service.APIKey, a *service.Account, _ []byte) (ledger.Reservation, ledger.Price, error) {
						return ledger.Reservation{RequestID: "regional-incomplete", UserID: key.UserID, APIKeyID: key.ID, AccountID: a.ID, AdmittedAt: time.Now().Add(-time.Millisecond)},
							ledger.Price{Model: "gpt-5.1", Input: 1_000_000_000, Output: 2_000_000_000, MaxInput: 1000, MaxOutput: 100}, nil
					}}
				httpUpstream := &incompleteRegionalHTTP{}
				billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billingCache.Stop)
				gateway := service.NewOpenAIGatewayService(&openAIWSUsageHandlerAccountRepoStub{account: account},
					nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, httpUpstream,
					nil, nil, nil, nil, nil, nil, nil, nil, service.WithRegionalMetering(meter))
				t.Cleanup(gateway.CloseOpenAIWSPool)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billingCache,
					service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				key := &service.APIKey{ID: 7772, UserID: 7773, GroupID: &groupID,
					User:  &service.User{ID: 7773, Status: service.StatusActive},
					Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}}
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(string(middleware.ContextKeyAPIKey), key)
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID})
				})
				router.POST("/v1/responses", h.Responses)
				rec := httptest.NewRecorder()
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", strings.NewReader(
					`{"model":"gpt-5.1","input":"synthetic text","max_output_tokens":10,"stream":true}`))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(rec, request)

				require.EqualValues(t, 1, handshakes.Load(), "no reconnect after an ambiguous submitted turn")
				require.EqualValues(t, 1, submissions.Load(), "exactly one billable response.create")
				require.Zero(t, httpUpstream.calls.Load(), "no HTTP replay")
				require.Equal(t, 1, meterLedger.reservations)
				require.Equal(t, 1, meterLedger.starts)
				require.Positive(t, int64(meterLedger.reservation.Maximum))
				require.Zero(t, meterLedger.completed, "missing terminal usage must retain the hold")
				require.Zero(t, meterLedger.canceled, "a started hold cannot be canceled")
				require.NoError(t, ctx.Err(), "handler must finish before the caller deadline")
				select {
				case <-peerDone:
				case <-time.After(2 * time.Second):
					t.Fatal("local WS peer did not finish")
				}
				body := rec.Body.String()
				if mode == "json" {
					require.Equal(t, http.StatusBadGateway, rec.Code)
					require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
					require.True(t, gjson.Valid(body), "regional JSON must remain one complete JSON response")
					require.NotContains(t, body, "event:")
					require.Equal(t, service.OpenAIWSSSEIncompleteCode, gjson.Get(body, "error.code").String())
				} else {
					require.Equal(t, http.StatusOK, rec.Code)
					require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
					require.Equal(t, 1, strings.Count(body, "event: response.failed\n"), "regional SSE must have exactly one failed terminal")
					var terminal string
					for _, line := range strings.Split(body, "\n") {
						if strings.HasPrefix(line, "data: ") {
							data := strings.TrimPrefix(line, "data: ")
							require.True(t, gjson.Valid(data))
							if gjson.Get(data, "type").String() == "response.failed" {
								terminal = data
							}
						}
					}
					require.Equal(t, service.OpenAIWSSSEIncompleteCode, gjson.Get(terminal, "response.error.code").String())
					require.Equal(t, "resp_regional_incomplete", gjson.Get(terminal, "response.id").String())
					require.Equal(t, "gpt-5.1", gjson.Get(terminal, "response.model").String())
					require.Greater(t, gjson.Get(terminal, "sequence_number").Int(), int64(7))
				}
				require.NotContains(t, body, "Upstream request failed", "the real handler must not append its generic fallback")
			})
		}
	}
}
