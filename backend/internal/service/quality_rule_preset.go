package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// QualityRulePreset has no account, filter, ownership or scheduling identity.
// Enabled is a default for a future plan; a preset itself is never executed.
type QualityRulePreset struct {
	ID             int64              `json:"id"`
	Name           string             `json:"name"`
	ModelID        string             `json:"model_id"`
	CronExpression string             `json:"cron_expression"`
	Enabled        bool               `json:"enabled"`
	MaxResults     int                `json:"max_results"`
	PelicanConfig  *PelicanTestConfig `json:"pelican_config"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// Rebuild the editable inputs rather than retaining result snapshots. The
// typed BPS and judge structs contain business options only, no credentials.
func reusableQualityConfig(source *PelicanTestConfig) (*PelicanTestConfig, error) {
	if source == nil || source.Quality != nil && source.Quality.Action == QualityActionOpenAITwoFAAdmission {
		return nil, fmt.Errorf("presets require a configurable quality rule")
	}
	q := source.Quality
	if q == nil {
		q = &QualityPolicy{}
	}
	action := q.Action
	// Old presets omitted penalty settings. They must never inherit the editor's
	// last policy (or its destructive new-rule default).
	if action == "" {
		action = QualityActionObserveOnly
	}
	policy := &QualityPolicy{
		OAuth401MaxRounds:    q.OAuth401MaxRounds,
		TriggerOnUpstream5xx: q.TriggerOnUpstream5xx, ExpectedAnswer: q.ExpectedAnswer,
		Action: action, RemoveGroupIDs: append([]int64(nil), q.RemoveGroupIDs...),
		RemoveModels: append([]string(nil), q.RemoveModels...), RecoveryConcurrency: q.RecoveryConcurrency,
		AutoRestore: q.AutoRestore, RetestRestMinutes: q.RetestRestMinutes,
	}
	if q.Judge != nil {
		judge := *q.Judge
		policy.Judge = &judge
	}
	if q.BPS != nil {
		bps := *q.BPS
		bps.Models = append([]string(nil), q.BPS.Models...)
		if q.BPS.RecoveryIntervalMinutes != nil {
			interval := *q.BPS.RecoveryIntervalMinutes
			bps.RecoveryIntervalMinutes = &interval
		}
		policy.BPS = &bps
	}
	if action != "remove_groups" && action != QualityActionIsolateModels {
		policy.RemoveGroupIDs = nil
	}
	if action != QualityActionRemoveModel {
		policy.RemoveModels = nil
		policy.RecoveryConcurrency = 0
	}
	if action != QualityActionEnableBPS {
		policy.BPS = nil
	}
	if action == QualityActionObserveOnly {
		policy.AutoRestore = false
	}
	if !policy.UsesRetestRest() {
		policy.RetestRestMinutes = 0
	}
	if source.QuestionKind == OpenAICodexStateProbeQuestionKind {
		policy.Judge = nil
		policy.ExpectedAnswer = ""
	}
	return &PelicanTestConfig{
		Quality: policy, QuestionKind: source.QuestionKind, TestChannel: source.TestChannel,
		Prompt: source.Prompt, ReasoningEffort: source.ReasoningEffort, ParallelCount: source.ParallelCount,
		ModelIDs: append([]string(nil), source.ModelIDs...),
	}, nil
}

func (s *ScheduledTestService) ListQualityPresets(ctx context.Context) ([]*QualityRulePreset, error) {
	if s.templateRepo == nil {
		return nil, errQualityTemplatesUnavailable
	}
	if _, observer := ObserverGroupIDs(ctx); observer {
		return nil, ErrObserverScope
	}
	presets, err := s.templateRepo.ListPresets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*QualityRulePreset, 0, len(presets))
	for _, preset := range presets {
		config, err := reusableQualityConfig(preset.PelicanConfig)
		if err != nil {
			return nil, err
		}
		copy := *preset
		copy.PelicanConfig = config
		out = append(out, &copy)
	}
	return out, nil
}

// CreateQualityPreset validates a future plan, but only persists its reusable
// configuration. Never call CreatePlan, syncQualityTemplate or account APIs.
func (s *ScheduledTestService) CreateQualityPreset(ctx context.Context, input *QualityRulePreset) (*QualityRulePreset, error) {
	if s.templateRepo == nil {
		return nil, errQualityTemplatesUnavailable
	}
	if _, observer := ObserverGroupIDs(ctx); observer {
		return nil, ErrObserverScope
	}
	preset, err := prepareQualityPreset(input)
	if err != nil {
		return nil, err
	}
	return s.templateRepo.CreatePreset(ctx, preset)
}

func prepareQualityPreset(input *QualityRulePreset) (*QualityRulePreset, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, fmt.Errorf("preset name must be 1-100 characters")
	}
	config, err := reusableQualityConfig(input.PelicanConfig)
	if err != nil {
		return nil, err
	}
	sample := &ScheduledTestPlan{ModelID: input.ModelID, CronExpression: input.CronExpression,
		MaxResults: input.MaxResults, PelicanConfig: config}
	if _, err := nextPlanRun(sample, time.Now()); err != nil {
		return nil, fmt.Errorf("invalid preset configuration: %w", err)
	}
	// nextPlanRun stamps ModelID for result snapshots. It is not reusable input.
	sample.PelicanConfig.ModelID = ""
	preset := &QualityRulePreset{Name: name, ModelID: sample.ModelID, CronExpression: sample.CronExpression,
		Enabled: input.Enabled, MaxResults: sample.MaxResults, PelicanConfig: sample.PelicanConfig}
	return preset, nil
}

// Editing a passive preset never distributes it to accounts or changes plans.
// The optional extension keeps existing template dispatch repositories unchanged.
type QualityRulePresetEditor interface {
	UpdatePreset(context.Context, *QualityRulePreset) (*QualityRulePreset, error)
}

func (s *ScheduledTestService) UpdateQualityPreset(ctx context.Context, id int64, input *QualityRulePreset) (*QualityRulePreset, error) {
	if _, observer := ObserverGroupIDs(ctx); observer {
		return nil, ErrObserverScope
	}
	repo, ok := s.templateRepo.(QualityRulePresetEditor)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("QUALITY_PRESET_EDIT_UNAVAILABLE", "preset editing unavailable")
	}
	preset, err := prepareQualityPreset(input)
	if err != nil {
		return nil, err
	}
	preset.ID = id
	return repo.UpdatePreset(ctx, preset)
}
