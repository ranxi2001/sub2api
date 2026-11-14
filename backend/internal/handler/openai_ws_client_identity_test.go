package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Only the final network destination is replaced for OAuth's fixed endpoint.
// Admission, request construction, identity projection, WS framing and the real
// HTTP transport still run. The adapter fails closed for every other host.
type issue600LocalTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (u issue600LocalTransport) local(req *http.Request) (*http.Request, error) {
	if req.URL.Host != "chatgpt.com" && req.URL.Host != u.target.Host {
		return nil, fmt.Errorf("unexpected fixture destination %s", req.URL.Host)
	}
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = u.target.Scheme
	cloned.URL.Host = u.target.Host
	cloned.Host = u.target.Host
	return cloned, nil
}
func (u issue600LocalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The test client also uses http.DefaultClient for its downstream handshake.
	// Loopback requests keep their own destination; only the fixed OAuth host
	// needs redirecting into the synthetic upstream.
	if host := req.URL.Hostname(); host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return u.transport.RoundTrip(req)
	}
	local, err := u.local(req)
	if err != nil {
		return nil, err
	}
	return u.transport.RoundTrip(local)
}

type issue600LocalHTTPUpstream struct {
	service.HTTPUpstream
	route issue600LocalTransport
}

func (u issue600LocalHTTPUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	local, err := u.route.local(req)
	if err != nil {
		return nil, err
	}
	return u.HTTPUpstream.Do(local, proxy, id, concurrency)
}
func (u issue600LocalHTTPUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

type issue600SwitchRepo struct {
	*openAIWSFailoverHandlerAccountRepoStub
}

// Snapshot writes are irrelevant to the fixed account fixture. Quota cooldown
// still uses the existing SetRateLimited stub and production runtime blocker.
func (r *issue600SwitchRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

type issue600FixtureOptions struct {
	moderationSettings map[string]string
	modelMapping       map[string]string
	switchAccount      bool
	switchAfterTurns   int
	disableCyber       bool
}

type issue600Upstream struct {
	mu              sync.Mutex
	frames          [][]byte
	headers         []http.Header
	rejectedHeaders []http.Header
	rejectedBodies  [][]byte
}

func (u *issue600Upstream) record(r *http.Request, body []byte) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.frames = append(u.frames, append([]byte(nil), body...))
	u.headers = append(u.headers, r.Header.Clone())
	return len(u.frames)
}
func (u *issue600Upstream) snapshot() ([][]byte, []http.Header) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.frames...), append([]http.Header(nil), u.headers...)
}

func (u *issue600Upstream) rejectedSnapshot() ([]http.Header, [][]byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]http.Header(nil), u.rejectedHeaders...), append([][]byte(nil), u.rejectedBodies...)
}

// The HTTP request builder deliberately omits thread-id. WS handshakes carry
// its projected value. Assert each actual transport policy explicitly.
func issue600ProjectedHeader(t *testing.T, mode string, header http.Header, projected string) {
	t.Helper()
	if mode == service.OpenAIWSIngressModeHTTPBridge {
		require.Empty(t, header.Get("thread-id"), "HTTP bridge must keep the existing thread-header allowlist")
	} else {
		require.Equal(t, projected, header.Get("thread-id"), "outbound header/body projection must agree")
	}
}

func issue600Thread(body []byte) string {
	if v := gjson.GetBytes(body, "client_metadata.thread_id").String(); v != "" {
		return v
	}
	return gjson.GetBytes(body, "response.client_metadata.thread_id").String()
}
func newIssue600NetworkHarness(t *testing.T, mode, namespace string, strict bool, headers http.Header, options ...issue600FixtureOptions) (*openAIWSPassthroughHandlerHarness, *issue600Upstream) {
	t.Helper()
	observed := &issue600Upstream{}
	opts := issue600FixtureOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	failover := opts.switchAccount || opts.switchAfterTurns > 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed.mu.Lock()
		completedGenerations := len(observed.frames)
		observed.mu.Unlock()
		if failover && completedGenerations >= opts.switchAfterTurns && r.Header.Get("Authorization") == "Bearer fixture-account-a" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			observed.mu.Lock()
			observed.rejectedHeaders = append(observed.rejectedHeaders, r.Header.Clone())
			observed.rejectedBodies = append(observed.rejectedBodies, body)
			observed.mu.Unlock()
			// A pre-generation refusal is safe for the existing account failover.
			w.Header().Set("Retry-After", "60")
			w.Header().Set("x-codex-primary-used-percent", "100")
			w.Header().Set("x-codex-primary-reset-after-seconds", "60")
			w.Header().Set("x-codex-primary-window-minutes", "300")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"fixture pre-generation refusal"}}`)
			return
		}
		completed := func(n int) []byte {
			return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_issue600_%d","model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`, n))
		}
		if mode == service.OpenAIWSIngressModeHTTPBridge {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			n := observed.record(r, body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", completed(n))
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			_, body, err := conn.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			n := observed.record(r, body)
			ctx, cancel = context.WithTimeout(r.Context(), 5*time.Second)
			err = conn.Write(ctx, coderws.MessageText, completed(n))
			cancel()
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	transport, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok, "fixture requires the standard HTTP transport")
	transport = transport.Clone()
	t.Cleanup(transport.CloseIdleConnections)
	route := issue600LocalTransport{target: target, transport: transport}
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: route}
	t.Cleanup(func() { http.DefaultClient = oldClient })
	settings := map[string]string{service.SettingKeyCyberSessionIdentityStrictEnabled: strconv.FormatBool(strict)}
	for key, value := range opts.moderationSettings {
		settings[key] = value
	}
	if opts.disableCyber {
		settings[service.SettingKeyCyberSessionBlockEnabled] = "false"
	}
	var gatewayCache service.GatewayCache
	if addr := os.Getenv("ISSUE600_REDIS_ADDR"); addr != "" {
		gatewayCache = testutil.NewIsolatedRedisGatewayCache(t, addr)
	}
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream.URL, headers, settings, gatewayCache, func(key *service.APIKey) { key.ModelMapping = opts.modelMapping }, func(a *service.Account, cfg *config.Config, up *service.HTTPUpstream, repo *service.AccountRepository) {
		a.Extra["codex_fingerprint_mode"] = "off"
		a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
		if namespace != "none" {
			a.Type = service.AccountTypeOAuth
			a.Credentials["access_token"] = "fixture-access-token"
			a.Credentials["chatgpt_account_id"] = "fixture-upstream-account-" + namespace
			a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
			a.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			if namespace == "managed" {
				a.AuthConnectionID = "11111111-1111-4111-8111-111111111111"
				a.Extra["openai_connections_managed"] = true
			}
		}
		cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = mode != "legacy"
		*up = issue600LocalHTTPUpstream{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), route: route}
		if failover {
			cfg.Gateway.MaxAccountSwitches = 2
			a.Priority = 2
			a.Credentials["access_token"] = "fixture-account-b"
			first := *a
			first.ID = a.ID + 1
			first.Priority = 1
			first.Credentials = map[string]any{"access_token": "fixture-account-a", "chatgpt_account_id": "fixture-other-account"}
			if a.AuthConnectionID != "" {
				first.AuthConnectionID = "fixture-other-connection"
			}
			*repo = &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{first, *a}}}
		}
	})
	return h, observed
}
func issue600Send(t *testing.T, h *openAIWSPassthroughHandlerHarness, payload string, turn int, rejected bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(payload)))
	for {
		_, event, err := h.clientConn.Read(ctx)
		if rejected {
			if err != nil {
				require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
				return
			}
			require.Equal(t, "error", gjson.GetBytes(event, "type").String(), "rejected turn must have no response generation: %s", event)
			require.Contains(t, []string{"cyber_session_identity_conflict", "cyber_session_identity_invalid", "cyber_session_identity_required"}, gjson.GetBytes(event, "error.code").String())
			_, _, err = h.clientConn.Read(ctx)
			require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
			return
		}
		require.NoError(t, err, "stable original client identity must admit turn %d", turn)
		require.NotEqual(t, "error", gjson.GetBytes(event, "type").String(), "stable original client identity must admit turn %d: %s", turn, event)
		if gjson.GetBytes(event, "type").String() == "response.completed" {
			return
		}
	}
}
func issue600Finish(t *testing.T, h *openAIWSPassthroughHandlerHarness, headers http.Header) {
	t.Helper()
	_ = h.clientConn.CloseNow()
	select {
	case <-h.handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not release connection")
	}
	select {
	case final := <-h.finalHeaders:
		for key, values := range headers {
			require.Equal(t, values, final.Values(key), "original upgrade header %s must stay untouched", key)
		}
		require.Equal(t, headers.Get("thread-id"), final.Get("thread-id"))
		require.Equal(t, headers.Get("session_id"), final.Get("session_id"))
	default:
		t.Fatal("original upgrade headers were not captured")
	}
}

// Real handler -> production forwarder -> synthetic WS/HTTP upstream. No call to
// an extracted identity resolver or an admission hook stands in for the network.
func TestIssue600ClientIdentityNetwork(t *testing.T) {
	stable := `{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`
	omitted := `{"type":"response.create","model":"gpt-5.1","input":"fixture-followup"}`
	changed := strings.ReplaceAll(stable, "fixture-client-thread", "fixture-different-thread")
	invalid := strings.ReplaceAll(stable, "fixture-client-thread", `fixture\ninvalid`)
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			for _, strict := range []bool{false, true} {
				t.Run("strict="+strconv.FormatBool(strict), func(t *testing.T) {
					for _, namespace := range []string{"none", "oauth"} {
						t.Run(namespace, func(t *testing.T) {
							for _, tc := range []struct {
								name          string
								headers       http.Header
								first, second string
								reject        bool
							}{
								{"stable_header_body", http.Header{"Thread-Id": []string{"fixture-client-thread"}}, stable, stable, false},
								{"body_only", nil, stable, stable, false},
								{"omitted_identity", nil, stable, omitted, false},
								{"static_session_inheritance", http.Header{"Session_id": []string{"fixture-static-session"}}, stable, omitted, false},
								{"genuine_header_body_conflict", http.Header{"Thread-Id": []string{"fixture-client-thread"}}, stable, changed, true},
								{"true_body_identity_switch", nil, stable, changed, true},
								{"invalid_followup", nil, stable, invalid, true},
							} {
								t.Run(tc.name, func(t *testing.T) {
									h, up := newIssue600NetworkHarness(t, mode, namespace, strict, tc.headers)
									issue600Send(t, h, tc.first, 1, false)
									issue600Send(t, h, tc.second, 2, tc.reject)
									issue600Finish(t, h, tc.headers)
									frames, outHeaders := up.snapshot()
									want := 2
									if tc.reject {
										want = 1
									}
									require.Len(t, frames, want, "rejected turn must cause zero extra upstream generations")
									projected := issue600Thread(frames[0])
									require.NotEmpty(t, projected)
									if namespace != "none" {
										require.NotEqual(t, "fixture-client-thread", projected, "original identity must not leak upstream")
									}
									for i, frame := range frames {
										if thread := issue600Thread(frame); thread != "" {
											require.Equal(t, projected, thread)
										}
										if tc.headers.Get("thread-id") != "" {
											issue600ProjectedHeader(t, mode, outHeaders[i], projected)
										}
										if namespace != "none" {
											require.NotContains(t, string(frame), "fixture-client-thread")
											require.NotContains(t, fmt.Sprint(outHeaders[i]), "fixture-client-thread")
											require.NotContains(t, fmt.Sprint(outHeaders[i]), "fixture-static-session")
										}
									}
								})
							}
						})
					}
				})
			}
		})
	}
}

func TestIssue600NamespaceAccountBoundaries(t *testing.T) {
	payload := `{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			var identities []string
			for _, namespace := range []string{"oauth-a", "oauth-b"} {
				// Each selection uses an independent connection. A client cannot move its
				// namespace into another selected credential, even with identical input.
				t.Run(namespace, func(t *testing.T) {
					headers := http.Header{"Thread-Id": []string{"fixture-client-thread"}}
					h, up := newIssue600NetworkHarness(t, mode, namespace, true, headers)
					issue600Send(t, h, payload, 1, false)
					issue600Send(t, h, payload, 2, false)
					issue600Finish(t, h, headers)
					frames, outHeaders := up.snapshot()
					require.Len(t, frames, 2)
					identity := issue600Thread(frames[0])
					require.NotEqual(t, "fixture-client-thread", identity)
					require.Equal(t, identity, issue600Thread(frames[1]))
					issue600ProjectedHeader(t, mode, outHeaders[1], identity)
					require.NotContains(t, identities, identity, "different account/authorized connection must use distinct outbound namespace")
					identities = append(identities, identity)
				})
			}
		})
	}
}

func TestIssue600WSTurnRejectionDiagnostic(t *testing.T) {
	// Decode the new metadata flag so this exact test file also compiles in the
	// disposable baseline RED checkout, where the old struct ignores that flag.
	var meta cyberPolicyOpsErrorMeta
	require.NoError(t, json.Unmarshal([]byte(`{"WSTurnAdmission":true,"IdentityStatus":"conflict","IdentityKind":"thread","IdentitySource":"header_body"}`), &meta))
	entry := buildCyberSessionIdentityRejectedOpsEntry(meta)
	require.Contains(t, entry.ErrorMessage, "websocket turn rejected during session identity admission")
	require.NotContains(t, entry.ErrorMessage, "before account selection")
	require.Contains(t, entry.ErrorMessage, "session_identity_status=conflict")
}

// The first selected credential refuses before generation. The same downstream
// connection then gets two completed turns from a different selected credential.
func TestIssue600AccountSwitchBeforeGeneration(t *testing.T) {
	payload := `{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			for _, namespace := range []string{"oauth"} {
				t.Run(namespace, func(t *testing.T) {
					headers := http.Header{"Thread-Id": []string{"fixture-client-thread"}}
					h, up := newIssue600NetworkHarness(t, mode, namespace, true, headers, issue600FixtureOptions{switchAccount: true})
					issue600Send(t, h, payload, 1, false)
					issue600Send(t, h, payload, 2, false)
					issue600Finish(t, h, headers)
					frames, outHeaders := up.snapshot()
					require.Len(t, frames, 2, "only the replacement account generates, once per turn")
					rejectedHeaders, rejectedBodies := up.rejectedSnapshot()
					require.Len(t, rejectedHeaders, 1, "exactly one pre-generation refusal, no blind replay")
					projected := issue600Thread(frames[0])
					require.NotEqual(t, "fixture-client-thread", projected)
					if mode == service.OpenAIWSIngressModeHTTPBridge {
						firstProjection := issue600Thread(rejectedBodies[0])
						require.NotEmpty(t, firstProjection)
						require.NotEqual(t, "fixture-client-thread", firstProjection)
						require.NotEqual(t, firstProjection, projected, "account switch must rotate outbound body namespace")
						issue600ProjectedHeader(t, mode, rejectedHeaders[0], firstProjection)
					} else {
						require.NotEqual(t, rejectedHeaders[0].Get("thread-id"), projected, "account switch must rotate outbound namespace")
					}
					for i, frame := range frames {
						require.Equal(t, projected, issue600Thread(frame), "replacement must scope the original, never the prior projected identity")
						issue600ProjectedHeader(t, mode, outHeaders[i], projected)
						require.Equal(t, "Bearer fixture-account-b", outHeaders[i].Get("Authorization"))
					}
				})
			}
		})
	}
}

// Managed connections intentionally have no WS ingress capability at this base.
// Preserve this account-selection fence instead of fabricating a usable grant.
func TestIssue600ManagedConnectionWSBoundary(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			for _, strict := range []bool{false, true} {
				t.Run(strconv.FormatBool(strict), func(t *testing.T) {
					headers := http.Header{"Thread-Id": []string{"fixture-client-thread"}}
					h, up := newIssue600NetworkHarness(t, mode, "managed", strict, headers)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`)))
					_, _, err := h.clientConn.Read(ctx)
					require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err), "managed connections must remain excluded from WS scheduling")
					issue600Finish(t, h, headers)
					frames, _ := up.snapshot()
					require.Empty(t, frames, "unauthorized WS transport must cause zero upstream generation")
				})
			}
		})
	}
}

func TestIssue600InitialIdentityPolicy(t *testing.T) {
	stable := `{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			for _, strict := range []bool{false, true} {
				t.Run(strconv.FormatBool(strict), func(t *testing.T) {
					for _, tc := range []struct{ name, first string }{
						{"missing", `{"type":"response.create","model":"gpt-5.1","input":"fixture"}`},
						{"invalid", strings.ReplaceAll(stable, "fixture-client-thread", `fixture\ninvalid`)},
						{"header_body_conflict", strings.ReplaceAll(stable, "fixture-client-thread", "fixture-conflicting-thread")},
					} {
						t.Run(tc.name, func(t *testing.T) {
							headers := http.Header{}
							if tc.name == "header_body_conflict" {
								headers.Set("thread-id", "fixture-client-thread")
							}
							h, up := newIssue600NetworkHarness(t, mode, "oauth", strict, headers)
							issue600Send(t, h, tc.first, 1, strict)
							if !strict {
								issue600Send(t, h, tc.first, 2, false)
							}
							issue600Finish(t, h, headers)
							frames, _ := up.snapshot()
							want := 2
							if strict {
								want = 0
							}
							require.Len(t, frames, want, "strict policy must remain unchanged at first-frame admission")
						})
					}
				})
			}
		})
	}
}

func TestIssue600DisabledSessionBlockKeepsIdentityPolicy(t *testing.T) {
	stable := `{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			headers := http.Header{"Thread-Id": []string{"fixture-client-thread"}}
			h, up := newIssue600NetworkHarness(t, mode, "oauth", true, headers, issue600FixtureOptions{disableCyber: true})
			issue600Send(t, h, stable, 1, false)
			issue600Send(t, h, strings.ReplaceAll(stable, "fixture-client-thread", "fixture-changed-thread"), 2, false)
			issue600Finish(t, h, headers)
			frames, _ := up.snapshot()
			require.Len(t, frames, 2, "disabled session blocking must not acquire a new identity gate")
		})
	}
}

func TestIssue600InheritedIdentityStillChecksSessionBlock(t *testing.T) {
	first := `{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`
	omitted := `{"type":"response.create","model":"gpt-5.1","input":"fixture-followup"}`
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			for _, strict := range []bool{false, true} {
				t.Run(strconv.FormatBool(strict), func(t *testing.T) {
					headers := http.Header{"Session_id": []string{"fixture-static-session"}}
					h, up := newIssue600NetworkHarness(t, mode, "oauth", strict, headers)
					issue600Send(t, h, first, 1, false)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(first))
					c.Request.Header = headers.Clone()
					key := service.CyberSessionExplicitBlockKey(h.apiKey.ID, c, []byte(first))
					require.NotEmpty(t, key)
					store, ok := h.gatewayCache.(service.CyberSessionBlockStore)
					require.True(t, ok)
					require.NoError(t, store.SetCyberSessionBlocked(context.Background(), "", []string{key}, time.Minute))
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(omitted)))
					_, event, err := h.clientConn.Read(ctx)
					require.NoError(t, err)
					require.Equal(t, "session_blocked_by_cyber_policy", gjson.GetBytes(event, "error.code").String(), "omitted body must inherit the trusted thread instead of static session header")
					_, _, err = h.clientConn.Read(ctx)
					require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
					issue600Finish(t, h, headers)
					frames, _ := up.snapshot()
					require.Len(t, frames, 1, "newly blocked inherited session must generate zero extra upstream work")
				})
			}
		})
	}
}
