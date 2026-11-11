//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"

	"github.com/stretchr/testify/require"
)

type latencyModeReader struct {
	UsageLogRepository
	mu        sync.Mutex
	allSlow   bool
	noSamples bool
}

func (r *latencyModeReader) ReadPrioritySchedulingSignals(_ context.Context, q PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]PrioritySchedulingSignal, len(q.AccountIDs))
	for _, id := range q.AccountIDs {
		signal := PrioritySchedulingSignal{}
		if q.Model == "public-model" && q.GroupID != nil && *q.GroupID == 11 && !r.noSamples {
			bad := time.Now().Add(-time.Minute)
			signal = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualitySamples: 10, QualityPassed: 10})
			if id == 1 || r.allSlow {
				signal.P90TTFTMs = 16000
				signal.LatestBadLatencyAt = &bad
			}
		}
		out[id] = withAccountPerformanceFixture(signal)
	}
	return out, nil
}
func latencyModeFixture(t *testing.T, mode string) (*OpenAIGatewayService, *defaultOpenAIAccountScheduler, OpenAIAccountScheduleRequest, []openAIAccountCandidateScore, *latencyModeReader) {
	t.Helper()
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.LatencyPenaltyMode = mode
	svc := priorityGateway(cfg, &priorityReaderStub{})
	reader := &latencyModeReader{}
	svc.usageLogRepo = reader
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0), priorityCandidate(2, 1, 0)}
	accounts := make([]Account, 0, 2)
	for _, item := range pool {
		item.account.Status = StatusActive
		item.account.Schedulable = true
		item.account.GroupIDs = []int64{11, 12}
		accounts = append(accounts, *item.account)
	}
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:mode-sticky": 1}}
	svc.cfg = newSchedulerTestOpenAIWSV2Config()
	svc.cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = false
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
	group := int64(11)
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: &group, RequestedModel: "gpt-test", HistoryModel: "public-model", InboundEndpoint: "/v1/responses", UseUpstreamTokenCost: true}
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, 2*time.Second, time.Millisecond)
	return svc, &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}, req, pool, reader
}
func modeSelect(t *testing.T, s *defaultOpenAIAccountScheduler, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, error) {
	t.Helper()
	selected, _, err := s.Select(context.Background(), req)
	if selected != nil && selected.ReleaseFunc != nil {
		t.Cleanup(selected.ReleaseFunc)
	}
	return selected, err
}
func setModeSignal(svc *OpenAIGatewayService, req OpenAIAccountScheduleRequest, id int64, change func(*PrioritySchedulingSignal)) {
	cfg := svc.prioritySchedulingRuntimeConfig()
	svc.priorityScheduling.mu.Lock()
	defer svc.priorityScheduling.mu.Unlock()
	entry := svc.priorityScheduling.entries[priorityHistoryKey(req, cfg)]
	signal := entry.signals[id]
	change(&signal)
	entry.signals[id] = withAccountPerformanceFixture(signal)
}
func TestPriorityLatencyModesDefaultAndSharedHistory(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	require.Equal(t, PriorityLatencyDeprioritize, cfg.LatencyPenaltyMode)
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	var old map[string]any
	require.NoError(t, json.Unmarshal(raw, &old))
	delete(old, "latency_penalty_mode")
	raw, err = json.Marshal(old)
	require.NoError(t, err)
	settings := &SettingService{settingRepo: &prioritySettingStub{value: string(raw)}}
	loaded, err := settings.GetPrioritySchedulingConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, PriorityLatencyDeprioritize, loaded.LatencyPenaltyMode)
	require.Equal(t, 10, loaded.LatencyPenaltyMinutes)
	req := OpenAIAccountScheduleRequest{RequestedModel: "route", HistoryModel: "public"}
	soft := priorityHistoryKey(req, cfg)
	cfg.LatencyPenaltyMode = PriorityLatencyStop
	require.Equal(t, soft, priorityHistoryKey(req, cfg), "changing only the action must retain measured evidence")
	cfg.LatencyPenaltyMode = "invented"
	require.Error(t, ValidatePrioritySchedulingConfig(cfg))
}
func TestPriorityLatencyModesPoolAndAllStopped(t *testing.T) {
	for _, mode := range []string{PriorityLatencyDeprioritize, PriorityLatencyStop} {
		t.Run(mode, func(t *testing.T) {
			svc, s, req, pool, _ := latencyModeFixture(t, mode)
			selected, err := modeSelect(t, s, req)
			require.NoError(t, err)
			require.Equal(t, int64(2), selected.Account.ID)
			req.ExcludedIDs = map[int64]struct{}{2: {}}
			selected, err = modeSelect(t, s, req)
			require.NoError(t, err)
			require.Equal(t, int64(1), selected.Account.ID, "only otherwise eligible account is the authorized fallback")
			require.NoError(t, svc.CheckPriorityLatencySelection(context.Background(), selected))
			require.True(t, pool[0].account.Schedulable)
			require.Equal(t, StatusActive, pool[0].account.Status)
			_ = svc
		})
	}
	t.Run("all slow including alternate", func(t *testing.T) {
		svc, s, req, _, _ := latencyModeFixture(t, PriorityLatencyStop)
		setModeSignal(svc, req, 2, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 16000; bad := time.Now(); v.LatestBadLatencyAt = &bad })
		selected, err := modeSelect(t, s, req)
		require.NoError(t, err)
		require.NotNil(t, selected)
		require.Contains(t, []int64{1, 2}, selected.Account.ID)
		require.NoError(t, svc.CheckPriorityLatencySelection(context.Background(), selected))
		snapshot := svc.PrioritySchedulingSnapshot()
		require.NotNil(t, snapshot)
		require.Len(t, snapshot.Candidates, 2)
		for _, candidate := range snapshot.Candidates {
			require.True(t, candidate.LatencyStopped)
		}
	})
}
func TestPriorityLatencyModesStickyFallbackAndWaitPlans(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		t.Run(map[bool]string{false: "session", true: "weighted"}[weighted], func(t *testing.T) {
			_, s, req, _, _ := latencyModeFixture(t, PriorityLatencyStop)
			req.SessionHash = "mode-sticky"
			req.StickyAccountID = 1
			req.StickyWeighted = weighted
			selected, err := modeSelect(t, s, req)
			require.NoError(t, err)
			require.Equal(t, int64(2), selected.Account.ID)
			req.StickyWeighted = true
			fallback, err := s.tryFallbackToWeightedSticky(context.Background(), req)
			require.NoError(t, err)
			require.Nil(t, fallback)
		})
	}
	t.Run("wait only on permitted account", func(t *testing.T) {
		svc, s, req, _, _ := latencyModeFixture(t, PriorityLatencyStop)
		svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: false, 2: false}})
		selected, err := modeSelect(t, s, req)
		require.NoError(t, err)
		require.NotNil(t, selected.WaitPlan)
		require.Equal(t, int64(2), selected.WaitPlan.AccountID)
		setModeSignal(svc, req, 2, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 16000; bad := time.Now(); v.LatestBadLatencyAt = &bad })
		setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 500; v.LatestBadLatencyAt = nil })
		require.ErrorIs(t, svc.CheckPriorityLatencySelection(context.Background(), selected), ErrPriorityLatencyUnavailable, "a queued selection must be rechecked after it gains its slot")
		selected, err = modeSelect(t, s, req)
		require.NoError(t, err, "a new request uses the healthy peer")
		require.NotNil(t, selected.WaitPlan)
		require.Equal(t, int64(1), selected.WaitPlan.AccountID)
		require.NoError(t, svc.CheckPriorityLatencySelection(context.Background(), selected))
	})
}
func TestPriorityLatencyModesScopeColdStartAndExpiry(t *testing.T) {
	svc, s, req, pool, _ := latencyModeFixture(t, PriorityLatencyStop)
	for _, scope := range []string{"model", "group", "nil group"} {
		t.Run(scope, func(t *testing.T) {
			other := req
			switch scope {
			case "model":
				other.HistoryModel = "different-public"
			case "group":
				id := int64(12)
				other.GroupID = &id
			case "nil group":
				other.GroupID = nil
			}
			cfg := svc.prioritySchedulingRuntimeConfig()
			require.Eventually(t, func() bool { return svc.priorityHistory(other, cfg, pool).ready() }, 3*time.Second, time.Millisecond)
			require.NoError(t, svc.priorityLatencyAdmission(other, pool[0].account), "matching account is not stopped in an unrelated scope")
		})
	}
	req.ExcludedIDs = map[int64]struct{}{2: {}}
	setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) {
		expired := time.Now().Add(-10*time.Minute - time.Second)
		v.LatestBadLatencyAt = &expired
	})
	selected, err := modeSelect(t, s, req)
	require.NoError(t, err)
	require.Equal(t, int64(1), selected.Account.ID)
	setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) { bad := time.Now(); v.LatestBadLatencyAt = &bad; v.Samples = 4 })
	require.NoError(t, svc.priorityLatencyAdmission(req, pool[0].account), "minimum samples are unchanged; unknown/insufficient is not a measured stop")
	cold := req
	cold.HistoryModel = "not loaded"
	err = svc.priorityLatencyAdmission(cold, pool[0].account)
	require.NoError(t, err, "pending history must not fabricate a measured stop")
	require.False(t, svc.cachedPriorityHistory(cold, svc.prioritySchedulingRuntimeConfig(), pool).ready())
}
func TestPriorityLatencyModesBoundOwnership(t *testing.T) {
	svc, s, req, _, _ := latencyModeFixture(t, PriorityLatencyStop)
	require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(context.Background(), 11, "resp_stopped", 1, time.Hour))
	for _, movable := range []bool{false, true} {
		bound := req
		bound.PreviousResponseID = "resp_stopped"
		bound.PreviousResponseCanMove = movable
		bound.StickyWeighted = movable
		selected, err := modeSelect(t, s, bound)
		require.Nil(t, selected)
		require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
		svc.invalidateOpenAIWSTurnStateAfterAdmissionFailure(context.Background(), 11, "mode-sticky", "resp_stopped", 1, err)
		require.Equal(t, int64(1), svc.ResolveAccountIDByPreviousResponseIDForScheduler(context.Background(), req.GroupID, "resp_stopped", req.RequestedModel, nil, req.RequiredCapability, false), "penalty must not delete provider state")
	}
	excludedOwner := req
	excludedOwner.PreviousResponseID = "resp_stopped"
	excludedOwner.ExcludedIDs = map[int64]struct{}{1: {}}
	excludedOwner.StickyWeighted = true
	excludedOwner.PreviousResponseCanMove = true
	selected, err := modeSelect(t, s, excludedOwner)
	require.Nil(t, selected)
	require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
	for _, flag := range []string{"guardian", "disable", "preserve", "encrypted"} {
		owned := req
		owned.SessionHash = "mode-sticky"
		owned.StickyAccountID = 1
		switch flag {
		case "guardian":
			owned.GuardianParentAccountID = 1
		case "disable":
			owned.DisableStickyEscape = true
		case "preserve":
			owned.PreserveStickyBinding = true
		case "encrypted":
			owned.EncryptedReasoning = true
		}
		selected, err := modeSelect(t, s, owned)
		require.Nil(t, selected)
		require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
	}
}

type latencyInFlightBody struct {
	io.Reader
	once  sync.Once
	start func()
}

func (b *latencyInFlightBody) Read(p []byte) (int, error) {
	b.once.Do(b.start)
	return b.Reader.Read(p)
}
func (b *latencyInFlightBody) Close() error { return nil }
func TestPriorityLatencyModesInFlightBoundary(t *testing.T) {
	wire := `{"id":"resp_ok","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`
	upstream := &httpUpstreamRecorder{resp: priorityMockResponse(200, wire, false)}
	svc := newOpenAIRejectedFieldTestService(upstream)
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.LatencyPenaltyMode = PriorityLatencyStop
	svc.settingService = priorityGateway(cfg, &priorityReaderStub{}).settingService
	reader := &latencyModeReader{noSamples: true}
	svc.usageLogRepo = reader
	account := newOpenAIRejectedFieldTestAccount()
	account.ID = 1
	group := int64(11)
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: &group, RequestedModel: "gpt-5.5", HistoryModel: "public-model", InboundEndpoint: "/v1/responses", UseUpstreamTokenCost: true}
	pool := []openAIAccountCandidateScore{{account: account}}
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, time.Second, time.Millisecond)
	upstream.resp.Body = &latencyInFlightBody{Reader: strings.NewReader(wire), start: func() {
		setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) {
			bad := time.Now()
			*v = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, LatestBadLatencyAt: &bad})
		})
	}}
	ctx := WithPrioritySchedulingObservation(context.Background(), &group, "public-model", "/v1/responses")
	body := []byte(`{"model":"gpt-5.5","input":"mock","stream":false}`)
	result, err := svc.Forward(ctx, newOpenAIRejectedFieldTestContext(body), account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 1, "in-flight response must finish without replay")
	_, err = svc.Forward(ctx, newOpenAIRejectedFieldTestContext(body), account, body)
	require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
	require.Len(t, upstream.requests, 1, "next turn must be stopped before any transport")
	// The admission is local; it must not authorize initial-binding replay.
	require.False(t, IsOpenAIInitialAdmissionRejection(err))
	require.True(t, IsOpenAITurnAdmissionError(err))
	require.False(t, errors.Is(err, ErrNoAvailableAccounts), "proxy fail-open must not bypass a strict penalty")
}

func TestPriorityLatencyModesFeatureScopeAndUnmeasured(t *testing.T) {
	svc, _, req, pool, _ := latencyModeFixture(t, PriorityLatencyStop)
	for _, change := range []string{"disabled", "group scope", "model scope", "image", "non token"} {
		t.Run(change, func(t *testing.T) {
			cfg := svc.prioritySchedulingRuntimeConfig()
			altered := req
			switch change {
			case "disabled":
				cfg.Enabled = false
			case "group scope":
				cfg.GroupIDs = []int64{12}
			case "model scope":
				cfg.Models = []string{"unrelated"}
			case "image":
				altered.RequiredImageCapability = OpenAIImagesCapabilityNative
			case "non token":
				altered.UseUpstreamTokenCost = false
			}
			old := svc.prioritySchedulingRuntimeConfig()
			require.NoError(t, svc.settingService.SavePrioritySchedulingConfig(context.Background(), cfg))
			require.NoError(t, svc.priorityLatencyAdmission(altered, pool[0].account))
			require.NoError(t, svc.settingService.SavePrioritySchedulingConfig(context.Background(), old))
		})
	}
	setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) {
		*v = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 0, LatencyUnknownSamples: 20})
	})
	require.NoError(t, svc.priorityLatencyAdmission(req, pool[0].account), "unknown timing must not become a measured hard stop")
}

type latencyStreamBody struct {
	chunks     []string
	reads      int
	afterFirst func()
}

func (b *latencyStreamBody) Read(p []byte) (int, error) {
	if b.reads == 1 && b.afterFirst != nil {
		b.afterFirst()
		b.afterFirst = nil
	}
	if len(b.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.chunks[0])
	b.chunks[0] = b.chunks[0][n:]
	if len(b.chunks[0]) == 0 {
		b.chunks = b.chunks[1:]
		b.reads++
	}
	return n, nil
}
func (b *latencyStreamBody) Close() error { return nil }
func TestPriorityLatencyModesStreamOutputContinues(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: priorityMockResponse(200, "", true)}
	svc := newOpenAIRejectedFieldTestService(upstream)
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.LatencyPenaltyMode = PriorityLatencyStop
	svc.settingService = priorityGateway(cfg, &priorityReaderStub{}).settingService
	svc.usageLogRepo = &latencyModeReader{noSamples: true}
	account := newOpenAIRejectedFieldTestAccount()
	account.ID = 1
	group := int64(11)
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, GroupID: &group, RequestedModel: "gpt-5.5", HistoryModel: "public-model", InboundEndpoint: "/v1/responses", UseUpstreamTokenCost: true}
	require.Eventually(t, func() bool {
		return svc.priorityHistory(req, cfg, []openAIAccountCandidateScore{{account: account}}).ready()
	}, time.Second, time.Millisecond)
	body := []byte(`{"model":"gpt-5.5","input":"mock","stream":true}`)
	c := newOpenAIRejectedFieldTestContext(body)
	upstream.resp.Body = &latencyStreamBody{chunks: []string{
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
	}, afterFirst: func() {
		require.True(t, c.Writer.Written(), "activate the penalty after client output has started")
		setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) {
			bad := time.Now()
			*v = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, LatestBadLatencyAt: &bad})
		})
	}}
	ctx := WithPrioritySchedulingObservation(context.Background(), &group, "public-model", "/v1/responses")
	result, err := svc.Forward(ctx, c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Len(t, upstream.requests, 1)
	_, err = svc.Forward(ctx, newOpenAIRejectedFieldTestContext(body), account, body)
	require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
	require.Len(t, upstream.requests, 1, "new turn is rejected without replay of previous output")
}

func TestPriorityLatencyModesWSTurnRoutingIdentity(t *testing.T) {
	svc, _, req, pool, _ := latencyModeFixture(t, PriorityLatencyStop)
	cfg := svc.prioritySchedulingRuntimeConfig()
	cfg.Models = []string{req.RequestedModel}
	require.NoError(t, svc.settingService.SavePrioritySchedulingConfig(context.Background(), cfg))
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, 2*time.Second, time.Millisecond)
	ctx := WithPrioritySchedulingObservation(context.Background(), req.GroupID, req.HistoryModel, "/v1/responses")
	ctx = WithOpenAIForwardModel(ctx, req.RequestedModel, false)
	c := newOpenAIRejectedFieldTestContext([]byte(`{"model":"account-upstream"}`))
	c.Request = c.Request.WithContext(ctx)
	c.Request.Header.Set("Upgrade", "websocket")
	_, err := svc.beginPriorityLatencyTurn(ctx, c, pool[0].account, "account-upstream")
	require.ErrorIs(t, err, ErrPriorityLatencyUnavailable, "legacy routing-model configuration must still stop the public model after account mapping")
	var denied *OpenAITurnAdmissionError
	require.ErrorAs(t, err, &denied)
	require.Equal(t, "latency_penalty", denied.Reason, "use loaded route/public evidence, not a new upstream-model cache scope")
}

func TestPriorityLatencyModesPassthroughNewTurnAndOutput(t *testing.T) {
	for _, initialStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "after output", true: "initial"}[initialStop], func(t *testing.T) {
			control, cancel := context.WithCancel(context.Background())
			defer cancel()
			control = WithPrioritySchedulingObservation(control, nil, "public-model", "/v1/responses")
			control = WithOpenAIForwardModel(control, "gpt-5.1", false)
			upstream := newStagedPassthroughConn()
			svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
			cfg := DefaultPrioritySchedulingConfig()
			cfg.Enabled = true
			cfg.LatencyPenaltyMode = PriorityLatencyStop
			svc.settingService = priorityGateway(cfg, &priorityReaderStub{}).settingService
			svc.usageLogRepo = &latencyModeReader{noSamples: true}
			account := passthroughLifecycleAccount()
			req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-5.1", HistoryModel: "public-model", InboundEndpoint: "/v1/responses", UseUpstreamTokenCost: true}
			require.Eventually(t, func() bool {
				return svc.priorityHistory(req, cfg, []openAIAccountCandidateScore{{account: account}}).ready()
			}, time.Second, time.Millisecond)
			stop := func() {
				setModeSignal(svc, req, account.ID, func(v *PrioritySchedulingSignal) {
					bad := time.Now()
					*v = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, LatestBadLatencyAt: &bad})
				})
			}
			if initialStop {
				stop()
			} else {
				// Raw passthrough reports the terminal; response ownership is
				// supplied by its caller. Seed that explicit precondition so
				// the rejection test verifies preservation, not auto-binding.
				require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(control, 0, "resp_mode_stream", account.ID, time.Hour))
			}
			server, done := startPassthroughHookRecordingServer(t, control, svc, account, nil)
			defer server.Close()
			client := dialPassthroughLifecycleClient(t, server)
			defer func() { _ = client.CloseNow() }()
			if !initialStop {
				requirePassthroughUpstreamWrite(t, upstream, time.Second)
				upstream.Send(`{"type":"response.output_text.delta","response_id":"resp_mode_stream","delta":"first"}`)
				delta, err := readPassthroughLifecycleFrame(t, client, time.Second)
				require.NoError(t, err)
				require.Contains(t, string(delta), "first")
				stop()
				upstream.Send(`{"type":"response.completed","response":{"id":"resp_mode_stream","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
				terminal, err := readPassthroughLifecycleFrame(t, client, time.Second)
				require.NoError(t, err)
				require.Contains(t, string(terminal), "response.completed", "an active penalty must not abort already emitted output")
				require.Eventually(t, func() bool {
					id, _ := svc.getOpenAIWSStateStore().GetResponseAccount(control, 0, "resp_mode_stream")
					return id == account.ID
				}, time.Second, time.Millisecond)
				writeCtx, endWrite := context.WithTimeout(control, time.Second)
				err = client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_mode_stream"}`))
				endWrite()
				require.NoError(t, err)
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
			case <-time.After(3 * time.Second):
				t.Fatal("stopped passthrough turn did not fail safely")
			}
			require.Empty(t, upstream.writes, "stopped generation must never be written to the provider")
			if !initialStop {
				id, err := svc.getOpenAIWSStateStore().GetResponseAccount(control, 0, "resp_mode_stream")
				require.NoError(t, err)
				require.Equal(t, account.ID, id, "temporary stop must retain provider response ownership")
			}
		})
	}
}
