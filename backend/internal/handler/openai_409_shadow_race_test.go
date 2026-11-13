//go:build unit

package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type shadowRaceRedis struct {
	mode             string
	armed            atomic.Bool
	entered, release chan struct{}
	once             sync.Once
}

type shadowRaceRead struct {
	seen chan string
	once sync.Once
	hold <-chan struct{}
}

// Observe the actual handler body after RestoreOpenAIReadableShadow returns,
// before its ordinary admission/send path. Do not synthesize readable input or
// depend on a user-slot callback: this fixture's concurrency=0 skips that cache.
type shadowRestoredRequestBarrier struct {
	observed chan []byte
	release  <-chan struct{}
}

func (b *shadowRestoredRequestBarrier) Check(ctx context.Context, req securityaudit.Request) (*securityaudit.LegacyDecision, error) {
	select {
	case b.observed <- append([]byte(nil), req.Body...):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-b.release:
		return nil, nil // Preserve this fixture's existing allow decision.
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *shadowRaceRead) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *shadowRaceRead) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *shadowRaceRead) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "get" && len(cmd.Args()) > 1 {
			key, _ := cmd.Args()[1].(string)
			if strings.HasPrefix(key, "openai_agent_shadow_epoch:") || strings.HasPrefix(key, "openai_readable_shadow:") {
				state := "miss-or-record"
				if value, ok := cmd.(*redis.StringCmd); ok && strings.HasPrefix(value.Val(), "pending:") {
					state = "pending"
				}
				first := false
				h.once.Do(func() { first = true; h.seen <- state })
				if first && h.hold != nil {
					select {
					case <-h.hold:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
		return err
	}
}
func (h *shadowRaceRedis) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *shadowRaceRedis) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *shadowRaceRedis) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.armed.Load() && (cmd.Name() == "eval" || cmd.Name() == "evalsha") {
			shadow, publication := false, false
			for _, arg := range cmd.Args() {
				text, _ := arg.(string)
				shadow = shadow || strings.HasPrefix(text, "openai_readable_shadow:v1:")
				publication = publication || (strings.HasPrefix(text, "{") && strings.Contains(text, `"version":`))
			}
			if shadow {
				if h.mode == "store_failure_existing" {
					h.once.Do(func() { close(h.entered) })
					return errors.New("fixture optional shadow unavailable")
				}
				if publication && (h.mode == "first_publication" || h.mode == "delayed_publisher") {
					block := false
					h.once.Do(func() { block = true; close(h.entered) })
					if block {
						select {
						case <-h.release:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
				}
			}
		}
		return next(ctx, cmd)
	}
}

type shadowRaceWriter struct {
	gin.ResponseWriter
	before func()
}

func (w *shadowRaceWriter) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "fixture-shadow-current-task") && w.before != nil {
		w.before()
	}
	n, err := w.ResponseWriter.Write(body)
	w.ResponseWriter.Flush() // client can see a terminal while publication waits
	return n, err
}
func (w *shadowRaceWriter) WriteString(body string) (int, error) { return w.Write([]byte(body)) }

func TestOpenAI409FutureShadowPublicationFences(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"first_publication", "invalidate_after_restore", "reused_exposure", "store_failure_existing", "delayed_publisher"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, scenario), func(t *testing.T) { runShadowPublicationRace(t, stream, scenario) })
		}
	}
}

func runShadowPublicationRace(t *testing.T, stream bool, scenario string) {
	clients := issue805Redis(t)
	probe := clients()
	var mu sync.Mutex
	var identities []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		identities = append(identities, r.Header.Get("Authorization"))
		mu.Unlock()
		output := json.RawMessage(shadowHTTPAgent)
		if r.Header.Get("Authorization") == "Bearer fixture-race-B" {
			if !strings.Contains(string(body), "fixture task X") || strings.Contains(string(body), "fixture task Y") || strings.Contains(string(body), "fixture-shadow-current-task") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			output = json.RawMessage(httpRebuildJSON(map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "race-task-complete"}}}))
		}
		outputs := []json.RawMessage{output}
		if scenario == "delayed_publisher" && strings.Contains(string(body), "fixture task Y") {
			outputs = append(outputs, json.RawMessage(httpRebuildCall("run_tests", "call_unknown_side_effect", map[string]any{"path": "sum.go"})))
		}
		answer := httpRebuildJSON(map[string]any{"id": "resp_race", "status": "completed", "model": "gpt-5.1", "error": nil, "output": outputs, "usage": map[string]int{"input_tokens": 2, "output_tokens": 1}})
		if gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", answer)
		} else {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, answer)
		}
	}))
	defer provider.Close()
	hook := &shadowRaceRedis{mode: scenario, entered: make(chan struct{}), release: make(chan struct{})}
	readHook := &shadowRaceRead{seen: make(chan string, 1)}
	consumerRelease := make(chan struct{})
	var consumerOnce sync.Once
	releaseConsumer := func() { consumerOnce.Do(func() { close(consumerRelease) }) }
	defer releaseConsumer()
	if scenario == "reused_exposure" {
		readHook.hold = consumerRelease
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hook.release) }) }
	defer release()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	node := func(consumer bool) *OpenAIGatewayHandler {
		accounts := issue715AccountsOfType(service.AccountTypeAPIKey)
		for i, label := range []string{"A", "B"} {
			accounts[i].Credentials = map[string]any{"base_url": provider.URL, "api_key": "fixture-race-" + label}
		}
		// The second node observes A's administrative disablement; the producer
		// is already completing an admitted generation when barriers are held.
		accounts[0].Schedulable = !consumer
		client := clients()
		if !consumer {
			client.AddHook(hook)
		} else {
			client.AddHook(readHook)
		}
		h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, testutil.NewRealHTTPUpstream(cfg), repository.NewGatewayCache(client), service.NewSettingService(&omit409SettingsRepo{enabled: true}, cfg), &readable409UsageRepo{}, accounts...)
		h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		return h
	}
	producer, consumer := node(false), node(true)
	restored, continueSend := make(chan []byte, 1), make(chan struct{})
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(continueSend) }) }
	defer resume()
	if scenario == "invalidate_after_restore" {
		consumer.securityAuditCoordinator = securityaudit.NewCoordinator(&shadowRestoredRequestBarrier{
			observed: restored, release: continueSend,
		}, nil)
	}
	var exposeY atomic.Bool
	yEntered, yRelease := make(chan struct{}), make(chan struct{})
	var yOnce, yReleaseOnce sync.Once
	releaseY := func() { yReleaseOnce.Do(func() { close(yRelease) }) }
	defer releaseY()
	template, _ := newIssue715Context(t, "{}")
	producerCompleted := make(chan struct{}, 2)
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		for key, value := range template.Keys {
			c.Set(key, value)
		}
		if c.GetHeader("X-Fixture-Node") == "consumer" {
			consumer.Responses(c)
			return
		}
		c.Writer = &shadowRaceWriter{ResponseWriter: c.Writer, before: func() {
			if exposeY.Load() {
				yOnce.Do(func() { close(yEntered) })
				<-yRelease
			}
		}}
		producer.Responses(c)
		if scenario == "invalidate_after_restore" {
			producerCompleted <- struct{}{} // publication has finished, not just the client write
		}
	})
	gateway := httptest.NewServer(router)
	defer func() { release(); resume(); releaseY(); releaseConsumer(); gateway.Close() }()
	client := gateway.Client()
	client.Timeout = 10 * time.Second
	type result struct {
		status int
		body   string
		err    error
	}
	request := func(input string, consume bool, visible chan<- struct{}) result {
		body := httpRebuildJSON(map[string]any{"model": "gpt-5.1", "stream": stream, "tools": httpRebuildToolDefinitions(), "input": json.RawMessage("[" + input + "]")})
		req, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/responses", strings.NewReader(body))
		if err != nil {
			return result{err: err}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("session_id", "fixture-race-session")
		if consume {
			req.Header.Set("X-Fixture-Node", "consumer")
		}
		resp, err := client.Do(req)
		if err != nil {
			return result{err: err}
		}
		defer resp.Body.Close()
		var answer string
		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			reader := bufio.NewReader(resp.Body)
			for {
				line, e := reader.ReadString('\n')
				answer += line
				if strings.Contains(line, `"type":"response.completed"`) {
					if visible != nil {
						close(visible)
						visible = nil
					}
				}
				if e != nil {
					if e != io.EOF {
						return result{err: e}
					}
					break
				}
			}
		} else {
			var value json.RawMessage
			if err = json.NewDecoder(resp.Body).Decode(&value); err != nil {
				return result{err: err}
			}
			answer = string(value)
			if visible != nil && gjson.Get(answer, "status").String() == "completed" {
				close(visible)
			}
			_, err = io.Copy(io.Discard, resp.Body)
		}
		return result{status: resp.StatusCode, body: answer, err: err}
	}
	x := httpRebuildMessage("user", "fixture task X: report saved work without repeating it")
	y := httpRebuildMessage("user", "fixture task Y: a distinct task")
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("SHADOW_RACE_BARRIER_MISSING")
		}
	}
	consume := func() <-chan result {
		done := make(chan result, 1)
		go func() { done <- request(shadowHTTPAgent, true, nil) }()
		return done
	}
	if scenario == "first_publication" || scenario == "delayed_publisher" {
		hook.armed.Store(true)
		visible, produced := make(chan struct{}), make(chan result, 1)
		go func() { produced <- request(x, false, visible) }()
		await(visible)
		await(hook.entered)
		if scenario == "delayed_publisher" {
			second := request(y, false, nil)
			require.NoError(t, second.err)
			require.Equal(t, http.StatusOK, second.status, second.body)
			require.Contains(t, second.body, "call_unknown_side_effect")
			release()
			require.NoError(t, (<-produced).err)
			got := request(shadowHTTPAgent, true, nil)
			require.NoError(t, got.err)
			mu.Lock()
			sends := len(identities)
			mu.Unlock()
			require.Equal(t, 2, sends, "SHADOW_DELAYED_PUBLICATION_REVIVED: an earlier publisher cannot revive context after a newer unknown tool output; response=%s", got.body)
			require.GreaterOrEqual(t, got.status, 400, got.body)
			return
		}
		done := consume()
		select {
		case state := <-readHook.seen:
			if state != "pending" {
				early := <-done
				t.Fatalf("SHADOW_PUBLICATION_RACE_REFUSED: no shared publication wait after visible terminal: %+v", early)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("SHADOW_RACE_BARRIER_MISSING: consumer did not read shared state")
		}
		select {
		case early := <-done:
			t.Fatalf("SHADOW_PUBLICATION_RACE_REFUSED: consumer ended before successful publication: %+v", early)
		case <-time.After(50 * time.Millisecond):
		}
		release()
		p := <-produced
		require.NoError(t, p.err)
		require.Equal(t, http.StatusOK, p.status, p.body)
		got := <-done
		require.NoError(t, got.err)
		require.Equal(t, http.StatusOK, got.status, got.body)
		require.Contains(t, got.body, "race-task-complete")
		return
	}
	first := request(x, false, nil)
	require.NoError(t, first.err)
	require.Equal(t, http.StatusOK, first.status, first.body)
	if scenario == "invalidate_after_restore" {
		await(producerCompleted)
	}
	keys, err := probe.Keys(context.Background(), "openai_readable_shadow:v1:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	original := issue805Owners(t, probe)
	require.Len(t, original, 1)
	var done <-chan result
	if scenario == "invalidate_after_restore" {
		done = consume()
		select {
		case body := <-restored:
			// The HTTP client sent only the opaque item. Seeing X here proves
			// real cross-node recovery, not just a cache read or a slot attempt.
			require.JSONEq(t, "["+x+","+shadowHTTPAgent+"]", gjson.GetBytes(body, "input").Raw,
				"SHADOW_RESTORE_OBSERVATION_MISSING: actual audit input must contain the recovered X task and original opaque item")
		case early := <-done:
			t.Fatalf("SHADOW_RACE_BARRIER_MISSING: consumer ended before post-restore observation: %+v", early)
		case <-time.After(2 * time.Second):
			t.Fatal("SHADOW_RACE_BARRIER_MISSING: post-restore audit stage was not reached")
		}
		mu.Lock()
		heldIdentities := append([]string(nil), identities...)
		mu.Unlock()
		require.Equal(t, []string{"Bearer fixture-race-A"}, heldIdentities,
			"consumer must be held after restoration and before any B send")
	}
	if scenario == "store_failure_existing" {
		hook.armed.Store(true)
	}
	if scenario == "reused_exposure" {
		exposeY.Store(true)
		produced := make(chan result, 1)
		go func() { produced <- request(y, false, nil) }()
		await(yEntered)
		done = consume()
		var state string
		select {
		case state = <-readHook.seen:
		case <-time.After(2 * time.Second):
			t.Fatal("SHADOW_RACE_BARRIER_MISSING: consumer never read the held association")
		}
		mu.Lock()
		heldSends := len(identities)
		mu.Unlock()
		require.Equal(t, 2, heldSends, "SHADOW_REUSED_EXPOSURE_LEAK: B was sent while the consumer read and Y exposure were held")
		if state != "pending" {
			// e593 reads old X here. Keep Y held until that exact consumer
			// finishes, so its unsafe B send cannot be hidden by invalidation.
			releaseConsumer()
			early := <-done
			require.NoError(t, early.err)
			mu.Lock()
			earlySends := len(identities)
			mu.Unlock()
			require.Equal(t, 2, earlySends, "SHADOW_REUSED_EXPOSURE_LEAK: consumer used stale X before Y's actual downstream write; response=%s", early.body)
			t.Fatal("SHADOW_REUSED_EXPOSURE_LEAK: reused association was not pending at its observed read")
		}
		releaseY()
		require.NoError(t, (<-produced).err)
		releaseConsumer()
	} else {
		second := request(y, false, nil)
		require.NoError(t, second.err)
		require.Equal(t, http.StatusOK, second.status, second.body)
	}
	if scenario == "store_failure_existing" {
		await(hook.entered)
	}
	if scenario == "invalidate_after_restore" {
		await(producerCompleted)
		invalidated, err := probe.Get(context.Background(), keys[0]).Result()
		require.NoError(t, err)
		require.Equal(t, "conflict", invalidated, "Y must invalidate the actual shared association before consumer release")
		require.Equal(t, original, issue805Owners(t, probe), "Y invalidation must preserve original owner bytes")
		mu.Lock()
		heldIdentities := append([]string(nil), identities...)
		mu.Unlock()
		require.Equal(t, []string{"Bearer fixture-race-A", "Bearer fixture-race-A"}, heldIdentities,
			"only X and Y may reach the provider while the consumer is held")
	}
	resume()
	if done == nil {
		done = consume()
	}
	got := <-done
	require.NoError(t, got.err)
	mu.Lock()
	sends := len(identities)
	mu.Unlock()
	diagnostic := "SHADOW_INVALIDATED_ASSOCIATION_DISPATCH"
	if scenario == "reused_exposure" {
		diagnostic = "SHADOW_REUSED_EXPOSURE_LEAK"
	}
	require.Equal(t, 2, sends, "%s: stale X must not reach B after Y invalidates the same opaque identity; response=%s", diagnostic, got.body)
	require.GreaterOrEqual(t, got.status, 400, got.body)
	require.Equal(t, original, issue805Owners(t, probe), "association changes must not rewrite original owner routes")
}
