//go:build unit && ownerconflict

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Actual authenticated ingress and native forwarders, with only the provider
// and account repository replaced. Both owners are earned by delivered output.
func TestOpenAIWSMixedOwnerContinuation(t *testing.T) {
	for _, turn := range []int{2, 53} {
		t.Run(fmt.Sprintf("turn_%d", turn), func(t *testing.T) {
			runWSMixedOwnerContinuation(t, service.OpenAIWSIngressModeCtxPool, turn)
		})
	}
}

func TestOpenAIWSMixedOwnerBoundaries(t *testing.T) {
	for _, state := range []string{"pending", "failed", "conflicted"} {
		for _, order := range []string{"first", "last"} {
			t.Run(state+"_"+order, func(t *testing.T) {
				runWSMixedOwnerContinuation(t, service.OpenAIWSIngressModeCtxPool, 3, state+"_"+order)
			})
		}
	}
	for _, scenario := range []string{"unknown", "unpaired_call", "unpaired_output", "duplicate_call", "duplicate_output", "incomplete", "off", "no_intent"} {
		t.Run(scenario, func(t *testing.T) { runWSMixedOwnerContinuation(t, service.OpenAIWSIngressModeCtxPool, 2, scenario) })
	}
}

func TestOpenAIWSMixedOwnerTransports(t *testing.T) {
	for _, mode := range readableWSModes {
		for _, scenario := range []string{"live", "fresh", "binding_changed"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) { runWSMixedOwnerContinuation(t, mode, 2, scenario) })
		}
	}
}

func runWSMixedOwnerContinuation(t *testing.T, mode string, turn int, scenarios ...string) {
	t.Helper()
	scenario := "live"
	if len(scenarios) > 0 {
		scenario = scenarios[0]
	}
	negative := scenario != "live" && scenario != "fresh" && scenario != "binding_changed"
	oldFence := strings.HasSuffix(scenario, "_first") || strings.HasSuffix(scenario, "_last")
	thirdItem := strings.ReplaceAll(httpRebuildAgentB, "fixture-readable-B", "fixture-old-completed-C")
	clients := issue805Redis(t)
	probe := clients()
	var mu sync.Mutex
	var frames [][]byte
	var identities []string
	response := func(r *http.Request, body []byte) []byte {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, append([]byte(nil), body...))
		identities = append(identities, r.Header.Get("Authorization"))
		n := len(frames)
		output := []json.RawMessage{json.RawMessage(httpRebuildMessage("assistant", "fixture warm complete"))}
		switch {
		case n == 1:
			output = []json.RawMessage{json.RawMessage(httpRebuildAgentA), json.RawMessage(httpRebuildCall("apply_patch", "call_mixed_patch", map[string]any{"path": "sum.go", "source": httpRebuildSource}))}
		case n == 2:
			output = []json.RawMessage{json.RawMessage(httpRebuildAgentB)}
		case n == 3 && oldFence:
			output = []json.RawMessage{json.RawMessage(thirdItem)}
		case n == turn+1:
			output = []json.RawMessage{json.RawMessage(httpRebuildCall("run_tests", "call_mixed_verify", map[string]any{"path": "sum.go"}))}
		case n == turn+2:
			output = []json.RawMessage{json.RawMessage(httpRebuildMessage("assistant", "fixture mixed task complete"))}
		}
		return []byte(httpRebuildJSON(map[string]any{"type": "response.completed", "response": map[string]any{
			"id": fmt.Sprintf("resp_mixed_%d", n), "status": "completed", "model": "gpt-5.1", "output": output,
			"usage": map[string]int{"input_tokens": 5, "output_tokens": 3},
		}}))
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", response(r, body))
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow() //nolint:errcheck // Isolated fixture cleanup.
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil || conn.Write(r.Context(), coderws.MessageText, response(r, body)) != nil {
				return
			}
		}
	}))
	defer provider.Close()
	headers := http.Header{"Authorization": {"Bearer sk-handler-cyber-test"}, "Session_id": {"fixture-mixed-session"}, "Thread-Id": {"fixture-mixed-thread"}}
	var accounts *readableWSAccounts
	h := newOpenAIWSPassthroughHandlerHarnessWithAuth(t, provider.URL, headers,
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: fmt.Sprint(scenario != "off")},
		repository.NewGatewayCache(clients()), func(key *service.APIKey) { key.AllowLongContext = true }, nil,
		func(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
			key.Status = service.StatusActive
			key.User.Concurrency = 1
			key.Group = &service.Group{ID: *key.GroupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			return service.NewAPIKeyService(&wsRevalidationKeyRepoStub{current: key}, nil, nil, nil, nil, nil, cfg)
		}, func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID, a.Priority = 1, 0
			a.Credentials["api_key"] = "fixture-mixed-A"
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.MaxAccountSwitches = 2
			cfg.Gateway.Scheduling.OpenAITurnAdmissionCacheTTLSeconds = 0
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			other := *a
			other.ID, other.Priority = 2, 1
			other.Credentials = map[string]any{"api_key": "fixture-mixed-B", "base_url": provider.URL}
			pool := []service.Account{*a, other}
			if scenario == "binding_changed" {
				third := other
				third.ID, third.Priority = 3, 2
				third.Credentials = map[string]any{"api_key": "fixture-mixed-C", "base_url": provider.URL}
				pool = append(pool, third)
			}
			accounts = &readableWSAccounts{issue600SwitchRepo: &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: pool}}}
			*repo = accounts
		})
	defer func() { _ = h.clientConn.CloseNow() }()
	// Long-lived sessions also exercise usage recording without filling the
	// shared harness's deliberately small observer buffer.
	stopUsage := make(chan struct{})
	defer close(stopUsage)
	go func() {
		for {
			select {
			case <-stopUsage:
				return
			case <-h.usageLogs:
			}
		}
	}()
	create := func(input []string, previous string) string {
		body := map[string]any{"type": "response.create", "model": "gpt-5.1", "store": true,
			"tools": httpRebuildToolDefinitions(), "input": json.RawMessage("[" + strings.Join(input, ",") + "]"),
			"client_metadata": map[string]string{"thread_id": "fixture-mixed-thread"}}
		if previous != "" {
			body["previous_response_id"] = previous
		}
		return httpRebuildJSON(body)
	}
	read := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var refusal []byte
		for {
			_, raw, err := h.clientConn.Read(ctx)
			if err != nil {
				if refusal != nil {
					return refusal, err // Drain the close handshake after an error event.
				}
				return raw, err
			}
			switch gjson.GetBytes(raw, "type").String() {
			case "error":
				refusal = raw
			case "response.completed", "response.failed":
				return raw, nil
			}
		}
	}
	history := []string{httpRebuildMessage("user", httpRebuildTask)}
	issue715WSSend(t, h.clientConn, create(history, ""))
	first := gjson.GetBytes(issue715WSRead(t, h.clientConn), "response")
	require.Equal(t, "completed", first.Get("status").String())
	tools := &httpRebuildTools{}
	for _, item := range first.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tools.execute(t, first)...)
	issue600Finish(t, h, headers)
	accounts.disable(1)
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	conn, _, err := coderws.Dial(dialCtx, h.wsURL, &coderws.DialOptions{HTTPHeader: headers})
	cancel()
	require.NoError(t, err)
	h.clientConn = conn
	waitOwners := func(count int) map[string]string {
		deadline := time.Now().Add(5 * time.Second)
		for {
			owners := issue805Owners(t, probe)
			completed := len(owners) == count
			for _, raw := range owners {
				completed = completed && json.Valid([]byte(raw)) && gjson.Parse(raw).IsArray()
			}
			if completed {
				return owners
			}
			require.True(t, time.Now().Before(deadline), "actual downstream deliveries must commit before fixture mutation")
			time.Sleep(5 * time.Millisecond)
		}
	}
	var previous string
	var firstTwoOwners map[string]string
	for warm := 1; warm < turn; warm++ {
		issue715WSSend(t, h.clientConn, create([]string{httpRebuildMessage("user", "fixture warm")}, ""))
		raw, readErr := read()
		require.NoError(t, readErr)
		completed := gjson.GetBytes(raw, "response")
		require.Equal(t, "completed", completed.Get("status").String())
		previous = completed.Get("id").String()
		if warm == 1 {
			history = append(history, completed.Get("output.0").Raw)
			firstTwoOwners = waitOwners(2)
		}
	}
	expectedOwners := 2
	if oldFence {
		expectedOwners++
	}
	owners := waitOwners(expectedOwners)
	if oldFence {
		// Fence only C, identified by the extra actual downstream delivery.
		// Both completed A/B owners remain, before/after this older fence.
		for key, raw := range owners {
			if _, exists := firstTwoOwners[key]; exists {
				continue
			}
			value := "conflict"
			if strings.HasPrefix(scenario, "pending") {
				value = "pending:fixture-old:" + raw
			}
			if strings.HasPrefix(scenario, "failed") {
				value = "failed"
			}
			require.NoError(t, probe.Set(context.Background(), key, value, time.Minute).Err())
		}
		history = append(history, thirdItem)
		if strings.HasSuffix(scenario, "_first") {
			history = append([]string{thirdItem}, history[:len(history)-1]...)
		}
	}
	switch scenario {
	case "unknown":
		history = append(history, thirdItem)
	case "unpaired_call":
		history = append(history[:3], history[4:]...)
	case "unpaired_output":
		history = append(history[:2], history[3:]...)
	case "duplicate_call":
		history = append(history, history[2])
	case "duplicate_output":
		history = append(history, history[3])
	case "incomplete":
		history[2], err = sjson.Set(history[2], "status", "in_progress")
		require.NoError(t, err)
	case "no_intent":
		history = history[1:]
	case "binding_changed":
		accounts.mu.Lock()
		accounts.accounts[1].Credentials = map[string]any{"api_key": "fixture-mixed-B-rotated", "base_url": provider.URL}
		accounts.mu.Unlock()
	case "fresh":
		issue600Finish(t, h, headers)
		dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		conn, _, dialErr := coderws.Dial(dialCtx, h.wsURL, &coderws.DialOptions{HTTPHeader: headers})
		cancel()
		require.NoError(t, dialErr)
		h.clientConn = conn
	}
	owners = issue805Owners(t, probe)
	issue715WSSend(t, h.clientConn, create(history, previous))
	resumed, resumeErr := read()
	mu.Lock()
	sends := len(frames)
	mu.Unlock()
	if negative {
		require.Equal(t, turn, sends, "unsafe history must produce zero extra generations")
		require.Equal(t, "error", gjson.GetBytes(resumed, "type").String(), "a setup EOF is not a refusal")
		require.Contains(t, []string{"encrypted_agent_recovery_owner_conflict", "encrypted_agent_recovery_owner_unavailable", "encrypted_agent_recovery_delivery_pending", "encrypted_agent_recovery_readable_intent_missing"}, gjson.GetBytes(resumed, "error.code").String())
		require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(resumeErr))
		require.Equal(t, 1, tools.patches)
		require.Zero(t, tools.verifications)
		require.Equal(t, owners, issue805Owners(t, probe), "refusals cannot modify owners")
		entries := h.requestLogs.FilterMessage("openai.agent_recovery_refused").All()
		require.Len(t, entries, 1, "the real handler logs one safe diagnostic for its terminal refusal")
		fields := entries[0].ContextMap()
		expectedDetail := map[string]string{
			"unknown": "owner_delivery_unknown", "unpaired_call": "tool_unpaired", "unpaired_output": "tool_unpaired",
			"duplicate_call": "tool_duplicate", "duplicate_output": "tool_duplicate", "incomplete": "tool_incomplete",
			"off": "mixed_completed_owners", "no_intent": "readable_intent_missing",
		}[scenario]
		if oldFence {
			state := strings.Split(scenario, "_")[0]
			expectedDetail = "owner_record_" + state
			require.Equal(t, int64(1), fields["owner_"+state])
			if strings.HasSuffix(scenario, "_first") {
				require.Equal(t, "partial", fields["owner_observation"])
				require.Equal(t, int64(2), fields["owner_unexamined"])
			} else {
				require.Equal(t, int64(2), fields["owner_completed"])
			}
		}
		require.Equal(t, expectedDetail, fields["subreason"])
		require.False(t, gjson.GetBytes(resumed, "error.subreason").Exists(), "public error contract is unchanged")
		logged, marshalErr := json.Marshal(fields)
		require.NoError(t, marshalErr)
		for _, secret := range []string{httpRebuildTask, httpRebuildPatch, "fixture-opaque-A", "fixture-mixed-B", "call_mixed_patch"} {
			require.NotContains(t, string(logged), secret)
		}
		return
	}
	require.Equal(t, turn+1, sends, "MIXED_COMPLETED_OWNER_NOT_RESUMED: completed mixed owners must reach B exactly once; code=%s close=%v", gjson.GetBytes(resumed, "error.code").String(), resumeErr)
	require.NoError(t, resumeErr)
	require.Equal(t, "completed", gjson.GetBytes(resumed, "response.status").String())
	second := gjson.GetBytes(resumed, "response")
	for _, item := range second.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tools.execute(t, second)...)
	issue715WSSend(t, h.clientConn, create(history, second.Get("id").String()))
	final, err := read()
	require.NoError(t, err)
	require.Contains(t, string(final), "fixture mixed task complete")
	require.Equal(t, 1, tools.patches)
	require.Equal(t, 1, tools.verifications)
	issue600Finish(t, h, headers)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, frames, turn+2, "no extra generation or retry")
	for i, wire := range frames[turn:] {
		expectedIdentity := "Bearer fixture-mixed-B"
		if scenario == "binding_changed" {
			expectedIdentity = "Bearer fixture-mixed-C"
		}
		require.Equal(t, expectedIdentity, identities[turn+i])
		require.True(t, httpRebuildToolsAvailable(wire), "both tool definitions and parameter schemas survive the actual wire")
		require.Contains(t, string(wire), httpRebuildTask)
		require.Equal(t, 1, strings.Count(string(wire), httpRebuildPatch))
		require.NotContains(t, string(wire), "fixture-opaque-A")
		require.NotContains(t, string(wire), "fixture-readable-B")
		require.False(t, gjson.GetBytes(wire, "previous_response_id").Exists())
		for _, item := range gjson.GetBytes(wire, "input").Array() {
			require.NotContains(t, item.Get("type").String(), "call", "completed tools must be inert readable history")
		}
	}
	require.Equal(t, 1, strings.Count(string(frames[turn+1]), httpRebuildCheck))
	require.Equal(t, owners, issue805Owners(t, probe), "historical owner records remain unchanged")
}
