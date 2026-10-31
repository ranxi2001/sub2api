package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const (
	priorityAttemptMaxKeys      = 4096
	priorityAttemptMaxMinutes   = 60
	priorityAttemptInspectBytes = 64 << 10
)

type priorityAttemptKey struct {
	effort    string
	accountID int64
	model     string
	endpoint  string
	groupID   int64
	hasGroup  bool
}

type priorityAttemptBucket struct {
	minute              int64
	successes, failures int64
	latencySamples      int64
	latencyTotalMS      float64
}
type priorityAttemptEntry struct {
	buckets [priorityAttemptMaxMinutes]priorityAttemptBucket
}

// Lives in the existing scheduler runtime container. This is process-local,
// bounded evidence, not a durable or cluster-wide availability measurement.
type priorityAttemptStats struct {
	mu      sync.Mutex
	entries map[priorityAttemptKey]*priorityAttemptEntry
	keys    [priorityAttemptMaxKeys]priorityAttemptKey
	next    int
}

type PriorityErrorEvidence struct {
	LatencySamples int64     `json:"attempt_latency_samples,omitempty"`
	LatencyTotalMS float64   `json:"attempt_latency_total_ms,omitempty"`
	ErrorRate      *float64  `json:"error_rate"`
	SmoothedRate   float64   `json:"error_smoothed_rate"`
	Successes      int64     `json:"error_successes"`
	Failures       int64     `json:"error_failures"`
	WindowMinutes  int       `json:"error_window_minutes"`
	PriorSamples   int       `json:"error_prior_samples"`
	ObservedAt     time.Time `json:"error_observed_at"`
	Endpoint       string    `json:"error_endpoint"`
}

func (s *priorityAttemptStats) report(key priorityAttemptKey, success bool, now time.Time, latencyMS ...float64) {
	if key.accountID <= 0 || key.model == "" || len(key.model) > 200 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[priorityAttemptKey]*priorityAttemptEntry)
	}
	e := s.entries[key]
	if e == nil {
		if len(s.entries) == priorityAttemptMaxKeys {
			delete(s.entries, s.keys[s.next])
		}
		s.keys[s.next] = key
		s.next = (s.next + 1) % priorityAttemptMaxKeys
		e = &priorityAttemptEntry{}
		s.entries[key] = e
	}
	minute := now.Unix() / 60
	b := &e.buckets[minute%priorityAttemptMaxMinutes]
	if b.minute != minute {
		*b = priorityAttemptBucket{minute: minute}
	}
	if success {
		b.successes++
		if len(latencyMS) > 0 && latencyMS[0] >= 0 && !math.IsNaN(latencyMS[0]) && !math.IsInf(latencyMS[0], 0) {
			b.latencySamples++
			b.latencyTotalMS += latencyMS[0]
		}
	} else {
		b.failures++
	}
}

func (s *priorityAttemptStats) snapshot(key priorityAttemptKey, now time.Time, window, prior int) PriorityErrorEvidence {
	prior = max(1, prior)
	out := PriorityErrorEvidence{WindowMinutes: window, PriorSamples: prior, ObservedAt: now, Endpoint: key.endpoint}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entries[key]; e != nil {
		minute := now.Unix() / 60
		for _, b := range e.buckets {
			if b.minute <= minute && b.minute > minute-int64(window) {
				out.Successes += b.successes
				out.Failures += b.failures
				out.LatencySamples += b.latencySamples
				out.LatencyTotalMS += b.latencyTotalMS
			}
		}
	}
	n := out.Successes + out.Failures
	if n > 0 {
		rate := float64(out.Failures) / float64(n)
		out.ErrorRate = &rate
		out.SmoothedRate = float64(out.Failures) / (float64(n) + float64(prior))
	}
	return out
}

type priorityAttemptScopeContextKey struct{}
type priorityAttemptObservation struct {
	report                                           func(bool, bool)
	finalized, reported, forcedFailure, disconnected bool
}
type priorityAttemptScope struct {
	effort          string
	ctx             context.Context
	model, endpoint string
	groupID         int64
	hasGroup        bool
	mu              sync.Mutex
	current         *priorityAttemptObservation
	disconnected    bool
}

func withPriorityAttemptScope(ctx context.Context, model, endpoint string) (context.Context, *priorityAttemptScope) {
	if current, ok := ctx.Value(priorityAttemptScopeContextKey{}).(*priorityAttemptScope); ok {
		return ctx, current
	}
	identity := priorityObservationFromContext(ctx, model)
	if identity.endpoint != "" {
		endpoint = identity.endpoint
	}
	scope := &priorityAttemptScope{ctx: ctx, model: identity.model, endpoint: endpoint, groupID: identity.groupID, hasGroup: identity.hasGroup}
	scope.effort, _ = observationEffort(ctx)
	return context.WithValue(ctx, priorityAttemptScopeContextKey{}, scope), scope
}
func (s *priorityAttemptScope) reportLocked(o *priorityAttemptObservation) {
	if o != nil && o.finalized && o.report != nil && !o.reported {
		o.reported = true
		o.report(o.disconnected, o.forcedFailure)
		o.report = nil
	}
}
func (s *priorityAttemptScope) begin() *priorityAttemptObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		s.current.finalized = true
		s.current.disconnected = s.disconnected
		s.reportLocked(s.current)
	}
	s.current = &priorityAttemptObservation{}
	return s.current
}
func (s *priorityAttemptScope) complete(o *priorityAttemptObservation, report func(bool, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.reported {
		return
	}
	o.report = report
	s.reportLocked(o)
}
func (s *priorityAttemptScope) finish(result *OpenAIForwardResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result != nil && result.ClientDisconnect {
		s.disconnected = true
	}
	if s.current != nil {
		s.current.finalized = true
		s.current.disconnected = s.disconnected
		s.reportLocked(s.current)
	}
}
func markPriorityDownstreamDisconnect(ctx context.Context) {
	if scope, ok := ctx.Value(priorityAttemptScopeContextKey{}).(*priorityAttemptScope); ok {
		scope.mu.Lock()
		scope.disconnected = true
		scope.mu.Unlock()
	}
}

// Observation begins only after local admission, RPM acquisition and storage
// work, immediately before the transport/plugin send. Handler health reports
// never write these counters; retry/fallback sends have distinct body owners.
func (s *OpenAIGatewayService) observePriorityAttempt(req *http.Request, account *Account) func(*http.Response, error) {
	scope, _ := req.Context().Value(priorityAttemptScopeContextKey{}).(*priorityAttemptScope)
	if scope == nil || account == nil || account.Platform != PlatformOpenAI {
		return func(*http.Response, error) {}
	}
	observation := scope.begin()
	s.initOpenAIAccountScheduler()
	stats := s.openaiAccountStats
	started := stats.currentTime()
	key := priorityAttemptKey{accountID: account.ID, model: scope.model, endpoint: scope.endpoint, groupID: scope.groupID, hasGroup: scope.hasGroup, effort: scope.effort}
	return func(resp *http.Response, err error) {
		if err != nil {
			var netErr net.Error
			var phaseErr *openAIHTTPPhaseTimeout
			include := errors.As(context.Cause(req.Context()), &phaseErr) || !errors.Is(err, context.Canceled) && errors.As(err, &netErr)
			scope.complete(observation, func(disconnected, forced bool) {
				if !disconnected && scope.ctx.Err() == nil && (include || forced) {
					stats.attempts.report(key, false, stats.currentTime())
				}
			})
			return
		}
		if resp == nil || resp.Body == nil {
			return
		}
		bps := req.URL != nil && req.URL.Host == "bps.openai.com"
		if bps {
			s.guardExcelBPSProgress(req.Context(), resp)
		}
		b := newPriorityAttemptBody(resp.Body, stats, key, scope.ctx, resp.StatusCode, bps || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream"))
		b.started = started
		// Delay the final sample until Forward knows whether downstream writing
		// failed. Starting a retry flushes the preceding attempt independently.
		b.scope = scope
		b.observation = observation
		b.upstreamCtx = req.Context()
		b.chat = req.URL != nil && strings.HasSuffix(req.URL.Path, "/chat/completions")
		resp.Body = b
	}
}

// Used by HTTP adapters whose physical sends bypass the native proxy pipeline.
func (s *OpenAIGatewayService) doPriorityObservedHTTP(req *http.Request, proxy string, account *Account) (*http.Response, error) {
	if s.excelOAuthReauth != nil {
		if err := s.excelOAuthReauth.validateAuthConnectionPolicy(req.Context(), account); err != nil {
			return nil, err
		}
	}
	if account != nil && account.AuthConnectionID != "" {
		if s.excelOAuthReauth == nil {
			return nil, errors.New("authorization connection service unavailable")
		}
		if err := s.excelOAuthReauth.ValidateAuthConnectionSend(req.Context(), account); err != nil {
			return nil, err
		}
	}

	observe := s.observePriorityAttempt(req, account)
	if err := s.fenceAuthConnectionEgress(req.Context(), req, account); err != nil {
		return nil, err
	}
	if err := s.priorityLatencyBeforeSend(req.Context(), account); err != nil {
		return nil, err
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	s.priorityLifecycleWriteResult(req.Context(), account, "http_adapter", err)
	observe(resp, err)
	return resp, err
}

// A bounded passive observer: it neither changes bytes nor creates retries.
// Unknown/oversized protocol evidence is excluded, never assumed successful.
type priorityAttemptBody struct {
	started         time.Time
	firstSemanticMS *float64
	io.ReadCloser
	mu                                                 sync.Mutex
	stats                                              *openAIAccountRuntimeStats
	key                                                priorityAttemptKey
	ctx                                                context.Context
	upstreamCtx                                        context.Context
	status                                             int
	stream                                             bool
	chat                                               bool
	scope                                              *priorityAttemptScope
	observation                                        *priorityAttemptObservation
	buf                                                []byte
	eof, closed, oversized, terminal, failed, excluded bool
	closeOnce                                          sync.Once
	closeErr                                           error
	readErr                                            error
}

func newPriorityAttemptBody(body io.ReadCloser, stats *openAIAccountRuntimeStats, key priorityAttemptKey, ctx context.Context, status int, stream bool) *priorityAttemptBody {
	return &priorityAttemptBody{ReadCloser: body, stats: stats, key: key, ctx: ctx, status: status, stream: stream, chat: key.endpoint == "/v1/chat/completions", started: stats.currentTime()}
}
func (b *priorityAttemptBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return n, err
	}
	if b.stream && b.status < 400 {
		for _, part := range bytes.SplitAfter(p[:n], []byte{'\n'}) {
			if len(b.buf)+len(part) > priorityAttemptInspectBytes {
				b.oversized = true
				b.buf = nil
				continue
			}
			b.buf = append(b.buf, part...)
			if bytes.HasSuffix(part, []byte{'\n'}) {
				b.observeLine(b.buf)
				b.buf = b.buf[:0]
			}
		}
	} else if len(b.buf)+n <= priorityAttemptInspectBytes {
		b.buf = append(b.buf, p[:n]...)
	} else {
		b.oversized = true
	}
	if err == io.EOF {
		b.eof = true
		if b.stream && b.status < 400 && len(b.buf) > 0 {
			b.observeLine(b.buf)
			b.buf = nil
		}
	} else if err != nil {
		b.readErr = err
	}
	return n, err
}

// Only inspect error objects and protocol status, never generated output text.
func priorityExcludedError(data []byte) bool {
	if isOpenAITransientProcessingError(http.StatusBadRequest, extractUpstreamErrorMessage(data), data) {
		return false
	}
	if isOpenAIRequestScopedCapacityShed(extractOpenAISSEErrorMessage(data), data) {
		return false
	}
	root := gjson.GetBytes(data, "error")
	if !root.Exists() {
		root = gjson.GetBytes(data, "response.error")
	}
	code := strings.ToLower(root.Get("code").String() + " " + root.Get("type").String())
	message := strings.ToLower(root.Get("message").String())
	if strings.Contains(message, "tool arguments stopped making progress: consecutive whitespace outside json strings") {
		return false
	}
	for _, v := range []string{"user_inactive", "invalid_request", "invalid_api_key", "authentication", "permission", "access_denied", "insufficient_quota", "quota_exceeded", "insufficient_balance", "billing", "content_policy", "context_length", "invalid_encrypted_content", "model_not_found"} {
		if strings.Contains(code, v) {
			return true
		}
	}
	for _, v := range []string{"user_inactive", "insufficient balance", "insufficient quota", "quota exceeded", "permission denied", "account is inactive", "credit balance"} {
		if strings.Contains(message, v) {
			return true
		}
	}
	return false
}
func (b *priorityAttemptBody) observeLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(data) == 0 {
		return
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		b.terminal = true
		return
	}
	if !gjson.ValidBytes(data) {
		b.oversized = true
		return
	}
	typ := gjson.GetBytes(data, "type").String()
	if b.firstSemanticMS == nil && openAIStreamDataStartsSemanticTTFT(string(data), typ) {
		ms := b.stats.currentTime().Sub(b.started).Seconds() * 1000
		b.firstSemanticMS = &ms
	}
	if typ == "error" || typ == "response.failed" || gjson.GetBytes(data, "error").Exists() {
		b.failed = true
		b.excluded = b.excluded || priorityExcludedError(data)
	}
	if IsSuccessfulStreamTerminal(data) || typ == "response.done" && gjson.GetBytes(data, "response.status").String() == "completed" {
		b.terminal = true
	}
	// CC compatibility accepts finish_reason/usage terminals, as the existing
	// raw adapter does. Neither can override a preceding application error.
	if b.chat {
		var state openAIRawStreamTerminalState
		state.ObserveDataLine(string(data))
		b.terminal = b.terminal || state.Terminated()
	}
}
func (b *priorityAttemptBody) Close() error {
	b.closeOnce.Do(func() { b.closeErr = b.ReadCloser.Close() })
	err := b.closeErr
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return err
	}
	b.closed = true
	success, include := b.outcome()
	b.buf = nil
	report := func(disconnected, forced bool) {
		if (include || forced) && !disconnected && b.ctx.Err() == nil {
			if b.firstSemanticMS != nil && success && !forced {
				b.stats.attempts.report(b.key, true, b.stats.currentTime(), *b.firstSemanticMS)
			} else {
				b.stats.attempts.report(b.key, success && !forced, b.stats.currentTime())
			}
		}
	}
	if b.scope != nil {
		b.scope.complete(b.observation, report)
	} else {
		report(false, false)
	}
	return err
}
func (b *priorityAttemptBody) outcome() (bool, bool) {
	var timeout *openAIHTTPPhaseTimeout
	upstreamTimeout := b.upstreamCtx != nil && errors.As(context.Cause(b.upstreamCtx), &timeout)
	if b.ctx.Err() != nil || b.oversized || b.excluded || errors.Is(b.readErr, context.Canceled) && !upstreamTimeout || errors.Is(b.readErr, ErrUpstreamResponseBodyTooLarge) {
		return false, false
	}
	if upstreamTimeout {
		return false, true
	}
	if b.status >= 400 {
		if priorityExcludedError(b.buf) {
			return false, false
		}
		return false, isOpenAITransientProcessingError(b.status, extractUpstreamErrorMessage(b.buf), b.buf) || b.status == 403 || b.status == 408 || b.status == 429 || b.status >= 500 && b.status <= 599
	}
	if b.status < 200 || b.status >= 300 {
		return false, false
	}
	if b.stream {
		if b.failed {
			return false, true
		}
		if b.terminal {
			return true, true
		}
		return false, b.eof || b.readErr != nil
	}
	if !b.eof || !gjson.ValidBytes(b.buf) {
		return false, false
	}
	if priorityExcludedError(b.buf) {
		return false, false
	}
	if gjson.GetBytes(b.buf, "error").Exists() || gjson.GetBytes(b.buf, "status").String() == "failed" {
		return false, true
	}
	if status := gjson.GetBytes(b.buf, "status").String(); status != "" && status != "completed" {
		return false, false
	}

	// A valid but unrecognized JSON object is not proof of a completed generation.
	if gjson.GetBytes(b.buf, "status").String() == "completed" && gjson.GetBytes(b.buf, "output").IsArray() {
		return true, true
	}
	if b.chat && gjson.GetBytes(b.buf, "choices").IsArray() {
		for _, choice := range gjson.GetBytes(b.buf, "choices").Array() {
			if choice.Get("finish_reason").String() != "" {
				return true, true
			}
		}
	}
	return false, false
}

func priorityErrorEndpoint(req OpenAIAccountScheduleRequest) string {
	if req.InboundEndpoint != "" {
		return req.InboundEndpoint
	}
	if req.RequireCompact {
		return "/v1/responses/compact"
	}
	if req.RequiredCapability == OpenAIEndpointCapabilityChatCompletions {
		return "/v1/chat/completions"
	}
	return "/v1/responses"
}
func (s *defaultOpenAIAccountScheduler) priorityErrorEvidence(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig, id int64, now time.Time) PriorityErrorEvidence {
	key := priorityRequestAttemptKey(req, id)
	if s.stats == nil {
		return PriorityErrorEvidence{WindowMinutes: c.ErrorRateWindowMinutes, PriorSamples: max(1, c.ErrorRatePriorSamples), ObservedAt: now, Endpoint: key.endpoint}
	}
	return s.stats.attempts.snapshot(key, now, c.ErrorRateWindowMinutes, c.ErrorRatePriorSamples)
}
func priorityErrorPenalty(c PrioritySchedulingConfig, e PriorityErrorEvidence) float64 {
	return math.Max(.1, 1-c.ErrorRateWeight/100*clamp01(e.SmoothedRate))
}

// Called only for a known upstream watchdog or protocol-validation failure.
// It annotates the physical attempt owner and never creates a new observation.
func markPriorityAttemptFailure(ctx context.Context) {
	if scope, ok := ctx.Value(priorityAttemptScopeContextKey{}).(*priorityAttemptScope); ok {
		scope.mu.Lock()
		if scope.current != nil {
			scope.current.forcedFailure = true
		}
		scope.mu.Unlock()
	}
}
