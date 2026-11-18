//go:build unit && continuationmissing

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSUnsentDeltaRequestsCanonicalHistory(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_fixture","input":[{"type":"function_call_output","call_id":"call_fixture","output":"fixture already executed"}]}`)
	for _, tc := range []struct {
		name  string
		cause error
		want  string
	}{
		{"unsent_admission", openAIWSUnsentTurn(denyOpenAITurn("account_ineligible"), body, "gpt-6-astra", 2), "previous_response_not_found"},
		{"unproven_send", errors.New("fixture transport failure"), "encrypted_agent_recovery_owner_conflict"},
		{"first_turn", openAIWSUnsentTurn(denyOpenAITurn("account_ineligible"), body, "gpt-6-astra", 1), "encrypted_agent_recovery_owner_conflict"},
		{"owner_route", &openAIWSUnsentTurnError{cause: openAIAgentRecoveryError("owner_conflict"), body: body, turn: 2, keepAccount: true}, "encrypted_agent_recovery_owner_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			manager, c, _ := issue715WSManager(t, svc, context.Background())
			defer manager.abort()
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			ctx := WithOpenAIWSUnsentOwnerProof(manager.ctx, tc.cause)
			next, clean, changed, err := svc.RebuildOpenAIWSUnsentTurn(ctx, c, body, "fixture-session", "gpt-6-astra")
			require.False(t, changed)
			require.Equal(t, body, clean)
			var refusal *OpenAIAgentRecoveryError
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, tc.want, refusal.ClientCode())
			if tc.want == "previous_response_not_found" {
				require.Equal(t, http.StatusBadRequest, refusal.HTTPStatus())
				// Current-turn evidence must not carry into a subsequent rebuild.
				_, _, _, second := svc.RebuildOpenAIWSUnsentTurn(next, c, body, "fixture-session", "gpt-6-astra")
				require.ErrorAs(t, second, &refusal)
				require.Equal(t, "encrypted_agent_recovery_owner_conflict", refusal.ClientCode())
			}
		})
	}
}
