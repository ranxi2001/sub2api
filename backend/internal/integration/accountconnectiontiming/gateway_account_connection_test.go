//go:build unit

package accountconnectiontiming

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayTestAccounts struct {
	service.AccountRepository
	account *service.Account
	live    *service.Account
	group   *service.Group
	writes  atomic.Int64
}

func cloneGatewayTestAccount(a *service.Account) *service.Account {
	copy := *a
	copy.Credentials = maps.Clone(a.Credentials)
	copy.Extra = maps.Clone(a.Extra)
	return &copy
}

func (r *gatewayTestAccounts) GetByID(context.Context, int64) (*service.Account, error) {
	return cloneGatewayTestAccount(r.account), nil
}

func (r *gatewayTestAccounts) GatewayRequestAccount(context.Context, int64, int64) (*service.Account, *service.Group, error) {
	current := r.account
	if r.live != nil {
		current = r.live
	}
	return cloneGatewayTestAccount(current), r.group, nil
}

func (r *gatewayTestAccounts) SetError(context.Context, int64, string) error {
	r.writes.Add(1)
	return nil
}

func (r *gatewayTestAccounts) UpdateExtra(context.Context, int64, map[string]any) error {
	r.writes.Add(1)
	return nil
}

type gatewayTestTransport struct {
	service.HTTPUpstream
	calls     atomic.Int64
	closed    atomic.Int64
	seen      chan gatewayTestEgress
	firstRead chan struct{}
	readOnce  atomic.Bool
}

type gatewayTestEgress struct {
	url         string
	proxy       string
	id          int64
	concurrency int
}

type gatewayTestBody struct {
	io.ReadCloser
	closed    *atomic.Int64
	once      atomic.Bool
	firstRead chan struct{}
	readOnce  *atomic.Bool
}

func (b *gatewayTestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && b.firstRead != nil && b.readOnce.CompareAndSwap(false, true) {
		close(b.firstRead)
	}
	return n, err
}

func (b *gatewayTestBody) Close() error {
	if b.once.CompareAndSwap(false, true) {
		b.closed.Add(1)
	}
	return b.ReadCloser.Close()
}

func (u *gatewayTestTransport) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.calls.Add(1)
	u.seen <- gatewayTestEgress{req.URL.String(), proxy, id, concurrency}
	resp, err := u.HTTPUpstream.Do(req, proxy, id, concurrency)
	if resp != nil && resp.Body != nil {
		resp.Body = &gatewayTestBody{ReadCloser: resp.Body, closed: &u.closed, firstRead: u.firstRead, readOnce: &u.readOnce}
	}
	return resp, err
}

func (u *gatewayTestTransport) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func newGatewayTestRouter(repo *gatewayTestAccounts, cfg *config.Config) (*gin.Engine, *gatewayTestTransport) {
	upstream := &gatewayTestTransport{HTTPUpstream: repository.NewHTTPUpstream(cfg), seen: make(chan gatewayTestEgress, 16)}
	svc := service.NewAccountTestService(repo, nil, nil, nil, nil, upstream, cfg, nil)
	// A nonnil recovery service makes success exercise the handler's observation
	// boundary too; attempting ordinary health recovery here would dereference
	// its deliberately absent persistence dependencies.
	h := admin.NewAccountHandler(nil, nil, nil, nil, nil, nil, &service.RateLimitService{}, nil, svc, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/:id/test", h.Test)
	return router, upstream
}

func gatewayTestFixture(base, platform string) (*gatewayTestAccounts, *config.Config) {
	group := &service.Group{ID: 1010, Platform: platform, Status: service.StatusActive, Hydrated: true}
	account := &service.Account{
		ID: 1010, Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 7,
		GroupIDs: []int64{group.ID}, AccountGroups: []service.AccountGroup{{GroupID: group.ID}},
		Credentials: map[string]any{"upstream_kind": "new_api", "base_url": base, "api_key": "synthetic-gateway-key"},
		Extra:       map[string]any{"synthetic_metadata": "preserve"},
	}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	return &gatewayTestAccounts{account: account, group: group}, cfg
}

func gatewayTestRequest(router *gin.Engine, ctx context.Context, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1010/test", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer inbound-admin-must-not-leak")
	router.ServeHTTP(recorder, req)
	return recorder
}

func gatewayTestStream(protocol string) string {
	switch protocol {
	case "anthropic":
		return "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"synthetic answer\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	case "gemini":
		return "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"synthetic answer\"}]},\"finishReason\":\"STOP\"}]}\n\n"
	case "chat":
		return "data: {\"choices\":[{\"delta\":{\"content\":\"synthetic answer\"}}]}\n\ndata: [DONE]\n\n"
	default:
		return "data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic answer\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"
	}
}

func TestGatewayAccountConnectionNativeHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"new_api", "sub2api"} {
		for _, scenario := range []struct {
			protocol string
			platform string
			model    string
			path     string
		}{
			{"responses", service.PlatformOpenAI, "gpt-6.1-sol", "/tenant/v1/responses"},
			{"chat", service.PlatformDeepseek, "deepseek-chat", "/tenant/v1/chat/completions"},
			{"anthropic", service.PlatformAnthropic, "claude-sonnet-4-6", "/tenant/v1/messages"},
			{"gemini", service.PlatformGemini, "gemini-3.1-pro", "/tenant/v1beta/models/gemini-3.1-pro:streamGenerateContent"},
		} {
			t.Run(kind+"/"+scenario.protocol, func(t *testing.T) {
				var hits, proxyHits atomic.Int64
				type received struct {
					path          string
					query         string
					authorization string
					key           string
					googleKey     string
					custom        string
					payload       map[string]any
				}
				requests := make(chan received, 2)
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					defer func() { _ = r.Body.Close() }()
					var payload map[string]any
					_ = json.NewDecoder(r.Body).Decode(&payload)
					requests <- received{r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("x-api-key"), r.Header.Get("x-goog-api-key"), r.Header.Get("X-Gateway-Fixture"), payload}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, gatewayTestStream(scenario.protocol))
				}))
				defer origin.Close()
				proxyTransport := &http.Transport{}
				defer proxyTransport.CloseIdleConnections()
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					proxyHits.Add(1)
					forward := r.Clone(r.Context())
					forward.RequestURI = ""
					resp, err := proxyTransport.RoundTrip(forward)
					if err != nil {
						http.Error(w, "synthetic proxy failure", http.StatusBadGateway)
						return
					}
					defer func() { _ = resp.Body.Close() }()
					for key, values := range resp.Header {
						w.Header()[key] = values
					}
					w.WriteHeader(resp.StatusCode)
					_, _ = io.Copy(w, resp.Body)
				}))
				defer proxy.Close()
				repo, cfg := gatewayTestFixture(origin.URL+"/tenant/v1", scenario.platform)
				repo.account.Credentials["upstream_kind"] = kind
				// The wire name is another source key too. Applying mapping twice
				// would change the requested protocol/model and fail this oracle.
				repo.account.Credentials["model_mapping"] = map[string]any{"public-*": scenario.model, scenario.model: "wrong-second-mapping"}
				repo.account.Credentials["header_override_enabled"] = true
				repo.account.Credentials["header_overrides"] = map[string]any{"X-Gateway-Fixture": "own-account"}
				if scenario.protocol == "chat" {
					repo.account.Extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeForceChatCompletions)
				}
				proxyURL, err := url.Parse(proxy.URL)
				require.NoError(t, err)
				port, err := strconv.Atoi(proxyURL.Port())
				require.NoError(t, err)
				proxyID := int64(88)
				repo.account.ProxyID = &proxyID
				repo.account.Proxy = &service.Proxy{ID: proxyID, Protocol: "http", Host: proxyURL.Hostname(), Port: port}
				before := cloneGatewayTestAccount(repo.account)
				router, transport := newGatewayTestRouter(repo, cfg)
				recorder := gatewayTestRequest(router, context.Background(), `{"model_id":"public-selected"}`)
				require.Equal(t, int64(1), hits.Load(), "gateway probe must reach synthetic upstream exactly once: %s", recorder.Body.String())
				require.Equal(t, int64(1), proxyHits.Load(), "the saved account proxy must carry the actual request")
				require.Equal(t, int64(1), transport.calls.Load())
				require.Equal(t, int64(1), transport.closed.Load())
				egress := <-transport.seen
				require.Equal(t, origin.URL+scenario.path, strings.Split(egress.url, "?")[0])
				require.Equal(t, proxy.URL, egress.proxy)
				require.Equal(t, repo.account.ID, egress.id)
				require.Equal(t, repo.account.Concurrency, egress.concurrency)
				request := <-requests
				require.Equal(t, scenario.path, request.path)
				if scenario.protocol == "gemini" {
					require.Equal(t, "alt=sse", request.query)
					require.Equal(t, "synthetic-gateway-key", request.googleKey)
					require.Empty(t, request.authorization)
				} else {
					require.Equal(t, "own-account", request.custom)
					require.Equal(t, scenario.model, request.payload["model"])
					if scenario.protocol == "anthropic" {
						require.Equal(t, "synthetic-gateway-key", request.key)
						require.Empty(t, request.authorization)
					} else {
						require.Equal(t, "Bearer synthetic-gateway-key", request.authorization)
					}
				}
				body := recorder.Body.String()
				require.Contains(t, body, "synthetic answer")
				require.Equal(t, 1, strings.Count(body, `"type":"test_complete"`))
				require.Contains(t, body, `"success":true`)
				require.NotContains(t, body, `"type":"error"`)
				require.Zero(t, repo.writes.Load())
				require.Equal(t, before, repo.account, "probe must preserve neutral identity, credentials and runtime state")
			})
		}
	}
}

func TestGatewayAccountConnectionUpstreamFailureAndEOF(t *testing.T) {
	for _, protocol := range []string{"responses", "chat", "anthropic", "gemini"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusOK} {
			t.Run(protocol+"/"+strconv.Itoa(status), func(t *testing.T) {
				var hits atomic.Int64
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.WriteHeader(status)
					if status != http.StatusOK {
						_, _ = io.WriteString(w, `{"error":{"message":"synthetic rejection"}}`)
					}
				}))
				defer origin.Close()
				platform, model := service.PlatformOpenAI, "gpt-6.1-sol"
				if protocol == "anthropic" {
					platform, model = service.PlatformAnthropic, "claude-sonnet-4-6"
				}
				if protocol == "gemini" {
					platform, model = service.PlatformGemini, "gemini-3.1-pro"
				}
				repo, cfg := gatewayTestFixture(origin.URL, platform)
				if protocol == "chat" {
					repo.account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
				}
				before := cloneGatewayTestAccount(repo.account)
				router, transport := newGatewayTestRouter(repo, cfg)
				recorder := gatewayTestRequest(router, context.Background(), `{"model_id":"`+model+`"}`)
				require.Equal(t, int64(1), hits.Load())
				require.Equal(t, int64(1), transport.calls.Load(), "failure must not probe another protocol or model")
				require.Equal(t, int64(1), transport.closed.Load())
				body := recorder.Body.String()
				require.Equal(t, 1, strings.Count(body, `"type":"error"`))
				require.Zero(t, strings.Count(body, `"type":"test_complete"`))
				require.NotContains(t, body, `"success":true`)
				if status != http.StatusOK {
					require.Contains(t, body, "synthetic rejection")
				}
				require.Zero(t, repo.writes.Load())
				require.Equal(t, before, repo.account)
			})
		}
	}
}

func TestGatewayAccountConnectionAdmissionNoEgress(t *testing.T) {
	for _, scenario := range []string{"empty-model", "ambiguous-model", "multiple-groups", "wrong-group", "observer", "unbound", "oauth-group", "disabled-group", "allowlist", "binding-scope", "source-scope", "changed-key", "changed-proxy", "unsupported-model", "wrong-family", "unsafe-url", "mode"} {
		t.Run(scenario, func(t *testing.T) {
			repo, cfg := gatewayTestFixture("https://gateway.example/tenant/v1", service.PlatformOpenAI)
			ctx := context.Background()
			body := `{"model_id":"gpt-6.1-sol"}`
			switch scenario {
			case "empty-model":
				body = `{}`
			case "ambiguous-model":
				repo.group.Platform = service.PlatformComposite
				body = `{"model_id":"unknown-alias"}`
			case "multiple-groups":
				repo.account.GroupIDs = []int64{1010, 1011}
			case "wrong-group":
				body = `{"model_id":"gpt-6.1-sol","group_id":1011}`
			case "observer":
				ctx = service.WithObserverScope(ctx, []int64{1011})
			case "unbound":
				repo.live = cloneGatewayTestAccount(repo.account)
				repo.live.GroupIDs, repo.live.AccountGroups = nil, nil
			case "oauth-group":
				repo.group.RequireOAuthOnly = true
			case "disabled-group":
				repo.group.Status = service.StatusDisabled
			case "allowlist":
				repo.group.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"other-model"}}
			case "binding-scope":
				repo.account.AccountGroups[0].AllowedModels = []string{"other-model"}
			case "source-scope":
				repo.account.Credentials["model_mapping"] = map[string]any{"public-alias": "gpt-6.1-sol"}
			case "changed-key":
				repo.live = cloneGatewayTestAccount(repo.account)
				repo.live.Credentials["api_key"] = "changed-synthetic-key"
			case "changed-proxy":
				repo.live = cloneGatewayTestAccount(repo.account)
				id := int64(9)
				repo.live.ProxyID = &id
			case "unsupported-model":
				body = `{"model_id":"text-embedding-3-large"}`
			case "wrong-family":
				body = `{"model_id":"claude-sonnet-4-6"}`
			case "unsafe-url":
				cfg.Security.URLAllowlist.Enabled = true
				cfg.Security.URLAllowlist.UpstreamHosts = []string{"allowed.example"}
			case "mode":
				body = `{"model_id":"gpt-6.1-sol","mode":"compact"}`
			}
			router, transport := newGatewayTestRouter(repo, cfg)
			recorder := gatewayTestRequest(router, ctx, body)
			require.Zero(t, transport.calls.Load(), "rejected gateway scope must not leave the process")
			require.Contains(t, recorder.Body.String(), `"type":"error"`)
			require.NotContains(t, recorder.Body.String(), `"success":true`)
			require.Zero(t, repo.writes.Load())
		})
	}
}

func TestGatewayAccountConnectionExplicitCompositeRoute(t *testing.T) {
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, gatewayTestStream("responses"))
	}))
	defer origin.Close()
	repo, cfg := gatewayTestFixture(origin.URL, service.PlatformComposite)
	repo.account.GroupIDs = []int64{1010, 1011}
	repo.account.Credentials["model_mapping"] = map[string]any{"public-selected": "private-wire-alias"}
	router, transport := newGatewayTestRouter(repo, cfg)
	ctx := service.WithObserverScope(context.Background(), []int64{1010})
	recorder := gatewayTestRequest(router, ctx, `{"model_id":"public-selected","group_id":1010,"platform":"openai"}`)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	require.Equal(t, int64(1), hits.Load())
	require.Equal(t, int64(1), transport.closed.Load())
	require.Zero(t, repo.writes.Load())
}

func TestGatewayAccountConnectionCancellationReleasesReader(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	firstRead := make(chan struct{})
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
			timingFlush(w)
			close(entered)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		_, _ = io.WriteString(w, gatewayTestStream("responses"))
	}))
	defer origin.Close()
	repo, cfg := gatewayTestFixture(origin.URL, service.PlatformOpenAI)
	router, transport := newGatewayTestRouter(repo, cfg)
	transport.firstRead = firstRead
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- gatewayTestRequest(router, ctx, `{"model_id":"gpt-6.1-sol"}`) }()
	timingWait(t, entered)
	// Server Flush precedes client response acquisition. Wait for the native
	// parser's first positive body read so cancellation exercises an owned reader.
	timingWait(t, firstRead)
	cancel()
	select {
	case recorder := <-finished:
		require.NotContains(t, recorder.Body.String(), `"success":true`)
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled gateway handler did not return")
	}
	timingWait(t, cancelled)
	require.Equal(t, int64(1), transport.closed.Load())
	// The next probe uses the same account/concurrency budget after cancellation.
	recorder := gatewayTestRequest(router, context.Background(), `{"model_id":"gpt-6.1-sol","group_id":1010,"platform":"openai"}`)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	require.Equal(t, int64(2), hits.Load())
	require.Equal(t, int64(2), transport.calls.Load())
	require.Equal(t, int64(2), transport.closed.Load())
	require.Zero(t, repo.writes.Load())
}
