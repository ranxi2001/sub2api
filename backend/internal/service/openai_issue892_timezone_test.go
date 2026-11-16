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

const issue892Environment = "<environment_context>\n<cwd>/fixture</cwd>\n<timezone>Asia/Singapore</timezone>\n</environment_context>"

func issue892FrozenSource(t *testing.T) []byte {
	t.Helper()
	// Independently remove the metadata normalized before the original freeze.
	body := []byte(strings.ReplaceAll(strings.ReplaceAll(issue842Source, `,"status":"completed"`, ""), `,"id":"rs_fixture842"`, ""))
	var err error
	body, err = sjson.SetBytes(body, "input.-1", map[string]any{"role": "user", "content": issue892Environment})
	require.NoError(t, err)
	body, err = sjson.SetBytes(body, "input.-1", map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": issue892Environment}, map[string]any{"type": "input_text", "text": "Keep Asia/Singapore in ordinary task text"}}})
	require.NoError(t, err)
	return body
}

func issue892Expected(t *testing.T, source []byte, timezone string) []byte {
	t.Helper()
	environment := strings.Replace(issue892Environment, "Asia/Singapore", timezone, 1)
	want, err := sjson.SetBytes(source, "input.5.content", environment)
	require.NoError(t, err)
	want, err = sjson.SetBytes(want, "input.6.content.0.text", environment)
	require.NoError(t, err)
	return want
}

func TestIssue892FrozenAccountTimezoneProjection(t *testing.T) {
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, transport := range []string{"http", "ws"} {
			for _, mutation := range []string{"intact", "task", "cipher", "arguments", "result", "number", "order", "timezone", "outside_environment", "source", "account"} {
				t.Run(kind+"/"+transport+"/"+mutation, func(t *testing.T) {
					svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: timezoneEnabledSettings()}
					svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
					manager, c, account := issue715WSManager(t, svc, context.Background())
					defer manager.abort()
					account.Type = kind
					account.Extra = map[string]any{openAIRequestTimezoneExtraKey: "America/Los_Angeles"}
					source := issue892FrozenSource(t)
					original := append([]byte(nil), source...)
					ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, source, "fixture-session", "gpt-6-astra")
					var payload []byte
					var err error
					if transport == "http" {
						ctx, payload, err = svc.prepareOpenAIAgentOAuthReplay(ctx, account, source)
					} else {
						payload, err = manager.prepareReadable(account, source)
					}
					require.NoError(t, err)
					require.Equal(t, original, source, "account preparation must preserve the original bytes")
					require.Equal(t, openAIAgentRecoveryInputDigest(issue892Expected(t, source, "America/Los_Angeles")), openAIAgentRecoveryInputDigest(payload), "Issue892 account timezone must be projected from the frozen source")
					path, value := "", "unauthorized892"
					switch mutation {
					case "task", "source":
						path = "input.2.content"
					case "cipher":
						path = "input.0.encrypted_content"
					case "arguments":
						path = "input.3.arguments"
					case "result":
						path = "input.4.output"
					case "number":
						payload, err = sjson.SetRawBytes(payload, "input.2.sequence", []byte("9007199254740992"))
					case "order":
						payload, err = sjson.DeleteBytes(payload, "input.2")
					case "timezone":
						path, value = "input.5.content", strings.Replace(issue892Environment, "Asia/Singapore", "Asia/Tokyo", 1)
					case "outside_environment":
						path = "input.6.content.1.text"
					case "account":
						account.ID++
					}
					require.NoError(t, err)
					if path != "" {
						payload, err = sjson.SetBytes(payload, path, value)
						require.NoError(t, err)
					}
					if mutation == "source" {
						source = append([]byte(nil), payload...)
						if transport == "http" {
							_, _, err = svc.prepareOpenAIAgentOAuthReplay(ctx, account, source)
							require.Error(t, err, "a prepared body cannot replace the original source")
							return
						}
					}
					if transport == "http" {
						req, requestErr := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(payload))
						require.NoError(t, requestErr)
						finish, attemptErr := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
						defer finish(false)
						err = attemptErr
					} else {
						err = manager.beginSocket(account, source, payload, "wss://native.example/v1/responses", http.Header{"Authorization": {"Bearer fixture"}}, "ctx_pool")
					}
					if mutation == "intact" {
						require.NoError(t, err)
					} else {
						var refusal *OpenAIAgentRecoveryError
						require.ErrorAs(t, err, &refusal)
						require.Equal(t, "wire_changed", refusal.Reason)
					}
				})
			}
		}
	}
}

func TestIssue892AccountReselectionUsesOriginalSource(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: timezoneEnabledSettings()}
	svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
	manager, c, account := issue715WSManager(t, svc, context.Background())
	defer manager.abort()
	source := issue892FrozenSource(t)
	ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, source, "fixture-session", "gpt-6-astra")
	for _, zone := range []string{"America/Los_Angeles", "Asia/Tokyo", "Asia/Singapore"} {
		account.ID++
		account.Extra = map[string]any{openAIRequestTimezoneExtraKey: zone}
		next, body, err := svc.prepareOpenAIAgentOAuthReplay(ctx, account, source)
		require.NoError(t, err)
		require.Equal(t, openAIAgentRecoveryInputDigest(issue892Expected(t, source, zone)), openAIAgentRecoveryInputDigest(body))
		require.Equal(t, source, openAIAgentRecoveryFromContext(next).replaySource)
		body, err = manager.prepareReadable(account, source)
		require.NoError(t, err)
		require.Equal(t, openAIAgentRecoveryInputDigest(issue892Expected(t, source, zone)), openAIAgentRecoveryInputDigest(body))
	}
	require.Equal(t, "9007199254740993", gjson.GetBytes(source, "input.2.sequence").Raw)
}
