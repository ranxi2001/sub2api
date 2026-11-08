package service

import (
	"context"
	"errors"
	"math"
	"regexp"
	"time"
)

var ErrAIOpsConflict = errors.New("AI_OPS_CONFLICT")
var ErrAIOpsBlocked = errors.New("AI_OPS_BLOCKED")
var ErrAIOpsUnavailable = errors.New("AI_OPS_UNAVAILABLE")

type AIOpsAccountScope struct {
	AccountID      int64  `json:"account_id"`
	CapacityDomain string `json:"capacity_domain"`
}

type AIOpsPolicy struct {
	Revision               int64               `json:"revision"`
	Mode                   string              `json:"mode"`
	Halted                 bool                `json:"halted"`
	GroupID                int64               `json:"group_id"`
	Accounts               []AIOpsAccountScope `json:"accounts"`
	MinHealthyAccounts     int                 `json:"min_healthy_accounts"`
	MinTotalConcurrency    int                 `json:"min_total_concurrency"`
	MinConcurrency         int                 `json:"min_concurrency"`
	MaxConcurrency         int                 `json:"max_concurrency"`
	MaxStepPercent         float64             `json:"max_step_percent"`
	DailyActionLimit       int                 `json:"daily_action_limit"`
	CooldownSeconds        int                 `json:"cooldown_seconds"`
	ObservationSeconds     int                 `json:"observation_seconds"`
	EvidenceMaxAgeSeconds  int                 `json:"evidence_max_age_seconds"`
	MinSamples             int                 `json:"min_samples"`
	ErrorRateThreshold     float64             `json:"error_rate_threshold"`
	RollbackErrorRateDelta float64             `json:"rollback_error_rate_delta"`
	AutoActions            []string            `json:"auto_actions"`
	MaxPauseSeconds        int                 `json:"max_pause_seconds"`
}

type AIOpsEvidenceSnapshot struct {
	ObservedAt            time.Time `json:"observed_at"`
	WindowStart           time.Time `json:"window_start"`
	WindowEnd             time.Time `json:"window_end"`
	Samples               int       `json:"samples"`
	ErrorRate             float64   `json:"error_rate"`
	Inflight              int       `json:"inflight"`
	EligibleAccounts      int       `json:"eligible_accounts"`
	TotalConcurrency      int       `json:"total_concurrency"`
	ModelCoverageComplete bool      `json:"model_coverage_complete"`
	ProbePassed           bool      `json:"probe_passed"`
	Model                 string    `json:"model"`
	Effort                string    `json:"effort"`
	Protocol              string    `json:"protocol"`
	InputBucket           string    `json:"input_bucket,omitempty"`
	CacheBucket           string    `json:"cache_bucket,omitempty"`
	OutputBucket          string    `json:"output_bucket,omitempty"`
	EligibleAccountIDs    []int64   `json:"-"`
}

// ComparableAIOpsEvidence requires a complete, identical request cohort. Legacy
// snapshots without bucket metadata cannot prove an effect or authorize a write.
func ComparableAIOpsEvidence(before, after AIOpsEvidenceSnapshot) bool {
	return before.ModelCoverageComplete && after.ModelCoverageComplete &&
		before.Model != "" && before.Effort != "" && before.Protocol != "" &&
		before.InputBucket != "" && before.CacheBucket != "" && before.OutputBucket != "" &&
		before.Model == after.Model && before.Effort == after.Effort && before.Protocol == after.Protocol &&
		before.InputBucket == after.InputBucket && before.CacheBucket == after.CacheBucket && before.OutputBucket == after.OutputBucket
}

type AIOpsPreviewInput struct {
	RunID              string    `json:"run_id"`
	ProposalIndex      int       `json:"proposal_index"`
	Action             string    `json:"action"`
	AccountID          int64     `json:"account_id"`
	ProposedValue      *float64  `json:"proposed_value"`
	EvidenceObservedAt time.Time `json:"evidence_observed_at"`
}

type AIOpsProposal struct {
	ID             string                `json:"id"`
	RunID          string                `json:"run_id"`
	ProposalIndex  int                   `json:"proposal_index"`
	Action         string                `json:"action"`
	AccountID      int64                 `json:"account_id"`
	ProposedValue  *float64              `json:"proposed_value"`
	BeforeValue    *float64              `json:"before_value"`
	PolicyRevision int64                 `json:"policy_revision"`
	TargetRevision int64                 `json:"target_revision"`
	Digest         string                `json:"digest"`
	ExpiresAt      time.Time             `json:"expires_at"`
	CreatedAt      time.Time             `json:"created_at"`
	Status         string                `json:"status"`
	CancelledAt    *time.Time            `json:"cancelled_at,omitempty"`
	CancelledBy    int64                 `json:"cancelled_by,omitempty"`
	BlockedReasons []string              `json:"blocked_reasons"`
	Evidence       AIOpsEvidenceSnapshot `json:"evidence"`
	ImpactGroupIDs []int64               `json:"impact_group_ids"`
}

type AIOpsAction struct {
	ID                   string    `json:"id"`
	ProposalID           string    `json:"proposal_id"`
	RunID                string    `json:"run_id"`
	Action               string    `json:"action"`
	AccountID            int64     `json:"account_id"`
	BeforeValue          *float64  `json:"before_value"`
	AfterValue           *float64  `json:"after_value"`
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	ObserveUntil         time.Time `json:"observe_until"`
	ObservationStartedAt time.Time `json:"observation_started_at"`
	OwnerRevision        int64     `json:"owner_revision"`
	TargetRevision       int64     `json:"target_revision"`
	ReasonCode           string    `json:"reason_code"`
	Auto                 bool      `json:"auto"`
}

type AIOpsRuntime struct {
	Paused      bool
	Concurrency int
	LoadFactor  *int
	Revision    int64
}

type AIOpsProbe struct {
	ID             string     `json:"id"`
	AccountID      int64      `json:"account_id"`
	Model          string     `json:"model"`
	Effort         string     `json:"effort"`
	Protocol       string     `json:"protocol"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	OwnerRevision  int64      `json:"owner_revision"`
	TargetRevision int64      `json:"target_revision"`
	ReasonCode     string     `json:"reason_code"`
}

type AIOpsRepository interface {
	AIOpsPolicy(context.Context) (AIOpsPolicy, error)
	SaveAIOpsPolicy(context.Context, AIOpsPolicy, int64, int64) (AIOpsPolicy, error)
	HaltAIOps(context.Context, int64) (AIOpsPolicy, error)
	PreviewAIOps(context.Context, AIOpsPreviewInput, int64) (AIOpsProposal, error)
	ApplyAIOps(context.Context, string, string, int64, bool) (AIOpsAction, error)
	CancelAIOpsProposal(context.Context, string, string, int64) (AIOpsProposal, error)
	RollbackAIOps(context.Context, string, int64, int64) (AIOpsAction, error)
	ListAIOpsActions(context.Context) ([]AIOpsAction, error)
	ReconcileAIOps(context.Context) error
	AIOpsAutoTick(context.Context) error
	AIOpsRuntime(context.Context, []int64) (map[int64]AIOpsRuntime, error)
	ReserveAIOpsProbe(context.Context, int64, string, int64) (AIOpsProbe, error)
	CompleteAIOpsProbe(context.Context, string, bool, int64) (AIOpsProbe, error)
	ListAIOpsProbes(context.Context) ([]AIOpsProbe, error)
}

func DefaultAIOpsPolicy() AIOpsPolicy {
	return AIOpsPolicy{Mode: "disabled", Halted: true, Accounts: []AIOpsAccountScope{}, AutoActions: []string{},
		MinHealthyAccounts: 1, MinTotalConcurrency: 1, MinConcurrency: 1, MaxConcurrency: 100,
		MaxStepPercent: 10, DailyActionLimit: 10, CooldownSeconds: 300, ObservationSeconds: 300,
		EvidenceMaxAgeSeconds: 120, MinSamples: 20, ErrorRateThreshold: 0.2, RollbackErrorRateDelta: 0.05, MaxPauseSeconds: 900}
}

func ValidateAIOpsPolicy(p AIOpsPolicy) error {
	if p.Mode != "disabled" && p.Mode != "approve_each" && p.Mode != "bounded_auto" {
		return ErrAIOpsBlocked
	}
	if p.Mode == "disabled" {
		return nil
	}
	if p.GroupID <= 0 || len(p.Accounts) == 0 || len(p.Accounts) > 200 || p.MinHealthyAccounts < 1 || p.MinHealthyAccounts > 200 ||
		p.MinTotalConcurrency < 1 || p.MinTotalConcurrency > 2000000 || p.MinConcurrency < 1 || p.MaxConcurrency < p.MinConcurrency || p.MaxConcurrency > 10000 ||
		math.IsNaN(p.MaxStepPercent) || p.MaxStepPercent < 1 || p.MaxStepPercent > 25 || p.DailyActionLimit < 1 || p.DailyActionLimit > 100 ||
		p.CooldownSeconds < 60 || p.CooldownSeconds > 86400 || p.ObservationSeconds < 60 || p.ObservationSeconds > 3600 ||
		p.EvidenceMaxAgeSeconds < 30 || p.EvidenceMaxAgeSeconds > 600 || p.MinSamples < 10 || p.MinSamples > 100000 ||
		math.IsNaN(p.ErrorRateThreshold) || p.ErrorRateThreshold <= 0 || p.ErrorRateThreshold > 1 ||
		math.IsNaN(p.RollbackErrorRateDelta) || p.RollbackErrorRateDelta < 0 || p.RollbackErrorRateDelta > 1 ||
		p.MaxPauseSeconds < 60 || p.MaxPauseSeconds > 86400 || len(p.AutoActions) > 3 {
		return ErrAIOpsBlocked
	}
	seen := map[int64]bool{}
	domain := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,63}$`)
	for _, a := range p.Accounts {
		if a.AccountID <= 0 || seen[a.AccountID] || !domain.MatchString(a.CapacityDomain) {
			return ErrAIOpsBlocked
		}
		seen[a.AccountID] = true
	}
	acts := map[string]bool{}
	for _, a := range p.AutoActions {
		if acts[a] || (a != "set_concurrency" && a != "pause_account" && a != "resume_account") {
			return ErrAIOpsBlocked
		}
		acts[a] = true
	}
	return nil
}
