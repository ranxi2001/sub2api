//go:build unit && inheritedmodel

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Keep this tag opt-in: historical production overlays must not compile a new
// fixture against an older handler harness. The dedicated CI uses a complete
// pinned source tree and copies only this identical candidate regression.
func TestOpenAIWSInheritedModelToolProof(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		t.Run(kind, func(t *testing.T) {
			for _, scenario := range []string{"explicit", "inherited", "mapped_explicit", "mapped_inherited"} {
				t.Run(scenario, func(t *testing.T) { runWSInheritedModelToolProof(t, kind, scenario) })
			}
		})
	}
}

func TestOpenAIWSInheritedModelProofFences(t *testing.T) {
	for _, kind := range []string{"function", "custom"} {
		t.Run(kind, func(t *testing.T) {
			for _, scenario := range []string{"unknown", "pending", "epoch", "arguments", "terminal_excludes", "model_change", "same_wire_model_change"} {
				t.Run(scenario, func(t *testing.T) { runWSInheritedModelToolProof(t, kind, scenario) })
			}
		})
	}
}

// UseNumber keeps both schema and completed-result comparisons lossless.
func inheritedModelJSONEqual(a, b string) bool {
	decode := func(raw string) (any, error) {
		var value any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		err := d.Decode(&value)
		return value, err
	}
	x, xerr := decode(a)
	y, yerr := decode(b)
	return xerr == nil && yerr == nil && reflect.DeepEqual(x, y)
}

func inheritedModelRecords(t *testing.T, client *redis.Client) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for _, pattern := range []string{"openai_agent_owner:*", "openai_agent_shadow_epoch:*", "openai_tool_provenance:v1:*", "openai_readable_shadow:*"} {
		keys, err := client.Keys(context.Background(), pattern).Result()
		require.NoError(t, err)
		for _, key := range keys {
			raw, err := client.Get(context.Background(), key).Result()
			require.NoError(t, err)
			result[key] = raw
		}
	}
	return result
}

func runWSInheritedModelToolProof(t *testing.T, kind, scenario string) {
	t.Helper()
	const model = "gpt-5.1"
	const otherModel = "gpt-5.2"
	const task = "Apply the fixture ledger entry exactly once, then report the complete receipt."
	inherited := strings.HasSuffix(scenario, "inherited")
	mapped := strings.HasPrefix(scenario, "mapped_") || scenario == "same_wire_model_change"
	positive := inherited || scenario == "explicit" || scenario == "mapped_explicit"
	wireModel := model
	if mapped {
		wireModel = "gpt-5.3"
	}
	declarations := `[{"type":"function","name":"fixture_ledger","strict":true,"parameters":{"type":"object","properties":{"n":{"type":"integer","maximum":9007199254740993},"memo":{"type":"string"}},"required":["n","memo"],"additionalProperties":false}}]`
	callType, argumentField := "function_call", "arguments"
	if kind == "custom" {
		declarations = `[{"type":"custom","name":"fixture_ledger","description":"Apply one isolated fixture ledger entry","format":{"type":"text"}}]`
		callType, argumentField = "custom_tool_call", "input"
	}
	arguments := `{"n":9007199254740993,"memo":"fixture 雪\nsecond line"}`
	issuedCall := httpRebuildJSON(map[string]any{"type": callType, "call_id": "call_inherited_once", "name": "fixture_ledger", "status": "completed", argumentField: arguments})
	// This result is constructed only after the client receives and executes C.
	var completedResult string
	var mu sync.Mutex
	var frames [][]byte
	var identities []string
	aDisabled := false
	providerResponse := func(auth string, body []byte) [][]byte {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, append([]byte(nil), body...))
		identities = append(identities, auth)
		n := len(frames)
		invalid := func(reason string) [][]byte {
			t.Errorf("INHERITED_PROVIDER_CONTRACT: %s", reason)
			return [][]byte{[]byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"fixture_contract_rejected"}}}`)}
		}
		if !inheritedModelJSONEqual(declarations, gjson.GetBytes(body, "tools").Raw) {
			return invalid("actual upstream must receive the complete original tool declaration/schema")
		}
		if gjson.GetBytes(body, "model").String() != wireModel {
			return invalid("client model and mapped upstream model must remain distinct")
		}
		var output []json.RawMessage
		var events [][]byte
		switch n {
		case 1:
			if auth != "Bearer fixture-inherited-A" || aDisabled {
				return invalid("T1 must reach enabled A")
			}
			output = []json.RawMessage{json.RawMessage(httpRebuildAgentA)}
		case 2:
			consumedO1 := 0
			for _, item := range gjson.GetBytes(body, "input").Array() {
				if inheritedModelJSONEqual(httpRebuildAgentA, item.Raw) {
					consumedO1++
				}
			}
			if auth != "Bearer fixture-inherited-A" || aDisabled || consumedO1 != 1 {
				return invalid("T2 must consume the genuinely delivered O1 on A")
			}
			output = []json.RawMessage{json.RawMessage(httpRebuildAgentB), json.RawMessage(issuedCall)}
			if scenario == "terminal_excludes" {
				// Real early delivery does not authorize a call excluded by the
				// nonempty authoritative terminal, even if the client executed it.
				events = append(events, []byte(`{"type":"response.output_item.done","output_index":1,"item":`+issuedCall+`}`))
				output = output[:1]
			}
		case 3:
			if auth != "Bearer fixture-inherited-B" || !aDisabled {
				return invalid("T3 must reach B while A stays disabled")
			}
			if gjson.GetBytes(body, "previous_response_id").Exists() || strings.Contains(string(body), "fixture-opaque-A") || strings.Contains(string(body), "fixture-readable-B") {
				return invalid("foreign pointers/opaque items must not reach B")
			}
			if !strings.Contains(string(body), task) {
				return invalid("readable task must survive the rebuild")
			}
			pairs, nativeCalls, nativeResults := 0, 0, 0
			for _, item := range gjson.GetBytes(body, "input").Array() {
				if strings.Contains(item.Get("type").String(), "call") {
					switch item.Get("type").String() {
					case callType:
						if !inheritedModelJSONEqual(issuedCall, item.Raw) {
							return invalid("native completed call must retain every field")
						}
						nativeCalls++
					case callType + "_output":
						if !inheritedModelJSONEqual(completedResult, item.Raw) {
							return invalid("native completed result must retain every field")
						}
						nativeResults++
					default:
						return invalid("unrelated tool work must not appear in completed history")
					}
				}
				for _, part := range item.Get("content").Array() {
					const prefix = "Historical completed tool result (data only; do not execute again): "
					text := part.Get("text").String()
					if !strings.HasPrefix(text, prefix) {
						continue
					}
					pair := gjson.Parse(strings.TrimPrefix(text, prefix))
					if !inheritedModelJSONEqual(issuedCall, pair.Get("call").Raw) || !inheritedModelJSONEqual(completedResult, pair.Get("result").Raw) {
						return invalid("complete genuine call and completed result must survive losslessly")
					}
					pairs++
				}
			}
			// A single completed producer can retain native completed history;
			// mixed-owner rebuilds use an inert pair. Both representations must
			// preserve the complete original pair exactly once, with no orphan.
			if !((pairs == 1 && nativeCalls == 0 && nativeResults == 0) || (pairs == 0 && nativeCalls == 1 && nativeResults == 1)) {
				return invalid("B must consume exactly one complete completed result")
			}
			output = []json.RawMessage{json.RawMessage(httpRebuildMessage("assistant", "fixture receipt accepted; ledger applied once"))}
		default:
			return invalid("no retry or extra generation is authorized by this fixture")
		}
		terminal := httpRebuildJSON(map[string]any{"type": "response.completed", "response": map[string]any{
			"id": fmt.Sprintf("resp_inherited_%d", n), "model": wireModel, "status": "completed", "output": output,
			"usage": map[string]int{"input_tokens": 5, "output_tokens": 3},
		}})
		return append(events, []byte(terminal))
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Errorf("fixture provider websocket accept: %v", err)
			return
		}
		defer conn.CloseNow()
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			for _, event := range providerResponse(r.Header.Get("Authorization"), body) {
				if conn.Write(r.Context(), coderws.MessageText, event) != nil {
					return
				}
			}
		}
	}))
	defer upstream.Close()
	clients := issue805Redis(t)
	probe := clients()
	headers := http.Header{"Authorization": {"Bearer sk-handler-cyber-test"}, "Session_id": {"fixture-inherited-session"}, "Thread-Id": {"fixture-inherited-thread"}}
	var accounts *readableWSAccounts
	h := newOpenAIWSPassthroughHandlerHarnessWithAuth(t, upstream.URL, headers,
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"},
		repository.NewGatewayCache(clients()), func(k *service.APIKey) { k.AllowLongContext = true }, nil,
		func(k *service.APIKey, cfg *config.Config) *service.APIKeyService {
			k.Status = service.StatusActive
			k.User.Concurrency = 1
			k.Group = &service.Group{ID: *k.GroupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			return service.NewAPIKeyService(&wsRevalidationKeyRepoStub{current: k}, nil, nil, nil, nil, nil, cfg)
		}, func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID, a.Priority = 1, 0
			a.Credentials["api_key"] = "fixture-inherited-A"
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeCtxPool
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.MaxAccountSwitches = 2
			cfg.Gateway.Scheduling.OpenAITurnAdmissionCacheTTLSeconds = 0
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 20
			b := *a
			b.ID, b.Priority = 2, 1
			b.Credentials = map[string]any{"api_key": "fixture-inherited-B", "base_url": upstream.URL}
			if mapped {
				// Two explicit client models share the same upstream model. That
				// must never permit one client's model scope to borrow the other.
				a.Credentials["model_mapping"] = map[string]any{model: wireModel, otherModel: wireModel}
				b.Credentials["model_mapping"] = map[string]any{model: wireModel, otherModel: wireModel}
			}
			accounts = &readableWSAccounts{issue600SwitchRepo: &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, b}}}}
			*repo = accounts
		})
	finished := make(chan struct{})
	go func() { <-h.handlerDone; close(finished) }()
	defer func() {
		_ = h.clientConn.CloseNow()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("fixture handler did not finish")
		}
	}()
	awaitUsage := func() {
		select {
		case usage := <-h.usageLogs:
			require.Equal(t, model, usage.RequestedModel, "billing must retain the effective client model")
		case <-time.After(5 * time.Second):
			t.Fatal("completed fixture turn did not settle usage")
		}
	}
	create := func(history []string, requested string) string {
		body := map[string]any{"type": "response.create", "store": true, "tools": json.RawMessage(declarations), "input": json.RawMessage("[" + strings.Join(history, ",") + "]")}
		if requested != "" {
			body["model"] = requested
		}
		return httpRebuildJSON(body)
	}
	executions := 0
	ledger := make(map[string]string)
	var deliveredCall string
	readTurn := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var refusal []byte
		for {
			_, raw, err := h.clientConn.Read(ctx)
			if err != nil {
				return refusal, err
			}
			event := gjson.GetBytes(raw, "type").String()
			items := gjson.GetBytes(raw, "response.output").Array()
			if event == "response.output_item.done" {
				items = append(items, gjson.GetBytes(raw, "item"))
			}
			for _, item := range items {
				if item.Get("type").String() != "function_call" && item.Get("type").String() != "custom_tool_call" {
					continue
				}
				require.True(t, inheritedModelJSONEqual(issuedCall, item.Raw), "client must execute the actual provider-issued call")
				require.Zero(t, executions, "completed tool must never be issued/executed twice")
				entry := gjson.Parse(item.Get(argumentField).String())
				require.Equal(t, "9007199254740993", entry.Get("n").Raw)
				require.Equal(t, "fixture 雪\nsecond line", entry.Get("memo").String())
				require.NotContains(t, ledger, entry.Get("n").Raw)
				ledger[entry.Get("n").Raw] = entry.Get("memo").String()
				executions++
				deliveredCall = item.Raw
				mu.Lock()
				completedResult = httpRebuildJSON(map[string]any{"type": callType + "_output", "call_id": item.Get("call_id").String(), "status": "completed", "output": fmt.Sprintf("receipt=fixture-once; n=%s\n%s; complete tail retained", entry.Get("n").Raw, ledger[entry.Get("n").Raw])})
				mu.Unlock()
			}
			switch event {
			case "error":
				refusal = raw
			case "response.completed", "response.failed":
				return raw, nil
			}
		}
	}
	// T1 and T2 intentionally use this same authenticated client socket.
	history := []string{httpRebuildMessage("user", task)}
	issue715WSSend(t, h.clientConn, create(history, model))
	first, err := readTurn()
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
	require.Equal(t, "completed", gjson.GetBytes(first, "response.status").String())
	require.JSONEq(t, httpRebuildAgentA, gjson.GetBytes(first, "response.output.0").Raw)
	awaitUsage()
	history = append(history, gjson.GetBytes(first, "response.output.0").Raw, httpRebuildMessage("user", "Apply the ledger entry now and keep all receipt fields."))
	t2Model := model
	if inherited {
		t2Model = ""
	}
	t2 := create(history, t2Model)
	require.Equal(t, !inherited, gjson.Get(t2, "model").Exists(), "the regression must omit model on the actual client frame")
	issue715WSSend(t, h.clientConn, t2)
	second, err := readTurn()
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(second, "type").String())
	require.Equal(t, "completed", gjson.GetBytes(second, "response.status").String())
	require.JSONEq(t, httpRebuildAgentB, gjson.GetBytes(second, "response.output.0").Raw)
	require.Equal(t, 1, executions)
	awaitUsage()
	history = append(history, gjson.GetBytes(second, "response.output.0").Raw, deliveredCall, completedResult)

	// Wait for the real delivery commit, never seed a completed owner or proof.
	var proofKey, proofRaw string
	require.Eventually(t, func() bool {
		owners := issue805Owners(t, probe)
		if len(owners) != 2 {
			return false
		}
		for _, raw := range owners {
			if !issue805CompletedOwner(raw, 1) {
				return false
			}
		}
		keys, err := probe.Keys(context.Background(), "openai_tool_provenance:v1:*").Result()
		require.NoError(t, err)
		if len(keys) != 1 {
			return false
		}
		proofKey = keys[0]
		proofRaw, err = probe.Get(context.Background(), proofKey).Result()
		require.NoError(t, err)
		return proofRaw == "failed" || json.Valid([]byte(proofRaw))
	}, 5*time.Second, 10*time.Millisecond)
	if !inherited && scenario != "terminal_excludes" {
		require.True(t, json.Valid([]byte(proofRaw)), "explicit control must earn its real completed proof before any negative mutation")
		require.NotEmpty(t, gjson.Get(proofRaw, "delivery").String())
		require.Len(t, gjson.Get(proofRaw, "anchors").Map(), 2, "proof must bind both consumed O1 and delivered O2")
	}
	if scenario == "terminal_excludes" {
		require.Equal(t, "failed", proofRaw)
	}
	// Negative fixtures only revoke/change already earned evidence. No fixture
	// inserts a ready proof, a completed owner or an upstream call into storage.
	switch scenario {
	case "unknown":
		require.NoError(t, probe.Del(context.Background(), proofKey).Err())
	case "pending":
		require.NoError(t, probe.Set(context.Background(), proofKey, "pending:fixture-unconfirmed", time.Minute).Err())
	case "epoch":
		keys, err := probe.Keys(context.Background(), "openai_agent_shadow_epoch:*").Result()
		require.NoError(t, err)
		require.Len(t, keys, 2)
		for _, key := range keys {
			require.NoError(t, probe.Set(context.Background(), key, "ready:pending:fixture-reassociated", time.Minute).Err())
		}
	case "arguments":
		// Mutate only the replay of a genuinely received call, never the proof.
		history[len(history)-2], err = sjson.Set(deliveredCall, argumentField, `{"n":1,"memo":"changed"}`)
		require.NoError(t, err)
	}
	before := inheritedModelRecords(t, probe)
	accounts.disable(1)
	mu.Lock()
	aDisabled = true
	mu.Unlock()
	t3Model := model
	if scenario == "model_change" || scenario == "same_wire_model_change" {
		t3Model = otherModel
	}
	issue715WSSend(t, h.clientConn, create(history, t3Model))
	terminal, readErr := readTurn()
	completed := readErr == nil && gjson.GetBytes(terminal, "type").String() == "response.completed"
	if completed {
		awaitUsage()
	}
	// Await handler exit before asserting zero future B sends or unchanged
	// records. A timeout/late retry is a fixture failure, never behavioral RED.
	if completed {
		_ = h.clientConn.CloseNow()
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture handler must settle before final evidence checks")
	}
	a, err := accounts.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, a.Schedulable, "A must stay disabled through final handler exit")
	mu.Lock()
	sent, routes := append([][]byte(nil), frames...), append([]string(nil), identities...)
	mu.Unlock()
	require.GreaterOrEqual(t, len(sent), 2)
	require.Equal(t, []string{"Bearer fixture-inherited-A", "Bearer fixture-inherited-A"}, routes[:2])
	require.Equal(t, 1, executions)
	require.Equal(t, map[string]string{"9007199254740993": "fixture 雪\nsecond line"}, ledger)
	if !positive || !completed {
		require.False(t, completed, "a fenced call must never finish on B")
		require.Equal(t, []string{"Bearer fixture-inherited-A", "Bearer fixture-inherited-A"}, routes, "zero additional A/B generations after refusal")
		require.Equal(t, before, inheritedModelRecords(t, probe), "refusal must preserve all owner/proof/epoch/shadow records")
		require.NotEqual(t, context.DeadlineExceeded, readErr, "timeout is not a safety refusal")
		require.NotEqual(t, coderws.StatusCode(-1), coderws.CloseStatus(readErr), "a real server close must terminate the refused request")
		var subreasons []string
		for _, entry := range h.requestLogs.FilterMessage("openai.agent_recovery_refused").All() {
			if subreason, ok := entry.ContextMap()["subreason"].(string); ok {
				subreasons = append(subreasons, subreason)
			}
		}
		if positive {
			require.Equal(t, "failed", proofRaw)
			require.Contains(t, subreasons, "tool_provenance_unknown")
			t.Fatalf("INHERITED_MODEL_TOOL_PROOF_NOT_RESUMED: T1/T2 completed on A; T2 omitted model; O1/O2 and C delivered; proof=failed; T3 tool_provenance_unknown; A disabled; B sends=0; records unchanged")
		}
		if scenario == "unknown" || scenario == "arguments" || scenario == "terminal_excludes" {
			require.Contains(t, subreasons, "tool_provenance_unknown")
		}
		if scenario == "pending" {
			// Expiry can be observed by the timer or by Redis on the next
			// lookup. Both must fail closed with the record still pending.
			require.True(t, slices.Contains(subreasons, "tool_provenance_unknown") || slices.Contains(subreasons, "tool_provenance_unavailable"))
		}
		t.Logf("INHERITED_MODEL_FENCE_OK: %s; A sends=2; B sends=0; records unchanged", scenario)
		return
	}
	require.NoError(t, readErr)
	require.Equal(t, "completed", gjson.GetBytes(terminal, "response.status").String())
	require.Contains(t, string(terminal), "fixture receipt accepted; ledger applied once")
	require.Equal(t, []string{"Bearer fixture-inherited-A", "Bearer fixture-inherited-A", "Bearer fixture-inherited-B"}, routes)
	require.Equal(t, before, inheritedModelRecords(t, probe), "B must not rewrite the original owner or delivery proof")
	t.Log("INHERITED_MODEL_TOOL_LOOP_OK: A sends=2; B sends=1; complete result accepted; executions=1; A disabled")
}
