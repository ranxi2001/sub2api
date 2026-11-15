//go:build unit && ownerconflict

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestOpenAIWSMixedOwnerFinalSendFences(t *testing.T) {
	for _, mutation := range []string{"none", "pending", "failed", "conflicted", "missing", "changed", "input", "tools"} {
		t.Run(mutation, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			a, b := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			other := strings.ReplaceAll(issue715OpaqueBody, "fixture-opaque-reference", "fixture-other-owner")
			issue715LearnRoute(t, svc, a, issue715OpaqueBody)
			issue715LearnRoute(t, svc, b, other)
			body := []byte(`{"model":"gpt-6-astra","tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{}}}],"input":[{"role":"user","content":"continue the supplied task"},` + gjson.Get(issue715OpaqueBody, "input.0").Raw + `,` + gjson.Get(other, "input.0").Raw + `,
			{"type":"function_call","name":"apply_patch","call_id":"fixture-first","arguments":"{}"},
			{"type":"function_call_output","call_id":"fixture-first","output":"fixture patch completed once"},
			{"type":"custom_tool_call","name":"exec","call_id":"fixture-second","input":"fixture command"},
			{"type":"custom_tool_call_output","call_id":"fixture-second","output":"fixture command completed once"}]}`)
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := svc.WithOpenAIWSAgentRecovery(context.Background(), c, body, "fixture-session", "gpt-6-astra")
			next, clean, changed, err := svc.RebuildOpenAIWSUnsentTurn(ctx, c, body, "fixture-session", "gpt-6-astra")
			require.NoError(t, err)
			require.True(t, changed)
			r := openAIAgentRecoveryFromContext(next)
			require.NotNil(t, r.omittedSource)
			require.True(t, r.omittedSource.requireCompletedOwners)
			for _, item := range gjson.GetBytes(clean, "input").Array() {
				require.NotContains(t, item.Get("type").String(), "call")
			}
			for _, result := range []string{"fixture patch completed once", "fixture command completed once"} {
				require.Equal(t, 1, strings.Count(string(clean), result))
			}
			key := r.omittedSource.keys[0]
			binding := svc.agentMessageRecovery.bindings[key]
			switch mutation {
			case "pending":
				binding.pending = "fixture-pending"
			case "failed":
				binding.failed = true
			case "conflicted":
				binding.conflicted = true
			case "changed":
				binding.accountID++
			case "input":
				clean, err = sjson.SetBytes(clean, "input.0.content", "fixture mutated input")
			case "tools":
				clean, err = sjson.SetBytes(clean, "tools.0.name", "fixture changed schema")
			}
			require.NoError(t, err)
			svc.agentMessageRecovery.bindings[key] = binding
			if mutation == "missing" {
				delete(svc.agentMessageRecovery.bindings, key)
			}
			before := make(map[[32]byte]openAIAgentRecoveryBinding)
			for k, v := range svc.agentMessageRecovery.bindings {
				before[k] = v
			}
			req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(clean))
			require.NoError(t, err)
			finish, err := svc.prepareOpenAIAgentRecoveryAttempt(next, b, req)
			finish(false)
			if mutation == "none" {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "a changed original record or final payload must refuse before generation")
				var recovery *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &recovery)
				require.Equal(t, http.StatusConflict, recovery.HTTPStatus())
			}
			require.Equal(t, before, svc.agentMessageRecovery.bindings)
		})
	}
}

func TestOpenAIWSMixedOwnerProofCannotComeFromDiagnostics(t *testing.T) {
	for _, detail := range []string{"unknown", "mixed_completed_owners"} {
		cause := openAIRecoveryDiagnosticError("owner_conflict", detail, nil)
		err := openAIWSUnsentAgentTurn(&openAIWSOwnerRouteError{cause: cause}, []byte(`{"input":"fixture"}`), "gpt-6-astra", 53)
		_, _, _, retry := OpenAIWSUnsentTurnPayload(err)
		require.False(t, retry)
	}
	for _, reason := range []string{"wire_changed", "delivery_pending", "store_unavailable"} {
		err := openAIWSUnsentAgentTurn(&openAIWSOwnerRouteError{cause: openAIAgentRecoveryError(reason), completedMixedOwners: true}, []byte(`{"input":"fixture"}`), "gpt-6-astra", 53)
		_, _, _, retry := OpenAIWSUnsentTurnPayload(err)
		require.False(t, retry)
	}
}

func TestOpenAIWSMixedOwnerProofRecheckedAtRebuild(t *testing.T) {
	for _, mutation := range []string{"none", "missing", "changed"} {
		t.Run(mutation, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			a, b := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			other := strings.ReplaceAll(issue715OpaqueBody, "fixture-opaque-reference", "fixture-other-owner")
			issue715LearnRoute(t, svc, a, issue715OpaqueBody)
			issue715LearnRoute(t, svc, b, other)
			body := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"continue this task"},` + gjson.Get(issue715OpaqueBody, "input.0").Raw + `,` + gjson.Get(other, "input.0").Raw + `]}`)
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := svc.WithOpenAIWSAgentRecovery(context.Background(), c, body, "fixture-session", "gpt-6-astra")
			proof := openAIAgentRecoveryFromContext(ctx)
			require.True(t, proof.hasCompletedMixedOwners())
			before := svc.checkOpenAIAgentRecoveryAccount(ctx, b)
			err := openAIWSUnsentAgentTurn(&openAIWSOwnerRouteError{cause: before, completedMixedOwners: true, completedSource: proof}, body, "gpt-6-astra", 53)
			_, _, _, unsent := OpenAIWSUnsentTurnPayload(err)
			require.True(t, unsent)
			key := proof.keys[0]
			if mutation == "missing" {
				delete(svc.agentMessageRecovery.bindings, key)
			} else if mutation == "changed" {
				binding := svc.agentMessageRecovery.bindings[key]
				binding.accountID = 3 // Still mixed and completed, but different evidence.
				svc.agentMessageRecovery.bindings[key] = binding
			}
			next, _, changed, rebuildErr := svc.RebuildOpenAIWSUnsentTurn(WithOpenAIWSUnsentOwnerProof(ctx, err), c, body, "fixture-session", "gpt-6-astra")
			if mutation == "none" {
				require.NoError(t, rebuildErr)
				require.True(t, changed)
			} else {
				require.Error(t, rebuildErr)
				require.False(t, changed)
			}
			remaining, _ := next.Value(openAIWSUnsentOwnerProofKey{}).(*openAIAgentRecoveryRequest)
			require.Nil(t, remaining, "proof belongs only to the current turn")
		})
	}
}

func TestOpenAIRecoveryDiagnosticsAreAllowlisted(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	canary := "fixture-private-cipher-body-key-label-itemhash"
	r := &openAIAgentRecoveryRequest{diagnostics: openAIRecoveryDiagnostics{
		owners: openAIRecoveryOwnerCounts{observed: true, total: 3, completed: 2, pending: 1},
		shadow: openAIRecoveryShadowState{lookup: canary, epoch: canary},
	}}
	r.diagnostics.tools, _ = inspectOpenAIWSCompletedTools([]byte(`{"input":[{"type":"function_call","call_id":"` + canary + `"},{"type":"function_call_output","call_id":"` + canary + `","output":"` + canary + `"}]}`))
	err := openAIRecoveryDiagnosticError("owner_conflict", canary, r)
	var recovery *OpenAIAgentRecoveryError
	require.True(t, errors.As(err, &recovery))
	LogOpenAIAgentRecoveryRefusal(ctx, recovery)
	entry := logs.All()[0]
	require.Equal(t, "openai.agent_recovery_refused", entry.Message)
	fields := entry.ContextMap()
	require.Equal(t, "unknown", fields["subreason"])
	require.Equal(t, "unknown", fields["shadow_lookup"])
	require.Equal(t, "unknown", fields["shadow_epoch"])
	require.Equal(t, int64(2), fields["owner_completed"])
	require.Equal(t, int64(1), fields["owner_pending"])
	require.Equal(t, int64(1), fields["tool_pairs"])
	encoded, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), canary)
	require.Equal(t, "encrypted_agent_recovery_owner_conflict", recovery.ClientCode())
	require.Equal(t, openAIAgentRecoveryError("owner_conflict").Error(), recovery.ClientMessage())
	LogOpenAIAgentRecoveryRefusal(ctx, &OpenAIAgentRecoveryError{Reason: "owner_conflict"})
	unknown := logs.All()[1].ContextMap()
	require.Equal(t, "unknown", unknown["owner_observation"])
	require.Equal(t, "unknown", unknown["tool_observation"])
}

type mixedOwnerShadowStore struct {
	GatewayCache
	OpenAIReadableShadowCache
	OpenAIAgentRecoveryEpochCache
	raw, epoch string
}

func (s *mixedOwnerShadowStore) GetOpenAIReadableShadow(context.Context, string, string) (string, error) {
	return s.raw, nil
}
func (s *mixedOwnerShadowStore) GetOpenAIAgentRecoveryEpoch(context.Context, string) (string, error) {
	return s.epoch, nil
}

func TestOpenAIRecoveryShadowRefusalDiagnostics(t *testing.T) {
	for _, mutation := range []string{"none", "record", "epoch"} {
		t.Run(mutation, func(t *testing.T) {
			observation := &openAIReadableShadowObservation{raw: "fixture-private-record", epoch: "ready:pending:fixture-private-epoch"}
			store := &mixedOwnerShadowStore{raw: observation.raw, epoch: observation.epoch}
			if mutation == "record" {
				store.raw += "-changed"
			}
			if mutation == "epoch" {
				store.epoch += "-changed"
			}
			svc := &OpenAIGatewayService{cache: store}
			err := svc.checkOpenAIReadableShadowObservation(context.Background(), observation)
			if mutation == "none" {
				require.NoError(t, err)
				return
			}
			var recovery *OpenAIAgentRecoveryError
			require.ErrorAs(t, err, &recovery)
			require.Equal(t, "encrypted_agent_recovery_owner_conflict", recovery.ClientCode())
			core, logs := observer.New(zap.WarnLevel)
			LogOpenAIAgentRecoveryRefusal(logger.IntoContext(context.Background(), zap.New(core)), recovery)
			fields := logs.All()[0].ContextMap()
			require.Equal(t, "shadow_"+mutation+"_changed", fields["subreason"])
			encoded, marshalErr := json.Marshal(fields)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(encoded), "fixture-private")
		})
	}
}
