//go:build unit && retrydeadline

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type wsRetryDeadlineSlots struct {
	service.ConcurrencyCache
	mu       sync.Mutex
	stage    string
	deadline time.Time

	userAttempts int
	userAcquired int
	userReleased int

	accountAttempts int
	accountAcquired int
	accountReleased int

	waitEnteredBeforeDeadline bool
}

func waitWSRetryDeadline(ctx context.Context, deadline time.Time) {
	timer := time.NewTimer(time.Until(deadline.Add(25 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (s *wsRetryDeadlineSlots) AcquireUserSlot(ctx context.Context, id int64, max int, request string) (bool, error) {
	s.mu.Lock()
	s.userAttempts++
	hold := s.stage == "user_slot" && s.userAttempts == 2
	deadline := s.deadline
	if hold {
		s.waitEnteredBeforeDeadline = time.Now().Before(deadline)
	}
	s.mu.Unlock()
	if hold {
		waitWSRetryDeadline(ctx, deadline)
	}
	acquired, err := s.ConcurrencyCache.AcquireUserSlot(ctx, id, max, request)
	if acquired {
		s.mu.Lock()
		s.userAcquired++
		s.mu.Unlock()
	}
	return acquired, err
}

func (s *wsRetryDeadlineSlots) ReleaseUserSlot(ctx context.Context, id int64, request string) error {
	s.mu.Lock()
	s.userReleased++
	s.mu.Unlock()
	return s.ConcurrencyCache.ReleaseUserSlot(ctx, id, request)
}

func (s *wsRetryDeadlineSlots) AcquireAccountSlot(ctx context.Context, id int64, max int, request string) (bool, error) {
	s.mu.Lock()
	s.accountAttempts++
	hold := s.stage == "account_slot" && s.accountAttempts == 2
	deadline := s.deadline
	if hold {
		s.waitEnteredBeforeDeadline = time.Now().Before(deadline)
	}
	s.mu.Unlock()
	if hold {
		waitWSRetryDeadline(ctx, deadline)
	}
	acquired, err := s.ConcurrencyCache.AcquireAccountSlot(ctx, id, max, request)
	if acquired {
		s.mu.Lock()
		s.accountAcquired++
		s.mu.Unlock()
	}
	return acquired, err
}

func (s *wsRetryDeadlineSlots) ReleaseAccountSlot(ctx context.Context, id int64, request string) error {
	s.mu.Lock()
	s.accountReleased++
	s.mu.Unlock()
	return s.ConcurrencyCache.ReleaseAccountSlot(ctx, id, request)
}

func TestOpenAIWSRetryDeadlineAtGenerationWrite(t *testing.T) {
	for _, stage := range []string{"backoff", "user_slot", "account_slot", "handshake", "inflight", "output", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			slots := &wsRetryDeadlineSlots{ConcurrencyCache: testutil.NewRedisConcurrencyCache(t), stage: stage}
			var gateway *service.OpenAIGatewayService
			var accountID int64
			var mu sync.Mutex
			var generations, handshakes int
			var deadline time.Time
			var owners []string
			var generationTimes []time.Time
			handshakeEntered := make(chan time.Time, 1)
			handshakeClosed := make(chan struct{}, 1)
			cancelHandshake := make(chan struct{})
			stopHandshake := sync.OnceFunc(func() { close(cancelHandshake) })
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				handshakes++
				n := handshakes
				d := deadline
				mu.Unlock()
				if n == 2 && (stage == "handshake" || stage == "canceled") {
					handshakeEntered <- time.Now()
					if stage == "handshake" {
						waitWSRetryDeadline(r.Context(), d)
					} else {
						select {
						case <-cancelHandshake:
						case <-r.Context().Done():
						}
					}
					defer func() { handshakeClosed <- struct{}{} }()
				}
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return // client cancellation may end the held handshake
				}
				defer func() { _ = conn.CloseNow() }()
				for {
					_, frame, readErr := conn.Read(r.Context())
					if readErr != nil {
						return
					}
					mu.Lock()
					generations++
					generation := generations
					owners = append(owners, r.Header.Get("Authorization"))
					generationTimes = append(generationTimes, time.Now())
					if generation == 1 {
						window := 1500 * time.Millisecond
						if stage == "backoff" {
							window = 150 * time.Millisecond
						}
						deadline = time.Now().Add(window)
						service.SetOpenAIRetryDeadlineForTest(gateway, accountID, deadline)
						slots.mu.Lock()
						slots.deadline = deadline
						slots.mu.Unlock()
					}
					d = deadline
					mu.Unlock()
					if gjson.GetBytes(frame, "generate").Exists() {
						t.Error("fixture must count generation frames, with prewarming disabled")
					}
					if generation == 1 {
						if stage == "output" {
							_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.output_text.delta","delta":"partial","output_index":0,"content_index":0}`))
						}
						_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"error","error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"limited"}}`))
						return
					}
					if stage == "inflight" && generation == 2 {
						_ = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.output_text.delta","delta":"ok","output_index":0,"content_index":0}`))
						waitWSRetryDeadline(r.Context(), d)
					}
					terminal := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_deadline_%d","model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`, generation)
					if conn.Write(r.Context(), coderws.MessageText, []byte(terminal)) != nil {
						return
					}
					if stage == "handshake" {
						return // bound the defective baseline's admitted generation
					}
				}
			}))
			t.Cleanup(provider.Close)
			defer stopHandshake()
			h := newOpenAIWSPassthroughHandlerHarnessWithAudit(t, provider.URL, nil,
				map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false"}, testutil.NewRedisGatewayCache(t),
				func(key *service.APIKey) { key.AllowLongContext = true },
				func(h *OpenAIGatewayHandler) {
					mu.Lock()
					gateway = h.gatewayService
					mu.Unlock()
					concurrency := service.NewConcurrencyService(slots)
					service.SetOpenAIRetryConcurrencyForTest(gateway, concurrency)
					h.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second)
					require.NoError(t, service.RouteIssue842OAuthSocketsToLoopbackForTest(gateway, provider.URL))
				},
				func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, repo *service.AccountRepository) {
					mu.Lock()
					accountID = a.ID
					mu.Unlock()
					a.Type = service.AccountTypeOAuth
					a.Credentials["access_token"] = "fixture-deadline-owner"
					a.Extra["codex_fingerprint_mode"] = "off"
					a.Extra["openai_oauth_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeCtxPool
					cfg.Gateway.OpenAIWS.OAuthEnabled = true
					cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
					cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
					cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = false
					cfg.Gateway.OpenAIWS.LBTopK = 1
					cfg.Gateway.MaxAccountSwitches = 2
					other := *a
					other.ID, other.Priority = a.ID+1, 1000
					other.Credentials = map[string]any{"access_token": "fixture-healthy-backup"}
					*repo = &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, other}}}
				})
			issue715WSSend(t, h.clientConn, `{"type":"response.create","model":"gpt-5.1","input":"hello"}`)
			var readTimedOut, unexpectedCompletion, outputObserved bool
			if stage == "canceled" {
				select {
				case entered := <-handshakeEntered:
					mu.Lock()
					require.True(t, entered.Before(deadline), "retry passed the handler's checks before D")
					mu.Unlock()
				case <-time.After(5 * time.Second):
					t.Fatal("retry never reached the held handshake")
				}
				require.NoError(t, h.clientConn.Close(coderws.StatusNormalClosure, "cancel pending retry"))
				stopHandshake()
			} else if stage == "inflight" {
				require.Equal(t, "response.completed", gjson.GetBytes(issue715WSRead(t, h.clientConn), "type").String())
				issue715WSSend(t, h.clientConn, `{"type":"response.create","model":"gpt-5.1","input":"next turn"}`)
				require.Equal(t, "response.completed", gjson.GetBytes(issue715WSRead(t, h.clientConn), "type").String())
				_ = h.clientConn.CloseNow()
			} else {
				readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				for {
					_, frame, err := h.clientConn.Read(readCtx)
					if err != nil {
						readTimedOut = readCtx.Err() == context.DeadlineExceeded
						break
					}
					if gjson.GetBytes(frame, "type").String() == "response.output_text.delta" {
						outputObserved = true
					}
					if gjson.GetBytes(frame, "type").String() == "response.completed" {
						// Defective production sends a second generation after D.
						// Close and join it now, then fail on its observed count.
						unexpectedCompletion = true
						break
					}
				}
				_ = h.clientConn.CloseNow()
			}
			select {
			case <-h.handlerDone:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not release the retry")
			}
			if stage == "handshake" || stage == "canceled" {
				if stage == "handshake" {
					var entered time.Time
					select {
					case entered = <-handshakeEntered:
					case <-time.After(time.Second):
						t.Fatal("retry handshake was not observed before cleanup")
					}
					mu.Lock()
					require.True(t, entered.Before(deadline), "handler checks must pass before the handshake crosses D")
					mu.Unlock()
				}
				select {
				case <-handshakeClosed:
				case <-time.After(time.Second):
					t.Fatal("upstream retry lease remained open")
				}
			}
			mu.Lock()
			observedGenerations := generations
			observedOwners := append([]string(nil), owners...)
			secondAfterDeadline := len(generationTimes) > 1 && !generationTimes[1].Before(deadline)
			mu.Unlock()
			want := 1
			if stage == "inflight" {
				want = 3
			}
			t.Logf("retry deadline observation: stage=%s generations=%d", stage, observedGenerations)
			t.Logf("retry deadline fallback observation: stage=%s second_after_deadline=%t owners=%v", stage, secondAfterDeadline, observedOwners)
			require.Equal(t, want, observedGenerations, "retry deadline: no generation may start after D")
			require.False(t, readTimedOut, "fixture read timed out before observing retry closure")
			require.False(t, unexpectedCompletion, "expired retry must not complete a generation")
			if stage == "output" {
				require.True(t, outputObserved, "the output control must actually commit output")
			} else if stage != "inflight" {
				require.False(t, outputObserved, "pre-output refusal must emit no generation output")
			}
			for _, owner := range observedOwners {
				require.Equal(t, "Bearer fixture-deadline-owner", owner)
			}
			slots.mu.Lock()
			require.Positive(t, slots.userAcquired)
			require.Positive(t, slots.accountAcquired)
			require.Equal(t, slots.userAcquired, slots.userReleased)
			require.Equal(t, slots.accountAcquired, slots.accountReleased)
			if stage == "user_slot" || stage == "account_slot" {
				require.True(t, slots.waitEnteredBeforeDeadline)
			}
			slots.mu.Unlock()
			inflight, waiters, _ := service.OpenAIRetryPoolLoadForTest(gateway, accountID)
			require.Zero(t, inflight)
			require.Zero(t, waiters)
		})
	}
}
