//go:build unit

package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type unionKeys struct {
	service.APIKeyRepository
	mu  sync.RWMutex
	key *service.APIKey
}

func (r *unionKeys) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k := *r.key
	u := *k.User
	k.User = &u
	return &k, nil
}
func (*unionKeys) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type unionGroups struct {
	service.GroupRepository
	mu     sync.RWMutex
	groups map[int64]*service.Group
	err    error
}

func (r *unionGroups) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.err != nil {
		return nil, r.err
	}
	if g := r.groups[id]; g != nil {
		return g, nil
	}
	return nil, service.ErrGroupNotFound
}
func (r *unionGroups) GetByID(ctx context.Context, id int64) (*service.Group, error) {
	return r.GetByIDLite(ctx, id)
}

type unionAccounts struct {
	service.AccountRepository
	groups map[int64][]service.Account
	err    error
}

func (r *unionAccounts) ListSchedulableByGroupID(_ context.Context, id int64) ([]service.Account, error) {
	return r.groups[id], r.err
}
func (r *unionAccounts) ListByGroup(ctx context.Context, id int64) ([]service.Account, error) {
	return r.ListSchedulableByGroupID(ctx, id)
}
func (r *unionAccounts) ListModelAvailabilityCandidates(ctx context.Context, id *int64, _ []string, _ bool) ([]service.Account, error) {
	if id == nil {
		return nil, errors.New("global account listing forbidden")
	}
	return r.ListSchedulableByGroupID(ctx, *id)
}

type unionRates struct {
	service.UserGroupRateRepository
	mu     sync.RWMutex
	denied map[int64][]string
	err    error
}

func (r *unionRates) GetDeniedModelsByUserAndGroup(_ context.Context, _, id int64) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.denied[id], r.err
}
func (*unionRates) GetRPMOverrideByUserAndGroup(context.Context, int64, int64) (*int, error) {
	return nil, nil
}
func (*unionRates) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	return nil, nil
}

type unionSubscriptions struct {
	service.UserSubscriptionRepository
	err    error
	active bool
}

func (r *unionSubscriptions) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*service.UserSubscription, error) {
	if r.err != nil {
		return nil, r.err
	}
	if !r.active {
		return nil, service.ErrSubscriptionNotFound
	}
	return &service.UserSubscription{Status: service.StatusActive, ExpiresAt: time.Now().Add(time.Hour), DailyUsageUSD: 100}, nil
}

type unionProbe struct {
	calls    int
	accounts *unionAccounts
}

func (p *unionProbe) ProbeAPIKeyGroup(_ context.Context, g *service.Group, model, _ string) (service.APIKeyGroupAvailability, error) {
	p.calls++
	if model != "" {
		for _, a := range p.accounts.groups[g.ID] {
			if a.IsModelSupported(model) {
				return service.APIKeyGroupAvailability{Available: true}, nil
			}
		}
		return service.APIKeyGroupAvailability{}, nil
	}
	return service.APIKeyGroupAvailability{Available: true}, nil
}

type unionFixture struct {
	key         *service.APIKey
	groups      *unionGroups
	accounts    *unionAccounts
	rates       *unionRates
	subs        *unionSubscriptions
	probe       *unionProbe
	concurrency *service.ConcurrencyService
	keyRepo     *unionKeys
	settings    *service.SettingService
}

func newUnionFixture() *unionFixture {
	groups := &unionGroups{groups: map[int64]*service.Group{
		1: {ID: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1},
		2: {ID: 2, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 0.01},
	}}
	account := func(id int64, models ...string) service.Account {
		mapping := map[string]any{}
		for _, m := range models {
			mapping[m] = m
		}
		return service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": mapping}}
	}
	return &unionFixture{
		key:    &service.APIKey{ID: 10, UserID: 7, Status: service.StatusActive, GroupID: &groups.groups[1].ID, GroupIDs: []int64{1, 2}, Group: groups.groups[1], User: &service.User{ID: 7, Status: service.StatusActive, Balance: 10}},
		groups: groups, accounts: &unionAccounts{groups: map[int64][]service.Account{1: {account(101, "text-model", "shared-model")}, 2: {account(102, "image-model", "shared-model")}}},
		rates: &unionRates{denied: map[int64][]string{}}, subs: &unionSubscriptions{}, probe: &unionProbe{},
	}
}
func (f *unionFixture) router(t *testing.T, forced string, openAI *service.OpenAIGatewayService, routeRepo service.CompositeModelRouteRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	f.keyRepo = &unionKeys{key: f.key}
	keys := service.NewAPIKeyService(f.keyRepo, nil, f.groups, f.subs, f.rates, nil, cfg)
	var resolver *service.CompositeRouteResolver
	if routeRepo != nil {
		resolver = service.NewCompositeRouteResolver(routeRepo)
	}
	gateway := service.NewGatewayService(f.accounts, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, resolver, nil, nil)
	h := &handler.Handlers{
		Gateway:       handler.NewGatewayHandler(gateway, openAI, nil, nil, nil, f.concurrency, nil, nil, keys, nil, nil, nil, nil, cfg, f.settings),
		OpenAIGateway: handler.NewOpenAIGatewayHandler(openAI, nil, nil, nil, nil, nil, nil, nil, cfg),
		AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
	}
	r := gin.New()
	if forced != "" {
		r.Use(middleware.ForcePlatform(forced))
	}
	RegisterGatewayRoutes(r, h, middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg), keys, nil, nil, f.settings, resolver, cfg)
	f.probe.accounts = f.accounts
	keys.SetGroupRouter(f.probe)
	// A POST authentication boundary records the selected billing identity without
	// making an upstream request or inventing usage records.
	r.POST("/union-auth", gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)), func(c *gin.Context) {
		k, _ := middleware.GetAPIKeyFromContext(c)
		c.JSON(200, gin.H{"group_id": *k.GroupID, "rate": k.Group.RateMultiplier})
	})
	return r
}
func unionRequest(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(`{"model":"shared-model"}`))
	req.Header.Set("Authorization", "Bearer synthetic-union-key")
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}
func unionIDs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	require.Equal(t, 200, w.Code, w.Body.String())
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestMultiGroupModelsUnionTextAndCheapImage(t *testing.T) {
	f := newUnionFixture()
	r := f.router(t, "", nil, nil)
	for _, path := range []string{"/v1/models", "/models", "/v1/models?client_version="} {
		w := unionRequest(r, "GET", path)
		require.Equal(t, []string{"image-model", "shared-model", "text-model"}, unionIDs(t, w), "UNION_REGRESSION: ordinary discovery must include text and cheap image groups")
		require.Empty(t, w.Header().Get("ETag"))
	}
	require.Zero(t, f.probe.calls, "catalog must not select a scheduling/billing group")
	require.Equal(t, int64(1), *f.key.GroupID)
	require.Empty(t, f.key.User.UserGroupDeniedModels)
}

func TestMultiGroupModelsUnionPermissionsAndDeny(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unionFixture)
		want   []string
	}{
		{"disabled", func(f *unionFixture) { f.groups.groups[1].Status = service.StatusDisabled }, []string{"image-model", "shared-model"}},
		{"deleted", func(f *unionFixture) { delete(f.groups.groups, 1) }, []string{"image-model", "shared-model"}},
		{"exclusive revoked", func(f *unionFixture) { f.groups.groups[1].IsExclusive = true }, []string{"image-model", "shared-model"}},
		{"public revoked", func(f *unionFixture) { f.key.User.RestrictPublicGroups = true; f.key.User.AllowedGroups = []int64{2} }, []string{"image-model", "shared-model"}},
		{"allowlist", func(f *unionFixture) {
			f.groups.groups[1].ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"text-*", "invented"}}
		}, []string{"image-model", "shared-model", "text-model"}},
		{"deny per group", func(f *unionFixture) { f.rates.denied[1] = []string{"text-model", "shared-model"} }, []string{"image-model", "shared-model"}},
		{"subscription absent", func(f *unionFixture) { f.groups.groups[1].SubscriptionType = service.SubscriptionTypeSubscription }, []string{"image-model", "shared-model"}},
		{"subscription active at limit", func(f *unionFixture) {
			f.groups.groups[1].SubscriptionType = service.SubscriptionTypeSubscription
			f.subs.active = true
			limit := 1.0
			f.groups.groups[1].DailyLimitUSD = &limit
		}, []string{"image-model", "shared-model", "text-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnionFixture()
			tc.change(f)
			require.Equal(t, tc.want, unionIDs(t, unionRequest(f.router(t, "", nil, nil), "GET", "/v1/models")))
		})
	}
}

func TestMultiGroupModelsUnionFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unionFixture)
		status int
	}{
		{"empty accounts", func(f *unionFixture) { f.accounts.groups = nil }, 200},
		{"no allowed models", func(f *unionFixture) {
			for _, g := range f.groups.groups {
				g.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"absent"}}
			}
		}, 200},
		{"account dependency", func(f *unionFixture) { f.accounts.err = errors.New("offline") }, 503},
		{"group dependency", func(f *unionFixture) { f.groups.err = errors.New("offline") }, 500},
		{"deny dependency", func(f *unionFixture) { f.rates.err = errors.New("offline") }, 500},
		{"subscription dependency", func(f *unionFixture) {
			f.groups.groups[1].SubscriptionType = service.SubscriptionTypeSubscription
			f.subs.err = errors.New("offline")
		}, 500},
		{"no entitled groups", func(f *unionFixture) {
			for _, g := range f.groups.groups {
				g.Status = service.StatusDisabled
			}
		}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnionFixture()
			tc.change(f)
			w := unionRequest(f.router(t, "", nil, nil), "GET", "/v1/models")
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 200 {
				require.Empty(t, unionIDs(t, w))
			} else {
				require.NotContains(t, w.Body.String(), `"data"`)
			}
		})
	}
}

func TestMultiGroupModelsUnionOrderPricesAndBilling(t *testing.T) {
	f := newUnionFixture()
	first := unionRequest(f.router(t, "", nil, nil), "GET", "/v1/models")
	f.key.GroupIDs = []int64{2, 1}
	f.key.GroupID = &f.groups.groups[2].ID
	f.key.Group = f.groups.groups[2]
	f.groups.groups[1].RateMultiplier = 0.001
	f.groups.groups[2].RateMultiplier = 100
	second := unionRequest(f.router(t, "", nil, nil), "GET", "/v1/models")
	require.Equal(t, first.Body.String(), second.Body.String())
	f.key.User.Balance = 0
	r := f.router(t, "", nil, nil)
	require.Equal(t, []string{"image-model", "shared-model", "text-model"}, unionIDs(t, unionRequest(r, "GET", "/v1/models")))
	require.Equal(t, 403, unionRequest(r, "POST", "/union-auth").Code, "POST must still enforce balance")
	f.key.User.Balance = 10
	r = f.router(t, "", nil, nil)
	w := unionRequest(r, "POST", "/union-auth")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.JSONEq(t, `{"group_id":1,"rate":0.001}`, w.Body.String())
}

func TestMultiGroupModelsUnionExplicitQueryAndSingleGroupCompatibility(t *testing.T) {
	f := newUnionFixture()
	r := f.router(t, "", nil, nil)
	require.Equal(t, []string{"shared-model", "text-model"}, unionIDs(t, unionRequest(r, "GET", "/v1/models?model=text-model")), "explicit query keeps model-based group selection")
	require.Equal(t, []string{"image-model", "shared-model"}, unionIDs(t, unionRequest(r, "GET", "/v1/models?model=")), "present empty query keeps legacy selection")
	require.Positive(t, f.probe.calls)
	f.key.GroupIDs = []int64{1}
	r = f.router(t, "", nil, nil)
	require.Equal(t, []string{"shared-model", "text-model"}, unionIDs(t, unionRequest(r, "GET", "/v1/models")))
}

func TestMultiGroupModelsUnionForcedPlatform(t *testing.T) {
	f := newUnionFixture()
	f.accounts.groups[1] = append(f.accounts.groups[1], service.Account{ID: 103, Platform: service.PlatformAnthropic, Credentials: map[string]any{"model_mapping": map[string]any{"claude-test": "claude-test"}}})
	require.Equal(t, []string{"claude-test"}, unionIDs(t, unionRequest(f.router(t, service.PlatformAnthropic, nil, nil), "GET", "/v1/models")))
}

type unionRoutes struct {
	service.CompositeModelRouteRepository
	routes []service.CompositeModelRoute
	err    error
}

func (r *unionRoutes) ListByGroup(context.Context, int64, bool) ([]service.CompositeModelRoute, error) {
	return r.routes, r.err
}

func TestMultiGroupModelsUnionCompositeRoutes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unionFixture, *unionRoutes)
		want   []string
		status int
	}{
		{"available alias", func(*unionFixture, *unionRoutes) {}, []string{"image-model", "public-text", "shared-model", "text-model"}, 200},
		{"no target accounts", func(f *unionFixture, _ *unionRoutes) { f.accounts.groups[1] = nil }, []string{"image-model", "shared-model"}, 200},
		{"wrong target platform", func(_ *unionFixture, r *unionRoutes) { r.routes[0].TargetPlatform = service.PlatformAnthropic }, []string{"image-model", "shared-model", "text-model"}, 200},
		{"upstream restricted", func(f *unionFixture, _ *unionRoutes) {
			f.accounts.groups[1][0].AccountGroups = []service.AccountGroup{{GroupID: 1, AllowedModels: []string{"shared-model"}}}
		}, []string{"image-model", "shared-model"}, 200},
		{"exact overrides account", func(_ *unionFixture, r *unionRoutes) {
			r.routes = []service.CompositeModelRoute{{PublicModel: "text-model", TargetPlatform: service.PlatformAnthropic, Enabled: true, MatchType: service.CompositeRouteMatchExact}}
		}, []string{"image-model", "shared-model"}, 200},
		{"prefix overrides account", func(_ *unionFixture, r *unionRoutes) {
			r.routes = []service.CompositeModelRoute{{PublicModel: "text-", TargetPlatform: service.PlatformAnthropic, Enabled: true, MatchType: service.CompositeRouteMatchPrefix}}
		}, []string{"image-model", "shared-model"}, 200},
		{"available endpoint overrides any", func(_ *unionFixture, r *unionRoutes) {
			r.routes = []service.CompositeModelRoute{
				{PublicModel: "text-model", TargetPlatform: service.PlatformAnthropic, Enabled: true, MatchType: service.CompositeRouteMatchExact},
				{PublicModel: "text-model", TargetPlatform: service.PlatformOpenAI, Enabled: true, MatchType: service.CompositeRouteMatchExact, Endpoint: service.CompositeRouteEndpointResponses},
			}
		}, []string{"image-model", "shared-model", "text-model"}, 200},
		{"other endpoint remains available", func(_ *unionFixture, r *unionRoutes) {
			r.routes = []service.CompositeModelRoute{
				{PublicModel: "text-model", TargetPlatform: service.PlatformOpenAI, Enabled: true, MatchType: service.CompositeRouteMatchExact},
				{PublicModel: "text-model", TargetPlatform: service.PlatformAnthropic, Enabled: true, MatchType: service.CompositeRouteMatchExact, Endpoint: service.CompositeRouteEndpointResponses},
			}
		}, []string{"image-model", "shared-model", "text-model"}, 200},
		{"exact overrides available prefix", func(_ *unionFixture, r *unionRoutes) {
			r.routes = []service.CompositeModelRoute{
				{PublicModel: "text-", TargetPlatform: service.PlatformOpenAI, Enabled: true, MatchType: service.CompositeRouteMatchPrefix},
				{PublicModel: "text-model", TargetPlatform: service.PlatformAnthropic, Enabled: true, MatchType: service.CompositeRouteMatchExact},
			}
		}, []string{"image-model", "shared-model"}, 200},
		{"priority winner unavailable", func(_ *unionFixture, r *unionRoutes) {
			r.routes = []service.CompositeModelRoute{
				{PublicModel: "text-model", TargetPlatform: service.PlatformOpenAI, Enabled: true, MatchType: service.CompositeRouteMatchExact},
				{PublicModel: "text-model", TargetPlatform: service.PlatformAnthropic, Enabled: true, MatchType: service.CompositeRouteMatchExact, Priority: -1},
			}
		}, []string{"image-model", "shared-model"}, 200},
		{"id winner unavailable", func(_ *unionFixture, r *unionRoutes) {
			r.routes = []service.CompositeModelRoute{
				{ID: 2, PublicModel: "text-model", TargetPlatform: service.PlatformOpenAI, Enabled: true, MatchType: service.CompositeRouteMatchExact},
				{ID: 1, PublicModel: "text-model", TargetPlatform: service.PlatformAnthropic, Enabled: true, MatchType: service.CompositeRouteMatchExact},
			}
		}, []string{"image-model", "shared-model"}, 200},
		{"route dependency", func(_ *unionFixture, r *unionRoutes) { r.err = errors.New("offline") }, nil, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnionFixture()
			f.groups.groups[1].Platform = service.PlatformComposite
			routes := &unionRoutes{routes: []service.CompositeModelRoute{
				{PublicModel: "public-text", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "text-model", Enabled: true, MatchType: service.CompositeRouteMatchExact},
				{PublicModel: "disabled-alias", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "text-model", Enabled: false, MatchType: service.CompositeRouteMatchExact},
				{PublicModel: "prefix-", TargetPlatform: service.PlatformOpenAI, UpstreamModel: "text-model", Enabled: true, MatchType: service.CompositeRouteMatchPrefix},
			}}
			tc.change(f, routes)
			w := unionRequest(f.router(t, "", nil, routes), http.MethodGet, "/v1/models")
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 200 {
				require.Equal(t, tc.want, unionIDs(t, w))
			}
		})
	}
}

type unionUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	fail       bool
	codexCalls int
	methods    []string
}

func (u *unionUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.methods = append(u.methods, req.Method)
	if u.fail {
		return nil, errors.New("discovery offline")
	}
	body := `{"data":[{"id":"upstream-text","context_window":123456,"source_account":` + strconv.FormatInt(id, 10) + `},{"id":"shared-model","source_account":` + strconv.FormatInt(id, 10) + `}]}`
	if req.URL.Query().Has("client_version") {
		u.codexCalls++
		body = `{"models":[{"slug":"gpt-6.1-sol","multi_agent_version":"v2","multi_agent_reasoning_effort":"xhigh"}]}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func (f *unionFixture) pinnedService(u *unionUpstream) *service.OpenAIGatewayService {
	for id, accounts := range f.accounts.groups {
		accounts[0].Credentials = map[string]any{"api_key": "synthetic-upstream-key", "base_url": "https://models.example/v1", "model_mapping": map[string]any{"public-text": "upstream-text", "shared-model": "shared-model"}}
		f.accounts.groups[id] = accounts
		f.groups.groups[id].CodexModelsManifestConfig = service.GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{accounts[0].ID}}
	}
	return service.NewOpenAIGatewayService(f.accounts, nil, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil, u, nil, nil, nil, nil, nil, nil, nil, nil)
}
func TestMultiGroupModelsUnionPinnedMappingAndDeterministicMetadata(t *testing.T) {
	f := newUnionFixture()
	u := &unionUpstream{}
	s := f.pinnedService(u)
	f.groups.groups[1].ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"public-*", "shared-model"}}
	f.rates.denied[1] = []string{"public-text"}
	r := f.router(t, "", s, nil)
	first := unionRequest(r, http.MethodGet, "/v1/models")
	require.Equal(t, []string{"public-text", "shared-model"}, unionIDs(t, first))
	require.Contains(t, first.Body.String(), `"context_window":123456`)
	require.Contains(t, first.Body.String(), `"source_account":101`)
	f.key.GroupIDs = []int64{2, 1}
	f.key.GroupID = &f.groups.groups[2].ID
	f.key.Group = f.groups.groups[2]
	f.groups.groups[1].RateMultiplier = 100
	f.groups.groups[2].RateMultiplier = 0.001
	second := unionRequest(f.router(t, "", s, nil), http.MethodGet, "/v1/models")
	require.Equal(t, first.Body.String(), second.Body.String())
	require.Empty(t, second.Header().Get("ETag"))
	require.Zero(t, u.codexCalls)
	for _, method := range u.methods {
		require.Equal(t, http.MethodGet, method, "discovery cannot issue paid generation POST")
	}
}
func TestMultiGroupModelsUnionPinnedFailureAndEmpty(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(strconv.FormatBool(empty), func(t *testing.T) {
			f := newUnionFixture()
			u := &unionUpstream{fail: !empty}
			s := f.pinnedService(u)
			if empty {
				for _, g := range f.groups.groups {
					g.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"missing"}}
				}
			}
			w := unionRequest(f.router(t, "", s, nil), http.MethodGet, "/v1/models")
			if empty {
				require.Empty(t, unionIDs(t, w))
			} else {
				require.Equal(t, 503, w.Code)
				require.NotContains(t, w.Body.String(), `"data"`)
			}
		})
	}
}
func TestMultiGroupModelsUnionCodexAndClientRestrictions(t *testing.T) {
	f := newUnionFixture()
	f.groups.groups[1].CodexCLIOnly = true
	r := f.router(t, "", nil, nil)
	require.Equal(t, []string{"image-model", "shared-model"}, unionIDs(t, unionRequest(r, http.MethodGet, "/v1/models")))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer synthetic-union-key")
	req.Header.Set("User-Agent", "codex-tui/0.163.0 (Ubuntu 22.4.0; x86_64) xterm-256color")
	req.Header.Set("Originator", "codex-tui")
	req.Header.Set("X-Codex-Window-Id", "test-codex-window")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, []string{"image-model", "shared-model", "text-model"}, unionIDs(t, w))
	f = newUnionFixture()
	u := &unionUpstream{}
	s := f.pinnedService(u)
	r = f.router(t, "", s, nil)
	w = unionRequest(r, http.MethodGet, "/v1/models?client_version=0.163.0")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"models"`)
	require.NotContains(t, w.Body.String(), `"data"`)
	require.Positive(t, u.codexCalls)
	require.Positive(t, f.probe.calls, "Codex must retain existing selected-group path")
}

func TestMultiGroupModelsUnionKeyExpiryAndQuota(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*service.APIKey)
		status int
	}{
		{"expired", func(k *service.APIKey) { past := time.Now().Add(-time.Hour); k.ExpiresAt = &past }, http.StatusForbidden},
		{"quota exhausted", func(k *service.APIKey) { k.Status = service.StatusAPIKeyQuotaExhausted }, http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnionFixture()
			tc.change(f.key)
			require.Equal(t, tc.status, unionRequest(f.router(t, "", nil, nil), http.MethodGet, "/v1/models").Code)
		})
	}
}

func TestMultiGroupModelsUnionRefreshesNonRepresentativeGroupAfterQueue(t *testing.T) {
	for _, change := range []string{"unbind", "disable", "deny"} {
		t.Run(change, func(t *testing.T) {
			f := newUnionFixture()
			f.key.ConcurrencyLimit = 1
			cache := testutil.NewRedisConcurrencyCache(t)
			f.concurrency = service.NewConcurrencyService(cache)
			f.concurrency.SetAPIKeyQueuePolicy(service.APIKeyQueuePolicy{MaxWaiting: 5, Timeout: 3 * time.Second})
			keyCache := cache.(service.APIKeyConcurrencyCache)
			queueCache := cache.(service.APIKeySlotQueueCache)
			require.NoError(t, keyCache.TrackAPIKeySlot(context.Background(), f.key.ID, "holder"))
			r := f.router(t, "", nil, nil)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- unionRequest(r, http.MethodGet, "/v1/models") }()
			require.Eventually(t, func() bool {
				_, waiting, err := queueCache.GetAPIKeyQueueStats(context.Background(), f.key.ID)
				return err == nil && waiting == 1
			}, 2*time.Second, time.Millisecond)
			switch change {
			case "unbind":
				f.keyRepo.mu.Lock()
				latest := *f.keyRepo.key
				latest.GroupIDs = []int64{1}
				f.keyRepo.key = &latest
				f.keyRepo.mu.Unlock()
			case "disable":
				f.groups.mu.Lock()
				g := *f.groups.groups[2]
				g.Status = service.StatusDisabled
				f.groups.groups[2] = &g
				f.groups.mu.Unlock()
			case "deny":
				f.rates.mu.Lock()
				f.rates.denied[2] = []string{"image-model", "shared-model"}
				f.rates.mu.Unlock()
			}
			require.NoError(t, keyCache.ReleaseAPIKeySlot(context.Background(), f.key.ID, "holder"))
			select {
			case w := <-done:
				require.Equal(t, []string{"shared-model", "text-model"}, unionIDs(t, w), "queued discovery must not reuse the non-representative group's stale permissions")
			case <-time.After(4 * time.Second):
				t.Fatal("queued discovery did not finish")
			}
		})
	}
}

type unionSettings struct {
	service.SettingRepository
	values map[string]string
}

func (r *unionSettings) GetValue(_ context.Context, key string) (string, error) {
	return r.values[key], nil
}
func TestMultiGroupModelsUnionConfiguredCodexPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		values   map[string]string
		official bool
		want     []string
	}{
		{"minimum version", map[string]string{service.SettingKeyMinCodexVersion: "0.200.0"}, true, []string{"shared-model", "text-model"}},
		{"blacklist", map[string]string{service.SettingKeyCodexCLIOnlyBlacklist: `[{"originator":"codex-tui"}]`}, true, []string{"shared-model", "text-model"}},
		{"configured whitelist", map[string]string{service.SettingKeyCodexCLIOnlyWhitelist: `[{"originator":"catalog-client","ua_contains":["catalog-client/"],"skip_engine_fingerprint":true}]`}, false, []string{"image-model", "shared-model", "text-model"}},
		{"incomplete whitelist rejected", map[string]string{service.SettingKeyCodexCLIOnlyWhitelist: `[{"originator":"catalog-client","skip_engine_fingerprint":true}]`}, false, []string{"shared-model", "text-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnionFixture()
			f.groups.groups[2].CodexCLIOnly = true
			f.settings = service.NewSettingService(&unionSettings{values: tc.values}, &config.Config{})
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer synthetic-union-key")
			if tc.official {
				req.Header.Set("User-Agent", "codex-tui/0.163.0 (Ubuntu 22.4.0; x86_64) xterm-256color")
				req.Header.Set("Originator", "codex-tui")
				req.Header.Set("X-Codex-Window-Id", "test-codex-window")
			} else {
				req.Header.Set("User-Agent", "catalog-client/1.0")
				req.Header.Set("Originator", "catalog-client")
			}
			w := httptest.NewRecorder()
			f.router(t, "", nil, nil).ServeHTTP(w, req)
			require.Equal(t, tc.want, unionIDs(t, w))
		})
	}
}

func TestMultiGroupModelsUnionDefaultAndAccountSources(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unionFixture)
		want   []string
	}{
		{"default source", func(f *unionFixture) {
			f.accounts.groups[1][0].Credentials = nil
			f.groups.groups[1].ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-6.1-sol", "unavailable-model"}}
		}, []string{"gpt-6.1-sol", "image-model", "shared-model"}},
		{"account group restriction", func(f *unionFixture) {
			f.accounts.groups[1][0].AccountGroups = []service.AccountGroup{{GroupID: 1, AllowedModels: []string{"shared-model"}}}
		}, []string{"image-model", "shared-model"}},
		{"unmapped restricted account", func(f *unionFixture) {
			f.accounts.groups[1][0].Credentials = nil
			f.accounts.groups[1][0].AccountGroups = []service.AccountGroup{{GroupID: 1, AllowedModels: []string{"gpt-6.1-sol"}}}
		}, []string{"gpt-6.1-sol", "image-model", "shared-model"}},
		{"wrong platform mapping", func(f *unionFixture) { f.accounts.groups[1][0].Platform = service.PlatformAnthropic }, []string{"image-model", "shared-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnionFixture()
			tc.change(f)
			require.Equal(t, tc.want, unionIDs(t, unionRequest(f.router(t, "", nil, nil), http.MethodGet, "/v1/models")))
		})
	}
}
