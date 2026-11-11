//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise real selection, sticky/ownership, slot waits and forwarding guards.
// These regressions also run unmodified on 9620d315 in the remote RED checkout.
func availabilityFixture(t *testing.T, latencies ...float64) (*OpenAIGatewayService, *defaultOpenAIAccountScheduler, OpenAIAccountScheduleRequest) {
	t.Helper()
	svc, scheduler, req, _, _ := latencyModeFixture(t, PriorityLatencyStop)
	accounts := make([]Account, len(latencies))
	pool := make([]openAIAccountCandidateScore, len(latencies))
	signals := make(map[int64]PrioritySchedulingSignal, len(latencies))
	bad := time.Now().Add(-time.Minute)
	for i, latency := range latencies {
		id := int64(i + 1)
		pool[i] = priorityCandidate(id, .1, 0)
		account := pool[i].account
		account.Status, account.Schedulable = StatusActive, true
		account.GroupIDs = []int64{11}
		accounts[i] = *account
		signals[id] = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: latency, LatestBadLatencyAt: &bad, QualitySamples: 10, QualityPassed: 10})
	}
	svc.accountRepo = schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}
	svc.usageLogRepo = &priorityReaderStub{signal: signals}
	svc.priorityScheduling.mu.Lock()
	svc.priorityScheduling.entries = nil
	svc.priorityScheduling.launches = [2]time.Time{}
	svc.priorityScheduling.mu.Unlock()
	cfg := svc.prioritySchedulingRuntimeConfig()
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, 3*time.Second, time.Millisecond)
	return svc, scheduler, req
}

func TestPriorityLatencyAvailabilityPoolBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		latencies []float64
		allowed   int
	}{
		{"only one", []float64{16000}, 1},
		{"minimum two", []float64{16000, 17000, 18000, 19000, 20000, 21000}, 2},
		{"lowest band three", []float64{16000, 16000, 16000, 19000, 20000, 21000}, 3},
		{"maximum five", []float64{16000, 16000, 16000, 16000, 16000, 16000, 16000}, 5},
		{"healthy wins", []float64{16000, 17000, 500, 19000, 20000, 21000}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, scheduler, req := availabilityFixture(t, tc.latencies...)
			var acquired []int64
			results := map[int64]bool{}
			for i := range tc.latencies {
				results[int64(i+1)] = false
			}
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: results, acquiredIDs: &acquired})
			selected, err := modeSelect(t, scheduler, req)
			require.NoError(t, err, "all stopped must open a bounded availability pool")
			require.NotNil(t, selected)
			require.NotNil(t, selected.WaitPlan)
			got := map[int64]bool{}
			for _, id := range acquired {
				got[id] = true
			}
			require.Len(t, got, tc.allowed, "slot probes must not expand the admitted pool")
			if tc.name == "healthy wins" {
				require.Equal(t, map[int64]bool{3: true}, got)
			} else {
				for i := 1; i <= tc.allowed; i++ {
					require.True(t, got[int64(i)], "only lowest latency ranks can enter the fallback")
				}
			}
			require.NoError(t, svc.CheckPriorityLatencySelection(context.Background(), selected), "post-slot guard must honor exactly the selected fallback")
			ctx := ContextWithSelectionPriorityRequest(context.Background(), selected)
			_, err = svc.beginPriorityLatencyTurn(ctx, nil, selected.Account, req.RequestedModel)
			require.NoError(t, err, "pre-write guard must honor the same fallback")
			if tc.name != "healthy wins" {
				snapshot := svc.PrioritySchedulingSnapshot()
				require.NotNil(t, snapshot)
				for _, score := range snapshot.Candidates {
					require.True(t, score.LatencyStopped)
					require.Nil(t, score.LatencyPenaltyUntil, "mean-based admission has no independent timer")
				}
			}
		})
	}
}

func TestPriorityLatencyAvailabilityHistoryDegrades(t *testing.T) {
	for _, state := range []string{"pending", "error", "unavailable", "unknown after measured"} {
		t.Run(state, func(t *testing.T) {
			svc, scheduler, req := availabilityFixture(t, 16000, 17000, 18000, 19000, 20000, 21000)
			svc.priorityScheduling.mu.Lock()
			entry := svc.priorityScheduling.entries[priorityHistoryKey(req, svc.prioritySchedulingRuntimeConfig())]
			if state == "unknown after measured" {
				delete(entry.signals, 1)
			} else {
				entry.signals = nil
				entry.observed = time.Time{}
				entry.expires = time.Now().Add(time.Hour)
				if state == "error" {
					entry.failure = "query_failed"
				}
			}
			svc.priorityScheduling.mu.Unlock()
			if state == "unavailable" {
				svc.usageLogRepo = nil
			}
			selected, err := modeSelect(t, scheduler, req)
			require.NoError(t, err, "history state alone must not cause a pool-wide 503")
			require.NotNil(t, selected)
			require.NoError(t, svc.CheckPriorityLatencySelection(context.Background(), selected))
			if state == "unknown after measured" {
				require.Contains(t, []int64{2, 3}, selected.Account.ID, "unknown must not rank as measured fastest")
			}
			snapshot := svc.PrioritySchedulingSnapshot()
			require.NotNil(t, snapshot)
			if state != "unknown after measured" {
				require.False(t, snapshot.HistoryReady)
				for _, score := range snapshot.Candidates {
					require.Zero(t, score.Samples)
					require.Nil(t, score.SelectedTTFTMs)
					require.False(t, score.LatencyStopped)
				}
			}
		})
	}
}

func TestPriorityLatencyAvailabilityBoundContinuation(t *testing.T) {
	svc, scheduler, req := availabilityFixture(t, 16000, 17000, 18000, 19000, 20000, 21000)
	for _, owner := range []int64{1, 6} {
		id := "resp_availability_" + string(rune('a'+owner))
		require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(context.Background(), 11, id, owner, time.Hour))
		for _, movable := range []bool{false, true} {
			bound := req
			bound.PreviousResponseID, bound.PreviousResponseCanMove, bound.StickyWeighted = id, movable, movable
			selected, err := modeSelect(t, scheduler, bound)
			if owner == 1 {
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.Equal(t, owner, selected.Account.ID)
			} else {
				require.Nil(t, selected)
				require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
			}
			got, err := svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 11, id)
			require.NoError(t, err)
			require.Equal(t, owner, got, "never delete ownership to evade latency admission")
		}
	}
}

func TestPriorityLatencyAvailabilityEligibilityAndScope(t *testing.T) {
	for _, gate := range []string{"disabled", "wrong group", "model", "privacy", "excluded"} {
		t.Run(gate, func(t *testing.T) {
			svc, scheduler, req := availabilityFixture(t, 16000, 17000, 500)
			repo := svc.accountRepo.(schedulerGroupAwareOpenAIAccountRepo)
			switch gate {
			case "disabled":
				repo.accounts[2].Schedulable = false
			case "wrong group":
				repo.accounts[2].GroupIDs = []int64{12}
			case "model":
				repo.accounts[2].Credentials = map[string]any{"model_mapping": map[string]any{"only-other-model": "other"}}
			case "privacy":
				req.RequirePrivacySet = true
				repo.accounts[0].Extra["privacy_mode"] = PrivacyModeTrainingOff
				repo.accounts[1].Extra["privacy_mode"] = PrivacyModeTrainingOff
			case "excluded":
				req.ExcludedIDs = map[int64]struct{}{3: {}}
			}
			svc.accountRepo = repo
			selected, err := modeSelect(t, scheduler, req)
			require.NoError(t, err, "ineligible healthy account must not hide authorized fallback")
			require.NotNil(t, selected)
			require.Contains(t, []int64{1, 2}, selected.Account.ID)
		})
	}
	_, scheduler, req := availabilityFixture(t, 16000, 17000, 18000)
	selected, err := modeSelect(t, scheduler, req)
	require.NoError(t, err)
	require.NotNil(t, selected)
	other := req
	group := int64(12)
	other.GroupID = &group
	selected, err = modeSelect(t, scheduler, other)
	require.Nil(t, selected)
	require.Error(t, err, "fallback must not import accounts across groups")
	require.False(t, errors.Is(err, context.Canceled))
}

func TestPriorityLatencyAvailabilityFrozenScope(t *testing.T) {
	svc, scheduler, req := availabilityFixture(t, 16000, 17000, 18000, 19000, 20000, 21000)
	req.SessionHash = "mode-sticky"
	req.StickyAccountID = 1
	selected, err := modeSelect(t, scheduler, req)
	require.NoError(t, err)
	require.NotNil(t, selected)
	cache := svc.cache.(*schedulerTestGatewayCache)
	require.Empty(t, cache.deletedSessions, "latency fallback must preserve sticky state")
	other, err := svc.accountRepo.GetByID(context.Background(), 6)
	require.NoError(t, err)
	forged := *selected
	forged.Account = other
	require.ErrorIs(t, svc.CheckPriorityLatencySelection(context.Background(), &forged), ErrPriorityLatencyUnavailable, "later guard cannot add a sixth candidate")
	setModeSignal(svc, req, 6, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 15001 })
	require.ErrorIs(t, svc.CheckPriorityLatencySelection(context.Background(), &forged), ErrPriorityLatencyUnavailable, "new ranking cannot expand this request's frozen set")
	for _, flag := range []string{"guardian", "disable", "preserve", "encrypted"} {
		bound := req
		bound.StickyAccountID = 6
		cache.sessionBindings["openai:mode-sticky"] = 6
		switch flag {
		case "guardian":
			bound.GuardianParentAccountID = 6
		case "disable":
			bound.DisableStickyEscape = true
		case "preserve":
			bound.PreserveStickyBinding = true
		case "encrypted":
			bound.EncryptedReasoning = true
		}
		// Put owner outside the lowest two before starting a new request.
		setModeSignal(svc, req, 6, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 21000 })
		got, err := modeSelect(t, scheduler, bound)
		require.Nil(t, got)
		require.ErrorIs(t, err, ErrPriorityLatencyUnavailable, flag)
	}
}

func TestPriorityLatencyAvailabilitySafetyNoWaiver(t *testing.T) {
	for _, gate := range []string{"admin disabled", "auth error", "rate limited", "overloaded", "excluded", "privacy", "quota"} {
		t.Run(gate, func(t *testing.T) {
			svc, scheduler, req := availabilityFixture(t, 16000)
			repo := svc.accountRepo.(schedulerGroupAwareOpenAIAccountRepo)
			until := time.Now().Add(time.Hour)
			switch gate {
			case "admin disabled":
				repo.accounts[0].Schedulable = false
			case "auth error":
				repo.accounts[0].Status = StatusError
			case "rate limited":
				repo.accounts[0].RateLimitResetAt = &until
			case "overloaded":
				repo.accounts[0].OverloadUntil = &until
			case "excluded":
				req.ExcludedIDs = map[int64]struct{}{1: {}}
			case "privacy":
				req.RequirePrivacySet = true
			case "quota":
				repo.accounts[0].Type = AccountTypeOAuth
				repo.accounts[0].Extra["codex_7d_used_percent"] = 95.0
				repo.accounts[0].Extra["codex_7d_reset_at"] = until.Format(time.RFC3339)
				repo.accounts[0].Extra["codex_usage_updated_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
			}
			svc.accountRepo = repo
			ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold7d: 0.9})
			selected, _, err := scheduler.Select(ctx, req)
			require.Nil(t, selected)
			require.Error(t, err, "availability must never waive "+gate)
		})
	}
}

func TestPriorityLatencyAvailabilityNativeFallback(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModeShared, OpenAIWSIngressModeDedicated} {
		t.Run(mode, func(t *testing.T) {
			svc, req, account, upstream, dialer, ctx := reviewNativeFixture(t, mode)
			var accounts []Account
			signals := map[int64]PrioritySchedulingSignal{}
			pool := []openAIAccountCandidateScore{}
			bad := time.Now().Add(-time.Minute)
			for id := int64(1); id <= 6; id++ {
				a := *account
				a.ID = id
				accounts = append(accounts, a)
				pool = append(pool, openAIAccountCandidateScore{account: &accounts[len(accounts)-1]})
				signals[id] = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 15000 + float64(id)*1000, LatestBadLatencyAt: &bad})
			}
			svc.accountRepo = schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}
			svc.usageLogRepo = &priorityReaderStub{signal: signals}
			svc.priorityScheduling.mu.Lock()
			svc.priorityScheduling.entries = nil
			svc.priorityScheduling.launches = [2]time.Time{}
			svc.priorityScheduling.mu.Unlock()
			cfg := svc.prioritySchedulingRuntimeConfig()
			require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, time.Second, time.Millisecond)
			close(dialer.release)
			client, done := reviewNativeClient(t, ctx, svc, req, account)
			requirePassthroughUpstreamWrite(t, upstream.stagedPassthroughConn, time.Second)
			upstream.Send(`{"type":"response.output_text.delta","delta":"fallback started"}`)
			delta, err := readPassthroughLifecycleFrame(t, client, time.Second)
			require.NoError(t, err)
			require.Contains(t, string(delta), "fallback started")
			// A late healthy alternative may restrict the next turn, never this output.
			setModeSignal(svc, req, 6, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 500; v.LatestBadLatencyAt = nil })
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_fallback_native","model":"gpt-test","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
			terminal, err := readPassthroughLifecycleFrame(t, client, time.Second)
			require.NoError(t, err)
			require.Contains(t, string(terminal), "response.completed")
			require.Empty(t, upstream.writes, "admitted fallback output must complete without replay")
			_ = client.Close(1000, "done")
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("native fallback cleanup timed out")
			}
		})
	}
}

func TestPriorityLatencyAvailabilityConfiguredMetric(t *testing.T) {
	svc, scheduler, req := availabilityFixture(t, 16000, 17000, 18000, 19000, 20000, 21000)
	cfg := svc.prioritySchedulingRuntimeConfig()
	cfg.LatencyPercentile = 95
	cfg.LatencyMeasurement = "attributed_v1"
	require.NoError(t, svc.settingService.SavePrioritySchedulingConfig(context.Background(), cfg))
	bad := time.Now().Add(-time.Minute)
	signals := map[int64]PrioritySchedulingSignal{}
	pool := []openAIAccountCandidateScore{}
	repo := svc.accountRepo.(schedulerGroupAwareOpenAIAccountRepo)
	for i := range repo.accounts {
		value := 21000 - float64(i)*1000
		id := repo.accounts[i].ID
		signals[id] = withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: float64(i + 1), SelectedTTFTMs: &value, LatencyPercentile: 95, LatencyMeasurement: "attributed_v1", LatencyEvidenceVersion: 1, LatestBadLatencyAt: &bad})
		pool = append(pool, openAIAccountCandidateScore{account: &repo.accounts[i]})
	}
	svc.usageLogRepo = &priorityReaderStub{signal: signals}
	require.Eventually(t, func() bool { return svc.priorityHistory(req, cfg, pool).ready() }, 2*time.Second, time.Millisecond)
	var acquired []int64
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: false, 2: false, 3: false, 4: false, 5: false, 6: false}, acquiredIDs: &acquired})
	selected, err := modeSelect(t, scheduler, req)
	require.NoError(t, err)
	require.NotNil(t, selected)
	got := map[int64]bool{}
	for _, id := range acquired {
		got[id] = true
	}
	require.Equal(t, map[int64]bool{5: true, 6: true}, got, "configured effective P95 must win over unrelated legacy P90")
}

func TestPriorityLatencyAvailabilityHTTPAndChatTransport(t *testing.T) {
	for _, chat := range []bool{false, true} {
		t.Run(map[bool]string{false: "responses", true: "chat to responses"}[chat], func(t *testing.T) {
			svc, scheduler, req := availabilityFixture(t, 16000, 17000, 18000, 19000, 20000, 21000)
			if chat {
				req.InboundEndpoint = "/v1/chat/completions"
			}
			selected, err := modeSelect(t, scheduler, req)
			require.NoError(t, err)
			require.NotNil(t, selected)
			selected.Account.Credentials = map[string]any{"api_key": "sk-synthetic", "base_url": "https://compat.example"}
			selected.Account.Extra["openai_responses_mode"] = "force_responses"
			wire := `{"id":"resp_fallback_http","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fallback completed"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
			// The real CC -> Responses adapter requests SSE even for a nonstream
			// client, then collects the terminal before writing the CC response.
			if chat {
				wire = "data: {\"type\":\"response.completed\",\"response\":" + wire + "}\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: priorityMockResponse(200, wire, chat)}
			svc.httpUpstream = upstream
			upstream.resp.Body = &latencyInFlightBody{Reader: strings.NewReader(wire), start: func() {
				setModeSignal(svc, req, 6, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 500; v.LatestBadLatencyAt = nil })
			}}
			body := []byte(`{"model":"gpt-test","input":"synthetic","stream":false}`)
			if chat {
				body = []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"synthetic"}],"stream":false}`)
			}
			ctx := ContextWithSelectionPriorityRequest(WithPrioritySchedulingObservation(context.Background(), req.GroupID, req.HistoryModel, req.InboundEndpoint), selected)
			c := newOpenAIRejectedFieldTestContext(body)
			c.Request = c.Request.WithContext(ctx)
			c.Request.URL.Path = req.InboundEndpoint
			var result *OpenAIForwardResult
			if chat {
				result, err = svc.ForwardAsChatCompletions(ctx, c, selected.Account, body, "", "")
			} else {
				result, err = svc.Forward(ctx, c, selected.Account, body)
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 1, "late healthy account never interrupts or replays an admitted generation")
			require.Equal(t, "/v1/responses", upstream.requests[0].URL.Path)
			require.ErrorIs(t, svc.CheckPriorityLatencySelection(ctx, selected), ErrPriorityLatencyUnavailable, "a new turn cannot keep the stopped fallback after a healthy peer returns")
		})
	}
}

func TestPriorityLatencyAvailabilityPreWriteRecheck(t *testing.T) {
	svc, scheduler, req := availabilityFixture(t, 16000, 17000, 18000, 19000, 20000, 21000)
	selected, err := modeSelect(t, scheduler, req)
	require.NoError(t, err)
	require.NotNil(t, selected)
	ctx := ContextWithSelectionPriorityRequest(context.Background(), selected)
	ctx, err = svc.beginPriorityLatencyTurn(ctx, nil, selected.Account, req.RequestedModel)
	require.NoError(t, err)
	setModeSignal(svc, req, 6, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 500; v.LatestBadLatencyAt = nil })
	upstream := &httpUpstreamRecorder{resp: priorityMockResponse(200, `{"status":"completed","output":[]}`, false)}
	svc.httpUpstream = upstream
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://compat.example/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	require.NoError(t, err)
	_, err = svc.doOpenAIProxyAttempt(request, selected.Account, runtimeProxyEgress{})
	require.ErrorIs(t, err, ErrPriorityLatencyUnavailable, "history learned during preparation must be checked before actual write")
	require.Empty(t, upstream.requests, "pre-write rejection must send no generation")
}

func TestPriorityLatencyAvailabilityNoRefillAcrossSelection(t *testing.T) {
	svc, scheduler, req := availabilityFixture(t, 16000, 17000, 18000, 19000, 20000, 21000)
	selected, err := modeSelect(t, scheduler, req)
	require.NoError(t, err)
	require.NotNil(t, selected)
	ctx := ContextWithSelectionPriorityRequest(context.Background(), selected)
	req.ExcludedIDs = map[int64]struct{}{1: {}, 2: {}}
	retry, _, err := scheduler.Select(ctx, req)
	if retry != nil && retry.ReleaseFunc != nil {
		retry.ReleaseFunc()
	}
	require.Nil(t, retry)
	require.ErrorIs(t, err, ErrPriorityLatencyUnavailable, "one logical request cannot expand fallback by excluding each failed member")
	_ = svc
}

func TestPriorityLatencyAvailabilityLastHealthyStopsDuringWait(t *testing.T) {
	svc, scheduler, req := availabilityFixture(t, 500, 17000, 18000, 19000, 20000, 21000)
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: false}})
	selected, err := modeSelect(t, scheduler, req)
	require.NoError(t, err)
	require.NotNil(t, selected.WaitPlan)
	require.Equal(t, int64(1), selected.Account.ID)
	setModeSignal(svc, req, 1, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 16000; bad := time.Now(); v.LatestBadLatencyAt = &bad })
	require.NoError(t, svc.CheckPriorityLatencySelection(context.Background(), selected), "last healthy stops during wait: open the same authorized lowest band instead of an avoidable 503")
	outside, err := svc.accountRepo.GetByID(context.Background(), 3)
	require.NoError(t, err)
	forged := *selected
	forged.Account = outside
	require.ErrorIs(t, svc.CheckPriorityLatencySelection(context.Background(), &forged), ErrPriorityLatencyUnavailable, "late fallback is frozen once and cannot expand")
}

func TestPriorityLatencyAvailabilityRecoveredHealthyPeer(t *testing.T) {
	svc, scheduler, req := availabilityFixture(t, 16000, 17000, 18000, 19000, 20000, 21000)
	selected, err := modeSelect(t, scheduler, req)
	require.NoError(t, err)
	require.NotNil(t, selected)
	ctx := ContextWithSelectionPriorityRequest(context.Background(), selected)
	setModeSignal(svc, req, 6, func(v *PrioritySchedulingSignal) { v.P90TTFTMs = 500; v.LatestBadLatencyAt = nil })
	recovered, _, err := scheduler.Select(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	if recovered.ReleaseFunc != nil {
		t.Cleanup(recovered.ReleaseFunc)
	}
	require.Equal(t, int64(6), recovered.Account.ID, "recovered healthy peer wins without enlarging the stopped fallback set")
	require.ErrorIs(t, svc.CheckPriorityLatencySelection(ctx, selected), ErrPriorityLatencyUnavailable)
}

func TestPriorityLatencyAvailabilityHTTPWSLease(t *testing.T) {
	svc, req, account, upstream, dialer, ctx := reviewNativeFixture(t, OpenAIWSIngressModeCtxPool)
	c := newOpenAIRejectedFieldTestContext([]byte(`{"model":"gpt-test","input":"synthetic"}`))
	c.Request = c.Request.WithContext(ctx)
	admitted, err := svc.beginPriorityLatencyTurn(ctx, c, account, req.RequestedModel)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := svc.forwardOpenAIWSV2(admitted, c, account, map[string]any{"model": req.RequestedModel, "input": "synthetic"}, "", "", "sk-synthetic", svc.getOpenAIWSProtocolResolver().Resolve(account), false, false, req.RequestedModel, req.RequestedModel, time.Now(), 0, "", nil)
		done <- err
	}()
	select {
	case <-dialer.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP WS forward did not reach lease barrier")
	}
	reviewActivateStop(svc, req, account.ID)
	close(dialer.release)
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrPriorityLatencyUnavailable)
	case write := <-upstream.writes:
		require.Empty(t, write, "HTTP-to-WS lease wait must reject a newly stopped generation before writing")
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP WS late admission did not complete")
	}
	require.Empty(t, upstream.writes, "latency rejection must not send or replay generation")
}
