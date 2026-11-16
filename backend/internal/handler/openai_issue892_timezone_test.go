//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
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
