package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountTestReasoningFromMetadataUsesSupportedLevelsAndDefault(t *testing.T) {
	enabled := true
	result, ok := accountTestReasoningFromMetadata(UpstreamModelMetadata{
		Reasoning:                &enabled,
		DefaultReasoningLevel:    "ultra",
		SupportedReasoningLevels: []string{"low", "high", "ultra"},
	})
	require.True(t, ok)
	require.Equal(t, []string{"low", "high", "ultra"}, result.SupportedReasoningLevels)
	require.Equal(t, "ultra", result.DefaultReasoningLevel)
}

func TestAccountTestReasoningFromMetadataKeepsDisabledModelsEmpty(t *testing.T) {
	disabled := false
	result, ok := accountTestReasoningFromMetadata(UpstreamModelMetadata{Reasoning: &disabled})
	require.True(t, ok)
	require.Empty(t, result.SupportedReasoningLevels)
	require.Empty(t, result.DefaultReasoningLevel)
}
