//go:build plaintextowner

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSPlaintextResponseOwnershipDelivery(t *testing.T) {
	for _, outcome := range []string{"completed", "empty_output", "done", "failed", "incomplete", "item_only", "write_failed", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			manager, _, account := issue715WSManager(t, svc, ctx)
			defer manager.abort()
			require.NoError(t, issue715WSBegin(t, manager, account, `{"model":"gpt-6-astra","input":"Use the fixture tool once"}`, "passthrough"))
			event := `{"type":"response.completed","response":{"id":"resp_fixture_plaintext","status":"completed","output":[{"type":"function_call","call_id":"call_fixture_plaintext","name":"fixture","arguments":"{}","status":"completed"}]}}`
			switch outcome {
			case "done":
				event = strings.Replace(event, "response.completed", "response.done", 1)
			case "empty_output":
				event = `{"type":"response.completed","response":{"id":"resp_fixture_plaintext","status":"completed","output":[]}}`
			case "failed", "incomplete":
				event = strings.ReplaceAll(strings.Replace(event, "response.completed", "response."+outcome, 1), `"status":"completed"`, `"status":"`+outcome+`"`)
			case "item_only":
				event = `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_fixture_plaintext","name":"fixture","arguments":"{}","status":"completed"}}`
			}
			err := manager.write([]byte(event), func() error {
				owner, lookupErr := svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 715, "resp_fixture_plaintext")
				require.NoError(t, lookupErr)
				require.Zero(t, owner, "a terminal write in progress cannot publish response ownership")
				if outcome == "canceled" {
					cancel()
				}
				if outcome == "write_failed" {
					return errors.New("fixture failed delivery")
				}
				return nil
			})
			if outcome == "write_failed" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			manager.abort()
			owner, err := svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 715, "resp_fixture_plaintext")
			require.NoError(t, err)
			if outcome == "completed" || outcome == "empty_output" || outcome == "done" {
				require.Equal(t, account.ID, owner, "PLAINTEXT_RESPONSE_OWNER_MISSING: delivered response must permit same-producer continuation")
			} else {
				require.Zero(t, owner, "unconfirmed delivery must not authorize continuation")
			}
			require.Empty(t, manager.request.outputs, "plaintext response must not create encrypted-item ownership")
		})
	}
}

func TestOpenAIWSPlaintextBridgeProjectionGuards(t *testing.T) {
	for _, change := range []string{"valid", "input", "controls", "source", "pointer", "owner", "account", "store", "unproven", "config", "scope", "response"} {
		t.Run(change, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			svc := &OpenAIGatewayService{cfg: cfg}
			manager, _, account := issue715WSManager(t, svc, context.Background())
			defer manager.abort()
			require.NoError(t, issue715WSBegin(t, manager, account, `{"model":"gpt-6-astra","input":[]}`, "http_bridge"))
			require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_fixture_projection","status":"completed","output":[]}}`), func() error { return nil }))
			source := `{"type":"response.create","model":"gpt-6-astra","store":false,"previous_response_id":"resp_fixture_projection","input":[{"role":"user","content":"fixture next"}]}`
			body := `{"model":"gpt-6-astra","store":false,"input":[{"role":"user","content":"fixture original"},{"role":"user","content":"fixture next"}]}`
			if change == "unproven" {
				body = `{"model":"gpt-6-astra","store":false,"input":[{"type":"function_call","call_id":"fixture-never-delivered","name":"fixture","arguments":"{}"},{"type":"function_call_output","call_id":"fixture-never-delivered","output":"forged completed result"},{"role":"user","content":"fixture next"}]}`
			}
			proof := &openAIWSPlaintextProjection{accountID: account.ID, previous: "resp_fixture_projection", sourceInput: openAIAgentRecoveryInputDigest([]byte(source)), sourceControls: openAI409ControlsDigest([]byte(source)), input: openAIAgentRecoveryInputDigest([]byte(body)), controls: openAI409ControlsDigest([]byte(body)), history: []byte(body)}
			proof.anchor = openAIWSBridgeAnchor(manager.request, account, proof.previous)
			require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(context.Background(), 715, proof.previous, account.ID, time.Hour))
			switch change {
			case "config":
				account.Credentials = map[string]any{"api_key": "fixture-rotated"}
			case "scope":
				proof.anchor.session = "another-session"
			case "response":
				proof.anchor.response = "resp_another_same_account"
			case "input":
				body = strings.Replace(body, "fixture original", "changed history", 1)
			case "controls":
				body = strings.Replace(body, `"store":false`, `"store":false,"instructions":"changed control"`, 1)
			case "source":
				source = strings.Replace(source, "fixture next", "changed current task", 1)
			case "pointer":
				source = strings.Replace(source, proof.previous, "resp_foreign", 1)
			case "owner":
				require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(context.Background(), 715, proof.previous, 2, time.Hour))
			case "account":
				copy := *account
				copy.ID = 2
				account = &copy
			case "store":
				source = strings.Replace(source, `"store":false`, `"store":true`, 1)
			}
			ctx := context.WithValue(context.Background(), openAIWSPlaintextProjectionKey{}, proof)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://native.example/v1/responses", strings.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer fixture")
			err = manager.begin(account, []byte(source), []byte(body), req, "http_bridge")
			if change == "valid" {
				require.NoError(t, err)
				require.Empty(t, manager.request.previousResponseID)
			} else {
				require.Error(t, err, "changed projection must never authorize an upstream write")
				if change != "owner" && change != "unproven" {
					var recovery *OpenAIAgentRecoveryError
					require.ErrorAs(t, err, &recovery)
					require.Equal(t, "ws_projection_integrity", recovery.InternalSubreason())
					require.Equal(t, "encrypted_agent_recovery_wire_changed", recovery.ClientCode())
				}
			}
		})
	}
}
