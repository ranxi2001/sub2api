//go:build unit && lossyprotocol

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAILossyProtocolBetaDiscoveryHTTPBridgeWire(t *testing.T) {
	for _, lossy := range []bool{false, true} {
		t.Run(fmt.Sprintf("lossy=%t", lossy), func(t *testing.T) { runLossyDiscoveryBridgeWire(t, lossy) })
	}
}

func runLossyDiscoveryBridgeWire(t *testing.T, lossy bool) {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = lossy
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_fixture_inspect\",\"call_id\":\"call_inspect_fixture\",\"name\":\"fixture_tools__inspect\",\"arguments\":\"{}\",\"status\":\"completed\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")),
	}}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, settingService: NewSettingService(&contentModerationTestSettingRepo{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
	c, _ := newTurnStateTestContext(t, 715, "fixture-session")
	c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
	SetOpenAIWSRecoveryPrincipal(c, 715, 715)
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	body := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"tools":[{"type":"tool_search"}],"input":[{"role":"user","content":"Continue fixture task with discovered tools"},{"type":"agent_message","encrypted_content":"fixture-opaque","content":[{"type":"input_text","text":"fixture progress"}]},{"type":"tool_search_call","call_id":"call_search_fixture","arguments":{"query":"fixture"},"status":"completed"},{"type":"tool_search_output","call_id":"call_search_fixture","execution":"client","status":"completed","tools":[{"type":"namespace","name":"fixture_tools","tools":[{"type":"function","name":"inspect","description":"Inspect fixture state","parameters":{"type":"object"}}]}]}]}`)
	// Earn discovery provenance through the actual downstream observer and
	// terminal commit before replaying this completed history on B.
	seed := []byte(`{"model":"gpt-5.1","input":[{"role":"user","content":"Continue fixture task with discovered tools"}]}`)
	seedCtx := svc.WithOpenAIWSAgentRecovery(context.Background(), c, seed, "fixture-session", "gpt-5.1")
	producer := newOpenAIWSAgentRecovery(svc, seedCtx, c)
	defer producer.abort()
	a := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.NoError(t, issue715WSBegin(t, producer, a, string(seed), "ctx_pool"))
	items := gjson.GetBytes(body, "input").Array()
	require.NoError(t, producer.write([]byte(`{"type":"response.completed","response":{"id":"resp_discovery_seed","status":"completed","output":[`+items[1].Raw+`,`+items[2].Raw+`]}}`), func() error { return nil }))
	// A is now a known, successfully delivered owner. Without a response
	// pointer the preselection gate correctly keeps that producer's route.
	// Continue from A's actual response, as the real socket/HTTP loop does.
	owner, err := svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 715, "resp_discovery_seed")
	require.NoError(t, err)
	require.Equal(t, a.ID, owner)
	body, err = sjson.SetBytes(body, "previous_response_id", "resp_discovery_seed")
	require.NoError(t, err)
	ctx := svc.WithOpenAIWSAgentRecovery(context.Background(), c, body, "fixture-session", "gpt-5.1")
	original := openAIAgentRecoveryFromContext(ctx)
	require.NotNil(t, original.owner)
	require.Equal(t, a.ID, original.owner.accountID)
	ctx, clean, changed, err := svc.PrepareOpenAI409ReadableContinuation(ctx, c, body, "fixture-session", "gpt-5.1")
	require.NoError(t, err)
	require.True(t, changed, "exercise Beta history rebuild, not just ordinary bridge adaptation")
	require.False(t, gjson.GetBytes(clean, "previous_response_id").Exists(), "the completed A pointer must detach before B")
	require.Same(t, original, openAIAgentRecoveryFromContext(ctx).omittedSource)
	require.NotEmpty(t, openAIAgentRecoveryFromContext(ctx).toolProofs, "the actually delivered search call must be verified")
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{715}}
	manager := newOpenAIWSAgentRecovery(svc, ctx, c)
	defer manager.abort()
	ctx = context.WithValue(ctx, openAIWSAgentRecoveryKey{}, manager)
	prepared, err := manager.prepareReadable(account, clean)
	require.NoError(t, err)
	// Ingress carries the pre-projection frame separately from the bridge body.
	// Keep the real begin gate active and prove against the same frozen source.
	ctx = context.WithValue(ctx, openAIWSAgentSourceKey{}, append([]byte(nil), clean...))
	var events [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(ctx, c, account, "fixture-token", prepared, len(prepared), "gpt-5.1", "", "", "", "", 1,
		func(event []byte) error {
			return manager.write(event, func() error {
				events = append(events, append([]byte(nil), event...))
				return nil
			})
		})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, string(upstream.lastBody), "fixture-opaque")
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, `tools.#(name==tool_search).type`).String())
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, `tools.#(name==fixture_tools__inspect).type`).String())
	output := gjson.GetBytes(upstream.lastBody, `input.#(type==function_call_output)`)
	require.Equal(t, "call_search_fixture", output.Get("call_id").String())
	require.Contains(t, output.Get("output").String(), "fixture_tools")
	require.Contains(t, output.Get("output").String(), "inspect")
	require.NotEmpty(t, events)
	require.Equal(t, "response.completed", gjson.GetBytes(events[len(events)-1], "type").String())
	call := gjson.GetBytes(events[len(events)-1], "response.output.0")
	require.Equal(t, "function_call", call.Get("type").String())
	require.Equal(t, "fixture_tools", call.Get("namespace").String())
	require.Equal(t, "inspect", call.Get("name").String())
	require.Equal(t, "call_inspect_fixture", call.Get("call_id").String())
	require.JSONEq(t, "{}", call.Get("arguments").String())
	owner, err = svc.getOpenAIWSStateStore().GetResponseAccount(context.Background(), 715, "resp_discovery_seed")
	require.NoError(t, err)
	require.Equal(t, a.ID, owner, "B must not rewrite A's delivered response owner")
}

func TestOpenAILossyProtocolWebSocketDeltaPreservesHistory(t *testing.T) {
	for _, marker := range []string{"transport", "recovery_context"} {
		t.Run(marker, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = true
			svc := &OpenAIGatewayService{cfg: cfg, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := context.Background()
			if marker == "transport" {
				SetOpenAIClientTransport(c, OpenAIClientTransportWS)
			} else {
				ctx = context.WithValue(ctx, openAIWSAgentRecoveryRetryKey{}, &atomic.Bool{})
			}
			// No tools or previous_response_id: the socket's inherited controls
			// are resolved later than this gate. Do not flatten this frame.
			body := []byte(`{"input":[{"type":"agent_message","encrypted_content":"fixture-opaque","content":[{"type":"input_text","text":"fixture task"}]}]}`)
			next, clean, changed := svc.PrepareOpenAILossyHistoryRestart(ctx, c, body, "fixture-session", "fixture-model")
			require.False(t, changed)
			require.Equal(t, body, clean)
			require.Equal(t, ctx, next)
		})
	}
}

func TestOpenAILossyProtocolPreservesAgentInput(t *testing.T) {
	for name, body := range map[string]string{
		"inherited_tools_delta": `{"previous_response_id":"resp_fixture","input":[{"role":"user","content":"continue fixture task"}]}`,
		"conversation_delta":    `{"conversation":"fixture_conversation","input":"continue fixture task"}`,
		"declarations":          `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}],"previous_response_id":"resp_fixture","input":[{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"additional_tools":      `{"additional_tools":[{"type":"function","name":"fixture"}],"input":[{"type":"reasoning","encrypted_content":"fixture-opaque"}]}`,
		"function_pair":         `{"previous_response_id":"resp_fixture","input":[{"type":"function_call","call_id":"call_fixture","namespace":"functions","name":"exec","arguments":"{\"n\":9007199254740993}"},{"type":"function_call_output","call_id":"call_fixture","output":"fixture result"}]}`,
		"custom_pair":           `{"input":[{"type":"custom_tool_call","call_id":"call_fixture","name":"exec","input":"fixture command"},{"type":"custom_tool_call_output","call_id":"call_fixture","output":"fixture result"}]}`,
		"discovery":             `{"input":[{"type":"tool_search_call","call_id":"search_fixture","arguments":{}},{"type":"tool_search_output","call_id":"search_fixture","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]}]}`,
		"orphan":                `{"input":[{"type":"function_call_output","call_id":"missing","output":"fixture result"},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"pending":               `{"input":[{"type":"function_call","call_id":"call_fixture","name":"fixture","arguments":"{}","status":"in_progress"}]}`,
		"compact_trigger":       `{"input":[{"type":"agent_message","encrypted_content":"fixture-opaque"},{"type":"compaction_trigger"}]}`,
		"mcp_discovery":         `{"input":[{"type":"mcp_list_tools","server_label":"fixture","tools":[{"name":"exec","input_schema":{"type":"object"}}]}]}`,
		"mcp_approval_request":  `{"input":[{"type":"mcp_approval_request","id":"approval_fixture","name":"exec","arguments":"{}","server_label":"fixture"}]}`,
		"mcp_approval_response": `{"input":[{"type":"mcp_approval_response","approval_request_id":"approval_fixture","approve":true}]}`,
		"unknown_item":          `{"input":[{"type":"future_tool_catalog","tools":[{"name":"exec"}]},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"malformed_item":        `{"input":[null,{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"nested_tool":           `{"input":[{"role":"assistant","content":[{"type":"tool_search_output","tools":[{"name":"exec"}]}]},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"tool_role":             `{"input":[{"role":"tool","content":"fixture result"},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"missing_role":          `{"input":[{"type":"message","content":"fixture result"},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"media":                 `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/fixture.png"}]},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"text_metadata":         `{"input":[{"role":"user","content":[{"type":"input_text","text":"fixture","future_state":{}}]},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"envelope_metadata":     `{"input":[{"type":"agent_message","encrypted_content":"fixture-opaque","recipient":"functions.exec"}]}`,
		"null_type":             `{"input":[{"type":null,"role":"user","content":"fixture"},{"type":"agent_message","encrypted_content":"fixture-opaque"}]}`,
		"object_input":          `{"input":{"role":"user","content":"fixture","encrypted_content":"fixture-opaque"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			original := []byte(body)
			result, changed := discardOpenAIProviderHistory(original)
			// Exact preservation includes discovery schemas, call IDs, namespaces,
			// large numeric arguments and malformed records for the normal gate.
			require.False(t, changed)
			require.Equal(t, original, result)
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = true
			svc := &OpenAIGatewayService{cfg: cfg, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			ctx := context.Background()
			next, prepared, changed := svc.PrepareOpenAILossyHistoryRestart(ctx, c, original, "fixture-session", "fixture-model")
			require.False(t, changed)
			require.Equal(t, original, prepared)
			require.Equal(t, ctx, next, "declining text recovery must not replace the original request context")
		})
	}
}

func TestOpenAILossyProtocolCompactionPreservesContext(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "compact_endpoint", true: "native_v2"}[native], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = true
			svc := &OpenAIGatewayService{cfg: cfg, settingService: NewSettingService(&codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			c.Set("api_key", &APIKey{ID: 715, GroupID: int64Ptr(715)})
			path := "/v1/responses/compact"
			if native {
				path = "/v1/responses"
				MarkOpenAINativeCompactionV2(c)
			}
			c.Request = httptest.NewRequest(http.MethodPost, path, nil)
			body := []byte(issue715OpaqueBody)
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, body, "fixture-session", "gpt-6-astra")
			next, result, changed := svc.PrepareOpenAILossyHistoryRestart(ctx, c, body, "fixture-session", "gpt-6-astra")
			require.False(t, changed)
			require.Equal(t, body, result)
			require.Same(t, openAIAgentRecoveryFromContext(ctx), openAIAgentRecoveryFromContext(next))
			require.False(t, openAIAgentRecoveryFromContext(next).omittedHistory)
			if native {
				// The incident reached this real pre-send gate after rewriting the
				// compaction input. An unchanged, unknown-owner request still uses
				// the ordinary admission behavior and does not invent an owner.
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
				req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(result))
				require.NoError(t, err)
				complete, err := svc.prepareOpenAIAgentRecoveryAttempt(next, account, req)
				require.NoError(t, err)
				complete(false)
			}
		})
	}
}

func TestOpenAILossyProtocolOwnerlessCompactionRefused(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"fixture context"},{"type":"compaction_trigger"}]}`)
	for _, omitted400 := range []bool{false, true} {
		r := &openAIAgentRecoveryRequest{keyID: 715, session: "fixture-session", model: "gpt-6-astra", nativeCompaction: true, disallowLearning: true,
			omittedHistory: !omitted400, omitted400History: omitted400, inputDigest: openAIAgentRecoveryInputDigest(body)}
		account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
		if omitted400 {
			r.retry400Route = &openAIAgentRecoveryBinding{accountID: account.ID, config: openAIAgentRecoveryConfig(account, r.model)}
		}
		ctx := context.WithValue(context.Background(), openAIAgentRecoveryContextKey{}, r)
		req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(body))
		require.NoError(t, err)
		svc := &OpenAIGatewayService{cfg: &config.Config{}}
		require.NotPanics(t, func() {
			complete, attemptErr := svc.prepareOpenAIAgentRecoveryAttempt(ctx, account, req)
			var refusal *OpenAIAgentRecoveryError
			require.ErrorAs(t, attemptErr, &refusal)
			require.Equal(t, "owner_unavailable", refusal.Reason)
			complete(false)
		}, "LOSSY_OWNERLESS_COMPACTION_GUARD")
		require.Nil(t, r.delivery, "a refused compaction must not reserve or publish delivery")
	}
}

// The mixed-owner projection runs on the third turn of the real WS loop.
// Discovery must stay structured so it can still declare callable tools, while
// completed effects remain inert and unknown/pending results cannot be replayed.
func TestOpenAILossyProtocolCompletedDiscoveryProjection(t *testing.T) {
	const call = `{"type":"tool_search_call","call_id":"tsc_projection","arguments":{"query":"fixture"},"status":"completed"}`
	const output = `{"type":"tool_search_output","call_id":"tsc_projection","status":"completed","tools":[{"type":"namespace","name":"fixture","tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}]}]}`
	const effect = `{"type":"custom_tool_call","name":"exec","call_id":"ctc_effect","input":"read fixture","status":"completed"},{"type":"custom_tool_call_output","call_id":"ctc_effect","output":"already applied"}`
	body := []byte(`{"input":[` + call + `,` + output + `,` + effect + `]}`)
	clean, err := projectOpenAIWSCompletedTools(body)
	require.NoError(t, err, "LOSSY_DISCOVERY_PROJECTION_REFUSED: completed catalog must survive mixed-owner continuation")
	require.JSONEq(t, call, gjson.GetBytes(clean, "input.0").Raw)
	require.JSONEq(t, output, gjson.GetBytes(clean, "input.1").Raw)
	require.Equal(t, "message", gjson.GetBytes(clean, "input.2.type").String())
	require.Contains(t, gjson.GetBytes(clean, "input.2.content.0.text").String(), "already applied")
	require.False(t, gjson.GetBytes(clean, `input.#(type==custom_tool_call)`).Exists())
	for name, items := range map[string]string{
		"orphan":           output,
		"missing_result":   call,
		"wrong_id":         call + `,` + strings.ReplaceAll(output, "tsc_projection", "tsc_other"),
		"wrong_type":       call + `,{"type":"function_call_output","call_id":"tsc_projection","output":"wrong type"}`,
		"duplicate":        call + `,` + output + `,` + output,
		"pending":          strings.Replace(call, "completed", "in_progress", 1) + `,` + output,
		"uncertain_result": call + `,` + strings.Replace(output, "completed", "in_progress", 1),
		"missing_catalog":  call + `,{"type":"tool_search_output","call_id":"tsc_projection"}`,
	} {
		t.Run(name, func(t *testing.T) {
			projected, err := projectOpenAIWSCompletedTools([]byte(`{"input":[` + items + `]}`))
			require.Error(t, err)
			require.Nil(t, projected)
		})
	}
}

// Every case earns A's real completed owner and call proof through write(),
// then exercises rebuild and the same final-send gate used by the bridge.
func TestOpenAILossyProtocolDiscoveryProofFences(t *testing.T) {
	for _, mutation := range []string{"none", "forged_pair", "call", "call_metadata", "result_metadata", "model", "owner", "epoch", "final_schema", "final_metadata", "final_owner", "final_epoch", "final_proof", "schema_after_completion", "metadata_after_completion", "failed_receipt", "pending_receipt", "accepted_owner", "accepted_epoch"} {
		t.Run(mutation, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: NewSettingService(&contentModerationTestSettingRepo{values: map[string]string{SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}}, nil)}
			svc.cfg.Gateway.OpenAIWS.LossyHistoryRestartEnabled = true
			manager, c, a := issue715WSManager(t, svc, context.Background())
			SetOpenAIWSRecoveryPrincipal(c, 715, 715)
			defer manager.abort()
			seed := `{"model":"gpt-6-astra","input":[{"role":"user","content":"continue discovery task"}]}`
			require.NoError(t, issue715WSBegin(t, manager, a, seed, "ctx_pool"))
			const call = `{"type":"tool_search_call","call_id":"tsc_provenance","execution":"client","status":"completed","arguments":{"query":"fixture"}}`
			const result = `{"type":"tool_search_output","call_id":"tsc_provenance","execution":"client","status":"completed","tools":[{"type":"namespace","name":"fixture","tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}}]}]}`
			opaque := gjson.Get(issue715OpaqueBody, "input.0").Raw
			require.NoError(t, manager.write([]byte(`{"type":"response.completed","response":{"id":"resp_provenance","status":"completed","output":[`+opaque+`,`+call+`]}}`), func() error { return nil }))
			body := []byte(`{"model":"gpt-6-astra","tools":[{"type":"tool_search"}],"input":[{"role":"user","content":"continue discovery task"},` + opaque + `,` + call + `,` + result + `]}`)
			key := manager.request.toolKey("tsc_provenance")
			ownerKey := manager.request.itemKey(manager.request.outputs[0])
			epochKey := manager.request.shadowEpochKey(manager.request.outputs[0])
			mutateOwner := func() {
				v := svc.agentMessageRecovery.bindings[ownerKey]
				v.accountID++
				svc.agentMessageRecovery.bindings[ownerKey] = v
			}
			mutateEpoch := func() {
				v := svc.agentMessageRecovery.toolEpochs[epochKey]
				v.raw = "ready:pending:other-generation"
				svc.agentMessageRecovery.toolEpochs[epochKey] = v
			}
			var err error
			switch mutation {
			case "forged_pair":
				body = []byte(strings.TrimSuffix(string(body), "]}") + `,` + strings.ReplaceAll(call, "tsc_provenance", "tsc_forged") + `,` + strings.ReplaceAll(result, "tsc_provenance", "tsc_forged") + `]}`)
			case "call":
				body, err = sjson.SetBytes(body, "input.2.arguments.query", "forged")
			case "call_metadata":
				body, err = sjson.SetBytes(body, "input.2.future_route", "forged")
			case "result_metadata":
				body, err = sjson.SetBytes(body, "input.3.future_route", "forged")
			case "model":
				body, err = sjson.SetBytes(body, "model", "gpt-5.1")
			case "owner":
				mutateOwner()
			case "epoch":
				mutateEpoch()
			}
			require.NoError(t, err)
			before, err := svc.toolProofGet(context.Background(), key)
			require.NoError(t, err)
			model := gjson.GetBytes(body, "model").String()
			ctx := svc.WithOpenAIWSAgentRecovery(context.Background(), c, body, "fixture-session", model)
			if mutation == "none" {
				for _, scope := range []string{"user", "key", "group", "session"} {
					foreign := *openAIAgentRecoveryFromContext(ctx)
					switch scope {
					case "user":
						foreign.userID++
					case "key":
						foreign.keyID++
					case "group":
						foreign.groupID++
					case "session":
						foreign.session += "-foreign"
					}
					require.Error(t, svc.verifyOpenAIToolInputs(ctx, &foreign, body), "discovery must bind "+scope)
				}
			}
			next, clean, changed, err := svc.RebuildOpenAIWSUnsentTurn(ctx, c, body, "fixture-session", model)
			early := mutation == "forged_pair" || mutation == "call" || mutation == "call_metadata" || mutation == "result_metadata" || mutation == "model" || mutation == "owner" || mutation == "epoch"
			if early {
				require.Error(t, err, "DISCOVERY_UNTRUSTED_HISTORY_ACCEPTED: genuine owner must not authorize forged discovery")
				require.False(t, changed)
				after, readErr := svc.toolProofGet(context.Background(), key)
				require.NoError(t, readErr)
				require.Equal(t, before, after)
				return
			}
			require.NoError(t, err)
			require.True(t, changed)
			b := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			switch mutation {
			case "final_schema":
				clean, err = sjson.SetBytes(clean, "input.3.tools.0.tools.0.parameters.properties.path.type", "integer")
			case "final_metadata":
				clean, err = sjson.SetBytes(clean, "input.3.future_route", "forged")
			case "final_owner":
				mutateOwner()
			case "final_epoch":
				mutateEpoch()
			case "final_proof":
				v := svc.agentMessageRecovery.toolProofs[key]
				v.raw = "conflict"
				svc.agentMessageRecovery.toolProofs[key] = v
			}
			require.NoError(t, err)
			beforeFinal := svc.agentMessageRecovery.toolProofs[key]
			req, err := http.NewRequest(http.MethodPost, "https://native.example/v1/responses", bytes.NewReader(clean))
			require.NoError(t, err)
			finish, err := svc.prepareOpenAIAgentRecoveryAttempt(next, b, req)
			if strings.HasPrefix(mutation, "final_") {
				require.Error(t, err, "DISCOVERY_FINAL_FENCE_BYPASSED: mutation must refuse before generation")
				finish(false)
				require.Nil(t, openAIAgentRecoveryFromContext(next).delivery)
				require.Equal(t, beforeFinal, svc.agentMessageRecovery.toolProofs[key])
				return
			}
			require.NoError(t, err)
			r := openAIAgentRecoveryFromContext(next)
			if mutation == "pending_receipt" {
				defer finish(false)
				pendingCtx := svc.WithOpenAIWSAgentRecovery(context.Background(), c, body, "fixture-session", model)
				_, _, _, err := svc.RebuildOpenAIWSUnsentTurn(pendingCtx, c, body, "fixture-session", model)
				require.Error(t, err, "DISCOVERY_CATALOG_REBOUND: pending receipt cannot authorize another generation")
				return
			}
			// Real output observer plus delivered terminal is required to commit
			// the catalog receipt; a successful send/HTTP200 is insufficient.
			observer := &gin.Context{Request: (&http.Request{}).WithContext(next)}
			acceptedOpaque := strings.ReplaceAll(opaque, "fixture-opaque-reference", "fixture-accepted-B")
			require.NoError(t, observeOpenAIAgentRecoveryOutput(observer, []byte(`{"type":"response.completed","response":{"id":"resp_catalog_accepted","status":"completed","output":[`+acceptedOpaque+`,{"type":"message","role":"assistant","content":"catalog accepted"}]}}`), true))
			finish(mutation != "failed_receipt")
			require.NotNil(t, r.delivery)
			if mutation == "accepted_owner" {
				key := r.itemKey(r.outputs[0])
				value := svc.agentMessageRecovery.bindings[key]
				value.accountID++
				svc.agentMessageRecovery.bindings[key] = value
			}
			if mutation == "accepted_epoch" {
				key := r.shadowEpochKey(r.outputs[0])
				value := svc.agentMessageRecovery.toolEpochs[key]
				value.raw = "ready:pending:changed-B"
				svc.agentMessageRecovery.toolEpochs[key] = value
			}
			if mutation == "schema_after_completion" {
				body, err = sjson.SetBytes(body, "input.3.tools.0.tools.0.parameters.properties.path.type", "integer")
			}
			if mutation == "metadata_after_completion" {
				body, err = sjson.SetBytes(body, "input.3.tools.0.tools.0.future_route", "forged")
			}
			require.NoError(t, err)
			ctx = svc.WithOpenAIWSAgentRecovery(context.Background(), c, body, "fixture-session", model)
			_, _, _, err = svc.RebuildOpenAIWSUnsentTurn(ctx, c, body, "fixture-session", model)
			if mutation == "none" {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "DISCOVERY_CATALOG_REBOUND: submitted schema/metadata or failed delivery must not mint new trust")
			}
		})
	}
}

// API-key native HTTP uses the same normalizer before freezing the readable
// projection and again in Forward. Discovery declarations must keep call
// namespaces without turning message metadata or compact input into tool state.
func TestOpenAILossyProtocolDiscoveryNamespaceProjection(t *testing.T) {
	const catalog = `[{"type":"namespace","name":"fixture_tools","tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","enum":[9007199254740993]}},"required":["path","limit"],"additionalProperties":false}}]}]`
	for _, tc := range []struct {
		name, status, kind string
		compact, keep      bool
	}{
		{"completed", `,"status":"completed"`, "tool_search_output", false, true},
		{"status_omitted", "", "tool_search_output", false, true},
		{"pending", `,"status":"in_progress"`, "tool_search_output", false, false},
		{"failed", `,"status":"failed"`, "tool_search_output", false, false},
		{"null_status", `,"status":null`, "tool_search_output", false, false},
		{"unknown_carrier", `,"status":"completed"`, "future_tool_catalog", false, false},
		{"compact", `,"status":"completed"`, "tool_search_output", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"tsc_namespace","arguments":{"query":"fixture"}},` +
				`{"type":"` + tc.kind + `","call_id":"tsc_namespace","tools":` + catalog + tc.status + `},` +
				`{"type":"function_call","namespace":"fixture_tools","name":"inspect","call_id":"fc_inspect","arguments":"{\"path\":\"sum.go\",\"limit\":9007199254740993}"},` +
				`{"type":"function_call_output","call_id":"fc_inspect","output":"done"},` +
				`{"type":"message","role":"user","namespace":"residual","content":"continue"}]}`)
			original := append([]byte(nil), body...)
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			prepared, err := normalizeOpenAIResponsesNamespaces(c, account, body, OpenAIUpstreamTransportHTTPSSE, false, tc.compact)
			require.NoError(t, err)
			require.Equal(t, tc.keep, gjson.GetBytes(prepared, "input.2.namespace").Exists(), "LOSSY_DISCOVERY_NAMESPACE_LOST: discovery schema must resolve its native call")
			if tc.keep {
				require.Equal(t, "fixture_tools", gjson.GetBytes(prepared, "input.2.namespace").String())
			}
			require.Equal(t, catalog, gjson.GetBytes(prepared, "input.1.tools").Raw)
			require.Equal(t, gjson.GetBytes(body, "input.2.arguments").Raw, gjson.GetBytes(prepared, "input.2.arguments").Raw)
			require.Equal(t, "fc_inspect", gjson.GetBytes(prepared, "input.3.call_id").String())
			require.False(t, gjson.GetBytes(prepared, "input.4.namespace").Exists())
			again, err := normalizeOpenAIResponsesNamespaces(c, account, prepared, OpenAIUpstreamTransportHTTPSSE, false, tc.compact)
			require.NoError(t, err)
			require.Equal(t, prepared, again, "actual builder must match the immutable pre-builder projection")
			require.Equal(t, original, body)
		})
	}
}
