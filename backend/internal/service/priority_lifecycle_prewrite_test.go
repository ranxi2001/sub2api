//go:build unit

package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type priorityLifecycleBlockingSignals struct {
	UsageLogRepository
	release chan struct{}
}

func (r *priorityLifecycleBlockingSignals) ReadPrioritySchedulingSignals(ctx context.Context, q PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	select {
	case <-r.release:
		return map[int64]PrioritySchedulingSignal{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestPriorityLifecycleAdminGETReadOnly(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	svc := priorityGateway(cfg, &priorityReaderStub{})
	reader := &priorityLifecycleBlockingSignals{release: make(chan struct{})}
	defer close(reader.release)
	svc.usageLogRepo = reader
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "synthetic", UseUpstreamTokenCost: true}
	svc.priorityScheduling.latestInput = newPrioritySnapshotInput(req, cfg, []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}, time.Now())
	require.NotNil(t, svc.PrioritySchedulingSnapshot())
	svc.priorityScheduling.mu.Lock()
	defer svc.priorityScheduling.mu.Unlock()
	if svc.priorityScheduling.active != 0 || len(svc.priorityScheduling.entries) != 0 {
		t.Fatal("LIFECYCLE_GET: management read launched or mutated routing history")
	}
}

// A loopback WS enters the production proxy and relay; only the provider socket
// is in-memory. The observation clock publishes a stop after frame admission,
// at the relay activity boundary before the physical write. Deadlines stay real.
func TestPriorityLifecyclePassthroughPhysicalWriteFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ctx = WithPrioritySchedulingObservation(ctx, nil, "public-model", "/v1/responses")
	ctx = WithOpenAIForwardModel(ctx, "gpt-5.1", false)
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
	var armed, published atomic.Bool
	ctx = requesttiming.WithClock(ctx, func() time.Time {
		now := time.Now()
		if armed.CompareAndSwap(true, false) {
			setModeSignal(svc, req, account.ID, func(v *PrioritySchedulingSignal) {
				*v = PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, LatestBadLatencyAt: &now}
			})
			published.Store(true)
		}
		return now
	})
	hooks := &OpenAIWSIngressHooks{BeforeTurn: func(turn int) error {
		if turn == 2 {
			armed.Store(true)
		}
		return nil
	}}
	server, done := startPassthroughHookRecordingServer(t, ctx, svc, account, hooks)
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send("{\"type\":\"response.output_text.delta\",\"delta\":\"already submitted\"}")
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	upstream.Send("{\"type\":\"response.completed\",\"response\":{\"id\":\"resp_lifecycle\",\"model\":\"gpt-5.1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}")
	_, err = readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err, "submitted output must drain normally")
	state := svc.getOpenAIWSStateStore()
	require.NoError(t, state.BindResponseAccount(ctx, 0, "resp_lifecycle", account.ID, time.Hour))
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte("{\"type\":\"response.create\",\"model\":\"gpt-5.1\",\"previous_response_id\":\"resp_lifecycle\"}")))
	select {
	case <-upstream.writes:
		require.True(t, published.Load(), "fixture must publish a stop at the write boundary")
		t.Fatal("LIFECYCLE_PREWRITE: stopped follow-up generation reached physical upstream write")
	case err := <-done:
		require.True(t, published.Load(), "fixture must publish a stop at the write boundary")
		require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
	case <-ctx.Done():
		t.Fatal("physical write fence did not settle")
	}
	require.Empty(t, upstream.writes, "blocked generation writes must be zero; no replay")
	owner, err := state.GetResponseAccount(ctx, 0, "resp_lifecycle")
	require.NoError(t, err)
	require.Equal(t, account.ID, owner, "temporary stop retains continuation ownership")
}
