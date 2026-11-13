//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type omit409SettingsRepo struct {
	service.SettingRepository
	enabled bool
}

func (s *omit409SettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled {
		return fmt.Sprint(s.enabled), nil
	}
	return "", service.ErrSettingNotFound
}

func (s *omit409SettingsRepo) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	return map[string]string{service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: fmt.Sprint(s.enabled)}, nil
}

func TestOpenAI409OmitHistoryHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"off", "paused", "route_changed", "no_accounts", "current_task", "upstream_rejects"} {
			t.Run(fmt.Sprintf("%s/stream=%t", scenario, stream), func(t *testing.T) {
				clients := issue805Redis(t)
				var mu sync.Mutex
				var bodies [][]byte
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					bodies = append(bodies, body)
					n := len(bodies)
					mu.Unlock()
					if n > 1 && scenario == "upstream_rejects" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"fixture rejects sanitized history"}}`)
						return
					}
					output := "[" + issue715Agent + "]"
					if n > 1 {
						output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued from visible history"}]}]`
					}
					payload := fmt.Sprintf(`{"id":"resp_omit_%d","status":"completed","error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, n, output)
					if gjson.GetBytes(body, "stream").Bool() {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", payload)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, payload)
					}
				}))
				defer srv.Close()
				accounts := issue715Accounts()
				for i := range accounts {
					accounts[i].Type = service.AccountTypeAPIKey
					accounts[i].Credentials = map[string]any{"api_key": fmt.Sprintf("fixture-omit-%d", i), "base_url": srv.URL}
				}
				newNode := func(enabled bool) *OpenAIGatewayHandler {
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					settings := service.NewSettingService(&omit409SettingsRepo{enabled: enabled}, cfg)
					h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, testutil.NewRealHTTPUpstream(cfg), repository.NewGatewayCache(clients()), settings, accounts...)
					h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					return h
				}
				first := issue758Request(t, newNode(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				originalOwners := issue805Owners(t, clients())
				require.Len(t, originalOwners, 1)
				accounts[0].Schedulable = false
				if scenario == "route_changed" {
					accounts[0].Schedulable = true
					accounts[0].Credentials["api_key"] = "fixture-rotated"
				}
				if scenario == "no_accounts" {
					accounts[1].Schedulable = false
				}
				followup := issue715Body(true, stream)
				if scenario == "current_task" {
					followup = fmt.Sprintf(`{"model":"gpt-5.1","stream":%t,"input":[%s]}`, stream, issue715Agent)
				}
				answer := issue758Request(t, newNode(scenario != "off"), followup)
				mu.Lock()
				defer mu.Unlock()
				switch scenario {
				case "off", "current_task":
					require.Equal(t, http.StatusConflict, answer.Code, answer.Body.String())
					require.Len(t, bodies, 1)
				case "no_accounts":
					require.GreaterOrEqual(t, answer.Code, 400)
					require.Len(t, bodies, 1)
				default:
					require.Len(t, bodies, 2, "one original request and exactly one sanitized continuation")
					require.NotContains(t, string(bodies[1]), "fixture-task-reference-715")
					require.Contains(t, string(bodies[1]), "Historical encrypted content omitted")
					for _, path := range []string{"input.1", "input.2", "input.3", "input.4"} {
						require.JSONEq(t, gjson.Get(followup, path).Raw, gjson.GetBytes(bodies[1], path).Raw, "readable history and tool results survive")
					}
					if scenario == "upstream_rejects" {
						require.GreaterOrEqual(t, answer.Code, 400)
					} else {
						require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
						require.Contains(t, answer.Body.String(), "continued from visible history")
					}
				}
				require.Equal(t, originalOwners, issue805Owners(t, clients()), "omission must not clear or rewrite the original owner")
			})
		}
	}
}

// This goes through actual handler selection, shared Redis ownership and a
// localhost HTTP provider. The provider validates the replay contract instead
// of accepting an arbitrary rewritten body as success.
func TestOpenAI409ReplayMetadataHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"reasoning_id", "reasoning_status", "reasoning_summary", "message_id", "message_call_id", "upstream_rejects"} {
			t.Run(fmt.Sprintf("%s/stream=%t", scenario, stream), func(t *testing.T) {
				clients := issue805Redis(t)
				var mu sync.Mutex
				var bodies [][]byte
				followup := issue715Body(true, stream)
				var err error
				followup, err = sjson.Set(followup, "store", false)
				require.NoError(t, err)
				// Exact integer and tool-result content are not replay metadata.
				followup, err = sjson.SetRaw(followup, "input.1.sequence", "9007199254740993")
				require.NoError(t, err)
				reasoning := `{"type":"reasoning","encrypted_content":"fixture-reasoning-823","summary":[]}`
				switch scenario {
				case "reasoning_id", "upstream_rejects":
					reasoning, err = sjson.Set(reasoning, "id", "rs_fixture_823")
				case "reasoning_status":
					reasoning, err = sjson.Set(reasoning, "status", "completed")
				case "reasoning_summary":
					reasoning, err = sjson.Delete(reasoning, "summary")
				case "message_id":
					followup, err = sjson.Set(followup, "input.1.id", "provider-item-823")
				case "message_call_id":
					followup, err = sjson.Set(followup, "input.1.call_id", "output-only-call-id")
				}
				require.NoError(t, err)
				followup, err = sjson.SetRaw(followup, "input.-1", reasoning)
				require.NoError(t, err)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, readErr := io.ReadAll(r.Body)
					if readErr != nil {
						t.Error(readErr)
						return
					}
					mu.Lock()
					bodies = append(bodies, body)
					n := len(bodies)
					mu.Unlock()
					if n > 1 {
						rule := gjson.GetBytes(body, "input.5")
						valid := !rule.Get("id").Exists() && !rule.Get("status").Exists() && rule.Get("summary").IsArray() &&
							!gjson.GetBytes(body, "input.1.id").Exists() && !gjson.GetBytes(body, "input.1.call_id").Exists() &&
							gjson.GetBytes(body, "input.1.sequence").Raw == "9007199254740993" &&
							strings.Contains(string(body), "Historical encrypted content omitted") &&
							!strings.Contains(string(body), "fixture-task-reference-715")
						if !valid || scenario == "upstream_rejects" {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusBadRequest)
							_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"fixture replay metadata rejected"}}`)
							return
						}
					}
					output := "[" + issue715Agent + "]"
					if n > 1 {
						output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued once"}]}]`
					}
					payload := fmt.Sprintf(`{"id":"resp_823_%d","status":"completed","error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, n, output)
					if gjson.GetBytes(body, "stream").Bool() {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", payload)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, payload)
					}
				}))
				defer srv.Close()
				accounts := issue715AccountsOfType(service.AccountTypeAPIKey)
				for i := range accounts {
					accounts[i].Credentials["base_url"] = srv.URL
				}
				newNode := func(enabled bool) *OpenAIGatewayHandler {
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					settings := service.NewSettingService(&omit409SettingsRepo{enabled: enabled}, cfg)
					h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, testutil.NewRealHTTPUpstream(cfg), repository.NewGatewayCache(clients()), settings, accounts...)
					h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					return h
				}
				first := issue758Request(t, newNode(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				owners := issue805Owners(t, clients())
				require.Len(t, owners, 1)
				accounts[0].Schedulable = false
				answer := issue758Request(t, newNode(true), followup)
				mu.Lock()
				defer mu.Unlock()
				require.Len(t, bodies, 2, "one producer and exactly one sanitized continuation; response: %s", answer.Body.String())
				if scenario == "upstream_rejects" {
					require.Equal(t, http.StatusBadRequest, answer.Code, answer.Body.String())
					require.Contains(t, answer.Body.String(), "fixture replay metadata rejected")
				} else {
					require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
					issue758CompletedResponse(t, answer.Body.String(), stream)
				}
				for _, path := range []string{"input.1.content", "input.1.sequence", "input.2", "input.3", "input.4", "input.5.encrypted_content"} {
					require.JSONEq(t, gjson.Get(followup, path).Raw, gjson.GetBytes(bodies[1], path).Raw, path)
				}
				require.Equal(t, owners, issue805Owners(t, clients()), "old ownership remains intact")
			})
		}
	}
}
