package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/google/uuid"
	"log/slog"
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
	Sequence             int64                  `json:"sequence"`
	ParentID             string                 `json:"parent_decision_id"`
	TraceID              string                 `json:"trace_id,omitempty"`
	StatisticsGeneration *int64                 `json:"statistics_generation"`
	Watermark            *int64                 `json:"watermark"`
	ID                   string                 `json:"decision_id"`
	Scope                PriorityLifecycleScope `json:"scope"`
	PolicyRevision       string                 `json:"policy_revision"`
	SelectedAccountID    int64                  `json:"selected_account_id"`
	At                   time.Time              `json:"at"`
	Phase                string                 `json:"phase"`
	Allowed              bool                   `json:"allowed"`
	Reason               string                 `json:"reason"`
	Mode                 string                 `json:"mode"`
}

type priorityLifecycleRequestTypeKey struct{}

func WithPriorityLifecycleRequestType(ctx context.Context, kind RequestType) context.Context {
	return context.WithValue(ctx, priorityLifecycleRequestTypeKey{}, kind)
}
func newPriorityLifecycleDecisionID() string { return uuid.NewString() }

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
	stats, _, err := repo.ReadPriorityLifecycle(audit, scope)
	if err == nil && stats != nil {
		d.StatisticsGeneration = &stats.Generation
		d.Watermark = &stats.Watermark
	}
	if err == nil {
		err = repo.RecordPriorityLifecycleDecision(audit, d)
	}
	if err != nil {
		s.priorityLifecycle.mu.Lock()
		s.priorityLifecycle.failure = "audit_incomplete"
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
		go func() {
			defer close(s.priorityLifecycle.done)
			timer := time.NewTicker(5 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					cfg := s.prioritySchedulingRuntimeConfig()
					if cfg.LifecycleMode == "off" || !cfg.Enabled {
						continue
					}
					work, end := context.WithTimeout(ctx, 4*time.Second)
					// Transactional outbox retries on the next tick. GETs never
					// acknowledge events, refresh evidence or evaluate policy.
					err := repo.AdvancePriorityLifecycle(work, cfg)
					end()
					s.priorityLifecycle.mu.Lock()
					s.priorityLifecycle.attempted = time.Now()
					s.priorityLifecycle.failure = ""
					if err != nil {
						s.priorityLifecycle.failure = "refresh_failed"
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
	if cfg.LifecycleMode == "off" {
		out.Mode = "off"
	}
	s.priorityLifecycle.mu.Lock()
	out.LastError, out.AttemptedAt = s.priorityLifecycle.failure, s.priorityLifecycle.attempted
	s.priorityLifecycle.mu.Unlock()
	byID := make(map[int64]PriorityLifecyclePolicy)
	for _, p := range policies {
		byID[p.AccountID] = p
	}
	for _, a := range accounts {
		row := PriorityLifecycleAccountState{AccountID: a.ID, AccountName: a.Name, Configuration: "ENABLED", Admission: "NOT_EVALUATED", Reasons: []string{"shadow_only; execution remains compatibility_v1"}, Content: "NO_OBSERVATIONS", Freshness: "UNKNOWN", Refresh: "IDLE"}
		if !a.Schedulable || a.Status != StatusActive {
			row.Configuration = "ADMIN_DISABLED"
			row.Admission = "BLOCKED"
			row.Reasons = append(row.Reasons, "account_not_schedulable")
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
			if !now.Before(p.ExpiresAt) {
				p.State = "RECOVERY_SHADOW"
			}
			row.Policy = &p
		}
		if out.LastError != "" {
			row.Refresh = "ERROR"
		}
		out.Accounts = append(out.Accounts, row)
	}
	return out, nil
}

func (s *OpenAIGatewayService) PriorityLifecycleHistory(ctx context.Context, scope PriorityLifecycleScope, after int64, events bool) (any, error) {
	repo, ok := s.usageLogRepo.(PriorityLifecycleRepository)
	if !ok {
		return nil, errors.New("lifecycle storage unavailable")
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
