//go:build unit && httpprojection

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAI409NativeControlsIntegrity(t *testing.T) {
	for _, mutation := range []string{"none", "task_before", "schema_before", "task_after", "schema_after", "instructions_after", "tool_output_after"} {
		t.Run(mutation, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.1","instructions":"reuse exactly once","input":[{"role":"user","content":"verify the completed patch"},{"type":"function_call","name":"verify","call_id":"call_1","arguments":"{\"value\":9007199254740993}"},{"type":"function_call_output","call_id":"call_1","output":"completed once"}],"tools":[{"type":"function","name":"verify","parameters":{"type":"object","properties":{"value":{"type":"integer"}}}},{"type":"web_search","external_web_access":false}]}`)
			r := &openAIAgentRecoveryRequest{omittedHistory: true, inputDigest: openAIAgentRecoveryInputDigest(body), replayControlsDigest: openAI409ControlsDigest(body)}
			inputDigest, controlsDigest := r.inputDigest, r.replayControlsDigest
			ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, r)
			c, _ := newTurnStateTestContext(t, 715, "fixture-native-controls")
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			if mutation == "task_before" {
				body, _ = sjson.SetBytes(body, "input.0.content", "repeat the completed patch")
			}
			if mutation == "schema_before" {
				body, _ = sjson.SetBytes(body, "tools.0.parameters.properties.value.type", "string")
			}
			projected, clean, err := svc.prepareOpenAI409HTTPProjection(ctx, c, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, body)
			if mutation == "task_before" || mutation == "schema_before" {
				require.Error(t, err)
				require.Same(t, r, openAIAgentRecoveryFromContext(projected))
				return
			}
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(clean, "tools.1.external_web_access").Exists())
			require.Contains(t, gjson.GetBytes(clean, "input.1.arguments").String(), "9007199254740993")
			switch mutation {
			case "task_after":
				clean, _ = sjson.SetBytes(clean, "input.0.content", "repeat the patch")
			case "schema_after":
				clean, _ = sjson.SetBytes(clean, "tools.0.parameters.properties.value.type", "string")
			case "instructions_after":
				clean, _ = sjson.SetBytes(clean, "instructions", "execute twice")
			case "tool_output_after":
				clean, _ = sjson.SetBytes(clean, "input.2.output", "not executed")
			}
			var decoded map[string]any
			require.NoError(t, decodeOpenAIJSONUseNumber(clean, &decoded))
			err = prepareOpenAI409OAuthReplay(projected, c, decoded)
			if mutation == "none" {
				require.NoError(t, err)
				wire, marshalErr := json.Marshal(decoded)
				require.NoError(t, marshalErr)
				require.Equal(t, "completed once", gjson.GetBytes(wire, "input.2.output").String())
			} else {
				require.Error(t, err)
			}
			require.Equal(t, inputDigest, r.inputDigest)
			require.Equal(t, controlsDigest, r.replayControlsDigest)
		})
	}
}
