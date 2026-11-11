//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// JSON deliberately ignores the future field on the pinned production RED.
// Both versions compile and execute the same network/business assertions.
func issue805Policy(t *testing.T, cfg *config.Config, disabled bool) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"DisableEncryptedAgentOwnerBinding":%t}`, disabled)), &cfg.Gateway))
}

// Never use miniredis for ownership lifecycle evidence. Each fixture has its
// own real Redis process/socket, with persistence disabled and no TCP listener.
func issue805Redis(t *testing.T) func() *redis.Client {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("Issue805 requires real redis-server in CI")
		}
		t.Skip("requires isolated real redis-server")
	}
	dir, err := os.MkdirTemp("", "s2-805-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "r.sock")
	//nolint:gosec // Fixed executable, isolated fixture arguments only.
	process := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	process.Stdout, process.Stderr = io.Discard, io.Discard
	require.NoError(t, process.Start())
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	newClient := func() *redis.Client {
		client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1})
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	probe := newClient()
	require.Eventually(t, func() bool { return probe.Ping(context.Background()).Err() == nil }, 3*time.Second, 10*time.Millisecond)
	info, err := probe.Info(context.Background(), "server").Result()
	require.NoError(t, err)
	require.Contains(t, info, "redis_version:")
	t.Log("production ownership Lua, isolated real Redis, independent node clients")
	return newClient
}

func issue805Owners(t *testing.T, client *redis.Client) map[string]string {
	t.Helper()
	keys, err := client.Keys(context.Background(), "openai_agent_owner:*").Result()
	require.NoError(t, err)
	result := make(map[string]string)
	for _, key := range keys {
		result[key], err = client.Get(context.Background(), key).Result()
		require.NoError(t, err)
	}
	return result
}

func TestIssue805HTTPCompletedHistorySelectsAlternative(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			clients := issue805Redis(t)
			var mu sync.Mutex
			var ids []string
			var bodies [][]byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				ids = append(ids, r.Header.Get("Authorization"))
				bodies = append(bodies, body)
				n := len(ids)
				mu.Unlock()
				output := "[" + issue715Agent + "]"
				if n > 1 {
					output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"existing ledger balance 18"}]}]`
				}
				response := fmt.Sprintf(`{"id":"resp_805_%d","status":"completed","error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, n, output)
				if gjson.GetBytes(body, "stream").Bool() {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
				}
			}))
			defer srv.Close()
			host, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
			require.NoError(t, err)
			require.Equal(t, "127.0.0.1", host)
			accounts := issue715Accounts()
			for i := range accounts {
				accounts[i].Type = service.AccountTypeAPIKey
				accounts[i].Credentials = map[string]any{"api_key": fmt.Sprintf("fixture-805-%d", i+1), "base_url": srv.URL}
			}
			newNode := func(disabled bool) *OpenAIGatewayHandler {
				cfg := &config.Config{}
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				h := newOpenAIResponsesFailoverTestHandlerWithCache(t, testutil.NewRealHTTPUpstream(cfg), repository.NewGatewayCache(clients()), accounts...)
				h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				issue805Policy(t, h.cfg, disabled)
				return h
			}
			first := issue758Request(t, newNode(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			require.JSONEq(t, issue715Agent, issue758CompletedResponse(t, first.Body.String(), stream).Get("output.0").Raw)
			probe := clients()
			original := issue805Owners(t, probe)
			require.Len(t, original, 1, "A must complete downstream delivery before the policy changes")
			reset := time.Now().Add(time.Hour)
			accounts[0].RateLimitResetAt = &reset
			strict := issue758Request(t, newNode(false), issue715Body(true, stream))
			require.Equal(t, http.StatusTooManyRequests, strict.Code, strict.Body.String())
			require.Contains(t, strict.Body.String(), "encrypted_agent_recovery_owner_rate_limited")
			followup := issue715Body(true, stream)
			answer := issue758Request(t, newNode(true), followup)
			require.Equal(t, http.StatusOK, answer.Code, "opt-out must dispatch complete A history to eligible B without clearing Redis: %s", answer.Body.String())
			require.Contains(t, answer.Body.String(), "existing ledger balance 18")
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []string{"Bearer fixture-805-1", "Bearer fixture-805-2"}, ids)
			require.JSONEq(t, gjson.Get(followup, "input").Raw, gjson.GetBytes(bodies[1], "input").Raw, "the real network send must preserve every original history/tool result item")
			require.Equal(t, original, issue805Owners(t, probe), "consuming A on B must not overwrite or poison A ownership")
		})
	}
}
