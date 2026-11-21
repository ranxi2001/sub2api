//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type gatewayMessagesHandlerRepo struct {
	service.AccountRepository
	account *service.Account
	group   *service.Group
	queries int
	reads   int
}

func (r *gatewayMessagesHandlerRepo) ListGatewayRequestCandidates(context.Context, int64, string) ([]service.Account, *service.Group, error) {
	r.queries++
	return []service.Account{*r.account}, r.group, nil
}

func (r *gatewayMessagesHandlerRepo) GatewayRequestAccount(context.Context, int64, int64) (*service.Account, *service.Group, error) {
	r.reads++
	return r.account, r.group, nil
}

type gatewayMessagesSlotCache struct {
	fakeConcurrencyCache
	id       int64
	limit    int
	active   int
	releases int
	before   func()
}

func (s *gatewayMessagesSlotCache) AcquireAccountSlot(_ context.Context, id int64, limit int, _ string) (bool, error) {
	s.id, s.limit = id, limit
	s.active++
	if s.before != nil {
		s.before()
	}
	return true, nil
}

func (s *gatewayMessagesSlotCache) ReleaseAccountSlot(context.Context, int64, string) error {
	s.active--
	s.releases++
	return nil
}

// These are real handler -> scheduler -> slot -> production HTTP adapter ->
// synthetic peer -> usage sink regressions, not direct Forward helper calls.
func TestGatewayProtocolMessagesHandlerSchedulerToUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, batch := range []bool{false, true} {
		for _, bound := range []bool{false, true} {
			for _, revoke := range []bool{false, true} {
				name := "fallback"
				if batch {
					name = "load-aware"
				}
				if bound {
					name += "/sticky"
				}
				if revoke {
					name += "/removed-after-slot"
				}
				t.Run(name, func(t *testing.T) {
					var peerCalls atomic.Int32
					peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						peerCalls.Add(1)
						body, err := io.ReadAll(req.Body)
						require.NoError(t, err)
						require.Equal(t, "/tenant/v1/messages", req.URL.Path)
						require.Equal(t, "upstream-claude", gjson.GetBytes(body, "model").String())
						require.Equal(t, "synthetic-gateway-key", req.Header.Get("x-api-key"))
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"msg_gateway","type":"message","role":"assistant","model":"upstream-claude","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":7}}`)
					}))
					defer peer.Close()
					cfg := &config.Config{}
					cfg.Security.URLAllowlist.AllowPrivateHosts = true
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					cfg.Gateway.Scheduling.LoadBatchEnabled = batch
					groupID := int64(9591)
					group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
					account := &service.Account{ID: 9592, Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
						Status: service.StatusActive, Schedulable: true, Concurrency: 2, Priority: 3,
						Credentials: map[string]any{"upstream_kind": "new_api", "api_key": "synthetic-gateway-key", "base_url": peer.URL + "/tenant/v1",
							"model_mapping": map[string]any{"claude-sonnet-4.5": "upstream-claude"}},
						GroupIDs: []int64{groupID}, AccountGroups: []service.AccountGroup{{AccountID: 9592, GroupID: groupID}}}
					repo := &gatewayMessagesHandlerRepo{account: account, group: group}
					slots := &gatewayMessagesSlotCache{}
					if revoke {
						slots.before = func() { account.GroupIDs, account.AccountGroups = nil, nil }
					}
					concurrency := service.NewConcurrencyService(slots)
					cache := &stickyWritebackCache{}
					if bound {
						cache.accountID = account.ID
					}
					// Warm shared snapshot is empty: no gateway platform view is cached.
					snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{}, nil, nil, nil, nil)
					usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
					billing := &compatStreamUsageBillingRepo{}
					billingCache := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil, cfg, nil)
					t.Cleanup(billingCache.Stop)
					gw := service.NewGatewayService(
						repo, &fakeGroupRepo{group: group}, usage, billing, nil, nil, nil, cache, cfg,
						snapshot, concurrency, service.NewBillingService(cfg, nil), nil, nil, nil, repository.NewHTTPUpstream(cfg),
						&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
					)
					h := &GatewayHandler{gatewayService: gw, billingCacheService: billingCache, cfg: cfg, maxAccountSwitches: 1,
						concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0)}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4.5","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
					c.Request.Header.Set("Content-Type", "application/json")
					key := &service.APIKey{ID: 9593, UserID: 9594, GroupID: &groupID, Group: group, User: &service.User{ID: 9594, Balance: 100}}
					c.Set(string(middleware.ContextKeyAPIKey), key)
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9594})
					h.Messages(c)
					require.Equal(t, account.ID, slots.id)
					require.Equal(t, account.Concurrency, slots.limit)
					require.Zero(t, slots.active)
					require.Positive(t, slots.releases)
					require.Positive(t, repo.reads)
					if revoke {
						require.Zero(t, peerCalls.Load())
						require.Empty(t, usage.created)
						return
					}
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, int32(1), peerCalls.Load())
					select {
					case log := <-usage.created:
						require.Equal(t, account.ID, log.AccountID)
						require.Equal(t, service.PlatformAnthropic, log.Platform)
						require.Equal(t, 12, log.InputTokens)
						require.NotNil(t, log.ProcurementCostSnapshot)
						require.Equal(t, service.PlatformAnthropic, log.ProcurementCostSnapshot.TargetPlatform)
					case <-time.After(3 * time.Second):
						t.Fatal("handler did not deliver usage to its sink")
					}
					require.False(t, account.IsSchedulable())
				})
			}
		}
	}
}
