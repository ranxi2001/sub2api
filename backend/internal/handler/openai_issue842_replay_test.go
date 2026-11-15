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
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The real native OAuth builder has a fixed URL. Redirect only its final
// transport to loopback; never dial a provider or change its body/credentials.
type issue842Transport struct {
	service.HTTPUpstream
	target *url.URL
}

func (u *issue842Transport) Do(req *http.Request, proxy string, account int64, concurrency int) (*http.Response, error) {
	if proxy != "" || (req.URL.String() != "https://chatgpt.com/backend-api/codex/responses" && req.URL.String() != u.target.String()+"/v1/responses") {
		return nil, fmt.Errorf("Issue842 refuses non-fixture transport")
	}
	local := req.Clone(req.Context())
	address := *req.URL
	address.Scheme, address.Host = u.target.Scheme, u.target.Host
	local.URL = &address
	return u.HTTPUpstream.Do(local, "", account, concurrency)
}

func (u *issue842Transport) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, account, concurrency)
}

// This uses the existing setting's literal key so historical RED assemblies
// predating Beta can still compile these otherwise independent fixtures.
type issue842Settings struct {
	service.SettingRepository
	beta   bool
	values map[string]string
}

func (s *issue842Settings) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	if key == "openai_409_omit_encrypted_history_enabled" {
		return fmt.Sprint(s.beta), nil
	}
	return "", service.ErrSettingNotFound
}

func (s *issue842Settings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{"openai_409_omit_encrypted_history_enabled": fmt.Sprint(s.beta)}, nil
}

func issue842History(t *testing.T, stream bool, variant string) (string, string) {
	t.Helper()
	body := issue715Body(true, stream)
	var err error
	body, err = sjson.Set(body, "store", false)
	require.NoError(t, err)
	body, err = sjson.SetRaw(body, "input.1.sequence", "9007199254740993")
	require.NoError(t, err)
	reasoning := `{"type":"reasoning","encrypted_content":"fixture-842-reasoning-cipher","summary":[]}`
	switch variant {
	case "reasoning_status":
		reasoning, err = sjson.Set(reasoning, "status", "completed")
	case "reasoning_content":
		reasoning, err = sjson.SetRaw(reasoning, "content", `[{"type":"reasoning_text","text":"Retain this readable reasoning exactly"}]`)
	case "plaintext_reasoning":
		reasoning = `{"type":"reasoning","summary":[{"type":"summary_text","text":"retain plaintext summary"}],"content":[{"type":"reasoning_text","text":"Retain this readable reasoning exactly"}]}`
	case "reasoning_id":
		reasoning, err = sjson.Set(reasoning, "id", "rs_fixture842")
	case "invalid_replay_id":
		body, err = sjson.Set(body, "input.1.id", "invalid-provider-item842")
	case "message_call_id":
		body, err = sjson.Set(body, "input.1.call_id", "unpaired-output-metadata842")
	}
	require.NoError(t, err)
	body, err = sjson.SetRaw(body, "input.-1", reasoning)
	require.NoError(t, err)
	// Independent expected representation: exactly the metadata permitted by
	// the pre-freeze contract; no production normalizer is used as the oracle.
	want := body
	for _, path := range []string{"input.5.status", "input.5.id", "input.1.id", "input.1.call_id"} {
		want, err = sjson.Delete(want, path)
		require.NoError(t, err)
	}
	return body, want
}

func TestIssue842HTTPReplayMetadata(t *testing.T) {
	for _, kind := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, variant := range []string{"reasoning_status", "reasoning_content", "plaintext_reasoning", "reasoning_id", "invalid_replay_id", "message_call_id"} {
				for _, known := range []bool{true, false} {
					t.Run(fmt.Sprintf("%s/stream=%t/%s/known=%t", kind, stream, variant, known), func(t *testing.T) {
						clients := issue805Redis(t)
						var mu sync.Mutex
						var bodies [][]byte
						var identities []string
						srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							require.Empty(t, r.Header.Get("Upgrade"), "count the actual HTTP adapter, including configured SSE acceleration")
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
							output := "[" + issue715Agent + "]"
							if n > 1 || gjson.GetBytes(body, "input.#(type==function_call_output)").Exists() {
								output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"recorded result retained once"}]}]`
							}
							response := fmt.Sprintf(`{"id":"resp_842_%d","status":"completed","error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, n, output)
							if gjson.GetBytes(body, "stream").Bool() {
								w.Header().Set("Content-Type", "text/event-stream")
								_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", response)
							} else {
								w.Header().Set("Content-Type", "application/json")
								_, _ = io.WriteString(w, response)
							}
						}))
						defer srv.Close()
						target, err := url.Parse(srv.URL)
						require.NoError(t, err)
						accounts := issue715Accounts()
						for i := range accounts {
							accounts[i].Type = kind
							accounts[i].Credentials = map[string]any{"api_key": fmt.Sprintf("fixture842-%d", i+1), "access_token": fmt.Sprintf("fixture842-%d", i+1), "base_url": srv.URL}
						}
						newNode := func(disabled bool) *OpenAIGatewayHandler {
							if disabled && kind == service.AccountTypeOAuth {
								accounts[1].Extra = map[string]any{"openai_oauth_responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": service.OpenAIWSIngressModeCtxPool, "openai_oauth_ws_sse_acceleration": true}
							}
							cfg := &config.Config{RunMode: config.RunModeSimple}
							cfg.Security.URLAllowlist.AllowInsecureHTTP = true
							upstream := &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
							settings := service.NewSettingService(&issue842Settings{}, cfg)
							h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, upstream, repository.NewGatewayCache(clients()), settings, accounts...)
							h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
							h.cfg.Gateway.OpenAIWS.Enabled = disabled
							h.cfg.Gateway.OpenAIWS.OAuthEnabled = disabled
							h.cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = disabled
							issue805Policy(t, h.cfg, disabled)
							return h
						}
						if known {
							first := issue758Request(t, newNode(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
							require.Equal(t, http.StatusOK, first.Code, first.Body.String())
							require.JSONEq(t, issue715Agent, issue758CompletedResponse(t, first.Body.String(), stream).Get("output.0").Raw)
						}
						original := issue805Owners(t, clients())
						offset := 0
						if known {
							offset = 1
						}
						require.Len(t, original, offset, "A must actually complete delivery")
						accounts[0].Schedulable = false
						body, want := issue842History(t, stream, variant)
						answer := issue758Request(t, newNode(true), body)
						require.Equal(t, http.StatusOK, answer.Code, "Issue842 benign replay metadata must reach B once: %s", answer.Body.String())
						issue758CompletedResponse(t, answer.Body.String(), stream)
						mu.Lock()
						defer mu.Unlock()
						expectedIDs := []string{"Bearer fixture842-2"}
						if known {
							expectedIDs = append([]string{"Bearer fixture842-1"}, expectedIDs...)
						}
						require.Equal(t, expectedIDs, identities)
						require.Len(t, bodies, offset+1, "one A generation and one intact B continuation")
						require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(bodies[offset], "input").Raw)
						require.Equal(t, "9007199254740993", gjson.GetBytes(bodies[offset], "input.1.sequence").Raw)
						require.Equal(t, original, issue805Owners(t, clients()), "consuming A on B must not republish its owner")
					})
				}
			}
		}
	}
}
