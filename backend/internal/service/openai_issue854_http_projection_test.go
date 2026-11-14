//go:build unit && issue854matrix

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

func TestIssue854HTTPProjectionIntegrity(t *testing.T) {
	for _, mutation := range []string{"none", "task_before", "tools_before", "task_after", "tools_after"} {
		t.Run(mutation, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			body := []byte(`{"model":"gpt-5.1","input":[{"type":"message","role":"user","namespace":"client_metadata","content":"reuse the completed patch"}],"tools":[{"type":"function","name":"verify","parameters":{"type":"object"}}]}`)
			r := &openAIAgentRecoveryRequest{omittedHistory: true, inputDigest: openAIAgentRecoveryInputDigest(body), replayControlsDigest: openAI409ControlsDigest(body)}
			originalInput, originalControls := r.inputDigest, r.replayControlsDigest
			ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, r)
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			switch mutation {
			case "task_before":
				body, _ = sjson.SetBytes(body, "input.0.content", "repeat the patch")
			case "tools_before":
				body, _ = sjson.DeleteBytes(body, "tools")
			}
			projected, clean, err := svc.prepareOpenAI409HTTPProjection(ctx, c, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, body)
			if mutation == "task_before" || mutation == "tools_before" {
				require.Error(t, err)
				require.Same(t, r, openAIAgentRecoveryFromContext(projected))
			} else {
				require.NoError(t, err)
				require.NotSame(t, r, openAIAgentRecoveryFromContext(projected))
				require.NotContains(t, string(clean), "client_metadata")
				if mutation == "task_after" {
					clean, _ = sjson.SetBytes(clean, "input.0.content", "repeat the patch")
				}
				if mutation == "tools_after" {
					clean, _ = sjson.DeleteBytes(clean, "tools")
				}
				_, err = prepareOpenAI409APIKeyReplay(projected, clean, false)
				if mutation == "none" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			}
			require.Equal(t, originalInput, r.inputDigest, "projection cannot overwrite the trusted input")
			require.Equal(t, originalControls, r.replayControlsDigest, "projection cannot overwrite trusted controls")
		})
	}
}
