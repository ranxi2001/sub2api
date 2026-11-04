package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAPIKeyMappedModelContextIsolation(t *testing.T) {
	base := context.Background()
	ctx := WithAPIKeyMappedModel(base, "gpt-5.6-sol")
	require.Equal(t, "gpt-5.6-sol", APIKeyMappedModelFromContext(ctx))
	require.Empty(t, APIKeyMappedModelFromContext(base))
	require.Empty(t, APIKeyMappedModelFromContext(WithAPIKeyMappedModel(base, "")))
}

func TestAPIKeyModelCandidatesKeepPermissionIdentitiesWithoutChaining(t *testing.T) {
	key := &APIKey{ModelMapping: map[string]string{"gpt-6.1-sol": "gpt-5.6-sol", "gpt-5.6-sol": "other"}}
	require.Equal(t, []string{"gpt-5.6-sol", "gpt-6.1-sol"}, APIKeyModelCandidates(key, []string{"gpt-6.1-sol"}))
	require.Equal(t, "gpt-6.1-sol", MapAPIKeyModel(&APIKey{}, "gpt-6.1-sol"))
	require.Equal(t, "unmatched", MapAPIKeyModel(key, "unmatched"))
	require.Equal(t, "gpt-5.6-sol", key.ModelMapping["gpt-6.1-sol"])
}

func TestAPIKeyReasoningEffortOverridesAndPreservesCeiling(t *testing.T) {
	for _, tc := range []struct{ name, body, path string }{
		{"responses omitted", `{"model":"gpt-5.6-sol","input":"hello"}`, "reasoning.effort"},
		{"responses explicit", `{"model":"gpt-5.6-sol","reasoning":{"effort":"minimal","summary":"auto"}}`, "reasoning.effort"},
		{"chat", `{"model":"gpt-5.6-sol","messages":[],"reasoning_effort":"low"}`, "reasoning_effort"},
		{"nested websocket", `{"type":"response.create","response":{"model":"gpt-5.6-sol","reasoning":{"effort":"low"}}}`, "response.reasoning.effort"},
		{"duplicate effort", `{"model":"gpt-5.6-sol","reasoning":{"effort":"low","effort":"ultra","Effort":"ultra"}}`, "reasoning.effort"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, changed, err := ApplyAPIKeyOpenAIReasoningEffortPolicy([]byte(tc.body), "high", "medium", []ReasoningEffortMapping{{From: "high", To: "low"}}, "downgrade")
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, "medium", gjson.GetBytes(body, tc.path).String())
			require.NotContains(t, string(body), `"Effort"`)
		})
	}
	_, _, err := ApplyAPIKeyOpenAIReasoningEffortPolicy([]byte(`{"model":"gpt-5.6-sol"}`), "high", "medium", nil, ReasoningEffortOverLimitDeny)
	require.Error(t, err)
}

func TestAPIKeyReasoningEffortEmptyPolicyAndConversionIsolation(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol", "input":"hello"}`)
	got, changed, err := ApplyAPIKeyOpenAIReasoningEffortPolicy(body, "", "", nil, "")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, got)

	ctx := WithAPIKeyOpenAIReasoningEffortPolicy(context.Background(), "high", "", nil, "")
	got, changed, err = ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "high", gjson.GetBytes(got, "reasoning.effort").String())
	got, changed, err = ApplyOpenAIReasoningEffortPolicyFromContext(context.Background(), body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, got)
}

func TestAPIKeyReasoningEffortCanonicalizesEveryAdapterShape(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","messages":[],"Reasoning_Effort":"ultra","Reasoning":{"Effort":"ultra"},"Output_Config":{"Effort":"ultra"}}`)
	updated, _, err := ApplyAPIKeyOpenAIReasoningEffortPolicy(body, "high", "medium", nil, "downgrade")
	require.NoError(t, err)
	var decoded struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Reasoning       struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	require.NoError(t, json.Unmarshal(updated, &decoded))
	require.Equal(t, "medium", decoded.ReasoningEffort)
	require.Equal(t, "medium", decoded.Reasoning.Effort)
	require.Equal(t, "medium", decoded.OutputConfig.Effort)
}
