//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const shadowHTTPAgent = `{"type":"agent_message","author":"/root","recipient":"/root/worker","encrypted_content":"fixture-shadow-current-task"}`
const shadowOtherTask = "Verify the existing sum.go only; do not edit it. This is independent task Y."

type shadowAuditProbe struct{ evaluated, private atomic.Bool }

func (*shadowAuditProbe) EffectiveMode() securityaudit.Mode { return securityaudit.ModeBlocking }
func (*shadowAuditProbe) Enqueue(context.Context, securityaudit.Request) error {
	return errors.New("unexpected asynchronous fixture")
}
func (p *shadowAuditProbe) Evaluate(_ context.Context, req securityaudit.Request) (*securityaudit.PromptDecision, error) {
	if strings.Contains(string(req.Body), httpRebuildTask) {
		p.evaluated.Store(true)
	}
	field := reflect.ValueOf(req).FieldByName("NoBodyRetention")
	if field.IsValid() && field.Bool() {
		p.private.Store(true)
	}
	return &securityaudit.PromptDecision{Kind: securityaudit.DecisionAllow, AllowNextStage: true}, nil
}

func TestOpenAI409FutureShadowAuditProvenance(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) { runFutureShadowHTTP(t, stream, "privacy") })
	}
}

// Observe the real downstream write; the shadow must not become recoverable
// merely because the provider returned a terminal payload.
type shadowHTTPWriter struct {
	gin.ResponseWriter
	fail    bool
	observe func()
}

func (w *shadowHTTPWriter) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "fixture-shadow-current-task") {
		w.observe()
		if w.fail {
			return 0, io.ErrClosedPipe
		}
	}
	return w.ResponseWriter.Write(body)
}

func (w *shadowHTTPWriter) WriteString(body string) (int, error) { return w.Write([]byte(body)) }

func TestOpenAI409FutureShadowHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			runFutureShadowHTTP(t, stream, "recover")
		})
	}
}

func TestOpenAI409FutureShadowOAuth(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"recover", "write_failed", "pending_owner", "store_read_failed", "two_known_tasks", "fresh_intent"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, scenario), func(t *testing.T) {
				runFutureShadowTransport(t, service.AccountTypeOAuth, stream, scenario)
			})
		}
	}
}

func TestOpenAI409FutureShadowTaskAssociation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			runFutureShadowHTTP(t, stream, "two_known_tasks")
		})
	}
}

func TestOpenAI409FutureShadowBoundaries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, boundary := range []string{"other_user", "other_key", "other_group", "other_model", "other_session", "other_task", "expired", "write_failed", "pending_owner", "failed_owner", "conflicted_owner", "off", "fresh_intent"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, boundary), func(t *testing.T) {
				runFutureShadowHTTP(t, stream, boundary)
			})
		}
	}
}

// Faults apply only to the optional shared shadow namespace. Owner, billing,
// scheduling and actual HTTP delivery still use the real Redis/transport.
type shadowStoreFailure struct{ calls atomic.Int64 }

func (h *shadowStoreFailure) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *shadowStoreFailure) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *shadowStoreFailure) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		for _, arg := range cmd.Args() {
			value, _ := arg.(string)
			if strings.HasPrefix(value, "openai_readable_shadow:") || strings.HasPrefix(value, "openai_readable_shadow_index:") {
				h.calls.Add(1)
				return errors.New("fixture shadow store unavailable")
			}
		}
		return next(ctx, cmd)
	}
}

func TestOpenAI409FutureShadowResources(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"capacity_oldest", "capacity_latest", "store_write_failed", "store_read_failed", "oversize", "pending_tool", "pending_output", "conflicting_output", "echoed_task"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, scenario), func(t *testing.T) {
				runFutureShadowHTTP(t, stream, scenario)
			})
		}
	}
}

func runFutureShadowHTTP(t *testing.T, stream bool, scenario string) {
	t.Helper()
	runFutureShadowTransport(t, service.AccountTypeAPIKey, stream, scenario)
}

func runFutureShadowTransport(t *testing.T, accountType string, stream bool, scenario string) {
	t.Helper()
	clients := issue805Redis(t)
	probe := clients()
	var mu sync.Mutex
	var bodies [][]byte
	var identities []string
	var providerHeaders []http.Header
	reusedOutput := scenario == "conflicting_output" || scenario == "echoed_task"
	twoTasks := scenario == "two_known_tasks" || reusedOutput
	patch := httpRebuildCall("apply_patch", "call_shadow_patch", map[string]any{"path": "sum.go", "source": httpRebuildSource})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		identities = append(identities, r.Header.Get("Authorization"))
		providerHeaders = append(providerHeaders, r.Header.Clone())
		n := len(bodies)
		mu.Unlock()
		output := []json.RawMessage{json.RawMessage(patch)}
		switch {
		case twoTasks && n == 3:
			if !strings.Contains(string(body), shadowOtherTask) || strings.Contains(string(body), httpRebuildTask) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			output = []json.RawMessage{json.RawMessage(httpRebuildCall("run_tests", "call_shadow_Y_verify", map[string]any{"path": "sum.go"}))}
		case r.Header.Get("Authorization") == "Bearer fixture-shadow-A" && n >= 2:
			validPair := httpRebuildToolPair(body, "apply_patch", httpRebuildPatch)
			if twoTasks && n == 4 {
				validPair = httpRebuildToolPair(body, "run_tests", httpRebuildCheck) && strings.Contains(string(body), shadowOtherTask)
			}
			if !validPair {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture requires the actual completed patch"}}`)
				return
			}
			agent := shadowHTTPAgent
			if n > 2 && !reusedOutput {
				agent = strings.ReplaceAll(agent, "fixture-shadow-current-task", fmt.Sprintf("fixture-shadow-current-task-%d", n))
			}
			output = []json.RawMessage{json.RawMessage(agent)}
			if scenario == "pending_output" {
				output = append(output, json.RawMessage(httpRebuildCall("run_tests", "call_unconfirmed_output", map[string]any{"path": "sum.go"})))
			}
		case r.Header.Get("Authorization") == "Bearer fixture-shadow-B":
			valid := r.Header.Get("Authorization") == "Bearer fixture-shadow-B" &&
				!strings.Contains(string(body), "fixture-shadow-current-task") &&
				!gjson.GetBytes(body, "previous_response_id").Exists() && !gjson.GetBytes(body, "conversation").Exists() &&
				httpRebuildToolsAvailable(body)
			if accountType == service.AccountTypeOAuth {
				valid = valid && r.URL.Path == "/backend-api/codex/responses" && r.Host == "chatgpt.com" &&
					r.Header.Get("ChatGPT-Account-ID") == "fixture-shadow-account-B" &&
					gjson.GetBytes(body, "stream").Bool() && gjson.GetBytes(body, "store").Type == gjson.False &&
					gjson.GetBytes(body, `input.#(role==system)`).Type == gjson.Null &&
					r.Header.Get("x-codex-turn-state") != "fixture-shadow-A-state" && r.Header.Get("conversation_id") != "fixture-shadow-A-conversation"
			}
			if scenario == "fresh_intent" {
				valid = valid && strings.Contains(string(body), "Report only the supplied fresh task") && !strings.Contains(string(body), httpRebuildTask)
			} else {
				valid = valid && strings.Contains(string(body), httpRebuildTask) && httpRebuildToolPair(body, "apply_patch", httpRebuildPatch)
			}
			if scenario == "two_known_tasks" {
				associationValid := strings.Contains(string(body), httpRebuildTask) && httpRebuildToolPair(body, "apply_patch", httpRebuildPatch) &&
					!strings.Contains(string(body), shadowOtherTask) && !strings.Contains(string(body), "call_shadow_Y_verify") && !strings.Contains(string(body), httpRebuildCheck)
				if !associationValid {
					t.Log("SHADOW_TASK_ASSOCIATION_MISMATCH: real B received the wrong delivered task/result pair")
				}
				valid = valid && associationValid
			}
			if !valid {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture rejects guessed task or missing completed result"}}`)
				return
			}
			output = []json.RawMessage{json.RawMessage(httpRebuildJSON(map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "shadow-task-complete; patch already applied once"}}}))}
		default:
			if n > 3 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		response := httpRebuildJSON(map[string]any{"id": fmt.Sprintf("resp_shadow_%d", n), "model": "gpt-5.1", "status": "completed", "error": nil, "output": output, "usage": map[string]int{"input_tokens": 5, "output_tokens": 3}})
		if gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, response)
		}
	}))
	defer provider.Close()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	target, err := url.Parse(provider.URL)
	require.NoError(t, err)
	transport := &httpRebuildTransport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
	accounts := issue715AccountsOfType(accountType)
	for i, label := range []string{"A", "B"} {
		accounts[i].Credentials = map[string]any{"base_url": provider.URL, "api_key": "fixture-shadow-" + label,
			"access_token": "fixture-shadow-" + label, "chatgpt_account_id": "fixture-shadow-account-" + label}
	}
	storeFailure := &shadowStoreFailure{}
	newNode := func(failStore bool) *OpenAIGatewayHandler {
		cfg := &config.Config{RunMode: config.RunModeSimple}
		cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		settings := service.NewSettingService(&omit409SettingsRepo{enabled: scenario != "off"}, cfg)
		client := clients()
		if failStore {
			client.AddHook(storeFailure)
		}
		h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, transport, repository.NewGatewayCache(client), settings, &readable409UsageRepo{}, accounts...)
		h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		return h
	}
	producer, consumer := newNode(scenario == "store_write_failed"), newNode(scenario == "store_read_failed") // independent service state/Redis clients
	auditProbe := &shadowAuditProbe{}
	if scenario == "privacy" {
		consumer.securityAuditCoordinator = securityaudit.NewCoordinator(nil, auditProbe)
	}
	done := make(chan struct{}, 1)
	var beforeDelivery []string
	var captureErr error
	deliveryObserved := false
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		defer func() { done <- struct{}{} }()
		clientIdentity := c.Request.Header.Clone()
		defer func() {
			for _, name := range []string{"session_id", "session-id", "thread-id"} {
				require.Equal(t, clientIdentity.Values(name), c.Request.Header.Values(name), "client identity must survive upstream rebuilding: %s", name)
			}
		}()
		keyID, userID, groupID := int64(99), int64(100), int64(3132)
		if c.GetHeader("X-Fixture-Node") == "consumer" {
			switch scenario {
			case "other_user":
				userID++
			case "other_key":
				keyID++
			case "other_group":
				groupID = 3131 // still a schedulable group; scope is what must deny
			}
		}
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: keyID, UserID: userID, GroupID: &groupID,
			Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI}, User: &service.User{ID: userID}, AllowLongContext: true})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
		if c.GetHeader("X-Fixture-Node") == "consumer" {
			consumer.Responses(c)
		} else {
			c.Writer = &shadowHTTPWriter{ResponseWriter: c.Writer, fail: scenario == "write_failed", observe: func() {
				deliveryObserved = true
				beforeDelivery, captureErr = probe.Keys(context.Background(), "openai_readable_shadow:v1:*").Result()
			}}
			producer.Responses(c)
		}
	})
	gateway := httptest.NewServer(router)
	defer gateway.Close()
	client := gateway.Client()
	client.Timeout = 10 * time.Second
	currentSession := "fixture-shadow-thread"
	request := func(input []string, consume bool) (int, string) {
		model, session := "gpt-5.1", currentSession
		if consume && scenario == "other_model" {
			model = "gpt-5.4"
		}
		if consume && scenario == "other_session" {
			session += "-unrelated"
		}
		body := httpRebuildJSON(map[string]any{"model": model, "stream": stream, "store": false, "tools": httpRebuildToolDefinitions(), "input": json.RawMessage("[" + strings.Join(input, ",") + "]")})
		req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/responses", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("session_id", session)
		if consume {
			req.Header.Set("X-Fixture-Node", "consumer")
			if accountType == service.AccountTypeOAuth {
				req.Header.Set("x-codex-turn-state", "fixture-shadow-A-state")
				req.Header.Set("conversation_id", "fixture-shadow-A-conversation")
			}
		}
		response, err := client.Do(req)
		require.NoError(t, err)
		answer, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		require.NoError(t, err)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("shadow fixture handler did not finish")
		}
		return response.StatusCode, string(answer)
	}
	task := httpRebuildTask
	if scenario == "oversize" {
		task += strings.Repeat(" fixture long task", 5000)
	}
	history := []string{httpRebuildMessage("user", task)}
	status, answer := request(history, false)
	require.Equal(t, http.StatusOK, status, answer)
	first := issue758CompletedResponse(t, answer, stream)
	tool := &httpRebuildTools{}
	for _, item := range first.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tool.execute(t, first)...)
	require.Equal(t, 1, tool.patches)
	if scenario == "pending_tool" {
		history = append(history, httpRebuildCall("run_tests", "call_unconfirmed_side_effect", map[string]any{"path": "sum.go"}))
	}
	status, answer = request(history, false)
	require.True(t, deliveryObserved, "must reach the actual opaque-output downstream write")
	require.NoError(t, captureErr)
	require.Empty(t, beforeDelivery, "future task context is committed only after successful terminal delivery")
	if scenario != "write_failed" {
		require.Equal(t, http.StatusOK, status, answer)
		second := issue758CompletedResponse(t, answer, stream)
		require.Contains(t, second.Get("output").Raw, "fixture-shadow-current-task")
	}
	opaque := shadowHTTPAgent
	producerRequests := 2
	if twoTasks {
		otherHistory := []string{httpRebuildMessage("user", shadowOtherTask)}
		if scenario == "echoed_task" {
			otherHistory = append([]string{shadowHTTPAgent}, otherHistory...)
		}
		status, answer = request(otherHistory, false)
		require.Equal(t, http.StatusOK, status, answer)
		otherCall := issue758CompletedResponse(t, answer, stream)
		for _, item := range otherCall.Get("output").Array() {
			otherHistory = append(otherHistory, item.Raw)
		}
		otherHistory = append(otherHistory, tool.execute(t, otherCall)...)
		status, answer = request(otherHistory, false)
		require.Equal(t, http.StatusOK, status, answer)
		otherDone := issue758CompletedResponse(t, answer, stream)
		if reusedOutput {
			require.JSONEq(t, shadowHTTPAgent, otherDone.Get("output.0").Raw)
		} else {
			require.Contains(t, otherDone.Get("output").Raw, "fixture-shadow-current-task-4")
		}
		require.Equal(t, 1, tool.verifications)
		producerRequests = 4
	}
	capacity := strings.HasPrefix(scenario, "capacity_")
	if capacity {
		for i := 0; i < 140; i++ {
			currentSession = fmt.Sprintf("fixture-shadow-thread-%d", i)
			status, answer = request(history, false)
			require.Equal(t, http.StatusOK, status, answer)
			second := issue758CompletedResponse(t, answer, stream)
			opaque = second.Get("output.0").Raw
			require.Contains(t, opaque, "fixture-shadow-current-task-")
			producerRequests++
		}
		if scenario == "capacity_oldest" {
			currentSession, opaque = "fixture-shadow-thread", shadowHTTPAgent
		}
	}
	owners := issue805Owners(t, probe)
	ownerCount := producerRequests - 1
	if scenario == "two_known_tasks" {
		ownerCount = 2
	} else if reusedOutput {
		ownerCount = 1
	}
	require.Len(t, owners, ownerCount)
	if scenario != "write_failed" {
		for _, owner := range owners {
			require.True(t, json.Valid([]byte(owner)) && gjson.Parse(owner).IsArray(), "every produced task must have an actual committed owner")
		}
	}
	accounts[0].Schedulable = false
	shadowKeys, err := probe.Keys(context.Background(), "openai_readable_shadow:v1:*").Result()
	require.NoError(t, err)
	if reusedOutput {
		require.Len(t, shadowKeys, 1)
		value, err := probe.Get(context.Background(), shadowKeys[0]).Result()
		require.NoError(t, err)
		require.Equal(t, "conflict", value, "same opaque output must not silently acquire another task")
	}
	if capacity {
		epochKeys, err := probe.Keys(context.Background(), "openai_agent_shadow_epoch:v1:*").Result()
		require.NoError(t, err)
		require.Len(t, epochKeys, 128, "association metadata must also be bounded across sessions")
		indexes, err := probe.Keys(context.Background(), "openai_agent_shadow_index:v1:*").Result()
		require.NoError(t, err)
		require.Len(t, indexes, 1)
		count, err := probe.ZCard(context.Background(), indexes[0]).Result()
		require.NoError(t, err)
		require.EqualValues(t, 128, count)
		ttl, err := probe.PTTL(context.Background(), indexes[0]).Result()
		require.NoError(t, err)
		require.Greater(t, ttl, time.Duration(0))
		require.LessOrEqual(t, ttl, 30*time.Minute)
		require.Len(t, shadowKeys, 8, "SHADOW_CAPACITY_UNBOUNDED: all sessions/models share the principal's eight-record budget")
		total := 0
		for _, key := range shadowKeys {
			value, err := probe.Get(context.Background(), key).Result()
			require.NoError(t, err)
			total += len(value)
		}
		require.LessOrEqual(t, total, 512*1024, "shadow payload bytes must be bounded across sessions")
	}
	if scenario == "store_read_failed" {
		require.NotEmpty(t, shadowKeys, "read fault must operate on a real committed record")
	}
	if scenario == "expired" {
		require.NotEmpty(t, shadowKeys, "expiry test must expire an actual committed future shadow")
		for _, key := range shadowKeys {
			require.NoError(t, probe.PExpire(context.Background(), key, -time.Millisecond).Err())
		}
	}
	if scenario == "failed_owner" || scenario == "pending_owner" || scenario == "conflicted_owner" {
		for key, owner := range owners {
			value := "failed"
			if scenario == "pending_owner" {
				value = "pending:fixture-shadow-unknown:" + owner
			} else if scenario == "conflicted_owner" {
				value = "conflict"
			}
			require.NoError(t, probe.Set(context.Background(), key, value, time.Minute).Err())
			owners[key] = value
		}
	}
	if scenario == "other_task" {
		opaque = strings.ReplaceAll(opaque, "fixture-shadow-current-task", "fixture-shadow-unrelated-task")
	}
	input := []string{opaque}
	if scenario == "fresh_intent" {
		input = append(input, httpRebuildMessage("user", "Report only the supplied fresh task"))
	}
	status, answer = request(input, true)
	mu.Lock()
	defer mu.Unlock()
	if scenario == "recover" || scenario == "fresh_intent" || scenario == "capacity_latest" || scenario == "two_known_tasks" || scenario == "privacy" {
		require.Len(t, bodies, producerRequests+1, "FUTURE_SHADOW_B_DISPATCH_MISSING: committed same-task context must reach B without manual input; response=%s", answer)
		require.Equal(t, http.StatusOK, status, answer)
		completed := issue758CompletedResponse(t, answer, stream)
		require.Contains(t, completed.Raw, "shadow-task-complete")
		require.NotContains(t, completed.Raw, `"type":"function_call"`)
		require.Equal(t, "Bearer fixture-shadow-B", identities[len(identities)-1])
		for _, identity := range identities[:producerRequests] {
			require.Equal(t, "Bearer fixture-shadow-A", identity)
		}
		wires := transport.snapshot()
		require.Len(t, wires, len(bodies))
		for i, wire := range wires {
			require.JSONEq(t, string(wire.body), string(bodies[i]), "native final-send payload must reach the network unchanged")
			if accountType == service.AccountTypeOAuth {
				if wire.account == 2 {
					require.NotEmpty(t, wire.header.Get("session_id"), "rebuilt B send must carry its isolated session")
				}
				require.Equal(t, wire.header.Get("session_id"), providerHeaders[i].Get("session_id"), "isolated session must reach the actual provider")
				require.NotEqual(t, "fixture-shadow-thread", wire.header.Get("session_id"), "raw client session must not be forwarded")
			}
		}
		if accountType == service.AccountTypeOAuth {
			last := wires[len(wires)-1]
			require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", last.url)
			require.Equal(t, int64(2), last.account)
			require.Equal(t, "fixture-shadow-account-B", last.header.Get("ChatGPT-Account-ID"))
			require.NotEmpty(t, last.header.Get("session_id"))
			require.NotEqual(t, wires[0].header.Get("session_id"), last.header.Get("session_id"), "B must receive a new upstream account namespace")
		}
		require.Equal(t, 1, tool.patches, "completed side effect was reused, never executed again")
		if scenario == "recover" {
			require.NotEmpty(t, shadowKeys, "cross-node recovery must use the shared store")
			for _, key := range shadowKeys {
				ttl, err := probe.PTTL(context.Background(), key).Result()
				require.NoError(t, err)
				require.Greater(t, ttl, time.Duration(0))
				require.LessOrEqual(t, ttl, 30*time.Minute, "shadow retention must be bounded")
				value, err := probe.Get(context.Background(), key).Result()
				require.NoError(t, err)
				require.LessOrEqual(t, len(value), 64*1024, "each shadow record must be bounded")
			}
		}
	} else {
		require.GreaterOrEqual(t, status, 400, answer)
		require.NotEmpty(t, gjson.Get(answer, "error.message").String(), answer)
		require.Len(t, bodies, producerRequests, "scope/expiry/delivery must refuse before any B send")
		require.NotContains(t, answer, "shadow-task-complete")
		if scenario == "write_failed" || scenario == "off" || scenario == "store_write_failed" || scenario == "oversize" || scenario == "pending_tool" || scenario == "pending_output" {
			require.Empty(t, shadowKeys, "unconfirmed/disabled capture must not persist task plaintext")
		}
	}
	if scenario == "privacy" {
		require.True(t, auditProbe.evaluated.Load(), "restored text must reach the actual HTTP moderation stage")
		require.True(t, auditProbe.private.Load(), "SHADOW_AUDIT_PROVENANCE_MISSING: restored plaintext must be evaluated without persistent body retention")
	}
	if scenario == "store_write_failed" || scenario == "store_read_failed" {
		require.Positive(t, storeFailure.calls.Load(), "must inject a fault in the actual shared shadow data plane")
	}
	require.Equal(t, owners, issue805Owners(t, probe), "shadow lookup must not clear or rewrite producer/delivery records")
}
