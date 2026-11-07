//go:build unit

package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type unionKeys struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *unionKeys) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	k := *r.key
	u := *k.User
	k.User = &u
	return &k, nil
}
func (*unionKeys) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type unionGroups struct {
	service.GroupRepository
	groups map[int64]*service.Group
	err    error
}

func (r *unionGroups) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
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
	denied map[int64][]string
	err    error
}

func (r *unionRates) GetDeniedModelsByUserAndGroup(_ context.Context, _, id int64) ([]string, error) {
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
	key      *service.APIKey
	groups   *unionGroups
	accounts *unionAccounts
	rates    *unionRates
	subs     *unionSubscriptions
	probe    *unionProbe
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
	keys := service.NewAPIKeyService(&unionKeys{key: f.key}, nil, f.groups, f.subs, f.rates, nil, cfg)
	var resolver *service.CompositeRouteResolver
	if routeRepo != nil {
		resolver = service.NewCompositeRouteResolver(routeRepo)
	}
	gateway := service.NewGatewayService(f.accounts, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, resolver, nil, nil)
	h := &handler.Handlers{
		Gateway:       handler.NewGatewayHandler(gateway, openAI, nil, nil, nil, nil, nil, nil, keys, nil, nil, nil, nil, cfg, nil),
		OpenAIGateway: handler.NewOpenAIGatewayHandler(openAI, nil, nil, nil, nil, nil, nil, nil, cfg),
		AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
	}
	r := gin.New()
	if forced != "" {
		r.Use(middleware.ForcePlatform(forced))
	}
	RegisterGatewayRoutes(r, h, middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg), keys, nil, nil, nil, resolver, cfg)
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
