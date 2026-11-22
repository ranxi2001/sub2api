//go:build unit

package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestEncryptedHistoryQuotaFailoverPreservesContinuation(t *testing.T) {
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", kind, stream), func(t *testing.T) {
				clients := issue805Redis(t)
				var mu sync.Mutex
				var identities []string
				var inputs []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					identities = append(identities, r.Header.Get("Authorization"))
					inputs = append(inputs, gjson.GetBytes(body, "input").Raw)
					attempt := len(identities)
					mu.Unlock()
					if attempt > 1 && r.Header.Get("Authorization") == "Bearer fixture-quota-1" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":4102444800}}`)
						return
					}
					output := "[" + issue715Agent + "]"
					if attempt > 1 {
						output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"existing ledger balance 18"}]}]`
					}
					response := fmt.Sprintf(`{"id":"resp_quota_%d","status":"completed","model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, attempt, output)
					if gjson.GetBytes(body, "stream").Bool() {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, response)
					}
				}))
				defer upstream.Close()
				target, err := url.Parse(upstream.URL)
				require.NoError(t, err)
				accounts := issue715Accounts()
				for i := range accounts {
					accounts[i].Type = kind
					accounts[i].Credentials = map[string]any{
						"api_key":      fmt.Sprintf("fixture-quota-%d", i+1),
						"access_token": fmt.Sprintf("fixture-quota-%d", i+1),
						"base_url":     upstream.URL,
					}
				}
				newNode := func() *OpenAIGatewayHandler {
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					transport := &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
					settings := service.NewSettingService(&issue842Settings{}, cfg)
					handler := newOpenAIResponsesFailoverTestHandlerWithSettings(t, transport, repository.NewGatewayCache(clients()), settings, accounts...)
					handler.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					issue805Policy(t, handler.cfg, true)
					return handler
				}
				first := issue758Request(t, newNode(), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				issue758CompletedResponse(t, first.Body.String(), stream)
				originalOwners := issue805Owners(t, clients())
				require.Len(t, originalOwners, 1)
				followup := issue715Body(true, stream)
				for request := 0; request < 2; request++ {
					answer := issue758Request(t, newNode(), followup)
					require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
					completed := issue758CompletedResponse(t, answer.Body.String(), stream)
					require.Contains(t, completed.Raw, "existing ledger balance 18")
					require.NotContains(t, answer.Body.String(), "response.failed")
				}
				require.Equal(t, originalOwners, issue805Owners(t, clients()), "quota rejection must preserve the completed producer records")
				mu.Lock()
				defer mu.Unlock()
				require.Equal(t, []string{"Bearer fixture-quota-1", "Bearer fixture-quota-1", "Bearer fixture-quota-2", "Bearer fixture-quota-2"}, identities)
				for _, input := range inputs[1:] {
					require.JSONEq(t, gjson.Get(followup, "input").Raw, input, "quota failover must retain the task and completed tool results")
				}
			})
		}
	}
}
