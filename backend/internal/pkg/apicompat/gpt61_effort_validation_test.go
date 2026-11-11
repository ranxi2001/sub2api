package apicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGPT61AdaptersValidateReasoningEffort(t *testing.T) {
	for _, model := range []string{"gpt-6.1-terra", "openai/GPT_6.1_TERRA", "gpt-6.1-sol"} {
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
			t.Run(model+"/"+effort, func(t *testing.T) {
				chat, chatErr := ChatCompletionsToResponses(&ChatCompletionsRequest{Model: model, ReasoningEffort: effort})
				anthropic, anthropicErr := AnthropicToResponses(&AnthropicRequest{Model: model, OutputConfig: &AnthropicOutputConfig{Effort: effort}})
				if effort == "minimal" || (model == "gpt-6.1-sol" && effort == "none") {
					require.ErrorContains(t, chatErr, "does not support reasoning effort")
					require.Nil(t, chat)
					require.ErrorContains(t, anthropicErr, "does not support reasoning effort")
					require.Nil(t, anthropic)
					return
				}
				require.NoError(t, chatErr)
				require.NoError(t, anthropicErr)
				require.Equal(t, effort, chat.Reasoning.Effort)
				require.Equal(t, effort, anthropic.Reasoning.Effort)
			})
		}
	}
}
