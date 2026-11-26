package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuality429ModelsRequiresCompleteTerminalEvidence(t *testing.T) {
	plan := &ScheduledTestPlan{PelicanConfig: &PelicanTestConfig{ModelIDs: []string{"a", "b"}, Quality: &QualityPolicy{}, ParallelCount: 2}}
	sample := func(model string, code int, status string) *ScheduledTestResult {
		return &ScheduledTestResult{Status: status, ErrorMessage: "API returned 429: quoted text", PelicanConfig: &PelicanTestConfig{ModelID: model, QualityHTTPStatus: code}}
	}
	results := []*ScheduledTestResult{sample("a", 429, "failed"), sample("b", 0, "failed"), sample("a", 429, "failed"), sample("b", 503, "failed")}
	require.Equal(t, []string{"a"}, Quality429Models(plan, results))
	results[2].Status = "success"
	require.Empty(t, Quality429Models(plan, results))
	results[2].Status = "skipped"
	require.Empty(t, Quality429Models(plan, results))
	require.Empty(t, Quality429Models(plan, results[:2]))
	results[1] = nil
	require.Empty(t, Quality429Models(plan, results))
}
