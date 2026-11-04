package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSReasoningEffortPolicyCompatibleCalls(t *testing.T) {
	for _, tc := range []struct {
		name, body, path, want, overLimit string
		forced                            []string
		wantDenied                        bool
	}{
		{name: "group-only mapping", body: `{"model":"gpt-5.6-sol","reasoning":{"effort":"low"}}`, path: "reasoning.effort", want: "medium"},
		{name: "group-only omitted", body: `{"model":"gpt-5.6-sol"}`, path: "reasoning.effort"},
		{name: "key preference skips group mapping", body: `{"model":"gpt-5.6-sol","reasoning":{"effort":"high"}}`, path: "reasoning.effort", forced: []string{"low"}, want: "low"},
		{name: "key preference capped", body: `{"model":"gpt-5.6-sol"}`, path: "reasoning.effort", forced: []string{"high"}, want: "medium"},
		{name: "nested key preference capped", body: `{"type":"response.create","response":{"model":"gpt-5.6-sol"}}`, path: "response.reasoning.effort", forced: []string{"high"}, want: "medium"},
		{name: "key preference denied", body: `{"model":"gpt-5.6-sol"}`, forced: []string{"high"}, overLimit: ReasoningEffortOverLimitDeny, wantDenied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hooks := &OpenAIWSIngressHooks{
				MaxReasoningEffort: "medium", MaxReasoningEffortOverLimit: tc.overLimit,
				ReasoningEffortMappings: []ReasoningEffortMapping{{From: "low", To: "high"}},
			}
			got, err := applyOpenAIWSReasoningEffortPolicy([]byte(tc.body), hooks, tc.forced...)
			if tc.wantDenied {
				var denied *ReasoningEffortOverLimitError
				require.ErrorAs(t, err, &denied)
				require.Equal(t, tc.body, string(got))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(got, tc.path).String())
			if tc.name == "group-only omitted" {
				require.Equal(t, tc.body, string(got))
			}
		})
	}
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	got, err := applyOpenAIWSReasoningEffortPolicy(body, nil)
	require.NoError(t, err)
	require.Equal(t, body, got)
}
