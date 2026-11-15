package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type pr610Barrier struct {
	phase   string
	release chan struct{}
}

// The clock is request-scoped. Barriers stall the actual slot-cache and pool
// acquisition calls in authenticated handler -> production ingress -> network.
// Busy user/account slots still reject according to the existing WS policy;
// these barriers model a delayed acquisition, not a new capacity-wait policy.
type pr610TimingScenario struct {
	t             *testing.T
	mode          string
	realtime      bool
	base          time.Time
	offset        atomic.Int64
	turn          atomic.Int32
	invalidClock  bool
	beyondHour    bool
	keyQueue      bool
	sameAccount   bool
	failover      bool
	failedSwitch  atomic.Bool
	poolHolder    bool
	holderHandled atomic.Bool
	holderArrived chan struct{}
	holderRelease chan struct{}
	holderDone    chan struct{}
	gateway       *service.OpenAIGatewayService
	ctxRetry      bool
	retry         bool
	longFirst     bool
	attempts      atomic.Int32
	mu            sync.Mutex
	frameSentAt   map[int32]time.Time
	providerWait  map[int32]time.Duration
	gated         map[string]bool
	barriers      chan pr610Barrier
	deltaRead     chan struct{}
	cache         *pr610SlotCache
}

func newPR610Scenario(t *testing.T, mode string) *pr610TimingScenario {
	t.Helper()
	s := &pr610TimingScenario{t: t, mode: mode, base: time.Now(), barriers: make(chan pr610Barrier), deltaRead: make(chan struct{}), gated: map[string]bool{}, frameSentAt: map[int32]time.Time{}, providerWait: map[int32]time.Duration{}}
	raw := testutil.NewRedisConcurrencyCache(t)
	q, ok := raw.(service.APIKeySlotQueueCache)
	require.True(t, ok)
	admission, ok := raw.(service.APIKeySlotAdmissionCache)
	require.True(t, ok)
	s.cache = &pr610SlotCache{ConcurrencyCache: raw, APIKeySlotQueueCache: q, admission: admission, s: s}
	return s
}
func (s *pr610TimingScenario) now() time.Time {
	if s.realtime {
		return time.Now()
	}
	return s.base.Add(time.Duration(s.offset.Load()))
}
func (s *pr610TimingScenario) advance(d time.Duration) {
	if s.realtime {
		time.Sleep(d / 100)
		return
	}
	s.offset.Add(int64(d))
}

type pr610HolderKey struct{}

func (s *pr610TimingScenario) middleware(c *gin.Context) {
	if c.GetHeader("X-PR610-Pool-Holder") == "true" {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), pr610HolderKey{}, true))
	}
	c.Request = c.Request.WithContext(requesttiming.WithClock(c.Request.Context(), s.now))
	c.Next()
}
func (s *pr610TimingScenario) configure(a *service.Account, cfg *config.Config) {
	if s.sameAccount {
		a.Type = service.AccountTypeOAuth
		a.Credentials["access_token"] = "fixture-account-a"
		a.Credentials["chatgpt_account_id"] = "fixture-account"
		a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
		a.Extra["openai_oauth_responses_websockets_v2_mode"] = s.mode
		a.Extra["codex_fingerprint_mode"] = "off"
		cfg.Gateway.OpenAIWS.OAuthEnabled = true
	}
	require.Equal(s.t, s.mode, a.ResolveOpenAIResponsesWebSocketV2Mode(cfg.Gateway.OpenAIWS.IngressModeDefault), "fixture must resolve the requested effective mode")
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	if s.poolHolder {
		a.Concurrency = 2
	}
}
func (s *pr610TimingScenario) block(phase string) {
	s.mu.Lock()
	if s.gated[phase] {
		s.mu.Unlock()
		return
	}
	s.gated[phase] = true
	s.mu.Unlock()
	b := pr610Barrier{phase: phase, release: make(chan struct{})}
	select {
	case s.barriers <- b:
	case <-time.After(3 * time.Second):
		s.t.Error("barrier was not consumed: " + phase)
		return
	}
	select {
	case <-b.release:
	case <-time.After(3 * time.Second):
		s.t.Error("barrier was not released: " + phase)
	}
}
func (s *pr610TimingScenario) prepareTurn() {
	turn := s.turn.Add(1)
	if s.keyQueue {
		q := s.cache
		require.NoError(s.t, q.TrackAPIKeySlot(context.Background(), 1801, "pr610-key-holder"))
	}
	s.mu.Lock()
	s.gated = map[string]bool{}
	s.frameSentAt[turn] = time.Now()
	s.mu.Unlock()
	phases := []string{"user", "account"}
	if s.failover && turn == 2 {
		phases = append(phases, "switch_account")
	}
	if turn == 1 && s.mode == service.OpenAIWSIngressModeCtxPool && !s.poolHolder {
		phases = append(phases, "pool")
	}
	go func() {
		if s.keyQueue {
			q := s.cache
			require.Eventually(s.t, func() bool {
				_, waiting, err := q.GetAPIKeyQueueStats(context.Background(), 1801)
				return err == nil && waiting == 1
			}, 3*time.Second, time.Millisecond, "actual Redis key ticket must wait before user slot")
			s.advance(5 * time.Second)
			require.NoError(s.t, q.ReleaseAPIKeySlot(context.Background(), 1801, "pr610-key-holder"))
		}
		for _, phase := range phases {
			select {
			case b := <-s.barriers:
				if b.phase != phase {
					s.t.Errorf("barrier order: got %s want %s", b.phase, phase)
				}
				d := 10 * time.Second
				if phase == "user" && s.invalidClock {
					s.advance(-2 * time.Hour)
				}
				if phase == "user" && s.beyondHour {
					d = 2 * time.Hour
				}
				if phase == "account" {
					d = 20 * time.Second
				}
				if phase == "switch_account" {
					d = 5 * time.Second
				}
				s.advance(d)
				close(b.release)
			case <-time.After(3 * time.Second):
				s.t.Error("production path did not reach barrier: " + phase)
				return
			}
		}
		if s.poolHolder {
			require.Eventually(s.t, func() bool { return s.gateway.SnapshotOpenAIWSPoolMetrics().AcquireQueueWaitTotal > 0 }, 3*time.Second, time.Millisecond, "target must enter real busy lease queue")
			s.advance(10 * time.Second)
			close(s.holderRelease)
		}
	}()
}
func (s *pr610TimingScenario) poolHandshake() {
	if s.mode == service.OpenAIWSIngressModeCtxPool && !s.poolHolder {
		s.block("pool")
	}
}
func (s *pr610TimingScenario) terminal(payload []byte, turn int) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_usage_e2e_%d","model":%q,"usage":{"input_tokens":2,"output_tokens":1}}}`, turn, gjson.GetBytes(payload, "model").String())
}
func (s *pr610TimingScenario) afterDelta() {
	select {
	case <-s.deltaRead:
	case <-time.After(3 * time.Second):
		s.t.Error("synthetic first token did not reach downstream")
	}
	if s.longFirst && s.turn.Load() == 1 {
		s.advance(80 * time.Second)
	}
}

// Capture the real client-send-to-provider boundary independently of the
// production timing arithmetic. Hosted-runner scheduling can extend waits.
func (s *pr610TimingScenario) observeProvider() {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn := s.turn.Load()
	s.providerWait[turn] = time.Since(s.frameSentAt[turn])
}
func (s *pr610TimingScenario) writeWS(c *coderws.Conn, turn int, payload []byte) {
	s.observeProvider()
	s.advance(2 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(s.t, c.Write(ctx, coderws.MessageText, []byte(`{"type":"response.output_text.delta","response_id":"resp_usage_e2e_`+fmt.Sprint(turn)+`","delta":"x"}`)))
	s.afterDelta()
	require.NoError(s.t, c.Write(ctx, coderws.MessageText, []byte(s.terminal(payload, turn))))
}
func (s *pr610TimingScenario) serveHTTP(w http.ResponseWriter, r *http.Request, received chan<- []byte) bool {
	if s.mode != service.OpenAIWSIngressModeHTTPBridge {
		return false
	}
	s.observeProvider()
	payload, err := io.ReadAll(r.Body)
	require.NoError(s.t, err)
	if (s.failover || s.sameAccount) && s.turn.Load() == 2 && r.Header.Get("Authorization") == "Bearer fixture-account-a" && s.failedSwitch.CompareAndSwap(false, true) {
		s.advance(7 * time.Second)
		if !s.sameAccount {
			w.Header().Set("Retry-After", "60")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"try again"}}`)
		return true
	}
	if s.retry && s.attempts.Add(1) == 1 {
		s.advance(7 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"invalid_parameter","param":"truncation","message":"Unsupported parameter: truncation"}}`)
		return true
	}
	received <- payload
	s.advance(2 * time.Second)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_usage_e2e_%d\",\"delta\":\"x\"}\n\n", s.turn.Load())
	flusher, ok := w.(http.Flusher)
	require.True(s.t, ok)
	flusher.Flush()
	s.afterDelta()
	_, _ = fmt.Fprintf(w, "data: %s\n\n", s.terminal(payload, int(s.turn.Load())))
	return true
}
func (s *pr610TimingScenario) assertReleased() {
	if s.poolHolder {
		select {
		case <-s.holderDone:
		case <-time.After(3 * time.Second):
			s.t.Fatal("pool holder did not release")
		}
	}
	users, err := s.cache.GetUserConcurrency(context.Background(), 1701)
	require.NoError(s.t, err)
	require.Zero(s.t, users)
	accounts, err := s.cache.GetAccountConcurrency(context.Background(), 9901)
	require.NoError(s.t, err)
	require.Zero(s.t, accounts)
}

type pr610SlotCache struct {
	admission service.APIKeySlotAdmissionCache
	service.APIKeySlotQueueCache
	service.ConcurrencyCache
	s *pr610TimingScenario
}

func (c *pr610SlotCache) AcquireAPIKeySlot(ctx context.Context, id int64, limit int, member string) (bool, error) {
	return c.admission.AcquireAPIKeySlot(ctx, id, limit, member)
}

var _ service.APIKeySlotAdmissionCache = (*pr610SlotCache)(nil)
var _ service.APIKeySlotQueueCache = (*pr610SlotCache)(nil)

func (c *pr610SlotCache) AcquireUserSlot(ctx context.Context, id int64, limit int, member string) (bool, error) {
	if ctx.Value(pr610HolderKey{}) != true {
		c.s.block("user")
	}
	return c.ConcurrencyCache.AcquireUserSlot(ctx, id, limit, member)
}
func (c *pr610SlotCache) AcquireAccountSlot(ctx context.Context, id int64, limit int, member string) (bool, error) {
	if c.s.failover && id == 9902 {
		c.s.block("switch_account")
	}
	if ctx.Value(pr610HolderKey{}) != true {
		c.s.block("account")
	}
	return c.ConcurrencyCache.AcquireAccountSlot(ctx, id, limit, member)
}

type pr610AuthRepo struct {
	service.APIKeyRepository
	key        *service.APIKey
	beforeRead func()
}

func (r *pr610AuthRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if r.beforeRead != nil {
		r.beforeRead()
	}
	if key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	return r.key, nil
}
func (*pr610AuthRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }
func pr610Auth(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
	key.User.Concurrency = 1
	key.Group = &service.Group{ID: *key.GroupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	return service.NewAPIKeyService(&pr610AuthRepo{key: key}, nil, nil, nil, nil, nil, cfg)
}

func TestPR610AuthenticatedTurnsCountWaitOnce(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			s := newPR610Scenario(t, mode)
			s.longFirst = true
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: mode, authSetup: pr610Auth,
				firstPayload:  `{"type":"response.create","model":"gpt-5.4","stream":true}`,
				secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":true}`})
			require.Len(t, got.logs, 2)
			for i, row := range got.logs {
				q := int64(30000)
				if i == 0 && mode == service.OpenAIWSIngressModeCtxPool {
					q += 10000
				}
				require.NotNil(t, row.QueueWaitMs, "authenticated native turn must publish measured pre-forward wait")
				require.EqualValues(t, q, *row.QueueWaitMs, "accepted frame must count each delayed slot/pool acquisition exactly once")
				require.NotNil(t, row.QueueTiming, "explicit sampled timing source must survive the real usage path")
				require.Equal(t, "ws_frame_"+mode, row.QueueTiming.Source, "effective mode must match fixture")
				require.EqualValues(t, q+2000, *row.QueueTiming.ServerFirstTokenMs)
				require.EqualValues(t, 2000, *row.QueueTiming.ForwardFirstTokenMs)
				legacyFirst := int64(2000)
				if mode == service.OpenAIWSIngressModePassthrough {
					legacyFirst += q
				}
				require.EqualValues(t, legacyFirst, *row.FirstTokenMs, "legacy timer contract must not be redefined")
				tail := int64(0)
				if i == 0 {
					tail = 80000
				}
				require.EqualValues(t, q+2000+tail, row.QueueTiming.ServerDurationMs)
				require.EqualValues(t, 2000+tail, row.QueueTiming.ForwardDurationMs)
				require.EqualValues(t, legacyFirst+tail, *row.DurationMs, "legacy duration remains the TPS denominator")
			}
		})
	}
}

func TestPR610BridgeRejectedFieldRetryKeepsLegacyDenominator(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeHTTPBridge)
	s.retry = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode, authSetup: pr610Auth,
		firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"truncation":"auto"}`})
	require.EqualValues(t, 2, s.attempts.Load(), "synthetic upstream must exercise the real rejected-field retry")
	row := got.log
	require.EqualValues(t, 30000, *row.QueueWaitMs, "failed network attempt must not move into queue")
	require.EqualValues(t, 9000, *row.FirstTokenMs, "bridge legacy first must include the rejected-field attempt")
	require.EqualValues(t, 9000, *row.DurationMs, "bridge legacy TPS denominator must include the rejected-field attempt")
	require.NotNil(t, row.QueueTiming)
	require.EqualValues(t, 39000, *row.QueueTiming.ServerFirstTokenMs)
	require.EqualValues(t, 9000, row.QueueTiming.ForwardDurationMs)
}

// This counterpart uses actual wall time, so the frozen defective source can
// consume the same fixture without depending on the newly introduced clock seam.
func TestPR610RealWaitNativeIngress(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			s := newPR610Scenario(t, mode)
			s.realtime = true
			s.longFirst = true
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: mode, authSetup: pr610Auth,
				firstPayload:  `{"type":"response.create","model":"gpt-5.4","stream":true}`,
				secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":true}`})
			for i, row := range got.logs {
				want := 300
				if i == 0 && mode == service.OpenAIWSIngressModeCtxPool {
					want = 400
				}
				require.NotNil(t, row.QueueWaitMs, "native authenticated turn must retain a measured wait")
				require.GreaterOrEqual(t, *row.QueueWaitMs, want-10, "accepted frame must count real blocked acquisition before forward")
				s.mu.Lock()
				observed := s.providerWait[int32(i+1)]
				s.mu.Unlock()
				require.Positive(t, observed, "fixture must observe the actual provider receive")
				require.LessOrEqual(t, int64(*row.QueueWaitMs), observed.Milliseconds()+2, "queue cannot exceed this turn's real client-to-provider interval or include the previous turn")
				require.NotNil(t, row.QueueTiming, "measured source must survive authenticated production usage path")
				require.InDelta(t, 20, *row.QueueTiming.ForwardFirstTokenMs, 100)
				require.GreaterOrEqual(t, *row.QueueTiming.ServerFirstTokenMs, int64(want+10))
				if mode == service.OpenAIWSIngressModePassthrough {
					require.InDelta(t, *row.FirstTokenMs, *row.QueueTiming.ServerFirstTokenMs, 2, "passthrough legacy already includes queue")
				}
			}
		})
	}
}
func TestPR610RealBridgeRetryDenominator(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeHTTPBridge)
	s.realtime = true
	s.retry = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode, authSetup: pr610Auth,
		firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"truncation":"auto"}`})
	require.EqualValues(t, 2, s.attempts.Load())
	row := got.log
	require.GreaterOrEqual(t, *row.DurationMs, 85, "rejected-field network time must stay in legacy TPS denominator")
	require.Less(t, *row.QueueWaitMs, 380, "rejected-field network time must not move into queue")
}

func (s *pr610TimingScenario) startHolder(serverURL, key string, gateway *service.OpenAIGatewayService) {
	s.gateway = gateway
	s.holderArrived = make(chan struct{})
	s.holderRelease = make(chan struct{})
	s.holderDone = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}, "X-PR610-Pool-Holder": {"true"}}})
	require.NoError(s.t, err)
	require.NoError(s.t, c.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","stream":true,"input":"pool-holder"}`)))
	go func() {
		defer close(s.holderDone)
		defer func() { _ = c.CloseNow() }()
		readCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, event, err := c.Read(readCtx)
		if err != nil {
			s.t.Error(err)
			return
		}
		if gjson.GetBytes(event, "response.id").String() != "resp_pool_holder" {
			s.t.Errorf("unexpected pool holder event %s", event)
		}
		_ = c.Close(coderws.StatusNormalClosure, "release lease")
	}()
	select {
	case <-s.holderArrived:
	case <-time.After(3 * time.Second):
		s.t.Fatal("holder did not acquire real pool lease")
	}
}
func (s *pr610TimingScenario) handleHolder(c *coderws.Conn, payload []byte) bool {
	if !s.poolHolder || gjson.GetBytes(payload, "input").String() != "pool-holder" || !s.holderHandled.CompareAndSwap(false, true) {
		return false
	}
	close(s.holderArrived)
	select {
	case <-s.holderRelease:
	case <-time.After(5 * time.Second):
		s.t.Error("target did not release pool holder")
		return true
	}
	require.NoError(s.t, c.Write(context.Background(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_pool_holder","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)))
	return true
}
func TestPR610AuthenticatedBusyPoolLeaseCountsOnce(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeCtxPool)
	s.poolHolder = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode,
		authSetup: func(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
			svc := pr610Auth(key, cfg)
			key.User.Concurrency = 2
			return svc
		},
		firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"input":"pool-holder"}`})
	require.Greater(t, s.gateway.SnapshotOpenAIWSPoolMetrics().AcquireQueueWaitTotal, int64(0), "must contend on a busy pool lease")
	require.NotNil(t, got.log.QueueTiming)
	require.EqualValues(t, 40000, *got.log.QueueWaitMs, "actual busy lease wait and both slot calls count once")
	require.EqualValues(t, 42000, *got.log.QueueTiming.ServerFirstTokenMs)
	require.EqualValues(t, 2000, *got.log.FirstTokenMs)
}

// Each account's snapshot is stable; only the synthetic runtime cooldown mutates.
type pr610SwitchRepo struct {
	service.AccountRepository
	mu       sync.Mutex
	accounts []service.Account
}

func (r *pr610SwitchRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []service.Account
	for _, a := range r.accounts {
		if a.Platform == platform && a.IsSchedulable() {
			out = append(out, a)
		}
	}
	return out, nil
}
func (r *pr610SwitchRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}
func (r *pr610SwitchRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.accounts {
		if a.ID == id {
			copy := a
			return &copy, nil
		}
	}
	return nil, nil
}
func (r *pr610SwitchRepo) GetOpenAITurnAdmission(ctx context.Context, id int64) (*service.Account, *service.Account, error) {
	a, err := r.GetByID(ctx, id)
	return a, nil, err
}
func (r *pr610SwitchRepo) SetRateLimited(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].RateLimitResetAt = &at
		}
	}
	return nil
}
func (*pr610SwitchRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }
func (s *pr610TimingScenario) accountRepository(a service.Account) service.AccountRepository {
	if !s.failover {
		return &openAIWSUsageHandlerAccountRepoStub{account: a}
	}
	a.Priority = 1
	a.Credentials = map[string]any{"base_url": a.Credentials["base_url"], "api_key": "fixture-account-a"}
	b := a
	b.ID = 9902
	b.Priority = 2
	b.Credentials = map[string]any{"base_url": a.Credentials["base_url"], "api_key": "fixture-account-b"}
	return &pr610SwitchRepo{accounts: []service.Account{a, b}}
}
func TestPR610AuthenticatedFollowupAccountSwitchKeepsFrameStart(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeHTTPBridge)
	s.failover = true
	s.longFirst = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode, authSetup: pr610Auth,
		firstPayload:  `{"type":"response.create","model":"gpt-5.4","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"portable synthetic request"}]}]}`,
		secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"portable synthetic request"}]}]}`})
	require.Len(t, got.logs, 2)
	require.True(t, s.failedSwitch.Load(), "real synthetic 429 must switch the current turn's account")
	require.EqualValues(t, 9901, got.logs[0].AccountID)
	require.EqualValues(t, 9902, got.logs[1].AccountID)
	require.NotNil(t, got.logs[1].QueueTiming)
	require.EqualValues(t, 42000, *got.logs[1].QueueWaitMs, "retain current frame start over failed 7s attempt and 5s account switch, excluding prior turn")
	require.EqualValues(t, 44000, *got.logs[1].QueueTiming.ServerFirstTokenMs)
	require.EqualValues(t, 2000, *got.logs[1].FirstTokenMs)
}

func TestPR610AuthenticatedRedisKeyQueueAcrossModes(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			s := newPR610Scenario(t, mode)
			s.keyQueue = true
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: mode,
				authSetup: func(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
					key.ConcurrencyLimit = 1
					return pr610Auth(key, cfg)
				},
				firstPayload:  `{"type":"response.create","model":"gpt-5.4","stream":true}`,
				secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":true}`})
			for i, row := range got.logs {
				q := 35000
				if i == 0 && mode == service.OpenAIWSIngressModeCtxPool {
					q += 10000
				}
				require.NotNil(t, row.QueueWaitMs)
				require.EqualValues(t, q, *row.QueueWaitMs, "actual queued Redis key ticket must count once before slot/pool acquisition")
				require.NotNil(t, row.QueueTiming)
				require.EqualValues(t, q+2000, *row.QueueTiming.ServerFirstTokenMs)
			}
		})
	}
}

func TestPR610PassthroughPreflightKeepsLegacyBaseline(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModePassthrough)
	var delayed atomic.Bool
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode,
		authSetup: func(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
			key.User.Concurrency = 1
			key.Group = &service.Group{ID: *key.GroupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			repo := &pr610AuthRepo{key: key, beforeRead: func() {
				if s.turn.Load() == 2 && delayed.CompareAndSwap(false, true) {
					s.advance(5 * time.Second)
				}
			}}
			return service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
		}, firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true}`,
		secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":true}`})
	require.True(t, delayed.Load(), "follow-up payload preflight must read current auth")
	row := got.logs[1]
	require.NotNil(t, row.QueueTiming)
	require.EqualValues(t, 35000, *row.QueueWaitMs, "new receipt scope includes payload preflight")
	require.EqualValues(t, 32000, *row.FirstTokenMs, "legacy passthrough timer still starts after payload preflight")
	require.EqualValues(t, 37000, *row.QueueTiming.ServerFirstTokenMs)
	require.EqualValues(t, 2000, *row.QueueTiming.ForwardFirstTokenMs)
}

func TestPR610ProtocolClockValidityAndClamp(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		for _, invalid := range []bool{false, true} {
			name := "clamp"
			if invalid {
				name = "future_ingress"
			}
			t.Run(mode+"/"+name, func(t *testing.T) {
				s := newPR610Scenario(t, mode)
				s.invalidClock = invalid
				s.beyondHour = !invalid
				got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: mode, authSetup: pr610Auth, firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true}`})
				row := got.log
				if invalid {
					require.Nil(t, row.QueueWaitMs, "future ingress must remain unknown, not a measured zero")
					require.Nil(t, row.QueueTiming)
					return
				}
				require.NotNil(t, row.QueueWaitMs)
				require.EqualValues(t, 3600000, *row.QueueWaitMs)
				require.NotNil(t, row.QueueTiming)
				require.True(t, row.QueueTiming.QueueClamped)
				require.Greater(t, *row.QueueTiming.ServerFirstTokenMs, int64(7200000), "queue cap must not truncate actual server elapsed time")
				require.EqualValues(t, 2000, *row.QueueTiming.ForwardFirstTokenMs)
			})
		}
	}
}

func (s *pr610TimingScenario) handleCtxRetry(c *coderws.Conn) bool {
	if !s.ctxRetry {
		return false
	}
	if s.attempts.Add(1) != 1 {
		return false
	}
	s.advance(7 * time.Second)
	require.NoError(s.t, c.Write(context.Background(), coderws.MessageText, []byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"invalid_parameter","param":"truncation","message":"Unsupported parameter: truncation"}}`)))
	return true
}
func TestPR610CtxPoolRejectedFieldRetryScope(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeCtxPool)
	s.ctxRetry = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode, authSetup: pr610Auth, firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"truncation":"auto"}`})
	require.EqualValues(t, 2, s.attempts.Load())
	row := got.log
	require.EqualValues(t, 47000, *row.QueueWaitMs, "ctx_pool final-attempt legacy baseline leaves earlier attempt in explicit pre-final scope")
	require.EqualValues(t, 2000, *row.FirstTokenMs)
	require.EqualValues(t, 2000, *row.DurationMs)
	require.NotNil(t, row.QueueTiming)
	require.Equal(t, "pre_final_forward", row.QueueTiming.Scope)
	require.EqualValues(t, 49000, *row.QueueTiming.ServerFirstTokenMs)
}
func TestPR610RealCtxPoolRetryKeepsFinalAttemptScope(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeCtxPool)
	s.ctxRetry = true
	s.realtime = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode, authSetup: pr610Auth, firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"truncation":"auto"}`})
	require.EqualValues(t, 2, s.attempts.Load())
	require.NotNil(t, got.log.QueueWaitMs)
	require.GreaterOrEqual(t, *got.log.QueueWaitMs, 460, "ctx_pool retry retains accepted frame and pre-final-forward scope")
	require.InDelta(t, 20, *got.log.DurationMs, 100, "legacy TPS denominator remains the final attempt")
}

// Redirect only the fixed OAuth endpoint into this fixture. No public egress or
// global client mutation; the production bridge still builds/adopts the request.
type pr610OAuthHTTPUpstream struct {
	service.HTTPUpstream
	target *url.URL
}

func (u *pr610OAuthHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req.URL.Host != "chatgpt.com" && req.URL.Host != u.target.Host {
		return nil, fmt.Errorf("unexpected synthetic destination %s", req.URL.Host)
	}
	local := req.Clone(req.Context())
	copy := *req.URL
	local.URL = &copy
	local.URL.Scheme = u.target.Scheme
	local.URL.Host = u.target.Host
	local.Host = u.target.Host
	return http.DefaultClient.Do(local)
}
func TestPR610AuthenticatedSameAccountRetryKeepsFollowupStart(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeHTTPBridge)
	s.sameAccount = true
	s.longFirst = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode, authSetup: pr610Auth,
		firstPayload:  `{"type":"response.create","model":"gpt-5.4","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first portable request"}]}]}`,
		secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"followup portable request"}]}]}`})
	require.True(t, s.failedSwitch.Load(), "transient OAuth 429 must exercise production same-account retry")
	require.EqualValues(t, 9901, got.logs[0].AccountID)
	require.EqualValues(t, 9901, got.logs[1].AccountID)
	require.NotNil(t, got.logs[1].QueueTiming)
	require.EqualValues(t, 37000, *got.logs[1].QueueWaitMs, "same-account retry must keep current frame start and exclude prior long turn")
	require.EqualValues(t, 39000, *got.logs[1].QueueTiming.ServerFirstTokenMs)
	require.EqualValues(t, 2000, *got.logs[1].FirstTokenMs)
}
