//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestModerationUpstreamSystemOneLocalDenial(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			var audits, generations atomic.Int32
			audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				audits.Add(1)
				_, _ = w.Write([]byte(`{"results":[{"category_scores":{"sexual":0.99}}]}`))
			}))
			defer audit.Close()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { generations.Add(1); w.WriteHeader(500) }))
			defer provider.Close()
			group := &service.Group{ID: 9, Platform: service.PlatformTypeSafe, Status: service.StatusActive, Hydrated: true}
			account := service.Account{ID: 1, Name: "fixture", Platform: service.PlatformTypeSafe, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{9}, Credentials: map[string]any{"api_key": "fixture", "base_url": provider.URL}}
			accounts := openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}
			snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{&account}}, nil, nil, nil, nil)
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Security.URLAllowlist.AllowPrivateHosts = true
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			gateway := service.NewGatewayService(accounts, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, cfg, snapshot, nil, nil, nil, nil, nil, testutil.NewRealHTTPUpstream(cfg), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			defer billing.Stop()
			membership := 9
			if stale {
				membership = 10
			}
			raw := fmt.Sprintf(`{"enabled":true,"mode":"pre_block","all_groups":true,"sample_rate":100,"base_url":%q,"api_keys":["fixture"],"upstream_account_scope":"selected","upstream_account_ids":[1],"upstream_account_bindings":{"1":{"platform":"typesafe","type":"apikey","group_ids":[%d]}}}`, audit.URL, membership)
			settings := &contentModerationHandlerSettingRepo{values: map[string]string{service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: raw}}
			h := &GatewayHandler{gatewayService: gateway, billingCacheService: billing, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0), cfg: cfg, contentModerationService: service.NewContentModerationService(settings, &contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil, nil, accounts)}
			c, w := newSystemOneHandlerContext(validSystemOneHandlerBody)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, UserID: 8, GroupID: &group.ID, Group: group, User: &service.User{ID: 8, Status: service.StatusActive}})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 8})
			h.SystemOne(c)
			code := 403
			if stale {
				code = 503
			}
			require.Equal(t, code, w.Code, w.Body.String())
			require.True(t, json.Valid(w.Body.Bytes()), w.Body.String())
			require.Equal(t, 1, strings.Count(w.Body.String(), `"error":`))
			require.Zero(t, generations.Load())
			if stale {
				require.Zero(t, audits.Load())
			} else {
				require.Equal(t, int32(1), audits.Load())
			}
		})
	}
}
