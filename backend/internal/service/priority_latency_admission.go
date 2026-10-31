package service

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// A local admission rejection. It never implies an upstream failure or permits
// fail-open proxy routing, response-ID deletion, or conversation replay.
var ErrPriorityLatencyUnavailable = errors.New("latency scheduling temporarily unavailable")

func priorityHardStopApplies(c PrioritySchedulingConfig, req OpenAIAccountScheduleRequest) bool {
	return c.LatencyPenaltyMode == PriorityLatencyStop && req.Platform == PlatformOpenAI && req.UseUpstreamTokenCost && req.RequiredImageCapability == "" && priorityApplies(c, req)
}

func priorityLatencyAdmissionError(known bool) error {
	reason := "latency_penalty"
	if !known {
		reason = "latency_history_unavailable"
	}
	return &OpenAITurnAdmissionError{Reason: reason, cause: ErrPriorityLatencyUnavailable}
}

func priorityLatencyStopped(c PrioritySchedulingConfig, item openAIAccountCandidateScore, signal PrioritySchedulingSignal, now time.Time) bool {
	return c.LatencyPenaltyMode == PriorityLatencyStop && slices.Contains(scorePriorityCandidate(c, item, signal, now).Reasons, "latency_above_target")
}

// Request-local eligible universe. The first fallback opening freezes membership;
// later checks cannot refill from a slower rank after waits or history refreshes.
// Only IDs and exact measurement scope survive in the selection context.
type priorityLatencyPool struct {
	key      string
	accounts []openAIAccountCandidateScore
	allowed  map[int64]struct{}
	fallback atomic.Pointer[priorityLatencyFallback]
}

type priorityLatencyFallback struct{ allowed map[int64]struct{} }

func (p *priorityLatencyPool) fallbackActive() bool { return p.fallback.Load() != nil }

// Freeze the first fallback set, including when the last healthy member becomes
// stopped during a slot/handshake wait. Atomic publication prevents concurrent
// guards from opening different sets. Later history can only narrow membership.
func (p *priorityLatencyPool) members(current map[int64]struct{}, fallback bool) map[int64]struct{} {
	// Healthy peers in the original eligible universe always take precedence;
	// allowing a recovered peer does not enlarge the stopped fallback set.
	if !fallback {
		return current
	}
	if fallback {
		p.fallback.CompareAndSwap(nil, &priorityLatencyFallback{allowed: current})
	}
	if opened := p.fallback.Load(); opened != nil {
		return opened.allowed
	}
	return p.allowed
}

func priorityLatencyMeasured(c PrioritySchedulingConfig, score PrioritySchedulingScore) bool {
	return score.Samples >= c.MinSamples && score.SelectedTTFTMs != nil && *score.SelectedTTFTMs > 0
}

func (s *defaultOpenAIAccountScheduler) preparePriorityLatencyRequest(ctx context.Context, req OpenAIAccountScheduleRequest) (OpenAIAccountScheduleRequest, error) {
	cfg := s.service.prioritySchedulingRuntimeConfig()
	if !priorityHardStopApplies(cfg, req) || req.priorityLatencyPool != nil {
		return req, nil
	}
	_, accounts, loads, _, err := s.loadOpenAIEligibleAccounts(ctx, req)
	if err != nil {
		// A local empty eligibility pool must not authorize proxy fail-open.
		if errors.Is(err, ErrNoAvailableAccounts) || errors.Is(err, ErrNoAvailableCompactAccounts) {
			return req, priorityLatencyAdmissionError(false)
		}
		return req, err
	}
	pool := make([]openAIAccountCandidateScore, 0, len(accounts))
	for _, account := range accounts {
		pool = append(pool, openAIAccountCandidateScore{account: account, loadInfo: loads[account.ID], loadKnown: loads[account.ID] != nil})
	}
	history := s.service.priorityHistory(req, cfg, pool)
	now := time.Now()
	admission := &priorityLatencyPool{key: priorityHistoryKey(req, cfg)}
	allowed, fallback := priorityLatencyAllowed(cfg, pool, history, now)
	admission.allowed = allowed
	admission.members(allowed, fallback)
	for i := range pool {
		item := &pool[i]
		item.priorityErrors = s.priorityErrorEvidence(req, cfg, item.account.ID, now)
		applyPriorityCandidate(cfg, item, history.signals[item.account.ID], now)
		admission.accounts = append(admission.accounts, openAIAccountCandidateScore{account: &Account{ID: item.account.ID}})
	}
	req.priorityLatencyPool = admission
	s.recordPrioritySnapshot(req, cfg, pool, now)
	if len(admission.allowed) == 0 {
		return req, priorityLatencyAdmissionError(false)
	}
	return req, nil
}

// Existing effective measurement validation determines both health and rank.
// Exact equal values define a band; the unknown band sorts after measurements.
func priorityLatencyAllowed(cfg PrioritySchedulingConfig, pool []openAIAccountCandidateScore, history priorityHistoryResult, now time.Time) (map[int64]struct{}, bool) {
	type ranked struct {
		id       int64
		latency  float64
		measured bool
	}
	ranks := make([]ranked, 0, len(pool))
	allowed := make(map[int64]struct{})
	for _, item := range pool {
		score := scorePriorityCandidate(cfg, item, history.signals[item.account.ID], now)
		measured := priorityLatencyMeasured(cfg, score)
		if measured && !score.LatencyStopped {
			allowed[item.account.ID] = struct{}{}
		}
		rank := ranked{id: item.account.ID, measured: measured}
		if measured {
			rank.latency = *score.SelectedTTFTMs
		}
		ranks = append(ranks, rank)
	}
	if len(allowed) > 0 {
		return allowed, false
	}
	slices.SortFunc(ranks, func(a, b ranked) int {
		if a.measured != b.measured {
			if a.measured {
				return -1
			}
			return 1
		}
		if order := cmp.Compare(a.latency, b.latency); order != 0 {
			return order
		}
		return cmp.Compare(a.id, b.id)
	})
	band := 0
	for _, rank := range ranks {
		if rank.measured != ranks[0].measured || rank.latency != ranks[0].latency {
			break
		}
		band++
	}
	for _, rank := range ranks[:min(len(ranks), min(5, max(2, band)))] {
		allowed[rank.id] = struct{}{}
	}
	return allowed, true
}

func (s *OpenAIGatewayService) priorityLatencyAdmission(req OpenAIAccountScheduleRequest, account *Account) error {
	cfg := s.prioritySchedulingRuntimeConfig()
	if account == nil || !priorityHardStopApplies(cfg, req) {
		return nil
	}
	pool := []openAIAccountCandidateScore{{account: account}}
	admission := req.priorityLatencyPool
	if admission != nil {
		if admission.key != priorityHistoryKey(req, cfg) {
			return priorityLatencyAdmissionError(false)
		}
		pool = admission.accounts
	}
	history := s.priorityHistory(req, cfg, pool)
	signal, known := history.signals[account.ID]
	stopped := known && priorityLatencyStopped(cfg, openAIAccountCandidateScore{account: account}, signal, time.Now())
	if admission != nil {
		current, fallback := priorityLatencyAllowed(cfg, pool, history, time.Now())
		members := admission.members(current, fallback)
		_, original := members[account.ID]
		if _, allowed := current[account.ID]; !original || !allowed {
			return priorityLatencyAdmissionError(known)
		}
		return nil
	}
	// Pending/unavailable history is unknown, not a measured stop. The full
	// scheduler bounds unknown candidates using the same fallback pool above.
	if stopped {
		return priorityLatencyAdmissionError(true)
	}
	return nil
}

func priorityLatencyOwnershipBound(req OpenAIAccountScheduleRequest) bool {
	return strings.TrimSpace(req.PreviousResponseID) != "" || req.GuardianParentAccountID > 0 || req.DisableStickyEscape || req.PreserveStickyBinding || req.EncryptedReasoning
}

// Partition and wait paths use the request's original membership. No local
// partition is allowed to decide that it is independently an all-stopped pool.
func (s *defaultOpenAIAccountScheduler) filterPriorityLatencyAccounts(req OpenAIAccountScheduleRequest, accounts []*Account, loads map[int64]*AccountLoadInfo) ([]*Account, error) {
	cfg := s.service.prioritySchedulingRuntimeConfig()
	if !priorityHardStopApplies(cfg, req) {
		return accounts, nil
	}
	allowed := make([]*Account, 0, len(accounts))
	if admission := req.priorityLatencyPool; admission != nil {
		if admission.key != priorityHistoryKey(req, cfg) {
			return nil, priorityLatencyAdmissionError(false)
		}
		history := s.service.priorityHistory(req, cfg, admission.accounts)
		current, fallback := priorityLatencyAllowed(cfg, admission.accounts, history, time.Now())
		members := admission.members(current, fallback)
		for _, account := range accounts {
			_, original := members[account.ID]
			_, ready := current[account.ID]
			if original && ready {
				allowed = append(allowed, account)
			}
		}
	} else {
		for _, account := range accounts {
			if s.service.priorityLatencyAdmission(req, account) == nil {
				allowed = append(allowed, account)
			}
		}
	}
	if len(allowed) == 0 {
		return nil, priorityLatencyAdmissionError(true)
	}
	return allowed, nil
}

type prioritySelectionRequestKey struct{}
type priorityLatencyTurnKey struct{}

type priorityLatencyTurn struct {
	mu                sync.Mutex
	accountID         int64
	request           OpenAIAccountScheduleRequest
	started           bool
	outcomeRegistered bool
}

// First physical HTTP/plugin send rechecks after preparation waits. Any attempted
// send (even an uncertain result) freezes admission for this generation's retries.
func (s *OpenAIGatewayService) priorityLatencyBeforeSend(ctx context.Context, account *Account) error {
	turn, _ := ctx.Value(priorityLatencyTurnKey{}).(*priorityLatencyTurn)
	if turn == nil || account == nil || turn.accountID != account.ID {
		return nil
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if turn.started {
		return nil
	}
	if err := s.priorityLatencyAdmission(turn.request, account); err != nil {
		return err
	}
	if ctx.Value(priorityLifecycleHTTPDeferredKey{}) != true {
		turn.started = true
	}
	return nil
}

func ownedPriorityScheduleRequest(req OpenAIAccountScheduleRequest) OpenAIAccountScheduleRequest {
	// Retain only identity and feature applicability, never session/response IDs.
	copy := OpenAIAccountScheduleRequest{Platform: req.Platform, RequestedModel: req.RequestedModel, HistoryModel: req.HistoryModel, InboundEndpoint: req.InboundEndpoint, UseUpstreamTokenCost: req.UseUpstreamTokenCost, RequiredImageCapability: req.RequiredImageCapability, RequireCompact: req.RequireCompact, RequiredCapability: req.RequiredCapability, RequiredTransport: req.RequiredTransport, RequirePrivacySet: req.RequirePrivacySet, ExcludedIDs: cloneExcludedAccountIDs(req.ExcludedIDs), priorityLatencyPool: req.priorityLatencyPool}
	if req.GroupID != nil {
		id := *req.GroupID
		copy.GroupID = &id
	}
	copy.priorityDecisionID = req.priorityDecisionID
	copy.priorityConsumed = req.priorityConsumed
	copy.LifecycleRequestType = req.LifecycleRequestType
	copy.ObservationEffort, copy.ObservationEffortScoped = req.ObservationEffort, req.ObservationEffortScoped
	return copy
}

// Selection carries the exact public/routing/group identity through slot waits.
func ContextWithSelectionPriorityRequest(ctx context.Context, selection *AccountSelectionResult) context.Context {
	if selection == nil || selection.priorityRequest == nil {
		return ctx
	}
	return context.WithValue(ctx, prioritySelectionRequestKey{}, *selection.priorityRequest)
}
func (s *OpenAIGatewayService) CheckPriorityLatencySelection(ctx context.Context, selection *AccountSelectionResult) error {
	if selection == nil || selection.priorityRequest == nil {
		return nil
	}
	return s.priorityLatencyAdmission(*selection.priorityRequest, selection.Account)
}

// This boundary runs once before a logical turn starts. Nested forwarding and
// retries retain the marker; a penalty learned after output starts cannot abort
// or replay that already admitted turn. A new WS turn explicitly checks again.
func (s *OpenAIGatewayService) beginPriorityLatencyTurn(ctx context.Context, c *gin.Context, account *Account, model string) (context.Context, error) {
	if turn, ok := ctx.Value(priorityLatencyTurnKey{}).(*priorityLatencyTurn); ok && turn != nil && account != nil && turn.accountID == account.ID {
		turn.mu.Lock()
		defer turn.mu.Unlock()
		if !turn.started {
			if err := s.priorityLatencyAdmission(turn.request, account); err != nil {
				return ctx, err
			}
		}
		return ctx, nil
	}
	req, selectedRequest := ctx.Value(prioritySelectionRequestKey{}).(OpenAIAccountScheduleRequest)
	if !selectedRequest {
		identityCtx := ctx
		if c != nil && c.Request != nil {
			if _, captured := c.Request.Context().Value(priorityObservationContextKey{}).(priorityObservationIdentity); captured {
				identityCtx = c.Request.Context()
			}
		}
		identity := priorityObservationFromContext(identityCtx, model)
		req = OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: strings.TrimSpace(model), HistoryModel: identity.model, InboundEndpoint: identity.endpoint, UseUpstreamTokenCost: true}
		if account != nil {
			req.Platform = account.Platform
		}
		if identity.hasGroup {
			id := identity.groupID
			req.GroupID = &id
		}
		if forward, found := openAIForwardModelFromContext(identityCtx); found {
			req.RequestedModel = forward.model
		}
		if strings.HasPrefix(identity.endpoint, "/v1/images") || IsGPTImageGenerationModel(model) {
			req.UseUpstreamTokenCost = false
		}
		if c != nil {
			if image, known := getOpenAIImageIntentHint(c); known && image {
				req.UseUpstreamTokenCost = false
			}
		}
	}
	if c != nil && c.Request != nil && strings.EqualFold(c.Request.Header.Get("Upgrade"), "websocket") {
		identity := priorityObservationFromContext(c.Request.Context(), model)
		req.HistoryModel = identity.model
		req.RequestedModel = strings.TrimSpace(model)
		if forward, found := openAIForwardModelFromContext(c.Request.Context()); found {
			req.RequestedModel = forward.model
		}
		if identity.hasGroup {
			id := identity.groupID
			req.GroupID = &id
		}
	}
	if kind, ok := ctx.Value(priorityLifecycleRequestTypeKey{}).(RequestType); ok {
		req.LifecycleRequestType = kind
	}
	if c != nil && c.Request != nil && strings.EqualFold(c.Request.Header.Get("Upgrade"), "websocket") {
		req.LifecycleRequestType = RequestTypeWSV2
	}
	if !selectedRequest && req.priorityLatencyPool == nil && priorityHardStopApplies(s.prioritySchedulingRuntimeConfig(), req) && (s.accountRepo != nil || s.schedulerSnapshot != nil) {
		scheduler := &defaultOpenAIAccountScheduler{service: s, stats: s.openaiAccountStats}
		prepared, err := scheduler.preparePriorityLatencyRequest(ctx, req)
		if err != nil {
			return ctx, err
		}
		req = prepared
	}
	if err := s.priorityLatencyAdmission(req, account); err != nil {
		return ctx, err
	}
	if account == nil {
		return ctx, nil
	}
	if req.priorityDecisionID == "" {
		req.priorityDecisionID = newPriorityLifecycleDecisionID()
	}
	if kind, ok := ctx.Value(priorityLifecycleRequestTypeKey{}).(RequestType); ok {
		req.LifecycleRequestType = kind
	}
	if c != nil && c.Request != nil && strings.EqualFold(c.Request.Header.Get("Upgrade"), "websocket") {
		req.LifecycleRequestType = RequestTypeWSV2
	}
	return context.WithValue(ctx, priorityLatencyTurnKey{}, &priorityLatencyTurn{accountID: account.ID, request: req}), nil
}

// Subsequent WS turns get a new bounded decision, while repeated checks within
// one turn retain the original set. Ownership stays with the connection account.
func (s *OpenAIGatewayService) nextPriorityLatencyWSTurn(ctx context.Context, c *gin.Context, account *Account, model string) (context.Context, error) {
	ctx = context.WithValue(ctx, priorityLatencyTurnKey{}, (*priorityLatencyTurn)(nil))
	if req, ok := ctx.Value(prioritySelectionRequestKey{}).(OpenAIAccountScheduleRequest); ok {
		req.priorityLatencyPool = nil
		req.priorityDecisionID = newPriorityLifecycleDecisionID()
		req.RequestedModel = strings.TrimSpace(model)
		if c != nil && c.Request != nil {
			identity := priorityObservationFromContext(c.Request.Context(), model)
			req.HistoryModel = identity.model
			if forward, found := openAIForwardModelFromContext(c.Request.Context()); found {
				req.RequestedModel = forward.model
			}
		}
		if priorityHardStopApplies(s.prioritySchedulingRuntimeConfig(), req) && (s.accountRepo != nil || s.schedulerSnapshot != nil) {
			scheduler := &defaultOpenAIAccountScheduler{service: s, stats: s.openaiAccountStats}
			prepared, err := scheduler.preparePriorityLatencyRequest(ctx, req)
			if err != nil {
				return ctx, err
			}
			req = prepared
		}
		ctx = context.WithValue(ctx, prioritySelectionRequestKey{}, req)
	}
	return s.beginPriorityLatencyTurn(ctx, c, account, model)
}
