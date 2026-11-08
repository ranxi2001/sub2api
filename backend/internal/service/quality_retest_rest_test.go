//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQualityRetestRestValidationAndReusableInputs(t *testing.T) {
	for _, minutes := range []int{-1, 0, 60, 10080, 10081} {
		plan := pelicanPlan()
		plan.PelicanConfig.QuestionKind = OpenAICodexStateProbeQuestionKind
		plan.PelicanConfig.ParallelCount = 1
		plan.PelicanConfig.Quality = &QualityPolicy{Action: "disable_scheduling", RetestRestMinutes: minutes}
		_, err := nextPlanRun(plan, time.Now())
		if minutes < 0 || minutes > 10080 {
			require.ErrorContains(t, err, "retest rest")
			continue
		}
		require.NoError(t, err)
		reusable, err := reusableQualityConfig(plan.PelicanConfig)
		require.NoError(t, err)
		require.Equal(t, minutes, reusable.Quality.RetestRestMinutes)
	}
	plan := pelicanPlan()
	plan.PelicanConfig.QuestionKind = OpenAICodexStateProbeQuestionKind
	plan.PelicanConfig.ParallelCount = 1
	plan.PelicanConfig.Quality = &QualityPolicy{Action: QualityActionObserveOnly, RetestRestMinutes: 60}
	_, err := nextPlanRun(plan, time.Now())
	require.NoError(t, err)
	require.Zero(t, plan.PelicanConfig.Quality.RetestRestMinutes)
}
