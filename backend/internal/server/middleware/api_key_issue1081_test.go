package middleware

import (
	"bytes"
	"context"
	"github.com/Wei-Shaw/sub2api/internal/regional"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type issue1081AdminUsers struct {
	service.UserRepository
	user *service.User
}

func (r issue1081AdminUsers) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}
func (r issue1081AdminUsers) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func TestIssue1081SettingRouteRequiresActiveAdmin(t *testing.T) {
	for _, tc := range []struct {
		role string
		want int
	}{{"", 401}, {service.RoleUser, 403}, {service.RoleObserver, 403}, {service.RoleAdmin, 204}} {
		t.Run(tc.role, func(t *testing.T) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "issue1081-fixture-secret", ExpireHour: 1}}
			auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			actor := &service.User{ID: 7, Role: tc.role, Status: service.StatusActive}
			r := gin.New()
			r.Use(gin.HandlerFunc(NewAdminAuthMiddleware(auth, service.NewUserService(issue1081AdminUsers{user: actor}, nil, nil, nil), nil, nil)))
			writes := 0
			r.PUT("/api/v1/admin/settings", func(c *gin.Context) { writes++; c.Status(204) })
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", strings.NewReader(`{"api_key_multi_group_enabled":false}`))
			req.Header.Set("Content-Type", "application/json")
			if tc.role != "" {
				token, err := auth.GenerateToken(context.Background(), actor)
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want != 204 {
				require.Zero(t, writes)
			}
		})
	}
}

func TestIssue1081QueueRevalidationRejectsDormantExtra(t *testing.T) {
	primary := int64(1)
	extra := int64(2)
	initial := &service.APIKey{ID: 11, UserID: 7, GroupID: &extra, GroupIDs: []int64{1, 2}, Status: service.StatusActive, User: &service.User{ID: 7, Status: service.StatusActive}, Group: &service.Group{ID: 2, Status: service.StatusActive}}
	latest := *initial
	latest.GroupID = &primary
	latest.GroupIDs = []int64{1}
	latest.Group = &service.Group{ID: 1, Status: service.StatusActive}
	revalidate := newAPIKeyAuthRevalidator(func(context.Context, string) (*service.APIKey, error) { return &latest, nil }, "fixture-key", "127.0.0.1", initial)
	_, err := revalidate(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "configuration changed")
	// Retained extras do not invalidate a main-group request when re-enabled.
	initial = &latest
	revalidate = newAPIKeyAuthRevalidator(func(context.Context, string) (*service.APIKey, error) {
		k := latest
		k.GroupIDs = []int64{1, 2}
		return &k, nil
	}, "fixture-key", "127.0.0.1", initial)
	_, err = revalidate(context.Background())
	require.NoError(t, err)
}

type issue1081LivePolicy struct{ enabled bool }

func (p *issue1081LivePolicy) APIKeyMultiGroupEnabled(context.Context) (bool, error) {
	return p.enabled, nil
}

type issue1081QueueKeys struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r issue1081QueueKeys) GetByKeyForAuth(context.Context, string) (*service.APIKey, error) {
	k := *r.key
	return &k, nil
}

type issue1081QueueGroups struct {
	service.GroupRepository
	groups map[int64]*service.Group
}

func (r issue1081QueueGroups) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	return r.groups[id], nil
}
func TestIssue1081QueueUsesAuthoritativePolicyBeforePinnedExtra(t *testing.T) {
	id := int64(1)
	groups := map[int64]*service.Group{1: {ID: 1, Status: service.StatusActive}, 2: {ID: 2, Status: service.StatusActive}}
	saved := &service.APIKey{ID: 11, UserID: 7, Key: "fixture-key", GroupID: &id, GroupIDs: []int64{1, 2}, Status: service.StatusActive, User: &service.User{ID: 7, Status: service.StatusActive}, Group: groups[1]}
	policy := &issue1081LivePolicy{enabled: true}
	svc := service.NewAPIKeyService(issue1081QueueKeys{key: saved}, nil, issue1081QueueGroups{groups: groups}, nil, nil, nil, &config.Config{})
	svc.SetGroupPolicy(policy)
	initial, err := svc.PinAPIKeyGroup(context.Background(), saved, 2)
	require.NoError(t, err)
	revalidate := newAPIKeyAuthRevalidator(func(ctx context.Context, credential string) (*service.APIKey, error) {
		latest, err := svc.GetByKeyUncached(ctx, credential)
		if err != nil {
			return nil, err
		}
		return svc.PinAPIKeyGroup(ctx, latest, 2)
	}, saved.Key, "127.0.0.1", initial)
	_, err = revalidate(context.Background())
	require.NoError(t, err)
	policy.enabled = false
	_, err = revalidate(context.Background())
	require.Error(t, err)
	require.Equal(t, []int64{1, 2}, saved.GroupIDs)
}

type issue1081RegionalRecords struct {
	enabled bool
	records []service.RegionalAuthRecord
}

func (r *issue1081RegionalRecords) ListRegionalAuth(context.Context, []int64) ([]service.RegionalAuthRecord, error) {
	return r.records, nil
}
func (r *issue1081RegionalRecords) APIKeyMultiGroupEnabled(context.Context) (bool, error) {
	return r.enabled, nil
}
func TestIssue1081RegionalMiddlewareReadsSignedPolicyAndKeepsServingGate(t *testing.T) {
	ctx := context.Background()
	lookup := bytes.Repeat([]byte{1}, 32)
	codec, err := regional.NewCodec(map[string][]byte{"test": bytes.Repeat([]byte{2}, 32)})
	require.NoError(t, err)
	replica, err := regional.OpenReplica(filepath.Join(t.TempDir(), "auth"), regional.Config{RegionID: "us", MaxSnapshotAgeSeconds: 60}, regional.SnapshotAuth, codec, service.RegionalAuthValidator([]int64{7}), true)
	require.NoError(t, err)
	reader, err := service.NewRegionalAuthReader(replica, lookup)
	require.NoError(t, err)
	id := int64(1)
	key := &service.APIKey{ID: 11, UserID: 7, Key: "fixture-key", GroupID: &id, GroupIDs: []int64{1, 2}, Status: service.StatusActive, User: &service.User{ID: 7, Status: service.StatusActive}, Group: &service.Group{ID: 1, Status: service.StatusActive}}
	source := &issue1081RegionalRecords{records: []service.RegionalAuthRecord{{APIKey: key}}}
	publish := func(revision uint64) {
		payload, err := service.ExportRegionalAuth(ctx, source, []int64{7}, lookup)
		require.NoError(t, err)
		now := time.Now()
		require.NoError(t, replica.Publish(ctx, "test", revision, now, now.Add(time.Minute), payload))
	}
	observed := false
	r := gin.New()
	r.Use(RegionalAPIKeyAuth(regionalAuthAuthenticatorFunc(reader.GetByKey), &config.Config{}))
	r.Use(func(c *gin.Context) {
		observed = true
		k, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.Equal(t, []int64{1}, k.ConfiguredGroupIDs())
		c.AbortWithStatusJSON(503, gin.H{"error": "regional gateway admission is not enabled"})
	})
	r.POST("/v1/responses", func(c *gin.Context) { t.Fatal("regional generation gate bypassed") })
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"fixture-model"}`))
		req.Header.Set("Authorization", "Bearer fixture-key")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	publish(1)
	w := request()
	require.True(t, observed)
	require.Contains(t, w.Body.String(), "admission is not enabled")
	source.enabled = true
	publish(2)
	observed = false
	w = request()
	require.False(t, observed)
	require.Contains(t, w.Body.String(), "MULTI_GROUP_ROUTING_UNAVAILABLE")
	source.enabled = false
	source.records = nil
	publish(3)
	w = request()
	require.Equal(t, 401, w.Code)
}
