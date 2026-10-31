package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/generationwrite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// V2 remains shadow until the unresolved aggregate trigger/renewal, recovery,
// freshness and fallback contracts are approved. It never sends extra requests
// or adds a second enforcing penalty engine.
type PriorityLifecycleScope struct {
	GroupID           *int64      `json:"group_id"`
	Model             string      `json:"model"`
	Endpoint          string      `json:"endpoint"`
	RequestType       RequestType `json:"request_type"`
	MeasurementSchema string      `json:"measurement_schema"`
	StatisticsSpec    string      `json:"statistics_spec"`
}

func PriorityLifecycleVersions(c PrioritySchedulingConfig) (measurement, statistics, policy string) {
	measurement = "ingress_alias_v1/" + c.LatencyMeasurement
	statistics = priorityLifecycleHash(struct {
		Window, QualityHours, Percentile, Threshold int
		Measurement                                 string
	}{c.WindowMinutes, c.QualityMaxAgeHours, c.LatencyPercentile, c.TargetTTFTMs, measurement})
	policy = priorityConfigRevision(c)
	return
}

func priorityLifecycleHash(v any) string {
	b, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func PriorityLifecycleEndpoint(raw string) string {
	switch strings.TrimSuffix(strings.TrimSpace(raw), "/") {
	case "/responses", "/v1/responses":
		return "/v1/responses"
	case "/chat/completions", "/v1/chat/completions":
		return "/v1/chat/completions"
	case "/messages", "/v1/messages":
		return "/v1/messages"
	default:
		return "unknown"
	}
}

func (s PriorityLifecycleScope) Key() string { return priorityLifecycleHash(s) }
func (s PriorityLifecycleScope) Identity() string {
	b, _ := json.Marshal(struct {
		GroupID     *int64      `json:"group_id"`
		Model       string      `json:"model"`
		Endpoint    string      `json:"endpoint"`
		RequestType RequestType `json:"request_type"`
	}{s.GroupID, s.Model, s.Endpoint, s.RequestType})
	return string(b)
}

// Penalties and audit identity survive statistical-spec and configuration changes.
func (s PriorityLifecycleScope) PolicyKey() string {
	s.StatisticsSpec = ""
	return priorityLifecycleHash(s)
}

type PriorityLifecycleStatistics struct {
	CollectedThrough time.Time                          `json:"collected_through"`
	PublishedAt      time.Time                          `json:"published_at"`
	SourceRevision   int64                              `json:"source_revision"`
	SourceVisibility string                             `json:"source_visibility"`
	RefreshState     string                             `json:"refresh_state"`
	LastError        string                             `json:"last_error"`
	PolicyRevision   string                             `json:"policy_revision"`
	Scope            PriorityLifecycleScope             `json:"scope"`
	Generation       int64                              `json:"generation"`
	Watermark        int64                              `json:"watermark"`
	WindowStart      time.Time                          `json:"window_start"`
	WindowEnd        time.Time                          `json:"window_end"`
	ObservedAt       time.Time                          `json:"observed_at"`
	Signals          map[int64]PrioritySchedulingSignal `json:"signals"`
}

type PriorityLifecyclePolicy struct {
	AccountID      int64     `json:"account_id"`
	State          string    `json:"state"`
	EvidenceID     string    `json:"evidence_id"`
	EvidenceAt     time.Time `json:"evidence_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	PolicyRevision string    `json:"policy_revision"`
	ObservedValue  *float64  `json:"observed_value"`
	Threshold      int       `json:"threshold"`
	Percentile     int       `json:"percentile"`
	Mode           string    `json:"mode"`
	Rule           string    `json:"rule"`
}

type PriorityLifecycleEvent struct {
	Sequence   int64                   `json:"sequence"`
	Kind       string                  `json:"kind"`
	EventAt    time.Time               `json:"event_at"`
	RecordedAt time.Time               `json:"recorded_at"`
	Policy     PriorityLifecyclePolicy `json:"policy"`
}

type PriorityLifecycleDecision struct {
	Fallback             string                          `json:"fallback"`
	FallbackMembers      []int64                         `json:"fallback_members"`
	Catalog              []PriorityLifecycleCatalogEntry `json:"catalog"`
	Outcome              string                          `json:"outcome,omitempty"`
	HTTPStatus           int                             `json:"http_status,omitempty"`
	AttemptCount         int                             `json:"attempt_count,omitempty"`
	Canceled             bool                            `json:"canceled"`
	Transport            string                          `json:"transport"`
	PhysicalWrite        string                          `json:"physical_write"`
	ConsumedSource       string                          `json:"consumed_source"`
	ConsumedRevision     string                          `json:"consumed_revision"`
	ConsumedObservedAt   *time.Time                      `json:"consumed_observed_at"`
	ConsumedWindowEnd    *time.Time                      `json:"consumed_window_end"`
	Candidates           []PrioritySchedulingScore       `json:"candidates"`
	ExcludedAccountIDs   []int64                         `json:"excluded_account_ids"`
	Sequence             int64                           `json:"sequence"`
	ParentID             string                          `json:"parent_decision_id"`
	TraceID              string                          `json:"trace_id,omitempty"`
	StatisticsGeneration *int64                          `json:"statistics_generation"`
	Watermark            *int64                          `json:"watermark"`
	ID                   string                          `json:"decision_id"`
	Scope                PriorityLifecycleScope          `json:"scope"`
	PolicyRevision       string                          `json:"policy_revision"`
	SelectedAccountID    int64                           `json:"selected_account_id"`
	At                   time.Time                       `json:"at"`
	Phase                string                          `json:"phase"`
	Allowed              bool                            `json:"allowed"`
	Reason               string                          `json:"reason"`
	Mode                 string                          `json:"mode"`
}
type PriorityLifecycleCatalogEntry struct {
	AccountID     int64    `json:"account_id"`
	Configuration string   `json:"configuration"`
	Exclusions    []string `json:"exclusions"`
}
type priorityLifecycleConsumed struct {
	mu                  sync.Mutex
	config              PrioritySchedulingConfig
	generation          uint64
	digest              string
	observed, windowEnd *time.Time
	candidates          []PrioritySchedulingScore
	catalog             []PriorityLifecycleCatalogEntry
}

func (s *OpenAIGatewayService) capturePriorityLifecycleCatalog(ctx context.Context, req OpenAIAccountScheduleRequest) {
	if req.priorityConsumed == nil || s.accountRepo == nil {
		return
	}
	if _, ok := s.usageLogRepo.(PriorityLifecycleRepository); !ok {
		return
	}
	catalog, ok := s.accountRepo.(PriorityLifecycleAccountCatalog)
	if !ok {
		return
	}
	rows, err := catalog.ListPriorityLifecycleAccounts(ctx)
	if err != nil {
		return
	}
	values := make([]PriorityLifecycleCatalogEntry, 0, len(rows))
	for _, a := range rows {
		value := PriorityLifecycleCatalogEntry{AccountID: a.ID, Configuration: "ENABLED", Exclusions: []string{}}
		if !a.IsSchedulable() {
			value.Configuration = "DISABLED_OR_BLOCKED"
			value.Exclusions = append(value.Exclusions, "not_schedulable")
		}
		if !s.openAIAccountMatchesSchedulingGroup(&a, req.GroupID) {
			value.Exclusions = append(value.Exclusions, "group_membership")
		}
		if _, excluded := req.ExcludedIDs[a.ID]; excluded {
			value.Exclusions = append(value.Exclusions, "request_excluded")
		}
		if !a.SupportsOpenAIEndpointCapability(req.RequiredCapability) {
			value.Exclusions = append(value.Exclusions, "capability")
		}
		values = append(values, value)
	}
	req.priorityConsumed.mu.Lock()
	req.priorityConsumed.catalog = values
	req.priorityConsumed.mu.Unlock()
}

type priorityLifecycleRequestTypeKey struct{}
type priorityLifecycleHTTPDeferredKey struct{}
type priorityLifecyclePrimaryReadKey struct{}

func (s *OpenAIGatewayService) guardPriorityGenerationHTTP(req *http.Request, account *Account) *http.Request {
	turn, _ := req.Context().Value(priorityLatencyTurnKey{}).(*priorityLatencyTurn)
	if turn == nil || account == nil {
		return req
	}
	original := req.Context()
	deferred := context.WithValue(original, priorityLifecycleHTTPDeferredKey{}, true)
	req = req.WithContext(deferred)
	return generationwrite.With(req, func(ctx context.Context) error {
		physical := context.WithValue(ctx, priorityLifecycleHTTPDeferredKey{}, false)
		physical = context.WithValue(physical, priorityLifecyclePrimaryReadKey{}, true)
		turn.mu.Lock()
		request := turn.request
		started := turn.started
		turn.mu.Unlock()
		if started {
			return nil
		}
		group, enforce := int64(0), request.GroupID != nil
		if enforce {
			group = *request.GroupID
		}
		if _, err := s.admitOpenAITurnForGroup(physical, group, enforce, account, request.RequestedModel); err != nil {
			return err
		}
		if err := s.fenceAuthConnectionEgress(physical, req, account); err != nil {
			return err
		}
		return s.priorityLatencyBeforeSend(physical, account)
	})
}

type priorityLifecyclePluginBoundaryKey struct{}
type priorityLifecyclePluginBoundary struct {
	before func(context.Context) error
	after  func(error)
}

func (s *OpenAIGatewayService) priorityLifecyclePluginRequest(req *http.Request, account *Account) *http.Request {
	boundary := priorityLifecyclePluginBoundary{
		before: func(ctx context.Context) error {
			if err := s.fenceAuthConnectionEgress(ctx, req, account); err != nil {
				return err
			}
			if turn, _ := ctx.Value(priorityLatencyTurnKey{}).(*priorityLatencyTurn); turn != nil {
				turn.mu.Lock()
				started := turn.started
				request := turn.request
				turn.mu.Unlock()
				if !started {
					if err := s.priorityLatencyAdmission(request, account); err != nil {
						return err
					}
				}
			}
			return s.priorityLatencyBeforeSend(ctx, account)
		},
		after: func(err error) { s.priorityLifecycleWriteResult(req.Context(), account, "plugin", err) },
	}
	return req.WithContext(context.WithValue(req.Context(), priorityLifecyclePluginBoundaryKey{}, boundary))
}

func WithPriorityLifecycleRequestType(ctx context.Context, kind RequestType) context.Context {
	return context.WithValue(ctx, priorityLifecycleRequestTypeKey{}, kind)
}
func newPriorityLifecycleDecisionID() string { return uuid.NewString() }

// Nonblocking in-memory publication only. It never inserts DB/network I/O in
// the last admission -> generation-write interval. Missing records are explicit
// coverage loss, never silently promoted to a successful physical send.
func (s *OpenAIGatewayService) priorityLifecycleWriteResult(ctx context.Context, account *Account, transport string, writeErr error) {
	if s == nil || account == nil {
		return
	}
	turn, _ := ctx.Value(priorityLatencyTurnKey{}).(*priorityLatencyTurn)
	if turn == nil {
		return
	}
	turn.mu.Lock()
	if !IsOpenAITurnAdmissionError(writeErr) {
		turn.started = true
	} // uncertain writes never replay
	req := turn.request
	registerOutcome := !turn.outcomeRegistered
	turn.outcomeRegistered = true
	turn.mu.Unlock()
	cfg := s.prioritySchedulingRuntimeConfig()
	if !cfg.Enabled || cfg.LifecycleMode == "off" {
		return
	}
	measurement, spec, revision := PriorityLifecycleVersions(cfg)
	event := PriorityLifecycleDecision{ID: uuid.NewString(), ParentID: req.priorityDecisionID, TraceID: requesttiming.From(ctx).Identity(), Scope: PriorityLifecycleScope{GroupID: req.GroupID, Model: priorityHistoryModel(req), Endpoint: PriorityLifecycleEndpoint(req.InboundEndpoint), RequestType: req.LifecycleRequestType, MeasurementSchema: measurement, StatisticsSpec: spec}, PolicyRevision: revision, SelectedAccountID: account.ID, At: time.Now().UTC(), Phase: "SUBMITTED", Allowed: true, Mode: "compatibility_v1/shadow_v2", Transport: transport, PhysicalWrite: "attempted", ConsumedSource: "compatibility_admission"}
	if writeErr != nil {
		event.Phase = "SUBMISSION_UNCERTAIN"
		event.Reason = "write_error_no_replay"
		if IsOpenAITurnAdmissionError(writeErr) {
			event.Phase = "PREWRITE_BLOCKED"
			event.PhysicalWrite = "zero"
			event.Allowed = false
			event.Reason = "admission_denied"
		}
	}
	select {
	case s.priorityLifecycle.audit <- event:
	default:
		s.priorityLifecycle.mu.Lock()
		s.priorityLifecycle.failure = "audit_queue_overflow"
		s.priorityLifecycle.auditLost++
		s.priorityLifecycle.mu.Unlock()
	}
	if registerOutcome {
		if collector := requesttiming.From(ctx); collector != nil {
			collector.WhenFinished(func(snapshot requesttiming.Snapshot) {
				final := event
				final.ID = uuid.NewString()
				final.At = time.Now().UTC()
				final.Phase = "OUTCOME"
				final.Outcome = snapshot.Outcome
				final.HTTPStatus = snapshot.Status
				final.AttemptCount = len(snapshot.Attempts)
				final.Canceled = snapshot.Canceled
				select {
				case s.priorityLifecycle.audit <- final:
				default:
					s.priorityLifecycle.mu.Lock()
					s.priorityLifecycle.auditLost++
					s.priorityLifecycle.failure = "audit_queue_overflow"
					s.priorityLifecycle.mu.Unlock()
				}
			})
		}
	}
}

// The compatibility executor remains authoritative. A failed shadow audit is
// visible as incomplete coverage and cannot create a new enforcing penalty.
func (s *OpenAIGatewayService) recordPriorityLifecycleDecision(ctx context.Context, req OpenAIAccountScheduleRequest, accountID int64, phase string, denied error) {
	if s == nil {
		return
	}
	repo, ok := s.usageLogRepo.(PriorityLifecycleRepository)
	if !ok {
		return
	}
	cfg := s.prioritySchedulingRuntimeConfig()
	if !cfg.Enabled || cfg.LifecycleMode == "off" {
		return
	}
	measurement, spec, revision := PriorityLifecycleVersions(cfg)
	scope := PriorityLifecycleScope{GroupID: req.GroupID, Model: priorityHistoryModel(req), Endpoint: PriorityLifecycleEndpoint(req.InboundEndpoint), RequestType: req.LifecycleRequestType, MeasurementSchema: measurement, StatisticsSpec: spec}
	d := PriorityLifecycleDecision{ID: uuid.NewString(), ParentID: req.priorityDecisionID, Scope: scope, SelectedAccountID: accountID, At: time.Now().UTC(), Phase: phase, Allowed: denied == nil, Mode: "compatibility_v1/shadow_v2", PolicyRevision: revision}
	d.TraceID = requesttiming.From(ctx).Identity()
	d.FallbackMembers = []int64{}
	if pool := req.priorityLatencyPool; pool != nil && pool.fallbackActive() {
		d.Fallback = "LATENCY_FALLBACK"
		if frozen := pool.fallback.Load(); frozen != nil {
			for id := range frozen.allowed {
				d.FallbackMembers = append(d.FallbackMembers, id)
			}
		}
	}
	d.ConsumedSource = "not_observed"
	d.Candidates = []PrioritySchedulingScore{}
	d.ExcludedAccountIDs = []int64{}
	if req.priorityConsumed != nil {
		consumed := req.priorityConsumed
		consumed.mu.Lock()
		if consumed.digest != "" {
			cfg = consumed.config
			d.PolicyRevision = priorityConfigRevision(cfg)
			d.ConsumedSource = "compatibility_cache"
			d.ConsumedRevision = fmt.Sprintf("%d/%s", consumed.generation, consumed.digest)
			d.ConsumedObservedAt, d.ConsumedWindowEnd = consumed.observed, consumed.windowEnd
			d.Candidates = append(d.Candidates, consumed.candidates...)
		}
		d.Catalog = append([]PriorityLifecycleCatalogEntry{}, consumed.catalog...)
		consumed.mu.Unlock()
	}
	for id := range req.ExcludedIDs {
		d.ExcludedAccountIDs = append(d.ExcludedAccountIDs, id)
	}
	if denied != nil {
		d.Reason = "admission_denied"
		var admission *OpenAITurnAdmissionError
		if errors.As(denied, &admission) {
			d.Reason = admission.Reason
		}
	}
	audit, end := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Millisecond)
	defer end()
	if registrar, ok := repo.(PriorityLifecycleScopeRegistrar); ok {
		if err := registrar.RegisterPriorityLifecycleScope(audit, scope); err != nil {
			slog.Warn("priority_lifecycle_scope_registration_failed")
		}
	}
	// No post-selection read may masquerade as the snapshot consumed by the
	// compatibility scorer. Durable shadow generation is intentionally null.
	err := repo.RecordPriorityLifecycleDecision(audit, d)
	if err != nil {
		s.priorityLifecycle.mu.Lock()
		s.priorityLifecycle.failure = "audit_incomplete"
		s.priorityLifecycle.auditLost++
		s.priorityLifecycle.mu.Unlock()
		slog.Warn("priority_lifecycle_audit_incomplete", "phase", phase)
	}
}

type PriorityLifecycleRepository interface {
	AdvancePriorityLifecycle(context.Context, PrioritySchedulingConfig) error
	ReadPriorityLifecycle(context.Context, PriorityLifecycleScope) (*PriorityLifecycleStatistics, []PriorityLifecyclePolicy, error)
	RecordPriorityLifecycleDecision(context.Context, PriorityLifecycleDecision) error
	ReadPriorityLifecycleDecisions(context.Context, PriorityLifecycleScope, int64) ([]PriorityLifecycleDecision, error)
	ReadPriorityLifecycleEvents(context.Context, PriorityLifecycleScope, int64) ([]PriorityLifecycleEvent, error)
}
type PriorityLifecycleScopeRegistrar interface {
	RegisterPriorityLifecycleScope(context.Context, PriorityLifecycleScope) error
}
type PriorityLifecycleRefreshJob struct {
	ID             string                 `json:"job_id"`
	Scope          PriorityLifecycleScope `json:"scope"`
	TargetRevision int64                  `json:"target_revision"`
	QueuedAt       time.Time              `json:"queued_at"`
	CompletedAt    *time.Time             `json:"completed_at"`
	Generation     *int64                 `json:"generation"`
}
type PriorityLifecycleRefreshRepository interface {
	QueuePriorityLifecycleRefresh(context.Context, PriorityLifecycleScope) (*PriorityLifecycleRefreshJob, error)
	ReadPriorityLifecycleRefresh(context.Context, string) (*PriorityLifecycleRefreshJob, error)
}

func (s *OpenAIGatewayService) QueuePriorityLifecycleRefresh(ctx context.Context, scope PriorityLifecycleScope) (*PriorityLifecycleRefreshJob, error) {
	repo, ok := s.usageLogRepo.(PriorityLifecycleRefreshRepository)
	if !ok {
		return nil, errors.New("refresh queue unavailable")
	}
	cfg, err := s.settingService.GetPrioritySchedulingConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || cfg.LifecycleMode == "off" {
		return nil, errors.New("lifecycle collection disabled")
	}
	scope.MeasurementSchema, scope.StatisticsSpec, _ = PriorityLifecycleVersions(cfg)
	return repo.QueuePriorityLifecycleRefresh(ctx, scope)
}
func (s *OpenAIGatewayService) ReadPriorityLifecycleRefresh(ctx context.Context, id string) (*PriorityLifecycleRefreshJob, error) {
	repo, ok := s.usageLogRepo.(PriorityLifecycleRefreshRepository)
	if !ok {
		return nil, errors.New("refresh queue unavailable")
	}
	return repo.ReadPriorityLifecycleRefresh(ctx, id)
}

type PriorityLifecycleCoverageRepository interface {
	RecordPriorityLifecycleCoverage(context.Context, string, string) error
	ReadPriorityLifecycleCoverage(context.Context) (map[string]string, error)
}

// Management inventory is deliberately separate from active-only routing reads.
// A disabled/error account must remain visible with its immutable policy evidence.
type PriorityLifecycleAccountCatalog interface {
	ListPriorityLifecycleAccounts(context.Context) ([]Account, error)
}

type priorityLifecycleRuntime struct {
	mu        sync.Mutex
	failure   string
	attempted time.Time
	once      sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
	audit     chan PriorityLifecycleDecision
	auditLost uint64
}

func (s *OpenAIGatewayService) startPriorityLifecycle() {
	repo, ok := s.usageLogRepo.(PriorityLifecycleRepository)
	if !ok || s.settingService == nil {
		return
	}
	s.priorityLifecycle.once.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.priorityLifecycle.cancel = cancel
		s.priorityLifecycle.done = make(chan struct{})
		s.priorityLifecycle.audit = make(chan PriorityLifecycleDecision, 256)
		go func() {
			defer close(s.priorityLifecycle.done)
			timer := time.NewTicker(5 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-s.priorityLifecycle.audit:
					work, end := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
					err := repo.RecordPriorityLifecycleDecision(work, event)
					if err != nil {
						if coverage, ok := repo.(PriorityLifecycleCoverageRepository); ok {
							_ = coverage.RecordPriorityLifecycleCoverage(work, "audit", "write_failed")
						}
					}
					end()
				case <-timer.C:
					cfg := s.prioritySchedulingRuntimeConfig()
					if cfg.LifecycleMode == "off" || !cfg.Enabled {
						continue
					}
					work, end := context.WithTimeout(ctx, 4*time.Second)
					// Transactional outbox retries on the next tick. GETs never
					// acknowledge events, refresh evidence or evaluate policy.
					err := repo.AdvancePriorityLifecycle(work, cfg)
					if coverage, ok := repo.(PriorityLifecycleCoverageRepository); ok {
						s.priorityLifecycle.mu.Lock()
						lost := s.priorityLifecycle.auditLost
						s.priorityLifecycle.mu.Unlock()
						if lost > 0 {
							_ = coverage.RecordPriorityLifecycleCoverage(work, "audit", "queue_overflow")
						}
					}
					end()
					s.priorityLifecycle.mu.Lock()
					s.priorityLifecycle.attempted = time.Now()
					if err != nil {
						slog.Warn("priority_lifecycle_refresh_failed", "reason", "query_failed")
					}
					s.priorityLifecycle.mu.Unlock()
				}
			}
		}()
	})
}

type PriorityLifecycleAccountState struct {
	AccountID     int64                     `json:"account_id"`
	AccountName   string                    `json:"account_name"`
	Configuration string                    `json:"configuration"`
	Admission     string                    `json:"admission"`
	Reasons       []string                  `json:"reasons"`
	Content       string                    `json:"content"`
	Freshness     string                    `json:"freshness"`
	Refresh       string                    `json:"refresh"`
	Signal        *PrioritySchedulingSignal `json:"signal"`
	Policy        *PriorityLifecyclePolicy  `json:"policy"`
}

type PriorityLifecycleState struct {
	Coverage       map[string]string               `json:"coverage"`
	Scope          PriorityLifecycleScope          `json:"scope"`
	ServerNow      time.Time                       `json:"server_now"`
	PolicyRevision string                          `json:"policy_revision"`
	Mode           string                          `json:"mode"`
	Statistics     *PriorityLifecycleStatistics    `json:"statistics"`
	LastError      string                          `json:"last_error"`
	AttemptedAt    time.Time                       `json:"attempted_at"`
	Accounts       []PriorityLifecycleAccountState `json:"accounts"`
}

// Pure primary reads, never scheduling SQL refresh or changing policy/expiry.
// Account membership comes from the authoritative catalog, not lastpool.
func (s *OpenAIGatewayService) PriorityLifecycleState(ctx context.Context, scope PriorityLifecycleScope) (*PriorityLifecycleState, error) {
	repo, ok := s.usageLogRepo.(PriorityLifecycleRepository)
	if !ok || s.accountRepo == nil {
		return nil, errors.New("lifecycle storage unavailable")
	}
	cfg, err := s.settingService.GetPrioritySchedulingConfig(ctx)
	if err != nil {
		return nil, err
	}
	scope.MeasurementSchema, scope.StatisticsSpec, _ = PriorityLifecycleVersions(cfg)
	stats, policies, err := repo.ReadPriorityLifecycle(ctx, scope)
	if err != nil {
		return nil, err
	}
	catalog, ok := s.accountRepo.(PriorityLifecycleAccountCatalog)
	if !ok {
		return nil, errors.New("complete lifecycle account catalog unavailable")
	}
	accounts, err := catalog.ListPriorityLifecycleAccounts(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := &PriorityLifecycleState{Scope: scope, ServerNow: now, PolicyRevision: priorityConfigRevision(cfg), Mode: "shadow", Statistics: stats, Accounts: make([]PriorityLifecycleAccountState, 0, len(accounts))}
	if coverage, ok := repo.(PriorityLifecycleCoverageRepository); ok {
		out.Coverage, err = coverage.ReadPriorityLifecycleCoverage(ctx)
		if err != nil {
			return nil, err
		}
	}
	if cfg.LifecycleMode == "off" {
		out.Mode = "off"
	}
	s.priorityLifecycle.mu.Lock()
	out.AttemptedAt = s.priorityLifecycle.attempted
	if s.priorityLifecycle.failure != "" {
		if out.Coverage == nil {
			out.Coverage = make(map[string]string)
		}
		out.Coverage["audit_local"] = s.priorityLifecycle.failure
	}
	s.priorityLifecycle.mu.Unlock()
	byID := make(map[int64]PriorityLifecyclePolicy)
	for _, p := range policies {
		byID[p.AccountID] = p
	}
	for _, a := range accounts {
		row := PriorityLifecycleAccountState{AccountID: a.ID, AccountName: a.Name, Configuration: "ENABLED", Admission: "NOT_EVALUATED", Reasons: []string{"shadow_only; execution remains compatibility_v1"}, Content: "NO_OBSERVATIONS", Freshness: "UNKNOWN", Refresh: "IDLE"}
		if !a.Schedulable || a.Status == StatusDisabled {
			row.Configuration = "ADMIN_DISABLED"
			row.Admission = "BLOCKED"
			row.Reasons = append(row.Reasons, "account_not_schedulable")
		}
		if a.Status != StatusActive && a.Status != StatusDisabled {
			row.Admission = "BLOCKED"
			row.Reasons = append(row.Reasons, "account_status_"+a.Status)
		}
		if scope.GroupID != nil {
			member := false
			for _, id := range a.GroupIDs {
				if id == *scope.GroupID {
					member = true
					break
				}
			}
			if !member {
				row.Admission = "BLOCKED"
				row.Reasons = append(row.Reasons, "group_membership")
			}
		}
		if stats != nil {
			row.Freshness = "STALE"
			if !stats.CollectedThrough.IsZero() && now.Sub(stats.CollectedThrough) <= priorityHistoryTTL && stats.RefreshState == "IDLE" && stats.LastError == "" {
				row.Freshness = "FRESH"
			}
			row.Refresh = stats.RefreshState
			if stats.LastError != "" {
				out.LastError = stats.LastError
				row.Refresh = "ERROR"
			}
			if signal, found := stats.Signals[a.ID]; found {
				row.Signal = &signal
				if signal.Samples > 0 {
					row.Content = "INSUFFICIENT"
					if signal.Samples >= cfg.MinSamples {
						row.Content = "VALID"
					}
				}
			}
		}
		if p, found := byID[a.ID]; found {
			row.Policy = &p
		}
		if out.LastError != "" {
			row.Refresh = "ERROR"
		}
		out.Accounts = append(out.Accounts, row)
	}
	// Deleted accounts keep their policy/evidence timeline and remain visible
	// as tombstones; absence from the live catalog must not erase a penalty.
	seen := make(map[int64]bool, len(accounts))
	for _, a := range accounts {
		seen[a.ID] = true
	}
	for _, p := range policies {
		if seen[p.AccountID] {
			continue
		}
		value := p
		out.Accounts = append(out.Accounts, PriorityLifecycleAccountState{AccountID: p.AccountID, Configuration: "DELETED", Admission: "BLOCKED", Reasons: []string{"account_deleted"}, Content: "NO_OBSERVATIONS", Freshness: "UNKNOWN", Refresh: "IDLE", Policy: &value})
	}
	return out, nil
}

func (s *OpenAIGatewayService) PriorityLifecycleHistory(ctx context.Context, scope PriorityLifecycleScope, after int64, events bool) (any, error) {
	repo, ok := s.usageLogRepo.(PriorityLifecycleRepository)
	if !ok {
		return nil, errors.New("lifecycle storage unavailable")
	}
	cfg, err := s.settingService.GetPrioritySchedulingConfig(ctx)
	if err != nil {
		return nil, err
	}
	if scope.MeasurementSchema == "" {
		scope.MeasurementSchema, scope.StatisticsSpec, _ = PriorityLifecycleVersions(cfg)
	}
	if events {
		return repo.ReadPriorityLifecycleEvents(ctx, scope, after)
	}
	return repo.ReadPriorityLifecycleDecisions(ctx, scope, after)
}

func (s *OpenAIGatewayService) stopPriorityLifecycle() {
	if s.priorityLifecycle.cancel != nil {
		s.priorityLifecycle.cancel()
		<-s.priorityLifecycle.done
	}
}
