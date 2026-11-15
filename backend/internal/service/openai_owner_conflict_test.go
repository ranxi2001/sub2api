//go:build unit && ownerconflict

package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOwnerConflictTypedBeforeSendBoundary(t *testing.T) {
	body := []byte(`{"input":"synthetic task"}`)
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"completed", &openAIWSOwnerRouteError{completedMixedOwners: true, cause: openAIAgentRecoveryDetailedError("owner_conflict", "mixed_completed_owners")}, true},
		{"plain conflict", openAIAgentRecoveryError("owner_conflict"), false},
		{"route conflict", &openAIWSOwnerRouteError{cause: openAIAgentRecoveryError("owner_conflict")}, false},
		{"lost owner", &openAIWSLostOwnerEvidenceError{cause: &openAIWSOwnerRouteError{completedMixedOwners: true, cause: openAIAgentRecoveryError("owner_conflict")}}, false},
		{"partial write", errors.New("synthetic partial write"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, ok := OpenAIWSUnsentTurnPayload(openAIWSUnsentAgentTurn(tc.err, body, "gpt-5.1", 53))
			require.Equal(t, tc.want, ok)
		})
	}
}

func TestOwnerConflictToolPairingDiagnostics(t *testing.T) {
	call := `{"type":"function_call","call_id":"c","name":"fixture","arguments":"{}"}`
	result := `{"type":"function_call_output","call_id":"c","output":"completed"}`
	for _, tc := range []struct{ input, want string }{
		{call + "," + result, ""}, {result, "tool_unpaired"}, {call, "tool_unpaired"},
		{call + "," + call + "," + result, "tool_duplicate_call"}, {call + "," + result + "," + result, "tool_duplicate_result"},
		{`{"type":"function_call","name":"secret"}`, "tool_missing_id"},
		{`{"type":"function_call","call_id":"c","status":"in_progress"}`, "tool_incomplete"},
		{`{"type":"function_call_output","call_id":"c","output":null}`, "tool_result_missing"},
		{call + `,{"type":"custom_tool_call_output","call_id":"c","output":"done"}`, "tool_unpaired"},
	} {
		require.Equal(t, tc.want, openAIWSToolPairingRefusal([]byte(`{"input":[`+tc.input+`]}`)))
	}
}

func TestOwnerConflictDiagnosticsDoNotAuthorizeStateChanges(t *testing.T) {
	for _, state := range []string{"pending", "failed", "conflict"} {
		t.Run(state, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			issue715LearnRoute(t, svc, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, issue715OpaqueBody)
			body := strings.TrimSuffix(issue715OpaqueBody, "]}") + `,{"role":"user","content":"complete existing task"}]}`
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(body), "fixture-session", "gpt-6-astra")
			next, clean, changed, err := svc.TryRebuildOpenAI409History(ctx, c, []byte(body), "fixture-session", "gpt-6-astra", openAIAgentRecoveryError("owner_unavailable"))
			require.NoError(t, err)
			require.True(t, changed)
			key := openAIAgentRecoveryFromContext(ctx).keys[0]
			binding := svc.agentMessageRecovery.bindings[key]
			want := "owner_record_conflicted"
			switch state {
			case "pending":
				binding.pending = "fixture"
				want = "owner_record_pending"
			case "failed":
				binding.failed = true
				want = "owner_record_failed"
			case "conflict":
				binding.conflicted = true
			}
			svc.agentMessageRecovery.bindings[key] = binding
			req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(clean))
			require.NoError(t, err)
			finish, err := svc.prepareOpenAIAgentRecoveryAttempt(next, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, req)
			defer finish(false)
			var refusal *OpenAIAgentRecoveryError
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, want, refusal.InternalSubreason())
			require.Equal(t, binding, svc.agentMessageRecovery.bindings[key])
		})
	}
	require.Equal(t, "unknown", OpenAIRecoverySubreason("user input or credentials"))
}
