//go:build unit

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

func runWSMixedOwnerContinuation(t *testing.T, mode string, turn int) {
	t.Helper()
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
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"},
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
			accounts = &readableWSAccounts{issue600SwitchRepo: &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, other}}}}
			*repo = accounts
		})
	defer func() { _ = h.clientConn.CloseNow() }()
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
	var previous string
	for warm := 1; warm < turn; warm++ {
		issue715WSSend(t, h.clientConn, create([]string{httpRebuildMessage("user", "fixture warm")}, ""))
		raw, readErr := read()
		require.NoError(t, readErr)
		completed := gjson.GetBytes(raw, "response")
		require.Equal(t, "completed", completed.Get("status").String())
		previous = completed.Get("id").String()
		if warm == 1 {
			history = append(history, completed.Get("output.0").Raw)
		}
	}
	require.Eventually(t, func() bool {
		owners := issue805Owners(t, probe)
		if len(owners) != 2 {
			return false
		}
		for _, raw := range owners {
			if !gjson.Parse(raw).IsArray() {
				return false
			}
		}
		return true
	}, 3*time.Second, 5*time.Millisecond)
	owners := issue805Owners(t, probe)
	issue715WSSend(t, h.clientConn, create(history, previous))
	resumed, resumeErr := read()
	mu.Lock()
	sends := len(frames)
	mu.Unlock()
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
		require.Equal(t, "Bearer fixture-mixed-B", identities[turn+i])
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
