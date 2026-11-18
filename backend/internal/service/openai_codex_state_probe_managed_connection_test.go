package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// managedConnectionAccount mirrors a production BPS account after managed
// authorization connections were enabled: the account row still carries the
// legacy openai_excel_bps switch with a pending marker, while the real
// credentials live on the selected connection.
func managedConnectionAccount() *Account {
	account := stateProbeAccount()
	account.Extra = map[string]any{
		"openai_connections_managed":    true,
		"openai_excel_bps":              true,
		ExcelBPSAuthorizationPendingKey: true,
	}
	return account
}

func TestStateProbeUnsupportedForManagedConnectionAccount(t *testing.T) {
	account := managedConnectionAccount()
	require.NotEmpty(t, openAICodexStateProbeUnsupportedReason(account, "gpt-6.2-astra", false))
	// Bypassing BPS cannot help: the token read itself is rejected with 409.
	require.NotEmpty(t, openAICodexStateProbeUnsupportedReason(account, "gpt-6.2-astra", true))

	// An account already projected onto a selected connection is not blocked.
	account.AuthConnectionID = "conn-1"
	require.Empty(t, openAICodexStateProbeUnsupportedReason(account, "gpt-6.2-astra", false))
}

func TestQuality5xxTriggerFallsBackToAnswerTestForManagedConnection(t *testing.T) {
	cfg := PelicanTestConfig{
		QuestionKind: "candy",
		Prompt:       CandyPrompt,
		ModelIDs:     []string{"gpt-6.2-astra"},
		Quality:      &QualityPolicy{Action: "remove_groups", ExpectedAnswer: "21"},
	}

	triggered, apply := quality5xxTestConfig(managedConnectionAccount(), "gpt-6.2-astra", cfg)
	require.Equal(t, "candy", triggered.QuestionKind, "a 5xx follow-up must not pick the ticket state probe for a managed-connection account")
	require.NotEqual(t, OpenAICodexStateProbeQuestionKind, triggered.QuestionKind)
	_ = apply

	// Unmanaged accounts keep using the state probe as before.
	triggered, _ = quality5xxTestConfig(stateProbeAccount(), "gpt-6.2-astra", cfg)
	require.Equal(t, OpenAICodexStateProbeQuestionKind, triggered.QuestionKind)
}

func TestStateProbeOnManagedConnectionAccountSendsNothing(t *testing.T) {
	upstream := &stateProbeUpstream{}
	svc := stateProbeTestService(upstream)
	svc.accountRepo = &stateProbeAccountRepo{account: managedConnectionAccount()}

	result, err := svc.RunPelicanBackground(context.Background(), 7, "gpt-6.2-astra", stateProbePlanConfig())
	require.NoError(t, err)
	require.Empty(t, upstream.calls)
	require.NotContains(t, result.ErrorMessage, OpenAICodexStateFailureAccountError)
}
