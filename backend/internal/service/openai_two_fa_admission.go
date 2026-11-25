package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const OpenAITwoFAAdmissionExtraKey = "openai_two_fa_admission"
const QualityActionOpenAITwoFAAdmission = "openai_two_fa_admission"

// A copy is attached to each newly imported account. Editing the global
// template never changes an account's enrollment or its progress.
type OpenAITwoFAAdmissionConfig struct {
	Enabled           bool   `json:"enabled"`
	ModelID           string `json:"model_id"`
	ReasoningEffort   string `json:"reasoning_effort"`
	WaitSeconds       int    `json:"wait_seconds"`
	PassCount         int    `json:"pass_count"`
	PassedConcurrency int    `json:"passed_concurrency"`
	IsolationCount    int    `json:"isolation_count"`
	IsolationGroupID  int64  `json:"isolation_group_id"`
}

func DefaultOpenAITwoFAAdmissionConfig() OpenAITwoFAAdmissionConfig {
	return OpenAITwoFAAdmissionConfig{ModelID: "gpt-6.1-sol", ReasoningEffort: "low", WaitSeconds: 60, PassCount: 6, PassedConcurrency: 2, IsolationCount: 6}
}

func (c OpenAITwoFAAdmissionConfig) Validate() error {
	if strings.TrimSpace(c.ModelID) == "" || len(c.ModelID) > 100 || strings.ContainsAny(c.ModelID, " \t\r\n*") || normalizePelicanReasoningEffort(c.ReasoningEffort) == "" ||
		c.WaitSeconds < 1 || c.WaitSeconds > 86400 || c.PassCount < 1 || c.PassCount > 1000 || c.PassedConcurrency < 1 || c.PassedConcurrency > 10000 || c.IsolationCount < 1 || c.IsolationCount > 1000 || c.IsolationGroupID < 0 || (c.Enabled && c.IsolationGroupID == 0) {
		return fmt.Errorf("invalid OpenAI 2FA admission settings; select an active OpenAI isolation group")
	}
	return nil
}

type OpenAITwoFAAdmissionState struct {
	KeepSchedulingOnPass bool                       `json:"keep_scheduling_on_pass,omitempty"` // preserve the native scheduling flag when the import was configured to start paused
	Policy               OpenAITwoFAAdmissionConfig `json:"policy"`
	Phase                string                     `json:"phase"`
	PlanID               int64                      `json:"plan_id"`
	PassStreak           int                        `json:"pass_streak"`
	IsolationStreak      int                        `json:"isolation_streak"`
	IsolationSchedulable *bool                      `json:"isolation_schedulable,omitempty"`
	ApprovedAt           *time.Time                 `json:"approved_at,omitempty"`
	LastStartedAt        *time.Time                 `json:"last_started_at,omitempty"`
}

func AdvanceOpenAITwoFAAdmission(s OpenAITwoFAAdmissionState, outcome string, started, now time.Time) (OpenAITwoFAAdmissionState, string) {
	if s.Phase == "isolation_pending" {
		return s, "admission_isolated"
	}
	if s.Phase == "isolated" || (s.LastStartedAt != nil && !started.After(*s.LastStartedAt)) {
		return s, "observed"
	}
	s.LastStartedAt = &started
	switch outcome {
	case "passed":
		s.IsolationStreak = 0
		if s.Phase == "pending" {
			s.PassStreak++
			if s.PassStreak >= s.Policy.PassCount {
				s.Phase, s.ApprovedAt = "passed", &now
				return s, "admission_passed"
			}
		}
	case "answer_29":
		s.PassStreak = 0
		s.IsolationStreak++
		if s.IsolationStreak >= s.Policy.IsolationCount {
			s.Phase = "isolated"
			return s, "admission_isolated"
		}
	default:
		// Incomplete responses, other answers and transport errors break both
		// streaks. None can be treated as an actual answer of 29.
		s.PassStreak, s.IsolationStreak = 0, 0
	}
	return s, "observed"
}

func (s *adminServiceImpl) applyOpenAITwoFAAdmission(ctx context.Context, input *CreateAccountInput) error {
	if input.OAuthImportSource != OAuthImportTwoFA || input.Platform != PlatformOpenAI || input.Type != AccountTypeOAuth || s.settingService == nil {
		return nil
	}
	if _, observer := ObserverGroupIDs(ctx); observer {
		return nil
	}
	c, err := s.settingService.GetOAuthAutoConfig(ctx)
	if err != nil {
		return err
	}
	p := c.OpenAITwoFAAdmission
	if !p.Enabled {
		return nil
	}
	g, err := s.groupRepo.GetByID(ctx, p.IsolationGroupID)
	if err != nil {
		return err
	}
	if g.Platform != PlatformOpenAI || g.Status != StatusActive {
		return fmt.Errorf("OpenAI 2FA isolation group is unavailable")
	}
	config := &PelicanTestConfig{QuestionKind: "candy", Prompt: CandyPrompt, ModelID: p.ModelID, ReasoningEffort: p.ReasoningEffort, ParallelCount: 1,
		Quality: &QualityPolicy{Action: QualityActionOpenAITwoFAAdmission, ExpectedAnswer: "21", Admission: &p}}
	now := time.Now().UTC()
	input.InitialAdmissionPlan = &ScheduledTestPlan{ModelID: p.ModelID, CronExpression: "* * * * *", Enabled: true, MaxResults: 100, NextRunAt: &now, PelicanConfig: config}
	input.Concurrency = 1
	if input.Extra == nil {
		input.Extra = map[string]any{}
	}
	input.Extra[OpenAITwoFAAdmissionExtraKey] = OpenAITwoFAAdmissionState{Policy: p, Phase: "pending", KeepSchedulingOnPass: input.initialAutoSchedule != nil && !*input.initialAutoSchedule}
	return nil
}
