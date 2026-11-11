//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type reviewGenerationConn struct{ *stagedPassthroughConn }

func (c *reviewGenerationConn) WriteJSON(ctx context.Context, v any) error {
	if prepared, ok := v.(openAIWSPreparedJSON); ok {
		v = json.RawMessage(prepared)
	}
	bytes, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, bytes)
}

type reviewLeaseDialer struct {
	conn    *reviewGenerationConn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *reviewLeaseDialer) Dial(ctx context.Context, _ string, _ http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	d.once.Do(func() { close(d.entered) })
	select {
	case <-d.release:
		return d.conn, 101, http.Header{}, nil
	case <-ctx.Done():
		return nil, 0, nil, ctx.Err()
	}
}
func reviewNativeFixture(t *testing.T, mode string) (*OpenAIGatewayService, OpenAIAccountScheduleRequest, *Account, *reviewGenerationConn, *reviewLeaseDialer, context.Context) {
	t.Helper()
	svc, _, req, pool, _ := latencyModeFixture(t, PriorityLatencyStop)
	setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 500; v.LatestBadLatencyAt = nil })
	cfg := newOpenAIWSExecutionScopeTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = mode
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 5
	svc.cfg = cfg
	svc.accountRepo = nil
	svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
	svc.httpUpstream = &httpUpstreamRecorder{}
	svc.toolCorrector = NewCodexToolCorrector()
	svc.cache = &stubGatewayCache{}
	svc.openaiWSStateStore = NewOpenAIWSStateStore(nil)
	account := pool[0].account
	account.Credentials = map[string]any{"api_key": "sk-synthetic"}
	account.Extra = map[string]any{"openai_apikey_responses_websockets_v2_mode": mode}
	conn := &reviewGenerationConn{newStagedPassthroughConn()}
	dialer := &reviewLeaseDialer{conn: conn, entered: make(chan struct{}), release: make(chan struct{})}
	wsPool := newOpenAIWSConnPool(cfg)
	wsPool.setClientDialerForTest(dialer)
	svc.openaiWSPool = wsPool
	t.Cleanup(wsPool.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx = WithPrioritySchedulingObservation(ctx, req.GroupID, req.HistoryModel, req.InboundEndpoint)
	ctx = WithOpenAIForwardModel(ctx, req.RequestedModel, false)
	return svc, req, account, conn, dialer, ctx
}
func reviewNativeClient(t *testing.T, ctx context.Context, svc *OpenAIGatewayService, req OpenAIAccountScheduleRequest, account *Account) (*coderws.Conn, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_, first, err := ReadOpenAIWSClientMessage(ctx, conn, 3*time.Second, coderws.StatusPolicyViolation, "missing request")
		if err != nil {
			done <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(ctx)
		c.Set("api_key", &APIKey{ID: 77, GroupID: req.GroupID})
		done <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "sk-synthetic", first, nil)
	}))
	t.Cleanup(server.Close)
	client := dialPassthroughLifecycleClientWithPayload(t, server, fmt.Sprintf(`{"type":"response.create","model":%q,"stream":true,"input":[{"role":"user","content":"synthetic"}]}`, req.RequestedModel))
	t.Cleanup(func() { _ = client.CloseNow() })
	return client, done
}
func reviewActivateStop(svc *OpenAIGatewayService, req OpenAIAccountScheduleRequest, id int64) {
	setModeSignal(svc, req, id, func(v *PrioritySchedulingSignal) {
		bad := time.Now()
		v.Samples = 20
		v.P90TTFTMs = 16000
		v.LatestBadLatencyAt = &bad
	})
}

func TestPriorityPenaltyReviewNativeLease(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModeShared, OpenAIWSIngressModeDedicated} {
		t.Run(mode, func(t *testing.T) {
			svc, req, account, upstream, dialer, ctx := reviewNativeFixture(t, mode)
			require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, 11, "resp_review_owner", account.ID, time.Hour))
			client, done := reviewNativeClient(t, ctx, svc, req, account)
			select {
			case <-dialer.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("native lease did not reach handshake barrier")
			}
			reviewActivateStop(svc, req, account.ID)
			close(dialer.release)
			var gotErr error
			select {
			case gotErr = <-done:
			case write := <-upstream.writes:
				// Fail on the exact observable defect on a426: a generation crossed the
				// barrier after the matching account/model/group was stopped.
				require.Empty(t, write, "native lease wait must recheck stop before generation write")
			case <-time.After(3 * time.Second):
				t.Fatal("native lease admission did not finish")
			}
			require.ErrorIs(t, gotErr, ErrPriorityLatencyUnavailable, "native stop returns temporary admission cause")
			require.Empty(t, upstream.writes)
			require.Eventually(t, func() bool {
				select {
				case <-upstream.closed:
					return true
				default:
					return false
				}
			}, time.Second, time.Millisecond, "rejected lease must be released and closed")
			id, err := svc.getOpenAIWSStateStore().GetResponseAccount(ctx, 11, "resp_review_owner")
			require.NoError(t, err)
			require.Equal(t, account.ID, id, "temporary stop preserves existing ownership")
			_ = client
		})
	}
}
func TestPriorityPenaltyReviewNativeStartedOutput(t *testing.T) {
	svc, req, account, upstream, dialer, ctx := reviewNativeFixture(t, OpenAIWSIngressModeCtxPool)
	close(dialer.release)
	client, done := reviewNativeClient(t, ctx, svc, req, account)
	requirePassthroughUpstreamWrite(t, upstream.stagedPassthroughConn, time.Second)
	upstream.Send(`{"type":"response.output_text.delta","delta":"started"}`)
	delta, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	require.Contains(t, string(delta), "started")
	reviewActivateStop(svc, req, account.ID)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_review_complete","model":"gpt-test","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	terminal, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	require.Contains(t, string(terminal), "response.completed", "already-started generation must complete")
	require.Empty(t, upstream.writes, "penalty does not replay output")
	_ = client.Close(coderws.StatusNormalClosure, "done")
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("native output cleanup did not finish")
	}
}

type reviewAcquireCache struct {
	schedulerTestConcurrencyCache
	onAcquire func()
	once      sync.Once
}

func (c *reviewAcquireCache) AcquireAccountSlot(ctx context.Context, id int64, max int, rid string) (bool, error) {
	c.once.Do(c.onAcquire)
	return c.schedulerTestConcurrencyCache.AcquireAccountSlot(ctx, id, max, rid)
}

type reviewFreshRepo struct {
	schedulerTestOpenAIAccountRepo
	onRead func()
	once   sync.Once
}

func (r *reviewFreshRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.once.Do(r.onRead)
	return r.schedulerTestOpenAIAccountRepo.GetByID(ctx, id)
}

func TestPriorityPenaltyReviewLateSelection(t *testing.T) {
	for _, stage := range []string{"after_pool", "after_slot", "fresh_state", "wait_fallback"} {
		for _, change := range []string{"history", "mode"} {
			for _, alternate := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/alternate=%t", stage, change, alternate), func(t *testing.T) {
					svc, s, req, pool, _ := latencyModeFixture(t, PriorityLatencyStop)
					setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 500; v.LatestBadLatencyAt = nil })
					if change == "mode" {
						reviewActivateStop(svc, req, 1)
						soft := svc.prioritySchedulingRuntimeConfig()
						soft.LatencyPenaltyMode = PriorityLatencyDeprioritize
						require.NoError(t, svc.settingService.SavePrioritySchedulingConfig(context.Background(), soft))
					}
					candidates := pool[:1]
					if alternate {
						candidates = pool
					}
					accounts := make([]*Account, 0, len(candidates))
					loads := map[int64]*AccountLoadInfo{}
					for _, item := range candidates {
						accounts = append(accounts, item.account)
						loads[item.account.ID] = item.loadInfo
					}
					// Exercise the real initial gate/plan before activating stop at each seam.
					allowed, err := s.filterPriorityLatencyAccounts(req, accounts, loads)
					require.NoError(t, err)
					plan := s.buildOpenAIAccountLoadPlan(context.Background(), req, allowed, loads)
					// Fix order only in the fixture so the stopped candidate is tried first.
					order := append([]openAIAccountCandidateScore(nil), candidates...)
					activate := func() {
						if change == "history" {
							reviewActivateStop(svc, req, 1)
						} else {
							hard := svc.prioritySchedulingRuntimeConfig()
							hard.LatencyPenaltyMode = PriorityLatencyStop
							require.NoError(t, svc.settingService.SavePrioritySchedulingConfig(context.Background(), hard))
						}
					}
					var releases []int64
					budget := newOpenAISelectionProbeBudget()
					if stage == "after_pool" {
						activate()
					}
					if stage == "after_slot" {
						svc.concurrencyService = NewConcurrencyService(&reviewAcquireCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{releasedIDs: &releases}, onAcquire: activate})
					}
					if stage == "fresh_state" || stage == "wait_fallback" {
						repo := &reviewFreshRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{*pool[0].account, *pool[1].account}}, onRead: activate}
						svc.accountRepo = repo
						svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{accountsByID: map[int64]*Account{1: pool[0].account, 2: pool[1].account}}, accountRepo: repo}
					}
					var selected *AccountSelectionResult
					if stage != "wait_fallback" {
						selected, _, err = s.tryAcquireOpenAISelectionOrderWithBudget(context.Background(), req, order, budget)
						require.NoError(t, err)
					}
					if selected == nil {
						attempt := openAIAccountLoadSelectionAttempt{selectionOrder: order, candidateCount: len(order), topK: plan.topK}
						selected, _, _, _, err = s.finishLoadBalanceSelectionFallback(context.Background(), req, attempt, budget, openAISelectionFilterStats{})
					}
					if alternate {
						require.NoError(t, err, "healthy alternate survives a late stop")
						require.NotNil(t, selected)
						require.Equal(t, int64(2), selected.Account.ID)
						if selected.ReleaseFunc != nil {
							selected.ReleaseFunc()
						}
					} else {
						require.Nil(t, selected)
						require.ErrorIs(t, err, ErrPriorityLatencyUnavailable, "late stop must retain dedicated temporary-unavailable cause")
						require.False(t, errors.Is(err, ErrNoAvailableAccounts), "late stop cannot authorize legacy/proxy fail-open fallback")
					}
					if stage == "after_slot" {
						require.Contains(t, releases, int64(1), "late rejection must release acquired slot")
					}
				})
			}
		}
	}
}

func TestPriorityPenaltyReviewFreshnessDeadline(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.LatencyPenaltyMode = PriorityLatencyStop
	bad := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, LatestBadLatencyAt: &bad})
	candidate := priorityCandidate(1, .1, 0)
	// The shared rolling mean, not a separate wall-clock timer, controls stop.
	visible := bad.Add(8 * time.Minute)
	score := scorePriorityCandidate(cfg, candidate, signal, visible)
	require.True(t, score.LatencyStopped)
	require.Nil(t, score.LatencyPenaltyUntil)
	later := scorePriorityCandidate(cfg, candidate, signal, bad.Add(10*time.Minute))
	require.True(t, later.LatencyStopped)
	require.False(t, later.LatencyRecovery)
	recovered := 4000.0
	signal.Performance.AvgTTFTMs = &recovered
	require.False(t, scorePriorityCandidate(cfg, candidate, signal, visible).LatencyStopped)
}
