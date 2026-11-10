package service

import (
	"context"
	"time"
)

// PelicanTestConfig stores intelligence test inputs; a missing kind preserves legacy HTML plans.
type PelicanTestConfig struct {
	// Optional display name stored in the existing configuration JSON.
	RuleName string `json:"rule_name,omitempty"`
	// Filled only on saved result snapshots, never used to configure a rule.
	QualityRestoreDiagnostic *QualityRestoreDiagnostic `json:"quality_restore_diagnostic,omitempty"`
	QualityModelOutcomes     map[string]string         `json:"quality_model_outcomes,omitempty"`
	QualityModelActions      map[string]string         `json:"quality_model_actions,omitempty"`
	TriggerSource            string                    `json:"trigger_source,omitempty"`
	RandomQuestion           *QualityRandomQuestion    `json:"random_question,omitempty"`
	QualityBatch             *QualityBatchEvidence     `json:"quality_batch,omitempty"`
	// BPSRecoveryPending is filled from persisted ownership when the runner
	// claims a plan. It is never accepted from or written to configuration JSON.
	BPSRecoveryPending bool           `json:"-"`
	Quality            *QualityPolicy `json:"quality,omitempty"`
	QuestionKind       string         `json:"question_kind,omitempty"`
	TestChannel        string         `json:"test_channel,omitempty"`
	Prompt             string         `json:"prompt"`
	ReasoningEffort    string         `json:"reasoning_effort"`
	ParallelCount      int            `json:"parallel_count"`
	// ModelID is recorded with each result so later edits do not relabel history.
	ModelID string `json:"model_id,omitempty"`
	// ModelIDs selects all models in a quality round; old plans use ModelID.
	ModelIDs []string `json:"model_ids,omitempty"`
}

// ScheduledTestPlan represents a scheduled test plan domain model.
type ScheduledTestPlan struct {
	QualityBatchEvaluated bool              `json:"-"`
	QualityBatchModels    map[string]string `json:"-"`
	// Request-only: importing configuration preserves this target's ownership.
	PreserveQualityState     bool                      `json:"-"`
	QualityRestoreDiagnostic *QualityRestoreDiagnostic `json:"-"`
	QualityModelActions      map[string]string         `json:"-"`
	QualityModelOutcomes     map[string]string         `json:"-"`
	Quality5xxEpisode        int64                     `json:"-"`
	TriggerSource            string                    `json:"-"`
	TriggerObservedAt        *time.Time                `json:"-"`
	AccountName              string                    `json:"account_name,omitempty"`
	PelicanConfig            *PelicanTestConfig        `json:"pelican_config,omitempty"`
	RunningUntil             *time.Time                `json:"running_until,omitempty"`
	ID                       int64                     `json:"id"`
	AccountID                int64                     `json:"account_id"`
	ModelID                  string                    `json:"model_id"`
	CronExpression           string                    `json:"cron_expression"`
	Enabled                  bool                      `json:"enabled"`
	MaxResults               int                       `json:"max_results"`
	AutoRecover              bool                      `json:"auto_recover"`
	LastRunAt                *time.Time                `json:"last_run_at"`
	NextRunAt                *time.Time                `json:"next_run_at"`
	CreatedAt                time.Time                 `json:"created_at"`
	UpdatedAt                time.Time                 `json:"updated_at"`
}

// ScheduledTestResult represents a single test execution result.
type ScheduledTestResult struct {
	// Runtime-only; set solely from an actual own-credential OAuth HTTP 401.
	DirectOAuth401        bool `json:"-"`
	DirectOAuthRevoked401 bool `json:"-"`

	QualityRoundID  string             `json:"quality_round_id,omitempty"`
	QualityJudgment *QualityJudgment   `json:"quality_judgment,omitempty"`
	QualityAction   string             `json:"quality_action,omitempty"`
	PelicanConfig   *PelicanTestConfig `json:"pelican_config,omitempty"`
	ID              int64              `json:"id"`
	PlanID          int64              `json:"plan_id"`
	Status          string             `json:"status"`
	ResponseText    string             `json:"response_text"`
	ErrorMessage    string             `json:"error_message"`
	LatencyMs       int64              `json:"latency_ms"`
	StartedAt       time.Time          `json:"started_at"`
	FinishedAt      time.Time          `json:"finished_at"`
	CreatedAt       time.Time          `json:"created_at"`
}

// ScheduledTestPlanRepository defines the data access interface for test plans.
type ScheduledTestPlanRepository interface {
	FinishTriggeredQuality(context.Context, *ScheduledTestPlan, time.Time, time.Time, time.Time) error
	ListQualityPlans(context.Context) ([]*ScheduledTestPlan, error)
	ApplyQualityOutcome(context.Context, *ScheduledTestPlan, time.Time, string) (string, error)
	TriggerQuality(context.Context, int64) error
	ClaimPelican(ctx context.Context, plan *ScheduledTestPlan, now, until, next time.Time) (bool, error)
	FinishPelican(ctx context.Context, id int64, until, finished time.Time) error
	Create(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	GetByID(ctx context.Context, id int64) (*ScheduledTestPlan, error)
	ListByAccountID(ctx context.Context, accountID int64) ([]*ScheduledTestPlan, error)
	ListDue(ctx context.Context, now time.Time) ([]*ScheduledTestPlan, error)
	Update(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	Delete(ctx context.Context, id int64) error
	UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error
}

// PelicanHistoryResult includes account identity without exposing account credentials.
type PelicanHistoryResult struct {
	ScheduledTestResult
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name"`
}
type PelicanHistoryPage struct {
	Items      []*PelicanHistoryResult `json:"items"`
	NextCursor int64                   `json:"next_cursor"`
}

// ScheduledTestResultRepository defines the data access interface for test results.
type ScheduledTestResultRepository interface {
	ListQualityHistory(context.Context, int64, int) ([]*QualityHistoryResult, error)
	// accountID 0 lists every account.
	ListPelicanHistory(ctx context.Context, beforeID int64, limit int, accountID int64) ([]*PelicanHistoryResult, error)
	PruneExpiredPelican(ctx context.Context, before time.Time) error
	Create(ctx context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error)
	GetResult(ctx context.Context, planID, resultID int64) (*ScheduledTestResult, error)
	ListByPlanID(ctx context.Context, planID int64, limit int, includeContent ...bool) ([]*ScheduledTestResult, error)
	PruneOldResults(ctx context.Context, planID int64, keepCount int) error
}

type QualityHistoryResult struct {
	PelicanHistoryResult
	PassedCount  int     `json:"passed_count"`
	SkippedCount int     `json:"skipped_count"`
	TotalCount   int     `json:"total_count"`
	ResultIDs    []int64 `json:"result_ids"`
}
type QualityHistoryPage struct {
	Items      []*QualityHistoryResult `json:"items"`
	NextCursor int64                   `json:"next_cursor"`
}
