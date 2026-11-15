package service

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const issue842Source = `{"model":"gpt-6-astra","store":false,"input":[{"type":"agent_message","author":"fixture-parent","recipient":"fixture-worker","encrypted_content":"fixture-agent842"},{"type":"reasoning","status":"completed","id":"rs_fixture842","summary":[],"content":[{"type":"reasoning_text","text":"readable reasoning842"}],"encrypted_content":"fixture-reasoning842"},{"type":"message","role":"user","content":"keep task842","sequence":9007199254740993},{"type":"function_call","name":"record","call_id":"fc_fixture842","arguments":"{\"n\":9007199254740993}"},{"type":"function_call_output","call_id":"fc_fixture842","output":"already recorded842"}]}`

func TestIssue842FrozenWSInputRejectsSemanticMutation(t *testing.T) {
	for _, mutation := range []string{"intact", "agent_cipher", "author", "reasoning_cipher", "readable_reasoning", "task", "arguments", "result", "pair", "order", "precision", "drop_reasoning", "inject_status", "mutate_source"} {
		t.Run(mutation, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			manager, _, account := issue715WSManager(t, svc, context.Background())
			defer manager.abort()
			source := []byte(issue842Source)
			original := append([]byte(nil), source...)
			// Keep historical production-overlay assemblies compilable. The
			// exact candidate must provide the pre-builder preparation method.
			preparer, ok := any(manager).(interface {
				prepareReadable(*Account, []byte) ([]byte, error)
			})
			require.True(t, ok)
			payload, err := preparer.prepareReadable(account, source)
			require.NoError(t, err)
			require.Equal(t, original, source, "preparation must not mutate the original frame")
			path, value := "", "changed842"
			switch mutation {
			case "agent_cipher":
				path = "input.0.encrypted_content"
			case "author":
				path = "input.0.author"
			case "reasoning_cipher":
				path = "input.1.encrypted_content"
			case "readable_reasoning":
				path = "input.1.content.0.text"
			case "task", "mutate_source":
				path = "input.2.content"
			case "arguments":
				path = "input.3.arguments"
			case "result":
				path = "input.4.output"
			case "pair":
				path = "input.4.call_id"
			case "inject_status":
				path, value = "input.1.status", "completed"
			case "precision":
				payload, err = sjson.SetRawBytes(payload, "input.2.sequence", []byte("9007199254740992"))
			case "drop_reasoning":
				payload, err = sjson.DeleteBytes(payload, "input.1")
			case "order":
				items := gjson.GetBytes(payload, "input").Array()
				items[0], items[2] = items[2], items[0]
				var raw []string
				for _, item := range items {
					raw = append(raw, item.Raw)
				}
				payload, err = sjson.SetRawBytes(payload, "input", []byte("["+strings.Join(raw, ",")+"]"))
			}
			require.NoError(t, err)
			if path != "" {
				payload, err = sjson.SetBytes(payload, path, value)
				require.NoError(t, err)
			}
			if mutation == "mutate_source" {
				source = append([]byte(nil), payload...)
			}
			err = manager.beginSocket(account, source, payload, "wss://native.example/v1/responses", http.Header{"Authorization": {"Bearer fixture"}}, "ctx_pool")
			if mutation == "intact" {
				require.NoError(t, err)
				require.Contains(t, string(payload), "readable reasoning842")
				require.Contains(t, string(payload), "9007199254740993")
			} else {
				var refusal *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &refusal, "a frozen snapshot cannot trust later source or wire mutation")
				require.Equal(t, "wire_changed", refusal.Reason)
			}
		})
	}
}

func TestIssue842FrozenHTTPInputRejectsSemanticMutation(t *testing.T) {
	for _, replacement := range []string{"fixture-agent842", "readable reasoning842", "keep task842", "9007199254740993", "already recorded842"} {
		t.Run(replacement, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			// Explicit expected metadata normalization, independent of helper.
			body := strings.ReplaceAll(strings.ReplaceAll(issue842Source, `,"status":"completed"`, ""), `,"id":"rs_fixture842"`, "")
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, []byte(body), "fixture-session", "gpt-6-astra")
			mutated := strings.Replace(body, replacement, replacement+"-changed", 1)
			if replacement == "9007199254740993" {
				mutated = strings.Replace(body, replacement, "9007199254740992", 1)
			}
			req, err := http.NewRequest(http.MethodPost, "https://fixture.example/v1/responses", bytes.NewBufferString(mutated))
			require.NoError(t, err)
			_, err = svc.prepareOpenAIAgentRecoveryAttempt(ctx, &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, req)
			var refusal *OpenAIAgentRecoveryError
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, "wire_changed", refusal.Reason)
		})
	}
}

func TestIssue842OAuthProjectionRejectsChangedSourceOrWire(t *testing.T) {
	for _, mutation := range []string{"intact", "source_input", "source_controls", "final_input"} {
		t.Run(mutation, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			body := []byte(strings.ReplaceAll(strings.ReplaceAll(issue842Source, `,"status":"completed"`, ""), `"store":false,`, ""))
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, body, "fixture-session", "gpt-6-astra")
			switch mutation {
			case "source_input":
				body = bytes.Replace(body, []byte("keep task842"), []byte("changed task842"), 1)
			case "source_controls":
				var err error
				body, err = sjson.SetBytes(body, "instructions", "unapproved instruction842")
				require.NoError(t, err)
			}
			// Historical overlay compiles against its old service; this method is
			// required only when this exact candidate regression actually runs.
			preparer, ok := any(svc).(interface {
				prepareOpenAIAgentOAuthReplay(context.Context, *Account, []byte) (context.Context, []byte, error)
			})
			require.True(t, ok)
			account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			preparedCtx, prepared, err := preparer.prepareOpenAIAgentOAuthReplay(ctx, account, body)
			if strings.HasPrefix(mutation, "source_") {
				var refusal *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, "wire_changed", refusal.Reason)
				return
			}
			require.NoError(t, err)
			if mutation == "final_input" {
				prepared = bytes.Replace(prepared, []byte("readable reasoning842"), []byte("changed reasoning842"), 1)
			}
			req, err := http.NewRequest(http.MethodPost, "https://fixture.example/v1/responses", bytes.NewReader(prepared))
			require.NoError(t, err)
			finish, err := svc.prepareOpenAIAgentRecoveryAttempt(preparedCtx, account, req)
			if mutation == "intact" {
				require.NoError(t, err)
				finish(true)
			} else {
				var refusal *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, "wire_changed", refusal.Reason)
			}
		})
	}
}
