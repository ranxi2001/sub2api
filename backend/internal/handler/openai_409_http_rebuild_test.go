//go:build unit

package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	httpRebuildSession     = "fixture-unattended-coding-thread"
	httpRebuildTask        = "Fix sum.go so Sum(2, 3) returns 5, then run verification. Reuse completed tool results."
	httpRebuildSystem      = "Use the supplied coding task and recorded results. Never repeat a completed patch or verification."
	httpRebuildSource      = "package fixture\nfunc Sum(a, b int) int { return a + b }\n"
	httpRebuildPatch       = "sum.go patched once: return a + b"
	httpRebuildCheck       = "Sum(2,3)=5; PASS"
	httpRebuildAgentA      = `{"type":"agent_message","author":"/root","recipient":"/root/worker","encrypted_content":"fixture-opaque-A"}`
	httpRebuildAgentB      = `{"type":"agent_message","author":"/root","recipient":"/root/worker","encrypted_content":"fixture-readable-B"}`
	httpRebuildAgentAExtra = `{"type":"agent_message","author":"/root","recipient":"/root/worker","encrypted_content":"fixture-opaque-A-extra"}`
)

type httpRebuildWire struct {
	account int64
	url     string
	header  http.Header
	body    []byte
}

// OAuth must use the real native builder and final send gate. Only at the
// HTTPUpstream boundary do we redirect its fixed production URL to localhost;
// the body and identity headers are unchanged. No external destination is ever
// dialed. The loopback provider independently observes the resulting request.
type httpRebuildTransport struct {
	service.HTTPUpstream
	target   *url.URL
	mu       sync.Mutex
	wires    []httpRebuildWire
	mutation string // test-only final-wire mutation, never a runtime option
}

func (u *httpRebuildTransport) Do(req *http.Request, proxy string, account int64, concurrency int) (*http.Response, error) {
	if proxy != "" || (req.URL.String() != "https://chatgpt.com/backend-api/codex/responses" &&
		req.URL.String() != u.target.String()+"/v1/responses") {
		return nil, fmt.Errorf("fixture refuses unexpected transport destination")
	}
	reader, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.wires = append(u.wires, httpRebuildWire{account: account, url: req.URL.String(), header: req.Header.Clone(), body: body})
	u.mu.Unlock()
	local := req.Clone(req.Context())
	address := *req.URL
	address.Scheme, address.Host = u.target.Scheme, u.target.Host
	local.URL = &address
	if account == 2 && u.mutation != "" {
		mutated := body
		switch u.mutation {
		case "drop_tools":
			mutated, err = sjson.DeleteBytes(body, "tools")
		case "change_schema":
			mutated, err = sjson.SetBytes(body, "tools.1.parameters.properties.path.type", "integer")
		default:
			return nil, fmt.Errorf("unknown fixture wire mutation")
		}
		if err != nil {
			return nil, err
		}
		local.Body = io.NopCloser(bytes.NewReader(mutated))
		local.ContentLength = int64(len(mutated))
		local.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(mutated)), nil }
	}
	return u.HTTPUpstream.Do(local, "", account, concurrency)
}

func httpRebuildToolDefinitions() []any {
	return []any{
		map[string]any{"type": "function", "name": "apply_patch", "parameters": map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "enum": []string{"sum.go"}}, "source": map[string]any{"type": "string"}},
			"required": []string{"path", "source"}, "additionalProperties": false,
		}},
		map[string]any{"type": "function", "name": "run_tests", "parameters": map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "enum": []string{"sum.go"}}},
			"required": []string{"path"}, "additionalProperties": false,
		}},
	}
}

func httpRebuildToolsAvailable(body []byte) bool {
	actual := gjson.GetBytes(body, "tools").Array()
	expected := gjson.Parse(httpRebuildJSON(httpRebuildToolDefinitions())).Array()
	if len(actual) != len(expected) {
		return false
	}
	for _, want := range expected {
		matches := 0
		for _, got := range actual {
			if got.Get("type").String() == "function" && got.Get("name").String() == want.Get("name").String() &&
				reflect.DeepEqual(got.Get("parameters").Value(), want.Get("parameters").Value()) {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	return true // Native optional description/strict defaults are not schemas.
}

func (u *httpRebuildTransport) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, account, concurrency)
}

func (u *httpRebuildTransport) snapshot() []httpRebuildWire {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]httpRebuildWire(nil), u.wires...)
}

func httpRebuildJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err) // Only fixed JSON fixture values are passed here.
	}
	return string(encoded)
}

func httpRebuildMessage(role, text string) string {
	return httpRebuildJSON(map[string]any{"type": "message", "role": role, "content": text})
}

func httpRebuildCall(name, id string, arguments map[string]any) string {
	return httpRebuildJSON(map[string]any{"type": "function_call", "name": name, "call_id": id, "arguments": httpRebuildJSON(arguments)})
}

func httpRebuildToolPair(body []byte, name, output string) bool {
	items := gjson.GetBytes(body, "input").Array()
	calls, results := 0, 0
	for _, call := range items {
		if call.Get("type").String() != "function_call" || call.Get("name").String() != name || call.Get("call_id").String() == "" {
			continue
		}
		calls++
		arguments := gjson.Parse(call.Get("arguments").String())
		if arguments.Get("path").String() != "sum.go" || (name == "apply_patch" && arguments.Get("source").String() != httpRebuildSource) {
			return false
		}
		for _, result := range items {
			if result.Get("type").String() == "function_call_output" && result.Get("call_id").String() == call.Get("call_id").String() && result.Get("output").String() == output {
				results++
			}
		}
	}
	return calls == 1 && results == 1
}

// A bounded client-side coding tool loop: one in-memory source edit and one
// AST-based verification. No external command, repository edit or real effect.
type httpRebuildTools struct {
	source        string
	patches       int
	verifications int
}

func (tools *httpRebuildTools) execute(t *testing.T, response gjson.Result) []string {
	t.Helper()
	var results []string
	for _, call := range response.Get("output").Array() {
		if call.Get("type").String() != "function_call" {
			continue
		}
		arguments := gjson.Parse(call.Get("arguments").String())
		require.Equal(t, "sum.go", arguments.Get("path").String())
		var output string
		switch call.Get("name").String() {
		case "apply_patch":
			require.Zero(t, tools.patches, "completed patch must never execute again")
			tools.source = arguments.Get("source").String()
			require.Equal(t, httpRebuildSource, tools.source)
			tools.patches++
			output = httpRebuildPatch
		case "run_tests":
			require.Equal(t, 1, tools.patches)
			require.Zero(t, tools.verifications, "completed verification must never execute again")
			parsed, err := parser.ParseFile(token.NewFileSet(), "sum.go", tools.source, 0)
			require.NoError(t, err)
			require.Len(t, parsed.Decls, 1)
			fn, ok := parsed.Decls[0].(*ast.FuncDecl)
			require.True(t, ok)
			require.Equal(t, "Sum", fn.Name.Name)
			require.Len(t, fn.Body.List, 1)
			statement, ok := fn.Body.List[0].(*ast.ReturnStmt)
			require.True(t, ok)
			require.Len(t, statement.Results, 1)
			expression, ok := statement.Results[0].(*ast.BinaryExpr)
			require.True(t, ok)
			require.Equal(t, token.ADD, expression.Op)
			left, leftOK := expression.X.(*ast.Ident)
			right, rightOK := expression.Y.(*ast.Ident)
			require.True(t, leftOK && rightOK)
			require.Equal(t, "a", left.Name)
			require.Equal(t, "b", right.Name)
			values := map[string]int{"a": 2, "b": 3}
			output = fmt.Sprintf("Sum(2,3)=%d; PASS", values[left.Name]+values[right.Name])
			tools.verifications++
		default:
			t.Fatalf("unexpected fixture tool: %s", call.Get("name").String())
		}
		results = append(results, httpRebuildJSON(map[string]any{"type": "function_call_output", "call_id": call.Get("call_id").String(), "output": output}))
	}
	return results
}

func TestOpenAI409HTTPTaskRebuild(t *testing.T) {
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, pointer := range []string{"intent", "previous", "conversation"} {
				for _, outcome := range []string{"completed", "rejected", "write_failed"} {
					t.Run(fmt.Sprintf("%s/stream=%t/%s/%s", accountType, stream, pointer, outcome), func(t *testing.T) {
						runHTTPRebuildFixture(t, accountType, stream, pointer, outcome, "", "")
					})
				}
			}
		}
	}
}

func TestOpenAI409HTTPControlNormalization(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
			t.Run(fmt.Sprintf("%s/stream=%t/blank_parts", accountType, stream), func(t *testing.T) {
				runHTTPRebuildFixture(t, accountType, stream, "blank_parts", "completed", "", "")
			})
		}
		t.Run(fmt.Sprintf("oauth/stream=%t/nested_choice", stream), func(t *testing.T) {
			runHTTPRebuildFixture(t, service.AccountTypeOAuth, stream, "nested_choice", "completed", "", "")
		})
		t.Run(fmt.Sprintf("apikey/stream=%t/no_tools_parallel", stream), func(t *testing.T) {
			runHTTPRebuildFixture(t, service.AccountTypeAPIKey, stream, "no_tools_parallel", "completed", "", "")
		})
	}
}

type httpRebuildFlippingSettings struct {
	omit409SettingsRepo
	mu    sync.Mutex
	reads int
	fail  bool
}

func (s *httpRebuildFlippingSettings) GetValue(ctx context.Context, key string) (string, error) {
	if key != service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled {
		return s.omit409SettingsRepo.GetValue(ctx, key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.reads <= 2 {
		return "true", nil // Waiver and recovery setup, before rebuilding.
	}
	if s.fail {
		return "", errors.New("fixture persisted setting unavailable")
	}
	return "false", nil
}

func TestOpenAI409HTTPUnownedPointerMustDetach(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("setting_error=%t", fail), func(t *testing.T) {
			clients := issue805Redis(t)
			var mu sync.Mutex
			sends := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				sends++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"resp_waiver_leak","model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer provider.Close()
			accounts := issue715AccountsOfType(service.AccountTypeAPIKey)
			for i := range accounts {
				accounts[i].Credentials["base_url"] = provider.URL
			}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			settingsRepo := &httpRebuildFlippingSettings{omit409SettingsRepo: omit409SettingsRepo{enabled: true}, fail: fail}
			cache := repository.NewGatewayCache(clients())
			store := service.NewOpenAIWSStateStore(cache)
			require.NoError(t, store.BindResponseAccount(context.Background(), 3132, "resp_orphan", 1, time.Minute))
			_, _, found, err := store.GetHTTPResponseOwner(context.Background(), 3132, "resp_orphan")
			require.NoError(t, err)
			require.False(t, found, "live provider-account mapping must have no downstream user grant")
			h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, testutil.NewRealHTTPUpstream(cfg), cache, service.NewSettingService(settingsRepo, cfg), accounts...)
			h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			template, _ := newIssue715Context(t, "{}")
			done := make(chan struct{}, 1)
			router := gin.New()
			router.POST("/v1/responses", func(c *gin.Context) {
				defer func() { done <- struct{}{} }()
				for key, value := range template.Keys {
					c.Set(key, value)
				}
				h.Responses(c)
			})
			gateway := httptest.NewServer(router)
			defer gateway.Close()
			req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-5.1","stream":false,"previous_response_id":"resp_orphan","input":"Continue only my supplied readable task"}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("session_id", "fixture-unowned-pointer")
			client := gateway.Client()
			client.Timeout = 10 * time.Second
			response, err := client.Do(req)
			require.NoError(t, err)
			answer, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			require.NoError(t, err)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("waiver fixture did not finish")
			}
			settingsRepo.mu.Lock()
			reads := settingsRepo.reads
			settingsRepo.mu.Unlock()
			require.GreaterOrEqual(t, reads, 3, "exercise the policy change after the initial waiver")
			mu.Lock()
			defer mu.Unlock()
			require.Zero(t, sends, "UNOWNED_POINTER_WAIVER_LEAK: failed detachment must never use provider state; response=%s", answer)
			require.Equal(t, http.StatusBadRequest, response.StatusCode, string(answer))
			require.Contains(t, string(answer), "safely detached")
		})
	}
}

func TestOpenAI409HTTPRebuildBoundaries(t *testing.T) {
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, boundary := range []string{"pointer_only", "opaque_only", "delivery_pending", "delivery_failed", "b_disabled"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", accountType, stream, boundary), func(t *testing.T) {
					runHTTPRebuildFixture(t, accountType, stream, "previous", "completed", boundary, "")
				})
			}
		}
	}
}

// The original composed fixture also had an encrypted-agent owner. These
// actual-wire cases deliberately produce no opaque item on A, so only HTTP
// response authorization and the provider pointer can constrain selection.
func TestOpenAI409HTTPReadablePointerWithoutOpaque(t *testing.T) {
	for _, account := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, pointer := range []string{"previous", "conversation"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", account, stream, pointer), func(t *testing.T) {
					runHTTPRebuildFixture(t, account, stream, pointer+"_no_opaque", "completed", "", "")
				})
			}
		}
	}
}

func TestOpenAI409HTTPPointerWithoutOpaqueBoundaries(t *testing.T) {
	for _, account := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, boundary := range []string{"strict", "pointer_only", "foreign_user", "b_disabled"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", account, stream, boundary), func(t *testing.T) {
					runHTTPRebuildFixture(t, account, stream, "previous_no_opaque", "completed", boundary, "")
				})
			}
		}
	}
}

func TestOpenAI409HTTPToolDefinitionOracle(t *testing.T) {
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, mutation := range []string{"drop_tools", "change_schema"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", accountType, stream, mutation), func(t *testing.T) {
					runHTTPRebuildFixture(t, accountType, stream, "previous", "completed", "", mutation)
				})
			}
		}
	}
}

func TestOpenAI409HTTPMixedDeliveryFences(t *testing.T) {
	for _, accountType := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, owner := range []string{"A", "B"} {
				for _, state := range []string{"pending", "failed", "conflict"} {
					for _, order := range []string{"forward", "reverse"} {
						t.Run(fmt.Sprintf("%s/stream=%t/%s/%s/%s", accountType, stream, owner, state, order), func(t *testing.T) {
							runHTTPRebuildFixture(t, accountType, stream, "previous", "completed", "mixed/"+owner+"/"+state+"/"+order, "")
						})
					}
				}
			}
		}
	}
}

// Address an existing fixture record, not manufacture ownership. Scope and
// payload hashes locate the actual Lua-written key, which callers verify exists
// before changing its delivery fence. No production cache APIs are overridden.
func httpRebuildOwnerKey(t *testing.T, h *OpenAIGatewayHandler, agent string) string {
	t.Helper()
	c, _ := newIssue715Context(t, "{}")
	c.Request.Header.Set("session_id", httpRebuildSession)
	session := h.gatewayService.GenerateSessionHash(c, []byte(`{"model":"gpt-5.1"}`))
	var item any
	require.NoError(t, json.Unmarshal([]byte(agent), &item))
	itemHash := sha256.Sum256([]byte(httpRebuildJSON(item)))
	scope := httpRebuildJSON([]string{"99", "3132", session, "gpt-5.1", hex.EncodeToString(itemHash[:])})
	key := sha256.Sum256([]byte(scope))
	return "openai_agent_owner:v1:" + hex.EncodeToString(key[:])
}

func runHTTPRebuildFixture(t *testing.T, accountType string, stream bool, pointer, outcome, boundary, mutation string) {
	t.Helper()
	noOpaque := strings.HasSuffix(pointer, "_no_opaque")
	pointer = strings.TrimSuffix(pointer, "_no_opaque")
	mixed := strings.HasPrefix(boundary, "mixed/")
	clients := issue805Redis(t)
	observer := clients()
	patchCall := httpRebuildCall("apply_patch", "call_patch_once", map[string]any{"path": "sum.go", "source": httpRebuildSource})
	checkCall := httpRebuildCall("run_tests", "call_verify_once", map[string]any{"path": "sum.go"})
	var mu sync.Mutex
	var providerBodies [][]byte
	var providerHeaders []http.Header
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		providerBodies = append(providerBodies, body)
		providerHeaders = append(providerHeaders, r.Header.Clone())
		n := len(providerBodies)
		mu.Unlock()
		noToolsFinal := pointer == "no_tools_parallel" && n == 3
		if (!noToolsFinal && !httpRebuildToolsAvailable(body)) || (noToolsFinal && (gjson.GetBytes(body, "tools").Exists() || gjson.GetBytes(body, "parallel_tool_calls").Exists())) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"fixture rejected missing or changed tool declarations"}}`)
			return
		}
		id, conversation := "resp_rebuild_A", "conv_rebuild_A"
		output := []json.RawMessage{json.RawMessage(patchCall), json.RawMessage(httpRebuildAgentA)}
		if noOpaque {
			output = []json.RawMessage{json.RawMessage(patchCall)}
		}
		if mixed {
			output = append(output, json.RawMessage(httpRebuildAgentAExtra))
		}
		if n > 1 {
			readable := gjson.GetBytes(body, "input").Raw + " " + gjson.GetBytes(body, "instructions").String()
			valid := r.Header.Get("Authorization") == "Bearer fixture-rebuild-B" &&
				strings.Contains(readable, httpRebuildTask) && strings.Contains(readable, httpRebuildSystem) &&
				!strings.Contains(string(body), "fixture-opaque-A") &&
				!strings.Contains(string(body), "resp_rebuild_A") && !strings.Contains(string(body), "conv_rebuild_A") &&
				httpRebuildToolPair(body, "apply_patch", httpRebuildPatch)
			if n == 2 {
				valid = valid && !gjson.GetBytes(body, "previous_response_id").Exists() && !gjson.GetBytes(body, "conversation").Exists()
				if pointer == "nested_choice" {
					valid = valid && gjson.GetBytes(body, "tool_choice.name").String() == "run_tests" && !gjson.GetBytes(body, "tool_choice.function").Exists()
				}
			}
			if accountType == service.AccountTypeOAuth {
				valid = valid && r.URL.Path == "/backend-api/codex/responses" && r.Host == "chatgpt.com" &&
					r.Header.Get("ChatGPT-Account-ID") == "fixture-chatgpt-B" &&
					gjson.GetBytes(body, "stream").Bool() && gjson.GetBytes(body, "store").Type == gjson.False &&
					gjson.GetBytes(body, `input.#(role==system)`).Type == gjson.Null
			}
			if n == 3 {
				valid = valid && httpRebuildToolPair(body, "run_tests", httpRebuildCheck)
			}
			if !valid || outcome == "rejected" || n > 3 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"fixture rejected final task, identity or provider pointer"}}`)
				return
			}
			id, conversation = "resp_rebuild_B", "conv_rebuild_B"
			output = []json.RawMessage{json.RawMessage(checkCall), json.RawMessage(httpRebuildAgentB)}
			if n == 3 {
				id = "resp_rebuild_done"
				output = []json.RawMessage{json.RawMessage(httpRebuildJSON(map[string]any{
					"type": "message", "role": "assistant", "content": []any{map[string]any{
						"type": "output_text", "text": "coding-task-complete: Sum(2,3)=5; patch and verification each executed once",
					}},
				}))}
			}
		}
		payload := httpRebuildJSON(map[string]any{"id": id, "conversation": map[string]any{"id": conversation}, "status": "completed", "error": nil, "model": "gpt-5.1", "output": output, "usage": map[string]int{"input_tokens": 5, "output_tokens": 3}})
		if gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", payload)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, payload)
		}
	}))
	defer provider.Close()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	target, err := url.Parse(provider.URL)
	require.NoError(t, err)
	transport := &httpRebuildTransport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target, mutation: mutation}
	accounts := issue715AccountsOfType(accountType)
	for i, label := range []string{"A", "B"} {
		accounts[i].Credentials = map[string]any{"base_url": provider.URL, "api_key": "fixture-rebuild-" + label, "access_token": "fixture-rebuild-" + label, "chatgpt_account_id": "fixture-chatgpt-" + label}
	}
	settings := service.NewSettingService(&omit409SettingsRepo{enabled: boundary != "strict"}, cfg)
	usage := &readable409UsageRepo{}
	h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, transport, repository.NewGatewayCache(clients()), settings, usage, accounts...)
	h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	template, _ := newIssue715Context(t, "{}")
	done := make(chan struct{}, 1)
	var sessions []string
	pendingBeforeWrite := false
	var observeErr error
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		defer func() { done <- struct{}{} }()
		sessions = append(sessions, c.GetHeader("session_id"))
		for key, value := range template.Keys {
			c.Set(key, value)
		}
		if c.GetHeader("X-Fixture-Foreign-User") == "true" {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 101})
		}
		c.Writer = &readable409DeliveryWriter{ResponseWriter: c.Writer, fail: outcome == "write_failed", observe: func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var keys []string
			keys, observeErr = observer.Keys(ctx, "openai_agent_owner:*").Result()
			for _, key := range keys {
				var value string
				value, observeErr = observer.Get(ctx, key).Result()
				if observeErr != nil {
					return
				}
				pendingBeforeWrite = pendingBeforeWrite || (strings.HasPrefix(value, "pending:") && strings.Contains(value, ":[1,2,"))
			}
		}}
		h.Responses(c)
	})
	gateway := httptest.NewServer(router)
	defer gateway.Close()
	client := gateway.Client()
	client.Timeout = 10 * time.Second
	request := func(history []string, previous gjson.Result) (int, string) {
		body := map[string]any{"model": "gpt-5.1", "stream": stream, "store": false, "prompt_cache_key": httpRebuildSession, "tools": httpRebuildToolDefinitions()}
		if previous.Exists() && pointer == "blank_parts" {
			history = append(append([]string(nil), history...), `{"role":"user","content":[{"type":"input_text","text":"  \n\t "}]}`)
		}
		if pointer == "nested_choice" && previous.Get("id").String() == "resp_rebuild_A" {
			body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "run_tests"}}
		}
		if pointer == "no_tools_parallel" && previous.Get("id").String() == "resp_rebuild_B" {
			delete(body, "tools")
			body["parallel_tool_calls"] = true
		}
		body["input"] = json.RawMessage("[" + strings.Join(history, ",") + "]")
		if previous.Exists() && (pointer == "previous" || boundary == "pointer_only") {
			body["previous_response_id"] = previous.Get("id").String()
		}
		if previous.Exists() && pointer == "conversation" {
			body["conversation"] = map[string]any{"id": previous.Get("conversation.id").String()}
		}
		req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/responses", strings.NewReader(httpRebuildJSON(body)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("session_id", httpRebuildSession)
		if boundary == "foreign_user" && previous.Exists() {
			req.Header.Set("X-Fixture-Foreign-User", "true")
		}
		response, err := client.Do(req)
		require.NoError(t, err)
		answer, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		require.NoError(t, err)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("fixture handler did not finish")
		}
		return response.StatusCode, string(answer)
	}
	history := []string{httpRebuildMessage("system", httpRebuildSystem), httpRebuildMessage("user", httpRebuildTask)}
	status, answer := request(history, gjson.Result{})
	require.Equal(t, http.StatusOK, status, answer)
	first := issue758CompletedResponse(t, answer, stream)
	require.Equal(t, "resp_rebuild_A", first.Get("id").String())
	require.Equal(t, "conv_rebuild_A", first.Get("conversation.id").String())
	initialWire := transport.snapshot()
	require.Len(t, initialWire, 1)
	require.Equal(t, int64(1), initialWire[0].account)
	require.Equal(t, "Bearer fixture-rebuild-A", initialWire[0].header.Get("Authorization"))
	if accountType == service.AccountTypeOAuth {
		require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", initialWire[0].url)
		require.Equal(t, "fixture-chatgpt-A", initialWire[0].header.Get("ChatGPT-Account-ID"))
	}
	tools := &httpRebuildTools{}
	for _, item := range first.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tools.execute(t, first)...)
	require.Equal(t, 1, tools.patches)
	require.Zero(t, tools.verifications)
	require.Len(t, usage.snapshot(), 1, "A must finish the real usage path")
	ownersA := issue805Owners(t, observer)
	if noOpaque {
		require.Empty(t, ownersA, "pointer-only ownership path must have no encrypted-agent owner to hide behind")
		require.NotContains(t, strings.Join(history, ","), "agent_message")
		owned, err := h.gatewayService.ValidateOpenAIHTTPResponseOwner(context.Background(), 3132, first.Get("id").String(), 100, 99)
		require.NoError(t, err)
		require.True(t, owned, "the actual A response must have downstream HTTP authorization")
	} else if mixed {
		require.Len(t, ownersA, 2)
	} else {
		require.Len(t, ownersA, 1)
	}
	accounts[0].Schedulable = false
	disabled, err := (openAIImagesFailoverAccountRepo{accounts: accounts}).GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, disabled.Schedulable)
	switch boundary {
	case "pointer_only":
		history = nil
	case "opaque_only":
		history = []string{httpRebuildAgentA}
	case "delivery_pending", "delivery_failed":
		for key, owner := range ownersA {
			value := "failed"
			if boundary == "delivery_pending" {
				value = "pending:fixture-unconfirmed:" + owner
			}
			require.NoError(t, observer.Set(context.Background(), key, value, time.Minute).Err())
			ownersA[key] = value
		}
	case "b_disabled":
		accounts[1].Schedulable = false
	}
	status, answer = request(history, first)
	if boundary != "" && !mixed {
		if boundary == "strict" {
			require.Len(t, transport.snapshot(), 1, "HTTP_POINTER_STRICT_A_STATE_LEAK: disabled A pointer must never reach B under default strict policy; response=%s", answer)
		}
		require.GreaterOrEqual(t, status, 400, "unrecoverable input/delivery must not pretend completion: %s", answer)
		require.NotEmpty(t, gjson.Get(answer, "error.message").String(), answer)
		require.NotContains(t, answer, "response.completed")
		require.Len(t, transport.snapshot(), 1, "boundary must refuse before any B generation")
		require.Equal(t, ownersA, issue805Owners(t, observer), "refusal must not clear pending/failed/old owner")
		require.Equal(t, 1, tools.patches)
		require.Zero(t, tools.verifications)
		return
	}
	wires := transport.snapshot()
	require.Len(t, wires, 2, "HTTP_REBUILD_B_DISPATCH_MISSING: automatic continuation with readable intent must reach B exactly once; response=%s", answer)
	require.Equal(t, int64(1), wires[0].account)
	require.Equal(t, int64(2), wires[1].account)
	require.Equal(t, "Bearer fixture-rebuild-B", wires[1].header.Get("Authorization"))
	require.NotContains(t, string(wires[1].body), "fixture-opaque-A")
	require.False(t, gjson.GetBytes(wires[1].body, "previous_response_id").Exists())
	require.False(t, gjson.GetBytes(wires[1].body, "conversation").Exists())
	require.True(t, httpRebuildToolPair(wires[1].body, "apply_patch", httpRebuildPatch), "completed call/result relation must survive")
	if accountType == service.AccountTypeOAuth {
		require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", wires[1].url)
		require.NotEmpty(t, wires[1].header.Get("session_id"))
		require.NotEqual(t, wires[0].header.Get("session_id"), wires[1].header.Get("session_id"), "upstream account namespace changes while the client session stays fixed")
	}
	owners := issue805Owners(t, observer)
	for key, owner := range ownersA {
		require.Equal(t, owner, owners[key], "old A owner remains intact")
		delete(owners, key)
	}
	if mutation != "" {
		require.Equal(t, http.StatusBadRequest, status, "wire mutant must not get a false GREEN: %s", answer)
		require.Contains(t, answer, "missing or changed tool declarations")
		require.Empty(t, owners, "provider must reject before generating a new tool or opaque item")
		require.Len(t, usage.snapshot(), 1)
		require.Zero(t, tools.verifications)
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, providerBodies, 2)
		require.True(t, httpRebuildToolsAvailable(wires[1].body), "unmutated builder output must be valid")
		require.False(t, httpRebuildToolsAvailable(providerBodies[1]), "mutation must actually reach the network provider")
		require.JSONEq(t, gjson.GetBytes(wires[1].body, "input").Raw, gjson.GetBytes(providerBodies[1], "input").Raw)
		t.Log("tool-definition wire mutation reached provider and was rejected before tool emission")
		return
	}
	if outcome == "rejected" {
		require.Equal(t, http.StatusBadRequest, status, answer)
		require.Contains(t, answer, "fixture rejected")
		require.Empty(t, owners)
		require.Len(t, usage.snapshot(), 1)
		return
	}
	require.NoError(t, observeErr)
	require.True(t, pendingBeforeWrite, "B output must be reserved before exposure")
	require.Len(t, owners, 1)
	require.Len(t, usage.snapshot(), 2, "completed B usage survives downstream failure")
	for _, owner := range owners {
		if outcome == "write_failed" {
			require.Equal(t, "failed", owner, "failed downstream write must not commit B")
		} else {
			require.True(t, strings.HasPrefix(owner, "[1,2,"), owner)
		}
	}
	if outcome == "write_failed" {
		require.NotContains(t, answer, "coding-task-complete")
		require.Equal(t, 1, tools.patches)
		require.Zero(t, tools.verifications)
		return
	}
	require.Equal(t, http.StatusOK, status, answer)
	second := issue758CompletedResponse(t, answer, stream)
	for _, item := range second.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tools.execute(t, second)...)
	if mixed {
		parts := strings.Split(boundary, "/")
		fenced := httpRebuildAgentA
		if parts[1] == "B" {
			fenced = httpRebuildAgentB
		}
		key := httpRebuildOwnerKey(t, h, fenced)
		expected := issue805Owners(t, observer)
		require.Len(t, expected, 3, "two delivered A items and a delivered B item")
		require.Contains(t, expected, key, "fence an actually delivered item")
		value := parts[2]
		if value == "pending" {
			value = "pending:fixture-mixed-unconfirmed:" + expected[key]
		}
		require.NoError(t, observer.Set(context.Background(), key, value, time.Minute).Err())
		expected[key] = value
		if parts[3] == "reverse" {
			var indices []int
			for i, item := range history {
				if gjson.Get(item, "type").String() == "agent_message" {
					indices = append(indices, i)
				}
			}
			for left, right := 0, len(indices)-1; left < right; left, right = left+1, right-1 {
				history[indices[left]], history[indices[right]] = history[indices[right]], history[indices[left]]
			}
		}
		// In reverse A-fenced order: completed B, completed A-extra, fenced A.
		// A reader that stops at the first owner mismatch misses the last fence.
		status, answer = request(history, second)
		require.Len(t, transport.snapshot(), 2, "HTTP_REBUILD_FENCE_BYPASSED: every mixed-history fence must stop the next generation; response=%s", answer)
		require.GreaterOrEqual(t, status, 400, answer)
		require.NotEmpty(t, gjson.Get(answer, "error.message").String(), answer)
		require.NotContains(t, answer, "response.completed")
		require.Equal(t, expected, issue805Owners(t, observer), "all Redis records must remain byte-identical on refusal")
		require.Equal(t, 1, tools.patches)
		require.Equal(t, 1, tools.verifications)
		require.Len(t, usage.snapshot(), 2)
		return
	}
	// No new user text, no retry and no client/session reset. This next tool turn
	// includes delivered A and B opaque history; both producer records survive.
	status, answer = request(history, second)
	require.Equal(t, http.StatusOK, status, "automatic next tool turn must finish: %s", answer)
	final := issue758CompletedResponse(t, answer, stream)
	require.Contains(t, final.Raw, "coding-task-complete")
	require.Empty(t, tools.execute(t, final), "final output must not request completed tools again")
	require.Equal(t, 1, tools.patches)
	require.Equal(t, 1, tools.verifications)
	require.Equal(t, []string{httpRebuildSession, httpRebuildSession, httpRebuildSession}, sessions)
	require.False(t, accounts[0].Schedulable)
	wires = transport.snapshot()
	require.Len(t, wires, 3, "one A generation and exactly two B generations")
	require.Equal(t, int64(2), wires[2].account)
	require.True(t, httpRebuildToolPair(wires[2].body, "run_tests", httpRebuildCheck))
	require.Len(t, usage.snapshot(), 3)
	finalOwners := issue805Owners(t, observer)
	for key, owner := range ownersA {
		require.Equal(t, owner, finalOwners[key])
	}
	for key, owner := range owners {
		require.Equal(t, owner, finalOwners[key], "completed B owner cannot be overwritten by subsequent consumption")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, providerBodies, 3)
	for i, wire := range wires {
		require.JSONEq(t, string(wire.body), string(providerBodies[i]), "provider must receive the final builder payload unchanged")
		require.Equal(t, wire.header.Get("Authorization"), providerHeaders[i].Get("Authorization"))
	}
}
