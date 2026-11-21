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
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type gatewayMessagesHandlerRepo struct {
	service.AccountRepository
	account   *service.Account
	group     *service.Group
	queries   int
	reads     int
	afterRead func()
}

func (r *gatewayMessagesHandlerRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	return nil, nil
}

func (r *gatewayMessagesHandlerRepo) ListGatewayRequestCandidates(context.Context, int64, string) ([]service.Account, *service.Group, error) {
	r.queries++
	return []service.Account{*r.account}, r.group, nil
}

func (r *gatewayMessagesHandlerRepo) GatewayRequestAccount(context.Context, int64, int64) (*service.Account, *service.Group, error) {
	r.reads++
	snapshot := *r.account
	snapshot.AccountGroups = append([]service.AccountGroup(nil), r.account.AccountGroups...)
	if r.afterRead != nil {
		r.afterRead()
	}
	return &snapshot, r.group, nil
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
					c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
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

type gatewayMessagesChannelRepo struct {
	service.ChannelRepository
	channel service.Channel
	groupID int64
}

func (r *gatewayMessagesChannelRepo) ListAll(context.Context) ([]service.Channel, error) {
	return []service.Channel{r.channel}, nil
}

func (r *gatewayMessagesChannelRepo) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{r.groupID: service.PlatformAnthropic}, nil
}

func TestGatewayProtocolMessagesChannelMappingPreservesBindingModel(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, scenario := range []string{"channel", "channel-account-mapping", "revoke-selection-model", "no-mapping", "legacy-channel", "legacy-no-mapping"} {
			name := "fallback/" + scenario
			if batch {
				name = "load-aware/" + scenario
			}
			t.Run(name, func(t *testing.T) {
				const publicModel = "claude-sonnet-4.5"
				mapped := scenario != "no-mapping" && scenario != "legacy-no-mapping"
				legacy := strings.HasPrefix(scenario, "legacy-")
				wantWire := publicModel
				if mapped {
					wantWire = "relay-sonnet"
				}
				if scenario == "channel-account-mapping" {
					wantWire = "account-relay-sonnet"
				}
				var peerCalls atomic.Int32
				peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					peerCalls.Add(1)
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.Equal(t, "/tenant/v1/messages", req.URL.Path)
					require.Equal(t, wantWire, gjson.GetBytes(body, "model").String())
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"msg_mapping","type":"message","role":"assistant","model":"`+wantWire+`","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":7}}`)
				}))
				defer peer.Close()
				cfg := &config.Config{}
				cfg.Security.URLAllowlist.AllowPrivateHosts = true
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				cfg.Gateway.Scheduling.LoadBatchEnabled = batch
				groupID := int64(9595)
				group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1,
					ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{publicModel}}}
				if scenario == "revoke-selection-model" {
					// Isolate the binding guard: a separate public allowlist must not
					// mask a final admission that incorrectly accepts the wire alias.
					group.ModelAllowlist = service.GroupModelAllowlist{}
				}
				account := &service.Account{ID: 9596, Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
					Status: service.StatusActive, Schedulable: true, Concurrency: 2,
					Credentials: map[string]any{"upstream_kind": "new_api", "api_key": "synthetic-only", "base_url": peer.URL + "/tenant/v1"},
					GroupIDs:    []int64{groupID}, AccountGroups: []service.AccountGroup{{AccountID: 9596, GroupID: groupID, AllowedModels: []string{publicModel}}}}
				if scenario == "channel-account-mapping" {
					// Selection checks source keys; forwarding maps the channel result,
					// exactly as the existing handler and legacy account contract do.
					account.Credentials["model_mapping"] = map[string]any{publicModel: "selection-target-not-wire", "relay-sonnet": wantWire}
				}
				sharedCache := &fakeSchedulerCache{}
				if legacy {
					account.Platform, account.Type = service.PlatformAnthropic, service.AccountTypeAPIKey
					account.Credentials["base_url"] = peer.URL + "/tenant"
					account.Extra = map[string]any{"anthropic_passthrough": true}
					sharedCache.accounts = []*service.Account{account}
				}
				repo := &gatewayMessagesHandlerRepo{account: account, group: group}
				slots := &gatewayMessagesSlotCache{}
				revokedAtRead := 0
				if scenario == "revoke-selection-model" {
					repo.afterRead = func() {
						if slots.active > 0 && revokedAtRead == 0 {
							// The returned hydration snapshot still permits selection.
							// The next authority read sees only the wire alias permitted.
							revokedAtRead = repo.reads
							account.AccountGroups = []service.AccountGroup{{AccountID: account.ID, GroupID: groupID, AllowedModels: []string{"relay-sonnet"}}}
						}
					}
				}
				channelRepo := &gatewayMessagesChannelRepo{groupID: groupID, channel: service.Channel{ID: 9597, Status: service.StatusActive,
					GroupIDs: []int64{groupID}, BillingModelSource: service.BillingModelSourceRequested}}
				if mapped {
					channelRepo.channel.ModelMapping = map[string]map[string]string{service.PlatformAnthropic: {publicModel: "relay-sonnet"}}
				}
				channels := service.NewChannelService(channelRepo, nil, nil, nil, nil)
				concurrency := service.NewConcurrencyService(slots)
				usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
				billingCache := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billingCache.Stop)
				gw := service.NewGatewayService(
					repo, &fakeGroupRepo{group: group}, usage, &compatStreamUsageBillingRepo{}, nil, nil, nil, nil, cfg,
					service.NewSchedulerSnapshotService(sharedCache, nil, nil, nil, nil), concurrency, service.NewBillingService(cfg, nil), nil, nil, nil, repository.NewHTTPUpstream(cfg),
					&service.DeferredService{}, nil, nil, nil, nil, nil, nil, channels, nil, nil, nil, nil,
				)
				h := &GatewayHandler{gatewayService: gw, billingCacheService: billingCache, cfg: cfg, maxAccountSwitches: 1,
					concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0)}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"`+publicModel+`","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
				c.Request.Header.Set("Content-Type", "application/json")
				key := &service.APIKey{ID: 9598, UserID: 9599, GroupID: &groupID, Group: group, User: &service.User{ID: 9599, Balance: 100}}
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9599})
				h.Messages(c)
				require.Equal(t, account.ID, slots.id)
				require.Equal(t, 2, slots.limit)
				require.Zero(t, slots.active)
				require.Positive(t, slots.releases)
				if scenario == "revoke-selection-model" {
					require.Positive(t, revokedAtRead)
					require.Greater(t, repo.reads, revokedAtRead, "final admission must read the changed binding after hydration")
					require.Zero(t, peerCalls.Load())
					require.Empty(t, usage.created)
					return
				}
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, int32(1), peerCalls.Load())
				select {
				case log := <-usage.created:
					require.Equal(t, account.ID, log.AccountID)
					require.Equal(t, publicModel, log.RequestedModel)
					channelModel := publicModel
					if mapped {
						channelModel = "relay-sonnet"
					}
					require.Equal(t, channelModel, log.Model, "retain the existing Forward model; RequestedModel records the public name")
					require.NotNil(t, log.UpstreamModel)
					require.Equal(t, wantWire, *log.UpstreamModel)
					require.Equal(t, 12, log.InputTokens)
					if mapped {
						chain := publicModel + "→relay-sonnet"
						if scenario == "channel-account-mapping" {
							chain += "→" + wantWire
						}
						require.NotNil(t, log.ModelMappingChain)
						require.Equal(t, chain, *log.ModelMappingChain)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("handler did not deliver mapped usage")
				}
			})
		}
	}
}
