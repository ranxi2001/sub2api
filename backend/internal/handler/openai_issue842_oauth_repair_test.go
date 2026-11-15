//go:build unit

package handler

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Independent oracle: OAuth stateless metadata is expected even when incoming
// store is omitted/true; API-key store:true must keep its provider reasoning ID.
func issue842StatelessHistory(t *testing.T, kind, store, summary string, stream bool) (string, string) {
	t.Helper()
	body, _ := issue842History(t, stream, "reasoning_id")
	var err error
	if store == "omitted" {
		body, err = sjson.Delete(body, "store")
	} else {
		body, err = sjson.SetRaw(body, "store", store)
	}
	require.NoError(t, err)
	body, err = sjson.SetRaw(body, "input.5.content", `[{"type":"reasoning_text","text":"Retain readable OAuth reasoning842"}]`)
	require.NoError(t, err)
	switch summary {
	case "omitted":
		body, err = sjson.Delete(body, "input.5.summary")
	case "null":
		body, err = sjson.SetRaw(body, "input.5.summary", "null")
	case "nonempty":
		body, err = sjson.SetRaw(body, "input.5.summary", `[{"type":"summary_text","text":"Retain nonempty summary842"}]`)
	}
	require.NoError(t, err)
	body, err = sjson.SetRaw(body, "input.-1", `{"type":"reasoning","id":"rs_plaintext842","summary":null,"content":[{"type":"reasoning_text","text":"Retain plaintext reasoning842"}]}`)
	require.NoError(t, err)
	want := body
	if kind == service.AccountTypeOAuth || store == "false" {
		want, err = sjson.Delete(want, "input.5.id")
		require.NoError(t, err)
		if summary != "nonempty" {
			want, err = sjson.SetRaw(want, "input.5.summary", "[]")
			require.NoError(t, err)
		}
	}
	if kind == service.AccountTypeOAuth {
		want, err = sjson.Delete(want, "input.6.id")
		require.NoError(t, err)
		want, err = sjson.SetRaw(want, "input.6.summary", "[]")
		require.NoError(t, err)
	}
	return body, want
}

type issue842RepairTransport struct {
	service.HTTPUpstream
	target *url.URL
	mu     sync.Mutex
	paths  []string
}

func (u *issue842RepairTransport) Do(req *http.Request, proxy string, account int64, concurrency int) (*http.Response, error) {
	u.mu.Lock()
	u.paths = append(u.paths, req.URL.Path)
	u.mu.Unlock()
	if proxy != "" || (req.URL.Host != "chatgpt.com" && req.URL.Host != "bps.openai.com" && req.URL.Host != u.target.Host) {
		return nil, fmt.Errorf("Issue842 refuses unexpected fixture route")
	}
	local := req.Clone(req.Context())
	address := *req.URL
	address.Scheme, address.Host = u.target.Scheme, u.target.Host
	local.URL = &address
	return u.HTTPUpstream.Do(local, "", account, concurrency)
}

func (u *issue842RepairTransport) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, account, concurrency)
}

func TestIssue842OAuthStatelessHTTP(t *testing.T) {
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		for _, stream := range []bool{false, true} {
			for _, store := range []string{"omitted", "true", "false"} {
				for _, summary := range []string{"omitted", "null", "nonempty"} {
					t.Run(fmt.Sprintf("%s/stream=%t/store=%s/summary=%s", kind, stream, store, summary), func(t *testing.T) {
						clients := issue805Redis(t)
						u := &issue805Oracle{}
						srv := u.server(t)
						target, err := url.Parse(srv.URL)
						require.NoError(t, err)
						cfg := &config.Config{RunMode: config.RunModeSimple}
						cfg.Security.URLAllowlist.AllowInsecureHTTP = true
						transport := &issue842RepairTransport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
						accounts := issue715AccountsOfType(kind)
						for i := range accounts {
							accounts[i].Credentials = map[string]any{"api_key": []string{"fixture-ws-one", "fixture-ws-two"}[i], "access_token": []string{"fixture-ws-one", "fixture-ws-two"}[i], "base_url": srv.URL}
						}
						newNode := func(disabled bool) *OpenAIGatewayHandler {
							settings := service.NewSettingService(&issue842Settings{}, cfg)
							h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, transport, repository.NewGatewayCache(clients()), settings, accounts...)
							h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
							issue805Policy(t, h.cfg, disabled)
							return h
						}
						first := issue758Request(t, newNode(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
						require.Equal(t, http.StatusOK, first.Code, first.Body.String())
						issue758CompletedResponse(t, first.Body.String(), stream)
						original := issue805Owners(t, clients())
						require.Len(t, original, 1)
						accounts[0].Schedulable = false
						body, want := issue842StatelessHistory(t, kind, store, summary, stream)
						answer := issue758Request(t, newNode(true), body)
						require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
						issue758CompletedResponse(t, answer.Body.String(), stream)
						u.mu.Lock()
						defer u.mu.Unlock()
						require.Equal(t, []string{"Bearer fixture-ws-one", "Bearer fixture-ws-two"}, u.ids, "exactly one admitted B generation")
						require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(u.frames[1], "input").Raw, "Issue842 OAuth stateless input must match immutable-source metadata projection")
						if kind == service.AccountTypeOAuth {
							require.Equal(t, gjson.False, gjson.GetBytes(u.frames[1], "store").Type)
						}
						for key, raw := range original {
							require.Equal(t, raw, issue805Owners(t, clients())[key], "A ownership must stay unchanged")
						}
					})
				}
			}
		}
	}
}

func issue842OAuthWSNode(t *testing.T, mode, upstream string, cache service.GatewayCache, disabled, limited bool) *openAIWSPassthroughHandlerHarness {
	t.Helper()
	target, err := url.Parse(upstream)
	require.NoError(t, err)
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream,
		http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}},
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false"}, cache,
		func(key *service.APIKey) { key.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, httpUpstream *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID, a.Priority, a.Type = 1, 0, service.AccountTypeOAuth
			a.Credentials = map[string]any{"access_token": "fixture-ws-one", "chatgpt_account_id": "fixture842-A"}
			a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
			a.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			issue805Policy(t, cfg, disabled)
			require.Equal(t, mode, a.ResolveOpenAIResponsesWebSocketV2Mode(cfg.Gateway.OpenAIWS.IngressModeDefault))
			other := *a
			other.ID, other.Priority = 2, 1
			other.Credentials = map[string]any{"access_token": "fixture-ws-two", "chatgpt_account_id": "fixture842-B"}
			if limited {
				reset := time.Now().Add(time.Hour)
				a.RateLimitResetAt = &reset
			}
			*httpUpstream = &issue842RepairTransport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
			*repo = &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, other}}}
		})
	require.NoError(t, service.RouteIssue842OAuthSocketsToLoopbackForTest(h.handler.gatewayService, upstream))
	return h
}

func TestIssue842OAuthStatelessWS(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
		for _, store := range []string{"omitted", "true", "false"} {
			for _, summary := range []string{"omitted", "null", "nonempty"} {
				t.Run(fmt.Sprintf("%s/store=%s/summary=%s", mode, store, summary), func(t *testing.T) {
					clients := issue805Redis(t)
					u := &issue805Oracle{}
					srv := u.server(t)
					producer := issue842OAuthWSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), false, false)
					issue715WSSend(t, producer.clientConn, issue715WSCreate("", false))
					require.Contains(t, string(issue715WSRead(t, producer.clientConn)), "fixture-task-reference-715")
					probe := clients()
					require.Eventually(t, func() bool {
						owners := issue805Owners(t, probe)
						if len(owners) != 1 {
							return false
						}
						for _, raw := range owners {
							if !issue805CompletedOwner(raw, 1) {
								return false
							}
						}
						return true
					}, time.Second, 5*time.Millisecond)
					original := issue805Owners(t, probe)
					consumer := issue842OAuthWSNode(t, mode, srv.URL, repository.NewGatewayCache(clients()), true, true)
					body, want := issue842StatelessHistory(t, service.AccountTypeOAuth, store, summary, true)
					body, err := sjson.Set(body, "type", "response.create")
					require.NoError(t, err)
					body, err = sjson.Set(body, "client_metadata.thread_id", "fixture-ws715-thread")
					require.NoError(t, err)
					for turn := 0; turn < 2; turn++ {
						issue715WSSend(t, consumer.clientConn, body)
						answer := issue715WSRead(t, consumer.clientConn)
						require.Equal(t, "response.completed", gjson.GetBytes(answer, "type").String(), string(answer))
					}
					for _, node := range []*openAIWSPassthroughHandlerHarness{consumer, producer} {
						issue600Finish(t, node, http.Header{"Session_id": {"fixture-ws715-session"}, "Thread-Id": {"fixture-ws715-thread"}})
					}
					u.mu.Lock()
					defer u.mu.Unlock()
					require.Equal(t, []string{"Bearer fixture-ws-one", "Bearer fixture-ws-two", "Bearer fixture-ws-two"}, u.ids)
					for _, frame := range u.frames[1:] {
						require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(frame, "input").Raw, "Issue842 OAuth WS stateless input must match immutable-source metadata projection")
					}
					if mode == service.OpenAIWSIngressModeHTTPBridge {
						require.Equal(t, 3, u.http)
						require.Zero(t, u.ws)
					} else {
						require.Equal(t, 3, u.ws, "native OAuth first/later frames must use real sockets")
						require.Zero(t, u.http)
					}
					for key, raw := range original {
						require.Equal(t, raw, issue805Owners(t, probe)[key])
					}
				})
			}
		}
	}
}

func TestIssue842BPSSearchRefusesBeforeAnySend(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			clients := issue805Redis(t)
			u := &issue805Oracle{}
			srv := u.server(t)
			target, err := url.Parse(srv.URL)
			require.NoError(t, err)
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			transport := &issue842RepairTransport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
			accounts := issue715Accounts()
			for i := range accounts {
				accounts[i].Credentials = map[string]any{"access_token": []string{"fixture-ws-one", "fixture-ws-two"}[i]}
			}
			newNode := func(disabled bool) *OpenAIGatewayHandler {
				settings := service.NewSettingService(&issue842Settings{values: map[string]string{"excel_bps_enabled": "true"}}, cfg)
				h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, transport, repository.NewGatewayCache(clients()), settings, accounts...)
				h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				issue805Policy(t, h.cfg, disabled)
				return h
			}
			first := issue758Request(t, newNode(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
			require.Equal(t, http.StatusOK, first.Code, first.Body.String())
			original := issue805Owners(t, clients())
			require.Len(t, original, 1)
			accounts[0].Schedulable = false
			accounts[1].Extra = map[string]any{"openai_excel_bps": true, "openai_excel_bps_web_search": true, "openai_excel_bps_web_search_auto": true}
			body, _ := issue842StatelessHistory(t, service.AccountTypeOAuth, "omitted", "null", stream)
			body, err = sjson.SetRaw(body, "tools", `[{"type":"web_search","external_web_access":true}]`)
			require.NoError(t, err)
			require.True(t, accounts[1].IsExcelBPSEnabledForModel(gjson.Get(body, "model").String()))
			answer := issue758Request(t, newNode(true), body)
			require.Equal(t, http.StatusConflict, answer.Code, answer.Body.String())
			require.Contains(t, answer.Body.String(), "encrypted_agent_recovery_route_changed")
			transport.mu.Lock()
			defer transport.mu.Unlock()
			require.Len(t, transport.paths, 1, "BPS generation/search/continuation must all have zero sends after A delivery")
			u.mu.Lock()
			defer u.mu.Unlock()
			require.Len(t, u.frames, 1)
			require.Equal(t, original, issue805Owners(t, clients()))
		})
	}
}
