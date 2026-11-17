package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const issue892Environment = "<environment_context>\n<cwd>/fixture</cwd>\n<timezone>Asia/Singapore</timezone>\n</environment_context>"

func issue892PrepareHTTP(t *testing.T, svc *OpenAIGatewayService, ctx context.Context, account *Account, source []byte) (context.Context, []byte, error) {
	t.Helper()
	// Older unrelated RED overlays predate this method. Keep them compilable;
	// this regression requires the method whenever it is actually executed.
	preparer, ok := any(svc).(interface {
		prepareOpenAIAgentOAuthReplay(context.Context, *Account, []byte) (context.Context, []byte, error)
	})
	require.True(t, ok)
	return preparer.prepareOpenAIAgentOAuthReplay(ctx, account, source)
}

func issue892PrepareWS(t *testing.T, manager *openAIWSAgentRecovery, account *Account, source []byte) ([]byte, error) {
	t.Helper()
	preparer, ok := any(manager).(interface {
		prepareReadable(*Account, []byte) ([]byte, error)
	})
	require.True(t, ok)
	return preparer.prepareReadable(account, source)
}

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
						ctx, payload, err = issue892PrepareHTTP(t, svc, ctx, account, source)
					} else {
						payload, err = issue892PrepareWS(t, manager, account, source)
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
							_, _, err = issue892PrepareHTTP(t, svc, ctx, account, source)
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
		next, body, err := issue892PrepareHTTP(t, svc, ctx, account, source)
		require.NoError(t, err)
		require.Equal(t, openAIAgentRecoveryInputDigest(issue892Expected(t, source, zone)), openAIAgentRecoveryInputDigest(body))
		require.Equal(t, openAIAgentRecoveryInputDigest(source), openAIAgentRecoveryFromContext(next).inputDigest)
		body, err = issue892PrepareWS(t, manager, account, source)
		require.NoError(t, err)
		require.Equal(t, openAIAgentRecoveryInputDigest(issue892Expected(t, source, zone)), openAIAgentRecoveryInputDigest(body))
	}
	require.Equal(t, "9007199254740993", gjson.GetBytes(source, "input.2.sequence").Raw)
}

func TestIssue892TimezonePolicyControlsProjection(t *testing.T) {
	for _, mode := range []string{"disabled", "auto", "manual", "aligned"} {
		t.Run(mode, func(t *testing.T) {
			svc := timezoneAutoService(mode != "disabled", mode == "auto", nil)
			svc.cfg = &config.Config{}
			svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			manager, c, account := issue715WSManager(t, svc, context.Background())
			defer manager.abort()
			timezone := "America/Los_Angeles"
			if mode == "aligned" {
				timezone = "Asia/Singapore"
			}
			account.Extra = map[string]any{openAIRequestTimezoneExtraKey: timezone}
			source := issue892FrozenSource(t)
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, source, "fixture-session", "gpt-6-astra")
			_, body, err := issue892PrepareHTTP(t, svc, ctx, account, source)
			require.NoError(t, err)
			want := source
			if mode == "manual" {
				want = issue892Expected(t, source, timezone)
			}
			require.Equal(t, openAIAgentRecoveryInputDigest(want), openAIAgentRecoveryInputDigest(body))
			body, err = issue892PrepareWS(t, manager, account, source)
			require.NoError(t, err)
			require.Equal(t, openAIAgentRecoveryInputDigest(want), openAIAgentRecoveryInputDigest(body))
		})
	}
}

func TestIssue892OpaqueEnvironmentProjectionRefused(t *testing.T) {
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, transport := range []string{"http", "ws"} {
			t.Run(kind+"/"+transport, func(t *testing.T) {
				svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: timezoneEnabledSettings()}
				svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
				manager, c, account := issue715WSManager(t, svc, context.Background())
				defer manager.abort()
				account.Type, account.Extra = kind, map[string]any{openAIRequestTimezoneExtraKey: "America/Los_Angeles"}
				source, err := sjson.SetBytes(issue892FrozenSource(t), "input.0.role", "user")
				require.NoError(t, err)
				source, err = sjson.SetBytes(source, "input.0.content", []any{map[string]any{"type": "input_text", "text": issue892Environment}})
				require.NoError(t, err)
				original := append([]byte(nil), source...)
				ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, source, "fixture-session", "gpt-6-astra")
				if transport == "http" {
					_, _, err = issue892PrepareHTTP(t, svc, ctx, account, source)
				} else {
					_, err = issue892PrepareWS(t, manager, account, source)
				}
				var refusal *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &refusal, "Issue895 F1 projection must refuse a changed opaque envelope")
				require.Equal(t, "wire_changed", refusal.Reason)
				require.Equal(t, original, source)
			})
		}
	}
}

func TestIssue892ActualSendOpaqueIntegrityAndDelivery(t *testing.T) {
	for _, transport := range []string{"http", "ws"} {
		for _, mutation := range []string{"intact", "envelope", "drop"} {
			t.Run(transport+"/"+mutation, func(t *testing.T) {
				svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: timezoneEnabledSettings()}
				svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
				manager, c, account := issue715WSManager(t, svc, context.Background())
				defer manager.abort()
				account.Extra = map[string]any{openAIRequestTimezoneExtraKey: "America/Los_Angeles"}
				source := issue892FrozenSource(t)
				ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, source, "fixture-session", "gpt-6-astra")
				var payload []byte
				var err error
				if transport == "http" {
					ctx, payload, err = issue892PrepareHTTP(t, svc, ctx, account, source)
				} else {
					payload, err = issue892PrepareWS(t, manager, account, source)
				}
				require.NoError(t, err)
				switch mutation {
				case "envelope":
					payload, err = sjson.SetBytes(payload, "input.0.content", "changed opaque envelope")
				case "drop":
					payload, err = sjson.DeleteBytes(payload, "input.0")
				}
				require.NoError(t, err)
				// Model the former permissive input projection. The independent
				// original item proof must still reject it at the actual send gate.
				// No item digest is replaced, including for an unknown owner.
				var finish func(bool)
				if transport == "http" {
					openAIAgentRecoveryFromContext(ctx).nativeInputDigest = openAIAgentRecoveryInputDigest(payload)
					req, requestErr := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(payload))
					require.NoError(t, requestErr)
					finish, err = svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
					defer finish(false)
				} else {
					manager.protected.inputDigest = openAIAgentRecoveryInputDigest(payload)
					err = manager.beginSocket(account, source, payload, "wss://native.example/v1/responses", http.Header{"Authorization": {"Bearer fixture"}}, "ctx_pool")
				}
				if mutation != "intact" {
					var refusal *OpenAIAgentRecoveryError
					require.ErrorAs(t, err, &refusal, "Issue895 F1 actual send must reject a changed opaque item even if input projection matched")
					require.Equal(t, "wire_changed", refusal.Reason)
					return
				}
				require.NoError(t, err)
				request := openAIAgentRecoveryFromContext(ctx)
				if transport == "ws" {
					request = manager.request
				}
				require.Nil(t, request.owner, "exercise a first-seen opaque item")
				require.NotNil(t, request.delivery, "an intact unknown owner must retain its delivery fence")
				output := []byte(`{"type":"response.completed","response":{"id":"resp_fixture892_delivery","status":"completed","output":[{"type":"agent_message","encrypted_content":"fixture-new-output892"}]}}`)
				assertReserved := func() error {
					require.Len(t, request.delivery.keys, 1, "reserve the new opaque output before client exposure")
					for key := range request.delivery.keys {
						svc.agentMessageRecovery.mu.Lock()
						binding := svc.agentMessageRecovery.bindings[key]
						svc.agentMessageRecovery.mu.Unlock()
						require.NotEmpty(t, binding.pending, "client write must observe a pending reservation")
					}
					return nil
				}
				if transport == "http" {
					c.Request = c.Request.WithContext(ctx)
					require.NoError(t, observeOpenAIAgentRecoveryOutput(c, output, true))
					require.NoError(t, assertReserved())
					finish(true)
				} else {
					require.NoError(t, manager.write(output, assertReserved))
				}
			})
		}
	}
}

func TestIssue892PreparedUnknownOwnerRechecksFences(t *testing.T) {
	for _, transport := range []string{"http", "ws"} {
		for _, fence := range []string{"pending", "failed"} {
			t.Run(transport+"/"+fence, func(t *testing.T) {
				svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: timezoneEnabledSettings()}
				svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
				manager, c, account := issue715WSManager(t, svc, context.Background())
				defer manager.abort()
				account.Extra = map[string]any{openAIRequestTimezoneExtraKey: "America/Los_Angeles"}
				source := issue892FrozenSource(t)
				ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, source, "fixture-session", "gpt-6-astra")
				var payload []byte
				var err error
				if transport == "http" {
					ctx, payload, err = issue892PrepareHTTP(t, svc, ctx, account, source)
				} else {
					payload, err = issue892PrepareWS(t, manager, account, source)
				}
				require.NoError(t, err)
				request := openAIAgentRecoveryFromContext(ctx)
				require.Nil(t, request.owner)
				require.Len(t, request.keys, 1)
				late := openAIAgentRecoveryBinding{accountID: account.ID, expiresAt: time.Now().Add(time.Minute), failed: fence == "failed"}
				if fence == "pending" {
					late.pending = "fixture-late-owner892"
				}
				svc.agentMessageRecovery.mu.Lock()
				svc.agentMessageRecovery.bindings = map[[32]byte]openAIAgentRecoveryBinding{request.keys[0]: late}
				svc.agentMessageRecovery.mu.Unlock()
				if transport == "http" {
					req, requestErr := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(payload))
					require.NoError(t, requestErr)
					finish, attemptErr := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
					defer finish(false)
					err = attemptErr
				} else {
					err = manager.beginSocket(account, source, payload, "wss://native.example/v1/responses", http.Header{"Authorization": {"Bearer fixture"}}, "ctx_pool")
				}
				var refusal *OpenAIAgentRecoveryError
				require.ErrorAs(t, err, &refusal, "a fence appearing after preparation must prevent the actual send")
				require.Contains(t, []string{"owner_conflict", "owner_unavailable", "delivery_pending"}, refusal.Reason)
				require.Nil(t, request.delivery)
			})
		}
	}
}

func TestIssue892ManualTimezone400Recovery(t *testing.T) {
	for _, mode := range []string{"enabled", "disabled", "ordinary400", "SSE"} {
		t.Run(mode, func(t *testing.T) {
			response := func(status int, body string) *http.Response {
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
			}
			failure := response(http.StatusBadRequest, history400Failure)
			if mode == "ordinary400" {
				failure = response(http.StatusBadRequest, `{"error":{"code":"invalid_request_error","message":"fixture rejection"}}`)
			}
			if mode == "SSE" {
				failure = newStreamLineageSSEResponse(encryptedSSERetryEvent(encryptedSSERetryFailure))
			}
			upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{failure, response(http.StatusOK, `{"id":"resp_fixture892","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}}
			svc := newStreamLineageHTTPService(upstream)
			svc.cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			svc.settingService = NewSettingService(&history400SettingsRepo{values: map[string]string{
				SettingKeyOpenAIRequestTimezoneEnabled:         "true",
				SettingKeyOpenAIRequestTimezoneAuto:            "false",
				SettingKeyOpenAI400OmitEncryptedHistoryEnabled: fmt.Sprint(mode != "disabled"),
			}}, svc.cfg)
			input := "[" + gjson.Get(issue715OpaqueBody, "input.0").Raw + "," + strings.TrimPrefix(history400Input, "[")
			body, err := sjson.SetBytes(history400Body(input, mode == "SSE"), "input.-1", map[string]any{"role": "user", "content": issue892Environment})
			require.NoError(t, err)
			c, rec := newStreamLineageTestContext("fixture892-400")
			c.Set("api_key", &APIKey{ID: 7, AllowLongContext: true})
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, body, "fixture892-400", "gpt-5.1")
			c.Request = c.Request.WithContext(ctx)
			account := newStreamLineageHTTPAccount()
			account.Extra = map[string]any{openAIRequestTimezoneExtraKey: "America/Los_Angeles"}
			_, err = svc.Forward(ctx, c, account, body)
			if mode == "enabled" {
				require.NoError(t, err, rec.Body.String())
				require.Equal(t, 2, upstream.callCount, "one authorized same-account HTTP 400 recovery")
				for _, path := range []string{"input.3", "input.4", "input.5", "input.6"} {
					require.JSONEq(t, gjson.GetBytes(upstream.bodies[0], path).Raw, gjson.GetBytes(upstream.bodies[1], path).Raw)
				}
				require.Equal(t, upstream.reqs[0].URL.String(), upstream.reqs[1].URL.String())
				require.Equal(t, upstream.reqs[0].Header.Get("Authorization"), upstream.reqs[1].Header.Get("Authorization"))
				require.NotContains(t, string(upstream.bodies[1]), "AAAA-fixture-1111")
				retryCtx := upstream.reqs[1].Context()
				require.True(t, openAIAgentRecoveryFromContext(retryCtx).omitted400History)
				_, replayed, prepareErr := issue892PrepareHTTP(t, svc, retryCtx, account, upstream.bodies[1])
				require.NoError(t, prepareErr)
				require.Equal(t, upstream.bodies[1], replayed, "an authorized 400 omission must not restore the original ciphertext")
				require.Same(t, openAIAgentRecoveryFromContext(ctx), openAIAgentRecoveryFromContext(c.Request.Context()), "Forward restores the original recovery after returning")
				_, _, prepareErr = issue892PrepareHTTP(t, svc, c.Request.Context(), account, upstream.bodies[1])
				require.Error(t, prepareErr, "the original context cannot authorize a body with omitted ciphertext")
			} else {
				require.Error(t, err)
				require.Equal(t, 1, upstream.callCount, "400 recovery must not expand to disabled, arbitrary 400 or SSE failures")
			}
			require.Contains(t, gjson.GetBytes(upstream.bodies[0], "input.6.content").String(), "America/Los_Angeles")
			require.Contains(t, string(body), "Asia/Singapore")
		})
	}
}
