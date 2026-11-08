package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// A native manifest is not evidence that a Chat Completions route can consume
// the encrypted task that Codex will create after seeing multi_agent_version=v2.
func TestIssue715BridgeDoesNotAdvertiseEncryptedAgent(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		account := Account{
			ID: 715, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{
				"base_url":      "https://bridge.example/v1",
				"model_mapping": map[string]any{"public-agent": "gpt-6-astra"},
			},
			Extra: map[string]any{"openai_responses_supported": false},
		}
		if snapshot {
			account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
				"gpt-6-astra": {CodexToolCapabilities: map[string]json.RawMessage{
					"multi_agent_version":          json.RawMessage(`"v2"`),
					"multi_agent_reasoning_effort": json.RawMessage(`"xhigh"`),
				}},
			}})
		}
		for _, modelID := range []string{"gpt-6-astra", "public-agent"} {
			body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{modelID}, []Account{account}, nil, nil, true)
			require.NoError(t, err)
			model := decodeCodexManifestModels(t, body)[0]
			require.Nil(t, model["multi_agent_version"], "bridge must not create unreadable encrypted tasks")
			require.Nil(t, model["multi_agent_reasoning_effort"])
			require.Equal(t, true, model["supports_search_tool"], "plaintext tool discovery remains usable")
		}
	}
}

func TestIssue715NativeAgentCapabilityIsPreserved(t *testing.T) {
	account := Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://native.example/v1"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
		"gpt-6-astra": {CodexToolCapabilities: map[string]json.RawMessage{
			"multi_agent_version":          json.RawMessage(`"v2"`),
			"multi_agent_reasoning_effort": json.RawMessage(`"xhigh"`),
		}},
	}})
	body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"gpt-6-astra"}, []Account{account}, nil, nil, true)
	require.NoError(t, err)
	model := decodeCodexManifestModels(t, body)[0]
	require.Equal(t, "v2", model["multi_agent_version"])
	require.Equal(t, "xhigh", model["multi_agent_reasoning_effort"])
}
