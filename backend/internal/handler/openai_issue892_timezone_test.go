//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func issue892Settings() map[string]string {
	return map[string]string{
		service.SettingKeyCyberSessionBlockEnabled:             "false",
		service.SettingKeyOpenAIRequestTimezoneEnabled:         "true",
		service.SettingKeyOpenAIRequestTimezoneAuto:            "false",
		service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "false",
		service.SettingKeyOpenAI400OmitEncryptedHistoryEnabled: "false",
	}
}

func issue892History(t *testing.T, stream bool, timezone string) (string, string) {
	t.Helper()
	body, want := issue842History(t, stream, "reasoning_content")
	const environment = "<environment_context>\n<cwd>/fixture</cwd>\n<timezone>Asia/Singapore</timezone>\n</environment_context>"
	var err error
	body, err = sjson.Set(body, "input.-1", map[string]any{"role": "user", "content": environment})
	require.NoError(t, err)
	want, err = sjson.Set(want, "input.-1", map[string]any{"role": "user", "content": strings.Replace(environment, "Asia/Singapore", timezone, 1)})
	require.NoError(t, err)
	return body, want
}

func TestIssue892HTTPManualTimezone(t *testing.T) {
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		for _, stream := range []bool{false, true} {
			for _, owner := range []string{"unknown", "same", "other"} {
				t.Run(fmt.Sprintf("%s/stream=%t/owner=%s", kind, stream, owner), func(t *testing.T) {
					clients := issue805Redis(t)
					u := &issue805Oracle{}
					srv := u.server(t)
					target, err := url.Parse(srv.URL)
					require.NoError(t, err)
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					transport := &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
					accounts := issue715AccountsOfType(kind)
					for i := range accounts {
						accounts[i].Credentials = map[string]any{"api_key": []string{"fixture-ws-one", "fixture-ws-two"}[i], "access_token": []string{"fixture-ws-one", "fixture-ws-two"}[i], "base_url": srv.URL}
						accounts[i].Extra = map[string]any{"openai_request_timezone": []string{"America/Los_Angeles", "Asia/Tokyo"}[i], "codex_fingerprint_mode": "off"}
					}
					accounts[1].Schedulable = false
					newNode := func(disabled bool) *OpenAIGatewayHandler {
						settings := service.NewSettingService(&issue842Settings{values: issue892Settings()}, cfg)
						h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, transport, repository.NewGatewayCache(clients()), settings, accounts...)
						h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
						issue805Policy(t, h.cfg, disabled)
						return h
					}
					offset := 0
					if owner != "unknown" {
						first := issue758Request(t, newNode(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
						require.Equal(t, http.StatusOK, first.Code, first.Body.String())
						issue758CompletedResponse(t, first.Body.String(), stream)
						offset = 1
					}
					original := issue805Owners(t, clients())
					require.Len(t, original, offset, "completed ownership must precede the opt-out request")
					for _, raw := range original {
						require.True(t, issue805CompletedOwner(raw, 1), "the same-account regression requires a completed producer on A")
					}
					selected, timezone := "Bearer fixture-ws-one", "America/Los_Angeles"
					if owner == "other" {
						accounts[0].Schedulable = false
						accounts[1].Schedulable = true
						selected, timezone = "Bearer fixture-ws-two", "Asia/Tokyo"
					}
					body, want := issue892History(t, stream, timezone)
					answer := issue758Request(t, newNode(true), body)
					require.Equal(t, http.StatusOK, answer.Code, "Issue892 manual timezone full history must reach selected account once: %s", answer.Body.String())
					issue758CompletedResponse(t, answer.Body.String(), stream)
					u.mu.Lock()
					defer u.mu.Unlock()
					require.Len(t, u.frames, offset+1, "no omission, retry, warmup or tool-result replay")
					require.Equal(t, selected, u.ids[offset])
					require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(u.frames[offset], "input").Raw)
					require.Equal(t, "9007199254740993", gjson.GetBytes(u.frames[offset], "input.1.sequence").Raw)
					for key, raw := range original {
						require.Equal(t, raw, issue805Owners(t, clients())[key], "consumption must preserve the completed producer")
					}
				})
			}
		}
	}
}

func issue892WSNode(t *testing.T, kind, mode, upstream string, cache service.GatewayCache, disabled, otherOwner bool) *openAIWSPassthroughHandlerHarness {
	t.Helper()
	target, err := url.Parse(upstream)
	require.NoError(t, err)
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream,
		http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}}, issue892Settings(), cache,
		func(key *service.APIKey) { key.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, httpUpstream *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID, a.Priority, a.Type = 1, 0, kind
			a.Credentials = map[string]any{"api_key": "fixture-ws-one", "access_token": "fixture-ws-one", "base_url": upstream, "chatgpt_account_id": "fixture892-A"}
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
			a.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
			a.Extra["openai_request_timezone"] = "America/Los_Angeles"
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			issue805Policy(t, cfg, disabled)
			other := *a
			other.ID, other.Priority = 2, 1
			other.Credentials = map[string]any{"api_key": "fixture-ws-two", "access_token": "fixture-ws-two", "base_url": upstream, "chatgpt_account_id": "fixture892-B"}
			other.Extra = make(map[string]any, len(a.Extra))
			for key, value := range a.Extra {
				other.Extra[key] = value
			}
			other.Extra["openai_request_timezone"] = "Asia/Tokyo"
			if otherOwner {
				reset := time.Now().Add(time.Hour)
				a.RateLimitResetAt = &reset
			}
			*httpUpstream = &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
			*repo = &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, other}}}
		})
	if kind == service.AccountTypeOAuth {
		require.NoError(t, service.RouteIssue842OAuthSocketsToLoopbackForTest(h.handler.gatewayService, upstream))
	}
	return h
}

func TestIssue892WSManualTimezone(t *testing.T) {
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
			for _, owner := range []string{"unknown", "same", "other"} {
				t.Run(kind+"/"+mode+"/owner="+owner, func(t *testing.T) {
					clients := issue805Redis(t)
					u := &issue805Oracle{}
					srv := u.server(t)
					offset := 0
					if owner != "unknown" {
						producer := issue892WSNode(t, kind, mode, srv.URL, repository.NewGatewayCache(clients()), false, false)
						issue715WSSend(t, producer.clientConn, issue715WSCreate("", false))
						require.Contains(t, string(issue715WSRead(t, producer.clientConn)), "fixture-task-reference-715")
						require.Eventually(t, func() bool {
							owners := issue805Owners(t, clients())
							for _, raw := range owners {
								if !issue805CompletedOwner(raw, 1) {
									return false
								}
							}
							return len(owners) == 1
						}, time.Second, 5*time.Millisecond)
						issue600Finish(t, producer, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
						offset = 1
					}
					original := issue805Owners(t, clients())
					consumer := issue892WSNode(t, kind, mode, srv.URL, repository.NewGatewayCache(clients()), true, owner == "other")
					selected, timezone := "Bearer fixture-ws-one", "America/Los_Angeles"
					if owner == "other" {
						selected, timezone = "Bearer fixture-ws-two", "Asia/Tokyo"
					}
					body, want := issue892History(t, true, timezone)
					body, err := sjson.Set(body, "type", "response.create")
					require.NoError(t, err)
					for turn := 0; turn < 2; turn++ {
						issue715WSSend(t, consumer.clientConn, body)
						ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
						_, answer, readErr := consumer.clientConn.Read(ctx)
						cancel()
						require.NotErrorIs(t, readErr, context.DeadlineExceeded)
						if readErr != nil {
							t.Logf("Issue892 WS refusal close_status=%d", coderws.CloseStatus(readErr))
						}
						require.NoError(t, readErr, "Issue892 WS manual timezone full history must reach selected account once")
						require.Equal(t, "response.completed", gjson.GetBytes(answer, "type").String(), "Issue892 WS manual timezone full history must reach selected account once: %s", answer)
					}
					issue600Finish(t, consumer, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
					u.mu.Lock()
					defer u.mu.Unlock()
					require.Len(t, u.frames, offset+2, "one generation per client turn")
					for i, frame := range u.frames[offset:] {
						require.Equal(t, selected, u.ids[offset+i])
						require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(frame, "input").Raw)
						require.Equal(t, "9007199254740993", gjson.GetBytes(frame, "input.1.sequence").Raw)
					}
					if mode == service.OpenAIWSIngressModeHTTPBridge {
						require.Equal(t, len(u.frames), u.http)
						require.Zero(t, u.ws)
					} else {
						require.Equal(t, len(u.frames), u.ws)
						require.Zero(t, u.http)
					}
					for key, raw := range original {
						require.Equal(t, raw, issue805Owners(t, clients())[key])
					}
				})
			}
		}
	}
}

type issue892LateFenceCache struct {
	service.GatewayCache
	issue715WSDeliveryCache
	client *redis.Client
	fence  string
	reads  atomic.Int64
}

func (c *issue892LateFenceCache) GetOpenAIAgentRecovery(ctx context.Context, key string) (string, error) {
	if c.reads.Add(1) == 2 {
		value := "failed"
		if c.fence == "pending" {
			value = `pending:fixture-late892:[1,1,"` + strings.Repeat("a", 64) + `","` + strings.Repeat("b", 64) + `"]`
		}
		if err := c.client.Set(ctx, "openai_agent_owner:v1:"+key, value, time.Minute).Err(); err != nil {
			return "", err
		}
	}
	return c.issue715WSDeliveryCache.GetOpenAIAgentRecovery(ctx, key)
}

func TestIssue892OpaqueEnvironmentAdapters(t *testing.T) {
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		for _, mode := range []string{"http", service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
			for _, scenario := range []string{"mismatch", "aligned", "pending", "failed"} {
				t.Run(kind+"/"+mode+"/"+scenario, func(t *testing.T) {
					clients := issue805Redis(t)
					baseCache := repository.NewGatewayCache(clients())
					var cache service.GatewayCache = baseCache
					if scenario == "pending" || scenario == "failed" {
						cache = &issue892LateFenceCache{GatewayCache: baseCache, issue715WSDeliveryCache: baseCache.(issue715WSDeliveryCache), client: clients(), fence: scenario}
					}
					u := &issue805Oracle{}
					srv := u.server(t)
					stream := mode != "http"
					body, want := issue892History(t, stream, "America/Los_Angeles")
					environment := strings.Replace(issue892EnvironmentForAdapter, "Asia/Singapore", "America/Los_Angeles", 1)
					if scenario == "mismatch" {
						environment = issue892EnvironmentForAdapter
					}
					var err error
					for _, target := range []*string{&body, &want} {
						*target, err = sjson.Set(*target, "input.0.role", "user")
						require.NoError(t, err)
						*target, err = sjson.Set(*target, "input.0.content.0.text", environment)
						require.NoError(t, err)
					}
					if mode == "http" {
						target, parseErr := url.Parse(srv.URL)
						require.NoError(t, parseErr)
						cfg := &config.Config{RunMode: config.RunModeSimple}
						cfg.Security.URLAllowlist.AllowInsecureHTTP = true
						account := issue715AccountsOfType(kind)[0]
						account.Credentials = map[string]any{"api_key": "fixture-ws-one", "access_token": "fixture-ws-one", "base_url": srv.URL}
						account.Extra = map[string]any{"openai_request_timezone": "America/Los_Angeles", "codex_fingerprint_mode": "off"}
						transport := &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
						settings := service.NewSettingService(&issue842Settings{values: issue892Settings()}, cfg)
						h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, transport, cache, settings, account)
						h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
						issue805Policy(t, h.cfg, true)
						answer := issue758Request(t, h, body)
						if scenario == "aligned" {
							require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
							issue758CompletedResponse(t, answer.Body.String(), false)
						} else {
							require.Equal(t, http.StatusConflict, answer.Code, answer.Body.String())
							if scenario == "mismatch" {
								require.Contains(t, answer.Body.String(), "encrypted_agent_recovery_wire_changed")
							} else {
								require.Contains(t, []string{"encrypted_agent_recovery_owner_conflict", "encrypted_agent_recovery_owner_unavailable", "encrypted_agent_recovery_delivery_pending"}, gjson.Get(answer.Body.String(), "error.code").String())
							}
						}
					} else {
						h := issue892WSNode(t, kind, mode, srv.URL, cache, true, false)
						body, err = sjson.Set(body, "type", "response.create")
						require.NoError(t, err)
						issue715WSSend(t, h.clientConn, body)
						if scenario == "aligned" {
							require.Equal(t, "response.completed", gjson.GetBytes(issue715WSRead(t, h.clientConn), "type").String())
						} else {
							code := "encrypted_agent_recovery_wire_changed"
							if scenario == "pending" {
								code = "encrypted_agent_recovery_delivery_pending"
							} else if scenario == "failed" {
								code = "encrypted_agent_recovery_owner_unavailable"
							}
							issue805WSRefusal(t, h.clientConn, code)
						}
						issue600Finish(t, h, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
					}
					u.mu.Lock()
					defer u.mu.Unlock()
					if scenario == "aligned" {
						require.Len(t, u.frames, 1, "an intact first-seen opaque envelope reaches the selected adapter once")
						require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(u.frames[0], "input").Raw)
						require.JSONEq(t, gjson.Get(body, "input.0").Raw, gjson.GetBytes(u.frames[0], "input.0").Raw)
						require.Equal(t, "9007199254740993", gjson.GetBytes(u.frames[0], "input.1.sequence").Raw)
						require.Len(t, issue805Owners(t, clients()), 1, "the new opaque output must complete transactional delivery")
					} else {
						require.Empty(t, u.frames, "changed opaque envelopes and new delivery fences must stop before physical send")
					}
				})
			}
		}
	}
}

const issue892EnvironmentForAdapter = "<environment_context>\n<timezone>Asia/Singapore</timezone>\n</environment_context>"

// The four scenario classes reproduce the reported matrix dimensions using
// public synthetic inputs: two unknown opaque shapes, a completed same owner,
// and an explicit HTTP 400 on two historical encrypted reasoning items.
// Baseline: 40 successes, 8 raw 400s, 16 local timezone 409s. Candidate: the
// same 8 raw 400s remain; the 16 local timezone refusals must reach upstream.
func TestIssue892HTTP64SwitchMatrix(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		for _, beta := range []bool{false, true} {
			for _, recovery400 := range []bool{false, true} {
				for _, stream := range []bool{false, true} {
					for _, scenario := range []string{"unknown_top", "unknown_part", "same", "reasoning400"} {
						t.Run(fmt.Sprintf("optout=%t/beta=%t/recovery400=%t/stream=%t/%s", disabled, beta, recovery400, stream, scenario), func(t *testing.T) {
							clients := issue805Redis(t)
							var mu sync.Mutex
							var bodies [][]byte
							var identities []string
							srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								body, err := io.ReadAll(r.Body)
								if err != nil {
									t.Error(err)
									return
								}
								mu.Lock()
								bodies = append(bodies, body)
								identities = append(identities, r.Header.Get("Authorization"))
								n := len(bodies)
								mu.Unlock()
								w.Header().Set("Content-Type", "application/json")
								if scenario == "reasoning400" && strings.Contains(string(body), "AAAA-fixture-1111") {
									w.WriteHeader(http.StatusBadRequest)
									_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified."}}`)
									return
								}
								output := `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture matrix completed"}]}]`
								if scenario == "same" && n == 1 {
									output = "[" + issue715Agent + "]"
								}
								response := fmt.Sprintf(`{"id":"resp_matrix892_%d","status":"completed","error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, n, output)
								if gjson.GetBytes(body, "stream").Bool() {
									w.Header().Set("Content-Type", "text/event-stream")
									_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
								} else {
									_, _ = io.WriteString(w, response)
								}
							}))
							defer srv.Close()
							target, err := url.Parse(srv.URL)
							require.NoError(t, err)
							cfg := &config.Config{RunMode: config.RunModeSimple}
							cfg.Security.URLAllowlist.AllowInsecureHTTP = true
							account := issue715Accounts()[0]
							account.Credentials = map[string]any{"access_token": "fixture-matrix892", "base_url": srv.URL}
							account.Extra = map[string]any{"openai_request_timezone": "America/Los_Angeles", "codex_fingerprint_mode": "off"}
							newNode := func(optout, omission bool) *OpenAIGatewayHandler {
								values := issue892Settings()
								values[service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled] = fmt.Sprint(omission)
								values[service.SettingKeyOpenAI400OmitEncryptedHistoryEnabled] = fmt.Sprint(recovery400)
								settings := service.NewSettingService(&issue842Settings{beta: omission, values: values}, cfg)
								transport := &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
								h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, transport, repository.NewGatewayCache(clients()), settings, account)
								h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
								issue805Policy(t, h.cfg, optout)
								return h
							}
							offset := 0
							if scenario == "same" {
								first := issue758Request(t, newNode(false, false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
								require.Equal(t, http.StatusOK, first.Code, first.Body.String())
								issue758CompletedResponse(t, first.Body.String(), stream)
								offset = 1
							}
							original := issue805Owners(t, clients())
							require.Len(t, original, offset)
							body, want := issue892History(t, stream, "America/Los_Angeles")
							if scenario == "unknown_top" {
								for _, text := range []*string{&body, &want} {
									*text, err = sjson.SetRaw(*text, "input.0", `{"type":"agent_message","author":"/fixture","recipient":"/fixture/worker","encrypted_content":"fixture-matrix892-top"}`)
									require.NoError(t, err)
								}
							}
							if scenario == "reasoning400" {
								input := `[{"type":"reasoning","encrypted_content":"AAAA-fixture-1111","summary":[{"type":"summary_text","text":"visible reasoning one"}]},{"type":"reasoning","encrypted_content":"BBBB-fixture-2222","summary":[{"type":"summary_text","text":"visible reasoning two"}]},{"type":"function_call","name":"inspect","call_id":"call_fixture","arguments":"{\"sequence\":9007199254740993}"},{"type":"function_call_output","call_id":"call_fixture","output":"already completed"},{"role":"user","content":"Explain recorded result","sequence":9007199254740993}]`
								body = fmt.Sprintf(`{"model":"gpt-5.1","store":false,"stream":%t,"input":%s}`, stream, input)
								body, err = sjson.Set(body, "input.-1", map[string]any{"role": "user", "content": issue892EnvironmentForAdapter})
								require.NoError(t, err)
								want, err = sjson.Set(body, "input.5.content", strings.Replace(issue892EnvironmentForAdapter, "Asia/Singapore", "America/Los_Angeles", 1))
								require.NoError(t, err)
							}
							answer := issue758Request(t, newNode(disabled, beta), body)
							status, sends := http.StatusOK, 1
							if scenario == "reasoning400" {
								if recovery400 {
									sends = 2
								} else {
									status = http.StatusBadRequest
								}
							}
							require.Equal(t, status, answer.Code, "Issue892 64-case matrix must preserve switch semantics: %s", answer.Body.String())
							if status == http.StatusOK {
								issue758CompletedResponse(t, answer.Body.String(), stream)
							}
							mu.Lock()
							defer mu.Unlock()
							require.Len(t, bodies, offset+sends)
							for _, id := range identities {
								require.Equal(t, "Bearer fixture-matrix892", id)
							}
							if scenario == "reasoning400" {
								// Reasoning-only history follows the existing OAuth call-ID
								// normalization on both sends. Keep call/output association exact.
								for _, path := range []string{"input.2.call_id", "input.3.call_id"} {
									want, err = sjson.Set(want, path, "fc_fixture")
									require.NoError(t, err)
								}
								for _, payload := range bodies[offset:] {
									for _, path := range []string{"input.2", "input.3", "input.4", "input.5"} {
										require.JSONEq(t, gjson.Get(want, path).Raw, gjson.GetBytes(payload, path).Raw)
									}
									require.Equal(t, "9007199254740993", gjson.GetBytes(payload, "input.4.sequence").Raw)
								}
								if recovery400 {
									require.NotContains(t, string(bodies[offset+1]), "AAAA-fixture-1111")
									require.NotContains(t, string(bodies[offset+1]), "BBBB-fixture-2222")
								}
							} else if beta && scenario != "same" {
								require.NotContains(t, string(bodies[offset]), "fixture-task-reference-715")
								require.NotContains(t, string(bodies[offset]), "fixture-matrix892-top")
								require.Equal(t, "9007199254740993", gjson.GetBytes(bodies[offset], "input.1.sequence").Raw)
							} else {
								if !disabled {
									// Independent legacy strict-protocol oracle: the
									// existing normalizer removes reasoning.content.
									// Protected opt-out history must retain that text.
									want, err = sjson.Delete(want, "input.5.content")
									require.NoError(t, err)
								}
								require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(bodies[offset], "input").Raw)
								require.Equal(t, "9007199254740993", gjson.GetBytes(bodies[offset], "input.1.sequence").Raw)
							}
							owners := issue805Owners(t, clients())
							if !disabled && !beta && strings.HasPrefix(scenario, "unknown_") {
								require.Len(t, owners, 1, "strict mode retains its existing successful consumption learning")
								for _, raw := range owners {
									require.True(t, issue805CompletedOwner(raw, 1))
								}
							} else {
								require.Equal(t, original, owners, "opt-out and omission cannot republish consumed ownership")
							}
						})
					}
				}
			}
		}
	}
}
