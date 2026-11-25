package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAITwoFAAdmissionStreaksAndPermanentIsolation(t *testing.T) {
	p := DefaultOpenAITwoFAAdmissionConfig()
	p.Enabled, p.IsolationGroupID = true, 44
	s := OpenAITwoFAAdmissionState{Policy: p, Phase: "pending"}
	now := time.Now().UTC()
	step := func(outcome string) string {
		now = now.Add(time.Second)
		var action string
		s, action = AdvanceOpenAITwoFAAdmission(s, outcome, now, now)
		return action
	}
	for range 5 {
		require.Equal(t, "observed", step("passed"))
	}
	require.Equal(t, "pending", s.Phase)
	step("inconclusive")
	require.Zero(t, s.PassStreak)
	for range 5 {
		step("passed")
	}
	require.Equal(t, "admission_passed", step("passed"))
	require.Equal(t, "passed", s.Phase)
	require.NotNil(t, s.ApprovedAt)
	approved := *s.ApprovedAt
	// Repeated successful probes do not reapply concurrency or reset approval.
	require.Equal(t, "observed", step("passed"))
	require.Equal(t, approved, *s.ApprovedAt)
	for range 5 {
		step("answer_29")
	}
	step("passed")
	require.Zero(t, s.IsolationStreak)
	for range 5 {
		step("answer_29")
	}
	require.Equal(t, "admission_isolated", step("answer_29"))
	for range 10 {
		require.Equal(t, "observed", step("passed"))
	}
	require.Equal(t, "isolated", s.Phase)
}

func TestOpenAITwoFAAdmissionRejectsReplayAndCanIsolateBeforePassing(t *testing.T) {
	p := DefaultOpenAITwoFAAdmissionConfig()
	p.Enabled, p.IsolationGroupID = true, 44
	now := time.Now().UTC()
	s := OpenAITwoFAAdmissionState{Policy: p, Phase: "pending"}
	s, _ = AdvanceOpenAITwoFAAdmission(s, "passed", now, now)
	for range 6 {
		s, _ = AdvanceOpenAITwoFAAdmission(s, "passed", now, now)
	}
	require.Equal(t, 1, s.PassStreak)
	for range 6 {
		now = now.Add(time.Second)
		s, _ = AdvanceOpenAITwoFAAdmission(s, "answer_29", now, now)
	}
	require.Equal(t, "isolated", s.Phase)
	require.Nil(t, s.ApprovedAt)
}

func TestOpenAITwoFAAdmissionImportScopeAndSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind   string
		source, observer, want bool
	}{
		{"2fa", PlatformOpenAI, AccountTypeOAuth, true, false, true},
		{"ordinary import", PlatformOpenAI, AccountTypeOAuth, false, false, false},
		{"api key", PlatformOpenAI, AccountTypeAPIKey, true, false, false},
		{"other platform", PlatformAnthropic, AccountTypeOAuth, true, false, false},
		{"observer", PlatformOpenAI, AccountTypeOAuth, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultOAuthAutoConfig()
			c.OpenAITwoFAAdmission.Enabled, c.OpenAITwoFAAdmission.IsolationGroupID = true, 44
			raw, err := json.Marshal(c)
			require.NoError(t, err)
			svc := &adminServiceImpl{settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil), groupRepo: autoConfigGroups{group: &Group{ID: 44, Platform: PlatformOpenAI, Status: StatusActive}}}
			ctx := t.Context()
			if tc.observer {
				ctx = WithObserverScope(ctx, []int64{2})
			}
			source := OAuthImportJSON
			if tc.source {
				source = OAuthImportTwoFA
			}
			input := &CreateAccountInput{OAuthImportSource: source, Platform: tc.platform, Type: tc.kind, Concurrency: 9, GroupIDs: []int64{2}, Extra: map[string]any{OpenAITwoFAAdmissionExtraKey: "forged", "keep": true}}
			require.NoError(t, svc.ApplyOAuthAutoConfig(ctx, input))
			require.NotContains(t, input.Extra, OpenAITwoFAAdmissionExtraKey)
			require.NoError(t, svc.applyOpenAITwoFAAdmission(ctx, input))
			if !tc.want {
				require.Nil(t, input.InitialAdmissionPlan)
				require.Equal(t, 9, input.Concurrency)
				return
			}
			require.Equal(t, 1, input.Concurrency)
			require.Equal(t, []int64{2}, input.GroupIDs)
			require.Equal(t, true, input.Extra["keep"])
			state, ok := input.Extra[OpenAITwoFAAdmissionExtraKey].(OpenAITwoFAAdmissionState)
			require.True(t, ok)
			require.Equal(t, "pending", state.Phase)
			p := input.InitialAdmissionPlan
			require.True(t, p.Enabled)
			require.Equal(t, "gpt-6.1-sol", p.ModelID)
			require.Equal(t, "low", p.PelicanConfig.ReasoningEffort)
			_, err = nextPlanRun(p, time.Now())
			require.NoError(t, err)
			account, err := buildAccountForCreate(input, input.Extra)
			require.NoError(t, err)
			require.False(t, account.Schedulable)
			require.Same(t, p, account.InitialAdmissionPlan)
			c.OpenAITwoFAAdmission.PassCount = 99
			require.Equal(t, 6, p.PelicanConfig.Quality.Admission.PassCount)
		})
	}
}

func TestOpenAITwoFAAdmissionOnlyCountsCompleteExactAnswers(t *testing.T) {
	for _, tc := range []struct{ answer, status, message, expected string }{
		{"21", "success", "", "passed"}, {"29", "success", "", "answer_29"},
		{" 29\n", "success", "", "answer_29"}, {"129", "success", "", "inconclusive"},
		{"21 or 29", "success", "", "inconclusive"}, {"29", "failed", "incomplete stream", "inconclusive"},
		{"21", "failed", "incomplete stream", "inconclusive"}, {"", "failed", "timeout", "inconclusive"},
	} {
		t.Run(tc.answer+tc.message, func(t *testing.T) {
			p := DefaultOpenAITwoFAAdmissionConfig()
			p.Enabled, p.IsolationGroupID = true, 44
			plan := &ScheduledTestPlan{ModelID: p.ModelID, PelicanConfig: &PelicanTestConfig{QuestionKind: "candy", Prompt: CandyPrompt, ParallelCount: 1, ReasoningEffort: "low", Quality: &QualityPolicy{Action: QualityActionOpenAITwoFAAdmission, Admission: &p}}}
			runner := &ScheduledTestRunnerService{runPelican: func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
				return &ScheduledTestResult{Status: tc.status, ResponseText: tc.answer, ErrorMessage: tc.message, StartedAt: time.Now()}, nil
			}, judgeQuality: func(context.Context, int64, *PelicanTestConfig, string) *QualityJudgment {
				t.Fatal("admission must not call an external judge")
				return nil
			}}
			result := runner.runPelicanSample(t.Context(), plan)
			require.Equal(t, tc.expected, qualityRoundOutcome([]*ScheduledTestResult{result}))
		})
	}
}

func TestOpenAITwoFAAdmissionSettingsValidation(t *testing.T) {
	base := DefaultOAuthAutoConfig()
	require.NoError(t, ValidateOAuthAutoConfig(base))
	for _, change := range []func(*OpenAITwoFAAdmissionConfig){
		func(c *OpenAITwoFAAdmissionConfig) { c.WaitSeconds = 0 },
		func(c *OpenAITwoFAAdmissionConfig) { c.PassCount = 0 },
		func(c *OpenAITwoFAAdmissionConfig) { c.IsolationCount = 1001 },
		func(c *OpenAITwoFAAdmissionConfig) { c.PassedConcurrency = 10001 },
		func(c *OpenAITwoFAAdmissionConfig) { c.ModelID = "bad model" },
		func(c *OpenAITwoFAAdmissionConfig) { c.ReasoningEffort = "bad" },
		func(c *OpenAITwoFAAdmissionConfig) { c.Enabled = true; c.IsolationGroupID = 0 },
	} {
		c := base
		change(&c.OpenAITwoFAAdmission)
		require.Error(t, ValidateOAuthAutoConfig(c))
	}
	// Legacy persisted settings receive disabled defaults on read.
	c, err := GetOAuthAutoConfig(t.Context(), &accountOpsSettingsStub{raw: `{"platform":"openai"}`})
	require.NoError(t, err)
	require.False(t, c.OpenAITwoFAAdmission.Enabled)
	require.Equal(t, "gpt-6.1-sol", c.OpenAITwoFAAdmission.ModelID)
}
