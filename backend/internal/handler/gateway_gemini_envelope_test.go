//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Only ForwardNative uses this reader. Scheduler hydration uses the separate
// gatewayMessagesHandlerRepo, so these hooks always run after selection/slots.
type gatewayGeminiEnvelopeRepo struct {
	service.AccountRepository
	source *gatewayMessagesHandlerRepo
	reads  int
	before func(int) error
}

func (r *gatewayGeminiEnvelopeRepo) GatewayRequestAccount(ctx context.Context, accountID, groupID int64) (*service.Account, *service.Group, error) {
	r.reads++
	if r.before != nil {
		if err := r.before(r.reads); err != nil {
			return nil, nil, err
		}
	}
	return r.source.GatewayRequestAccount(ctx, accountID, groupID)
}

type gatewayGeminiEnvelopeSlots struct {
	gatewayMessagesSlotCache
	users        int
	userReleases int
}

func (s *gatewayGeminiEnvelopeSlots) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	s.users++
	return true, nil
}

func (s *gatewayGeminiEnvelopeSlots) ReleaseUserSlot(context.Context, int64, string) error {
	s.users--
	s.userReleases++
	return nil
}

type gatewayGeminiEnvelopeFixture struct {
	handler   *GatewayHandler
	account   *service.Account
	repo      *gatewayGeminiEnvelopeRepo
	slots     *gatewayGeminiEnvelopeSlots
	usage     *openAIWSUsageHandlerUsageLogRepoStub
	billing   *compatStreamUsageBillingRepo
	c         *gin.Context
	rec       *httptest.ResponseRecorder
	peerCalls atomic.Int32
	peerCode  int
	peerBody  string
	cancel    context.CancelFunc
}

func newGatewayGeminiEnvelopeFixture(t *testing.T, stream, batch bool) *gatewayGeminiEnvelopeFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &gatewayGeminiEnvelopeFixture{peerCode: http.StatusOK,
		peerBody: `{"candidates":[{"content":{"parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.peerCalls.Add(1)
		if r.Header.Get("x-goog-api-key") != "synthetic-gateway-key" {
			t.Error("native Gemini transport lost its selected credential")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.peerCode)
		_, _ = io.WriteString(w, f.peerBody)
	}))
	t.Cleanup(peer.Close)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.Scheduling.LoadBatchEnabled = batch
	groupID := int64(9711)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformGemini, Status: service.StatusActive, RateMultiplier: 1}
	f.account = &service.Account{ID: 9712, Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
		Status: service.StatusActive, Schedulable: true, Concurrency: 2, Priority: 3,
		Credentials: map[string]any{"upstream_kind": "new_api", "api_key": "synthetic-gateway-key", "base_url": peer.URL + "/tenant/v1",
			"model_mapping": map[string]any{"gemini-2.5-flash": "wire-gemini"}},
		GroupIDs: []int64{groupID}, AccountGroups: []service.AccountGroup{{AccountID: 9712, GroupID: groupID}}}
	schedulerRepo := &gatewayMessagesHandlerRepo{account: f.account, group: group}
	f.repo = &gatewayGeminiEnvelopeRepo{source: schedulerRepo}
	f.slots = &gatewayGeminiEnvelopeSlots{}
	concurrency := service.NewConcurrencyService(f.slots)
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{}, nil, nil, nil, nil)
	f.usage = &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 2)}
	f.billing = &compatStreamUsageBillingRepo{}
	billingCache := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	upstream := repository.NewHTTPUpstream(cfg)
	gw := service.NewGatewayService(
		schedulerRepo, &fakeGroupRepo{group: group}, f.usage, f.billing, nil, nil, nil, &stickyWritebackCache{}, cfg,
		snapshot, concurrency, service.NewBillingService(cfg, nil), nil, nil, nil, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	f.handler = &GatewayHandler{gatewayService: gw, billingCacheService: billingCache, cfg: cfg, maxAccountSwitchesGemini: 1,
		concurrencyHelper:   NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0),
		geminiCompatService: service.NewGeminiMessagesCompatService(f.repo, nil, nil, nil, nil, nil, upstream, nil, cfg)}
	action := "generateContent"
	if stream {
		action = "streamGenerateContent"
	}
	f.rec = httptest.NewRecorder()
	f.c, _ = gin.CreateTestContext(f.rec)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxkey.Group, group))
	f.cancel = cancel
	t.Cleanup(cancel)
	f.c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:"+action,
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)).WithContext(ctx)
	f.c.Request.Header.Set("Content-Type", "application/json")
	f.c.Params = gin.Params{{Key: "modelAction", Value: "/gemini-2.5-flash:" + action}}
	key := &service.APIKey{ID: 9713, UserID: 9714, GroupID: &groupID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9714, Balance: 100, Concurrency: 1}}
	f.c.Set(string(middleware.ContextKeyAPIKey), key)
	f.c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
	return f
}

func (f *gatewayGeminiEnvelopeFixture) assertReleasedWithoutBilling(t *testing.T) {
	t.Helper()
	require.Equal(t, f.account.ID, f.slots.id, "request must reach real selection and acquire the shared account slot")
	require.Zero(t, f.slots.active)
	require.Equal(t, 1, f.slots.releases)
	require.Zero(t, f.slots.users)
	require.Equal(t, 1, f.slots.userReleases)
	require.Empty(t, f.usage.created)
	f.billing.mu.Lock()
	defer f.billing.mu.Unlock()
	require.Empty(t, f.billing.commands)
}

func assertGatewayGeminiSafeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"error":{"code":502,"message":"Unable to forward request to upstream","status":"INTERNAL"}}`, rec.Body.String())
	var envelope map[string]any
	decoder := json.NewDecoder(strings.NewReader(rec.Body.String()))
	require.NoError(t, decoder.Decode(&envelope))
	require.ErrorIs(t, decoder.Decode(&envelope), io.EOF, "only one terminal envelope may be written")
}

func TestGatewayProtocolGeminiFreshAdmissionErrorEnvelope(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			for _, change := range []string{"membership", "key", "status", "mapping-before-send"} {
				name := change + map[bool]string{false: "/nonstream", true: "/stream"}[stream] + map[bool]string{false: "/fallback", true: "/load-aware"}[batch]
				t.Run(name, func(t *testing.T) {
					f := newGatewayGeminiEnvelopeFixture(t, stream, batch)
					trigger := 1
					if change == "mapping-before-send" {
						trigger = 2 // initial ForwardNative admission passed; mapped URL has been built.
					}
					f.repo.before = func(read int) error {
						if read != trigger {
							return nil
						}
						switch change {
						case "membership":
							f.account.GroupIDs, f.account.AccountGroups = nil, nil
						case "key":
							f.account.Credentials = maps.Clone(f.account.Credentials)
							f.account.Credentials["api_key"] = "rotated-key-must-not-leak"
						case "status":
							f.account.Status = service.StatusDisabled
						case "mapping-before-send":
							f.account.Credentials = maps.Clone(f.account.Credentials)
							f.account.Credentials["model_mapping"] = map[string]any{"gemini-2.5-flash": "changed-wire-model"}
						}
						return nil
					}
					f.handler.GeminiV1BetaModels(f.c)
					assertGatewayGeminiSafeEnvelope(t, f.rec)
					require.Equal(t, trigger, f.repo.reads, "ordinary admission errors must terminate without reselection/replay")
					require.Zero(t, f.peerCalls.Load())
					f.assertReleasedWithoutBilling(t)
				})
			}
		}
	}
}

// A deterministic body error exercises real native streaming before/after its
// first write, without relying on socket buffering or transport retry timing.
type gatewayGeminiEnvelopeReadError struct{}

func (gatewayGeminiEnvelopeReadError) Read([]byte) (int, error) {
	return 0, errors.New("synthetic internal read failure: credential/config must not leak")
}

type gatewayGeminiEnvelopeUpstream struct {
	service.HTTPUpstream
	body  io.Reader
	calls int
}

func (u *gatewayGeminiEnvelopeUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(u.body)}, nil
}

func TestGatewayProtocolGeminiForwardErrorResponseOwnership(t *testing.T) {
	for _, control := range []string{"alive-uncommitted", "committed-headers-only", "committed-marker", "committed-error", "partial-stream", "cancelled"} {
		t.Run(control, func(t *testing.T) {
			f := newGatewayGeminiEnvelopeFixture(t, true, false)
			var upstream *gatewayGeminiEnvelopeUpstream
			const partial = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
			switch control {
			case "alive-uncommitted", "partial-stream":
				var body io.Reader = gatewayGeminiEnvelopeReadError{}
				if control == "partial-stream" {
					body = io.MultiReader(strings.NewReader(partial), body)
				}
				upstream = &gatewayGeminiEnvelopeUpstream{body: body}
				f.handler.geminiCompatService = service.NewGeminiMessagesCompatService(f.repo, nil, nil, nil, nil, nil, upstream, nil, f.handler.cfg)
				if control == "alive-uncommitted" {
					f.repo.before = func(int) error {
						f.c.Header("Content-Length", "999")
						f.c.Header("Content-Encoding", "gzip")
						return nil
					}
				}
			case "committed-headers-only", "committed-marker", "cancelled":
				f.repo.before = func(int) error {
					if control == "cancelled" {
						f.cancel()
						return context.Canceled
					}
					if control == "committed-marker" {
						service.MarkResponseCommitted(f.c)
						return errors.New("synthetic response already owned by forwarder")
					}
					f.c.Header("Content-Type", "text/event-stream")
					f.c.Status(http.StatusAccepted)
					f.c.Writer.WriteHeaderNow() // Written=true, Size=0: must not append an envelope.
					return errors.New("synthetic refusal after header commit")
				}
			case "committed-error":
				f.peerCode = http.StatusBadRequest
				f.peerBody = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Invalid fixture field"}}`
			}
			f.handler.GeminiV1BetaModels(f.c)
			switch control {
			case "alive-uncommitted":
				assertGatewayGeminiSafeEnvelope(t, f.rec)
				require.Empty(t, f.rec.Header().Get("Cache-Control"))
				require.Empty(t, f.rec.Header().Get("Connection"))
				require.Empty(t, f.rec.Header().Get("X-Accel-Buffering"))
				require.Empty(t, f.rec.Header().Get("Content-Length"))
				require.Empty(t, f.rec.Header().Get("Content-Encoding"))
			case "committed-headers-only":
				require.Equal(t, http.StatusAccepted, f.rec.Code)
				require.Zero(t, f.rec.Body.Len())
				require.Equal(t, "text/event-stream", f.rec.Header().Get("Content-Type"))
			case "committed-marker":
				require.True(t, service.IsResponseCommitted(f.c))
				require.False(t, f.c.Writer.Written())
				require.Zero(t, f.rec.Body.Len())
			case "committed-error":
				require.Equal(t, http.StatusBadRequest, f.rec.Code)
				require.Equal(t, f.peerBody, f.rec.Body.String())
				require.True(t, service.IsResponseCommitted(f.c))
			case "partial-stream":
				require.Equal(t, http.StatusOK, f.rec.Code)
				require.Equal(t, partial, f.rec.Body.String())
				require.Equal(t, "text/event-stream", f.rec.Header().Get("Content-Type"))
			case "cancelled":
				require.Equal(t, statusClientClosedRequest, f.c.Writer.Status())
				require.False(t, f.c.Writer.Written())
				require.Zero(t, f.rec.Body.Len())
			}
			if upstream != nil {
				require.Equal(t, 1, upstream.calls, "a native read failure must never replay")
			}
			wantPeers := int32(0)
			if control == "committed-error" {
				wantPeers = 1
			}
			require.Equal(t, wantPeers, f.peerCalls.Load())
			f.assertReleasedWithoutBilling(t)
		})
	}
}
