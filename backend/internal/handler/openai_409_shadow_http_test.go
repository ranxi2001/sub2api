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
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const shadowHTTPAgent = `{"type":"agent_message","author":"/root","recipient":"/root/worker","encrypted_content":"fixture-shadow-current-task"}`

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

func TestOpenAI409FutureShadowBoundaries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, boundary := range []string{"other_user", "other_key", "other_group", "other_model", "other_session", "other_task", "expired", "write_failed", "pending_owner", "failed_owner", "conflicted_owner", "off", "fresh_intent"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, boundary), func(t *testing.T) {
				runFutureShadowHTTP(t, stream, boundary)
			})
		}
	}
}

func runFutureShadowHTTP(t *testing.T, stream bool, scenario string) {
	t.Helper()
	clients := issue805Redis(t)
	probe := clients()
	var mu sync.Mutex
	var bodies [][]byte
	var identities []string
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
		n := len(bodies)
		mu.Unlock()
		output := []json.RawMessage{json.RawMessage(patch)}
		switch n {
		case 2:
			if !httpRebuildToolPair(body, "apply_patch", httpRebuildPatch) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture requires the actual completed patch"}}`)
				return
			}
			output = []json.RawMessage{json.RawMessage(shadowHTTPAgent)}
		case 3:
			valid := r.Header.Get("Authorization") == "Bearer fixture-shadow-B" &&
				!strings.Contains(string(body), "fixture-shadow-current-task") &&
				!gjson.GetBytes(body, "previous_response_id").Exists() && !gjson.GetBytes(body, "conversation").Exists() &&
				httpRebuildToolsAvailable(body)
			if scenario == "fresh_intent" {
				valid = valid && strings.Contains(string(body), "Report only the supplied fresh task") && !strings.Contains(string(body), httpRebuildTask)
			} else {
				valid = valid && strings.Contains(string(body), httpRebuildTask) && httpRebuildToolPair(body, "apply_patch", httpRebuildPatch)
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
	accounts := issue715AccountsOfType(service.AccountTypeAPIKey)
	for i, label := range []string{"A", "B"} {
		accounts[i].Credentials = map[string]any{"base_url": provider.URL, "api_key": "fixture-shadow-" + label}
	}
	newNode := func() *OpenAIGatewayHandler {
		cfg := &config.Config{RunMode: config.RunModeSimple}
		cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		settings := service.NewSettingService(&omit409SettingsRepo{enabled: scenario != "off"}, cfg)
		h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, testutil.NewRealHTTPUpstream(cfg), repository.NewGatewayCache(clients()), settings, &readable409UsageRepo{}, accounts...)
		h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		return h
	}
	producer, consumer := newNode(), newNode() // independent service state/Redis clients
	done := make(chan struct{}, 1)
	var beforeDelivery []string
	var captureErr error
	deliveryObserved := false
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		defer func() { done <- struct{}{} }()
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
	request := func(input []string, consume bool) (int, string) {
		model, session := "gpt-5.1", "fixture-shadow-thread"
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
	history := []string{httpRebuildMessage("user", httpRebuildTask)}
	status, answer := request(history, false)
	require.Equal(t, http.StatusOK, status, answer)
	first := issue758CompletedResponse(t, answer, stream)
	tool := &httpRebuildTools{}
	for _, item := range first.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tool.execute(t, first)...)
	require.Equal(t, 1, tool.patches)
	status, answer = request(history, false)
	require.True(t, deliveryObserved, "must reach the actual opaque-output downstream write")
	require.NoError(t, captureErr)
	require.Empty(t, beforeDelivery, "future task context is committed only after successful terminal delivery")
	if scenario != "write_failed" {
		require.Equal(t, http.StatusOK, status, answer)
		second := issue758CompletedResponse(t, answer, stream)
		require.Contains(t, second.Get("output").Raw, "fixture-shadow-current-task")
	}
	owners := issue805Owners(t, probe)
	require.Len(t, owners, 1)
	accounts[0].Schedulable = false
	shadowKeys, err := probe.Keys(context.Background(), "openai_readable_shadow:v1:*").Result()
	require.NoError(t, err)
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
	opaque := shadowHTTPAgent
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
	if scenario == "recover" || scenario == "fresh_intent" {
		require.Len(t, bodies, 3, "FUTURE_SHADOW_B_DISPATCH_MISSING: committed same-task context must reach B without manual input; response=%s", answer)
		require.Equal(t, http.StatusOK, status, answer)
		completed := issue758CompletedResponse(t, answer, stream)
		require.Contains(t, completed.Raw, "shadow-task-complete")
		require.NotContains(t, completed.Raw, `"type":"function_call"`)
		require.Equal(t, []string{"Bearer fixture-shadow-A", "Bearer fixture-shadow-A", "Bearer fixture-shadow-B"}, identities)
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
		require.Len(t, bodies, 2, "scope/expiry/delivery must refuse before any B send")
		require.NotContains(t, answer, "shadow-task-complete")
		if scenario == "write_failed" || scenario == "off" {
			require.Empty(t, shadowKeys, "unconfirmed/disabled capture must not persist task plaintext")
		}
	}
	require.Equal(t, owners, issue805Owners(t, probe), "shadow lookup must not clear or rewrite producer/delivery records")
}
