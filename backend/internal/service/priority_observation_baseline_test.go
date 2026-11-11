//go:build unit

package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This test file also compiles against the complete unmodified production
// baseline. Reflection supplies additive identity fields when the schema has
// them; expected behavior and assertions are identical on both revisions.
func baselineObservationIdentity(req *OpenAIAccountScheduleRequest, public, endpoint string) {
	value := reflect.ValueOf(req).Elem()
	for key, text := range map[string]string{"HistoryModel": public, "InboundEndpoint": endpoint} {
		if field := value.FieldByName(key); field.IsValid() && field.CanSet() {
			field.SetString(text)
		}
	}
}

func TestPriorityObservationBaselineEndpoint(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"mock","stream":false}`)
	svc := newOpenAIRejectedFieldTestService(&httpUpstreamRecorder{resp: priorityMockResponse(200, `{"id":"resp_mock","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`, false)})
	account := newOpenAIRejectedFieldTestAccount()
	_, err := svc.Forward(context.Background(), newOpenAIRejectedFieldTestContext(body), account, body)
	require.NoError(t, err)
	req := OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.5", RequiredCapability: OpenAIEndpointCapabilityChatCompletions}
	baselineObservationIdentity(&req, "gpt-5.5", "/v1/responses")
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: svc.openaiAccountStats}
	got := scheduler.priorityErrorEvidence(req, DefaultPrioritySchedulingConfig(), account.ID, time.Now())
	require.Equal(t, int64(1), got.Successes, "actual Responses ingress must survive CC capability fallback")
}

type baselineObservationReader struct {
	UsageLogRepository
	queries chan PrioritySchedulingQuery
}

func (r *baselineObservationReader) ReadPrioritySchedulingSignals(_ context.Context, q PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error) {
	r.queries <- q
	out := make(map[int64]PrioritySchedulingSignal, len(q.AccountIDs))
	for _, id := range q.AccountIDs {
		out[id] = PrioritySchedulingSignal{}
	}
	return out, nil
}
func TestPriorityObservationBaselineHistory(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	reader := &baselineObservationReader{queries: make(chan PrioritySchedulingQuery, 2)}
	svc := &OpenAIGatewayService{usageLogRepo: reader}
	req := OpenAIAccountScheduleRequest{RequestedModel: "channel-route"}
	baselineObservationIdentity(&req, "public-alias", "/v1/responses")
	svc.priorityHistory(req, cfg, []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)})
	select {
	case query := <-reader.queries:
		require.Equal(t, "public-alias", query.Model, "persisted requested_model must be read with the public identity")
	case <-time.After(time.Second):
		t.Fatal("history query did not start")
	}
}
func TestPriorityObservationBaselineFreshness(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	reader := &baselineObservationReader{queries: make(chan PrioritySchedulingQuery, 2)}
	svc := &OpenAIGatewayService{usageLogRepo: reader}
	req := OpenAIAccountScheduleRequest{RequestedModel: "gpt-test"}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0)}
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, time.Second, time.Millisecond)
	svc.priorityScheduling.mu.Lock()
	defer svc.priorityScheduling.mu.Unlock()
	entry := svc.priorityScheduling.entries[priorityHistoryKey(req, cfg)]
	require.LessOrEqual(t, entry.expires.Sub(entry.observed), 5*time.Second, "an empty completed read must not hide new-account persistence for 30 seconds")
}
func TestPriorityObservationBaselineSticky(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	bad := time.Now().Add(-time.Minute)
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 20000, LatestBadLatencyAt: &bad, QualitySamples: 10, QualityPassed: 10}), 2: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualitySamples: 10, QualityPassed: 10})}}
	svc := priorityGateway(cfg, reader)
	pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0), priorityCandidate(2, 1, 0)}
	accounts := make([]Account, 0, 2)
	for _, item := range pool {
		item.account.Status = StatusActive
		item.account.Schedulable = true
		accounts = append(accounts, *item.account)
	}
	svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:baseline-sticky": 1}}
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	svc.cfg = newSchedulerTestOpenAIWSV2Config()
	svc.cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = false
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test", UseUpstreamTokenCost: true, SessionHash: "baseline-sticky", StickyAccountID: 1}
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, time.Second, time.Millisecond)
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
	selected, _, err := scheduler.Select(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, selected)
	if selected.ReleaseFunc != nil {
		defer selected.ReleaseFunc()
	}
	require.Equal(t, int64(2), selected.Account.ID, "ordinary sticky cannot bypass a current measured penalty")
}

// Same behavioral assertion also runs on the complete immutable old checkout.
// Missing action schema on old source must yield a behavioral failure, not a
// compilation failure or a fabricated new penalty threshold.
func TestPriorityObservationBaselineHardStop(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	field := reflect.ValueOf(&cfg).Elem().FieldByName("LatencyPenaltyMode")
	if field.IsValid() && field.CanSet() {
		field.SetString("stop")
	}
	bad := time.Now().Add(-time.Minute)
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, LatestBadLatencyAt: &bad})}}
	svc := priorityGateway(cfg, reader)
	candidate := priorityCandidate(1, .1, 0)
	candidate.account.Status = StatusActive
	candidate.account.Schedulable = true
	peer := priorityCandidate(2, 1, 0)
	peer.account.Status = StatusActive
	peer.account.Schedulable = true
	reader.signal[2] = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500})
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*candidate.account, *peer.account}}
	svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:baseline-hard": 1}}
	svc.cfg = newSchedulerTestOpenAIWSV2Config()
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test", UseUpstreamTokenCost: true, SessionHash: "baseline-hard", StickyAccountID: 1, DisableStickyEscape: true}
	require.Eventually(t, func() bool {
		return svc.priorityHistory(req, cfg, []openAIAccountCandidateScore{candidate, peer}).ready()
	}, time.Second, time.Millisecond)
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
	selected, _, err := scheduler.Select(context.Background(), req)
	if selected != nil && selected.ReleaseFunc != nil {
		defer selected.ReleaseFunc()
	}
	require.Nil(t, selected, "all matching accounts are stopped; prohibited capacity must never be used")
	require.Error(t, err)
}
