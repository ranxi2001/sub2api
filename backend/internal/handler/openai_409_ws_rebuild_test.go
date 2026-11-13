//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var readableWSModes = []string{service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModeCtxPool,
	service.OpenAIWSIngressModeShared, service.OpenAIWSIngressModeDedicated, service.OpenAIWSIngressModePassthrough}

// Mutate administrative scheduling state under a lock while a real client
// connection remains open. Every authoritative repository read sees the update.
type readableWSAccounts struct {
	*issue600SwitchRepo
	mu                sync.Mutex
	disableAdmission  int
	admissionObserved []bool
}

func (r *readableWSAccounts) disable(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].Schedulable = false
		}
	}
}
func (r *readableWSAccounts) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.issue600SwitchRepo.GetByID(ctx, id)
}
func (r *readableWSAccounts) GetOpenAITurnAdmission(ctx context.Context, id int64) (*service.Account, *service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == 1 && r.disableAdmission > 0 {
		r.disableAdmission--
		if r.disableAdmission == 0 {
			for i := range r.accounts {
				if r.accounts[i].ID == 1 {
					r.accounts[i].Schedulable = false
				}
			}
		}
		r.admissionObserved = append(r.admissionObserved, r.disableAdmission > 0)
	}
	a, err := r.issue600SwitchRepo.GetByID(ctx, id)
	return a, nil, err
}
func (r *readableWSAccounts) ListSchedulableByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.issue600SwitchRepo.ListSchedulableByPlatform(ctx, platform)
}
func (r *readableWSAccounts) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}
func (r *readableWSAccounts) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}
func (r *readableWSAccounts) SetRateLimited(ctx context.Context, id int64, until time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.issue600SwitchRepo.SetRateLimited(ctx, id, until)
}

func TestOpenAI409WSReadableContinuation(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode, func(t *testing.T) { runReadableWSContinuation(t, mode, "completed") })
	}
}

func TestOpenAI409WSReadableBoundaries(t *testing.T) {
	for _, mode := range readableWSModes {
		for _, boundary := range []string{"off", "no_intent", "b_disabled", "pending_owner", "failed_owner", "ambiguous_B"} {
			t.Run(mode+"/"+boundary, func(t *testing.T) { runReadableWSContinuation(t, mode, boundary) })
		}
	}
}

func TestOpenAI409WSReviewRepairs(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run("identity/"+mode, func(t *testing.T) { runReadableWSContinuation(t, mode, "identity") })
	}
	for _, mode := range []string{"auto", "ws", "http"} {
		t.Run("smart/"+mode, func(t *testing.T) { runReadableWSContinuation(t, service.OpenAIWSIngressModeCtxPool, "smart_"+mode) })
	}
	t.Run("bridge_inherited_model", func(t *testing.T) {
		runReadableWSContinuation(t, service.OpenAIWSIngressModeHTTPBridge, "bridge_model")
	})
}

func TestOpenAI409WSResidualRepairs(t *testing.T) {
	for _, scenario := range []string{"header_apikey", "header_oauth", "tools_omitted", "tools_explicit"} {
		t.Run(scenario, func(t *testing.T) {
			runReadableWSContinuation(t, service.OpenAIWSIngressModeHTTPBridge, scenario)
		})
	}
}

func TestOpenAI409WSNativeNamespaceRecovery(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeShared, service.OpenAIWSIngressModeDedicated, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) { runReadableWSContinuation(t, mode, "native_namespace") })
	}
}

func readableWSNativeNamespaceTools() []any {
	return append(httpRebuildToolDefinitions(),
		map[string]any{"type": "namespace", "name": "files", "tools": []any{
			map[string]any{"type": "function", "name": "read", "parameters": map[string]any{
				"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "enum": []string{"sum.go"}}},
				"required": []string{"path"}, "additionalProperties": false,
			}},
		}},
		map[string]any{"type": "function", "name": "files__read", "parameters": map[string]any{
			"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "enum": []string{"README.md"}}},
			"required": []string{"name"}, "additionalProperties": false,
		}},
	)
}

// A wire close intentionally omits internal causes. Attribute the observed
// close to the real B dispatch error, never synthesize a diagnostic from the
// scenario name: historical RED must still prove its specific underlying cause.
func logReadableWSDispatchFailure(t *testing.T, h *openAIWSPassthroughHandlerHarness, clientErr error) {
	t.Helper()
	var peerClose coderws.CloseError
	require.ErrorAs(t, clientErr, &peerClose)
	require.Equal(t, coderws.StatusPolicyViolation, peerClose.Code)
	entries := h.requestLogs.FilterMessage("openai.websocket_proxy_failed").All()
	require.Len(t, entries, 1, "expected one actual failed B dispatch")
	require.Equal(t, int64(2), entries[0].ContextMap()["account_id"])
	var reported error
	for _, field := range entries[0].Context {
		if field.Key == "error" {
			reported, _ = field.Interface.(error)
		}
	}
	var serverClose *service.OpenAIWSClientCloseError
	require.ErrorAs(t, reported, &serverClose)
	require.Equal(t, peerClose.Code, serverClose.StatusCode())
	require.Equal(t, peerClose.Reason, serverClose.Reason())
	require.NotNil(t, errors.Unwrap(serverClose), "dispatch refusal must retain its underlying cause")
	t.Logf("actual B dispatch error: %v; client close: %v", reported, clientErr)
}

func runReadableWSContinuation(t *testing.T, mode, scenario string) {
	t.Helper()
	clients := issue805Redis(t)
	probe := clients()
	oracle := &readableWSOracle{scenario: scenario}
	provider := oracle.server(t)
	defer provider.Close()
	headers := http.Header{"Session_id": {"fixture-readable-ws-session"}, "Thread-Id": {"fixture-readable-ws-thread"}}
	settings := map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: fmt.Sprint(scenario != "off")}
	identityCase, modelCase := scenario == "identity" || strings.HasPrefix(scenario, "header_"), scenario == "bridge_model"
	customTools := strings.HasPrefix(scenario, "tools_")
	nativeNamespace := scenario == "native_namespace"
	smartMode := ""
	if strings.HasPrefix(scenario, "smart_") {
		smartMode = strings.TrimPrefix(scenario, "smart_")
	}
	if customTools {
		smartMode = "http"
	}
	if identityCase {
		headers.Del("thread-id")
		headers.Set("conversation_id", "fixture-client-conversation-T")
		headers.Set("x-codex-turn-state", "fixture-original-A-state")
		settings[service.SettingKeyCyberSessionBlockEnabled] = "true"
		settings[service.SettingKeyCyberSessionIdentityStrictEnabled] = "true"
	}
	var accounts *readableWSAccounts
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, provider.URL, headers,
		settings,
		repository.NewGatewayCache(clients()), func(key *service.APIKey) { key.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, upstream *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID, a.Priority = 1, 0
			a.Credentials["api_key"] = "fixture-readable-ws-A"
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			cfg.Gateway.MaxAccountSwitches = 2
			cfg.Gateway.Scheduling.OpenAITurnAdmissionCacheTTLSeconds = 0
			if smartMode != "" {
				cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
				cfg.Gateway.OpenAIWS.SmartProtocol = config.GatewayOpenAIWSSmartProtocolConfig{Enabled: true, Mode: smartMode}
				a.Extra["openai_responses_supported"] = true
			}
			require.Equal(t, mode, a.ResolveOpenAIResponsesWebSocketV2Mode(cfg.Gateway.OpenAIWS.IngressModeDefault))
			other := *a
			other.ID, other.Priority = 2, 1
			other.Credentials = map[string]any{"api_key": "fixture-readable-ws-B", "base_url": provider.URL}
			if scenario == "header_oauth" {
				cfg.Gateway.OpenAIWS.OAuthEnabled = true
				for i, account := range []*service.Account{a, &other} {
					account.Type = service.AccountTypeOAuth
					account.Credentials["access_token"] = []string{"fixture-readable-ws-A", "fixture-readable-ws-B"}[i]
					account.Credentials["chatgpt_account_id"] = fmt.Sprintf("fixture-ws-account-%d", i)
					account.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
					account.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
				}
				target, err := url.Parse(provider.URL)
				require.NoError(t, err)
				*upstream = &httpRebuildTransport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
			}
			accounts = &readableWSAccounts{issue600SwitchRepo: &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, other}}}}
			*repo = accounts
		})
	finished := false
	defer func() {
		if !finished {
			issue600Finish(t, h, headers)
		}
	}()
	requestModel := "gpt-5.1"
	declarations := httpRebuildToolDefinitions()
	if nativeNamespace {
		declarations = readableWSNativeNamespaceTools()
	}
	if customTools {
		declarations = append(declarations, map[string]any{"type": "custom", "name": "exec", "description": "Read the completed patch exactly once"})
	}
	omitTools := false
	create := func(input []string, previous string) string {
		body := map[string]any{"type": "response.create", "store": smartMode != "",
			"tools": declarations,
			"input": json.RawMessage("[" + strings.Join(input, ",") + "]")}
		if omitTools {
			delete(body, "tools")
		}
		if requestModel != "" {
			body["model"] = requestModel
		}
		if !identityCase {
			body["client_metadata"] = map[string]string{"thread_id": "fixture-readable-ws-thread"}
		}
		if previous != "" {
			body["previous_response_id"] = previous
		}
		return httpRebuildJSON(body)
	}
	history := []string{httpRebuildMessage("user", httpRebuildTask)}
	if scenario == "no_intent" {
		history = nil
	} // No earlier readable intent exists in the connection either.
	issue715WSSend(t, h.clientConn, create(history, ""))
	first := gjson.GetBytes(issue715WSRead(t, h.clientConn), "response")
	require.Equal(t, "completed", first.Get("status").String())
	tools := &httpRebuildTools{}
	for _, item := range first.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tools.execute(t, first)...)
	if customTools {
		customCalls := 0
		for _, item := range first.Get("output").Array() {
			if item.Get("type").String() == "custom_tool_call" {
				customCalls++
				require.Equal(t, "exec", item.Get("name").String())
				require.Equal(t, "read sum.go", item.Get("input").String())
				history = append(history, `{"type":"custom_tool_call_output","call_id":"call_ws_exec","output":"custom-exec-once"}`)
			}
		}
		require.Equal(t, 1, customCalls, "A must actually deliver the completed custom tool")
		omitTools = scenario == "tools_omitted"
	}
	require.Eventually(t, func() bool {
		owners := issue805Owners(t, probe)
		if len(owners) != 1 {
			return false
		}
		for _, raw := range owners {
			if !json.Valid([]byte(raw)) || !gjson.Parse(raw).IsArray() || gjson.Get(raw, "1").Int() != 1 {
				return false
			}
		}
		return true
	}, time.Second, 5*time.Millisecond)
	owners := issue805Owners(t, probe)
	previous := first.Get("id").String()
	extraSends := 0
	if modelCase {
		// Establish M2 through the real transport, then omit model. In this
		// bridge, BeforeTurn and final HTTP send are the two authoritative
		// admission reads. Only the second read observes administrative disable.
		requestModel = "gpt-5.4"
		issue715WSSend(t, h.clientConn, create([]string{httpRebuildMessage("user", "model-switch-warmup")}, ""))
		warmup := gjson.GetBytes(issue715WSRead(t, h.clientConn), "response")
		require.Equal(t, "completed", warmup.Get("status").String())
		require.Contains(t, warmup.Raw, "model-switch-complete")
		previous = warmup.Get("id").String()
		requestModel, extraSends = "", 1
		accounts.mu.Lock()
		accounts.disableAdmission = 2
		accounts.mu.Unlock()
	} else {
		accounts.disable(1)
	}
	if scenario == "b_disabled" {
		accounts.disable(2)
	}
	if scenario == "no_intent" {
		history = []string{httpRebuildAgentA}
	}
	if scenario == "pending_owner" || scenario == "failed_owner" {
		for key, raw := range owners {
			value := "failed"
			if scenario == "pending_owner" {
				value = "pending:fixture-unknown:" + raw
			}
			require.NoError(t, probe.Set(context.Background(), key, value, time.Minute).Err())
			owners[key] = value
		}
	}
	readTerminal := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for {
			_, raw, err := h.clientConn.Read(ctx)
			if err != nil {
				return raw, err
			}
			kind := gjson.GetBytes(raw, "type").String()
			if kind == "response.completed" || kind == "response.failed" || kind == "error" {
				return raw, nil
			}
		}
	}
	issue715WSSend(t, h.clientConn, create(history, previous))
	secondRaw, readErr := readTerminal()
	oracle.mu.Lock()
	sends := len(oracle.frames)
	oracle.mu.Unlock()
	if scenario != "completed" && !identityCase && !modelCase && !nativeNamespace && smartMode == "" {
		require.NotErrorIs(t, readErr, context.DeadlineExceeded)
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelClose()
		if readErr == nil {
			// A structured refusal precedes synchronous coderws.Close. Keep
			// reading so the peer close is acknowledged before awaiting return.
			for readErr == nil {
				var extra []byte
				_, extra, readErr = h.clientConn.Read(closeCtx)
				require.NotContains(t, string(extra), "response.completed")
			}
			require.NotErrorIs(t, readErr, context.DeadlineExceeded)
		}
		// Observe natural handler termination before counting B sends. Closing
		// the test client here could hide an automatic retry still in flight.
		select {
		case <-h.handlerDone:
		case <-closeCtx.Done():
			t.Fatal("WS boundary handler did not terminate")
		}
		finished = true
		select {
		case final := <-h.finalHeaders:
			require.Equal(t, headers.Get("session_id"), final.Get("session_id"))
			require.Equal(t, headers.Get("thread-id"), final.Get("thread-id"))
		default:
			t.Fatal("missing final client identity")
		}
		if scenario == "ambiguous_B" {
			require.NoError(t, oracle.ambiguousEvidence(mode, secondRaw), "B must accept one valid frame before the intentional disconnect")
		} else {
			require.NoError(t, readableWSRefusalEvidence(scenario, secondRaw, readErr))
			oracle.mu.Lock()
			finalSends := len(oracle.frames)
			oracle.mu.Unlock()
			require.Equal(t, 1, finalSends, "unsafe task or current eligibility must refuse before B")
		}
		require.NotContains(t, string(secondRaw), "response.completed")
		for key, raw := range owners {
			require.Equal(t, raw, issue805Owners(t, probe)[key])
		}
		require.Equal(t, 1, tools.patches)
		require.Zero(t, tools.verifications)
		return
	}
	if (nativeNamespace || customTools) && sends != 2 {
		require.Equal(t, 1, sends, "refused rebuilt B dispatch must not emit a generation")
		logReadableWSDispatchFailure(t, h, readErr)
	}
	if nativeNamespace {
		require.Equal(t, 2, sends, "WS_NATIVE_NAMESPACE_B_DISPATCH_MISSING: close=%v terminal=%s", readErr, secondRaw)
	} else if customTools {
		require.Equal(t, 2, sends, "WS_RESIDUAL_TOOL_PROJECTION_REFUSED: close=%v terminal=%s", readErr, secondRaw)
	} else if smartMode != "" {
		require.Equal(t, 2, sends, "WS_REVIEW_SMART_SESSION_STALE: close=%v terminal=%s", readErr, secondRaw)
	}
	require.Equal(t, 2+extraSends, sends, "WS_READABLE_B_DISPATCH_MISSING: same client must automatically replace disabled A before any next generation; close=%v terminal=%s", readErr, secondRaw)
	if modelCase {
		accounts.mu.Lock()
		observed := append([]bool(nil), accounts.admissionObserved...)
		accounts.mu.Unlock()
		require.Equal(t, []bool{true, false}, observed, "fixture must reach the final bridge admission after a healthy BeforeTurn")
		oracle.mu.Lock()
		actualModel := gjson.GetBytes(oracle.frames[2], "model").String()
		oracle.mu.Unlock()
		require.Equal(t, "gpt-5.4", actualModel, "WS_REVIEW_INHERITED_MODEL_LOST: B must receive the last successfully selected client model")
	}
	require.NoError(t, readErr)
	second := gjson.GetBytes(secondRaw, "response")
	require.Equal(t, "completed", second.Get("status").String(), string(secondRaw))
	if customTools {
		require.NotContains(t, second.Raw, "custom_tool_call", "completed custom effect must never execute again")
	}
	for _, item := range second.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tools.execute(t, second)...)
	issue715WSSend(t, h.clientConn, create(history, second.Get("id").String()))
	final, err := readTerminal()
	if identityCase {
		oracle.mu.Lock()
		finalSends := len(oracle.frames)
		oracle.mu.Unlock()
		if !assert.Equal(t, 3, finalSends, "WS_REVIEW_CLIENT_IDENTITY_LOST: close=%v terminal=%s", err, final) {
			// The historical identity defect also mutates upgrade headers. Check
			// teardown on the normal stack, before FailNow unwinds deferred calls.
			finished = true
			issue600Finish(t, h, headers)
			return
		}
	}
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(final, "type").String(), string(final))
	require.Contains(t, string(final), "ws-coding-task-complete")
	if customTools {
		require.NotContains(t, string(final), "custom_tool_call")
	}
	require.Empty(t, tools.execute(t, gjson.GetBytes(final, "response")))
	require.Equal(t, 1, tools.patches)
	require.Equal(t, 1, tools.verifications)
	a, err := accounts.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, a.Schedulable)
	oracle.mu.Lock()
	defer oracle.mu.Unlock()
	expectedIdentities := []string{"Bearer fixture-readable-ws-A", "Bearer fixture-readable-ws-B", "Bearer fixture-readable-ws-B"}
	if modelCase {
		expectedIdentities = append([]string{"Bearer fixture-readable-ws-A"}, expectedIdentities...)
		require.Equal(t, "gpt-5.4", gjson.GetBytes(oracle.frames[1], "model").String(), "A actually accepted M2 before the later omission")
		require.Equal(t, "gpt-5.4", gjson.GetBytes(oracle.frames[3], "model").String())
	}
	require.Equal(t, expectedIdentities, oracle.identities)
	if nativeNamespace {
		for _, frame := range oracle.frames {
			require.JSONEq(t, httpRebuildJSON(declarations), gjson.GetBytes(frame, "tools").Raw, "native declarations and both distinct schemas must survive A/B/B")
		}
	}
	if identityCase {
		for _, header := range oracle.headers[1:] {
			require.NotEqual(t, headers.Get("conversation_id"), header.Get("conversation_id"), "WS_RESIDUAL_A_HEADER_LEAK: only outbound provider state is detached")
			require.Empty(t, header.Get("x-codex-turn-state"), "WS_RESIDUAL_A_HEADER_LEAK: retained A state must not reach B")
			if scenario == "header_oauth" {
				require.NotEmpty(t, header.Get("session_id"))
				require.NotEqual(t, headers.Get("session_id"), header.Get("session_id"))
				require.Equal(t, header.Get("session_id"), header.Get("conversation_id"), "WS_RESIDUAL_A_HEADER_LEAK: B conversation must use B's isolated session")
				require.NotEqual(t, oracle.headers[0].Get("session_id"), header.Get("session_id"))
			}
		}
	}
	if mode == service.OpenAIWSIngressModeHTTPBridge || smartMode == "http" {
		require.Equal(t, 3+extraSends, oracle.httpSends)
		require.Zero(t, oracle.wsSends)
	} else {
		require.Equal(t, 3+extraSends, oracle.wsSends)
		require.Zero(t, oracle.httpSends)
	}
	for key, raw := range owners {
		require.Equal(t, raw, issue805Owners(t, probe)[key], "A owner is never overwritten")
	}
}
