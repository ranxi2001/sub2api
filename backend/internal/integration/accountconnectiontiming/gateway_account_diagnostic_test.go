//go:build unit

package accountconnectiontiming

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayDiagnosticAccounts struct {
	service.AccountRepository
	mu       sync.Mutex
	account  *service.Account
	groups   map[int64]*service.Group
	live     map[int64]*service.Account
	readErr  map[int64]error
	admitted []int64
	writes   atomic.Int64
}

func (r *gatewayDiagnosticAccounts) GetByID(context.Context, int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneGatewayTestAccount(r.account), nil
}

func (r *gatewayDiagnosticAccounts) GatewayRequestAccount(_ context.Context, _, id int64) (*service.Account, *service.Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admitted = append(r.admitted, id)
	if err := r.readErr[id]; err != nil {
		return nil, nil, err
	}
	current := r.account
	if live := r.live[id]; live != nil {
		current = live
	}
	return cloneGatewayTestAccount(current), r.groups[id], nil
}

func (r *gatewayDiagnosticAccounts) SetError(context.Context, int64, string) error {
	r.writes.Add(1)
	return nil
}

func (r *gatewayDiagnosticAccounts) UpdateExtra(context.Context, int64, map[string]any) error {
	r.writes.Add(1)
	return nil
}

func gatewayDiagnosticFixture(base, platform string) (*gatewayDiagnosticAccounts, *config.Config) {
	fixture, cfg := gatewayTestFixture(base, platform)
	// Match createAccountRecord's stored gateway default, without enabling it.
	fixture.account.Schedulable = false
	return &gatewayDiagnosticAccounts{account: fixture.account, groups: map[int64]*service.Group{1010: fixture.group}}, cfg
}

func gatewayDiagnosticRouter(repo service.AccountRepository, cfg *config.Config, recovery *service.RateLimitService) (*gin.Engine, *gatewayTestTransport) {
	upstream := &gatewayTestTransport{HTTPUpstream: repository.NewHTTPUpstream(cfg), seen: make(chan gatewayTestEgress, 16)}
	svc := service.NewAccountTestService(repo, nil, nil, nil, nil, upstream, cfg, nil)
	if recovery == nil {
		// Retain the nonnil recovery sentinel: an accidental callback must fail.
		recovery = &service.RateLimitService{}
	}
	h := admin.NewAccountHandler(nil, nil, nil, nil, nil, nil, recovery, nil, svc, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/:id/test", h.Test)
	return router, upstream
}

func TestGatewayAccountConnectionNonSchedulableDiagnostic(t *testing.T) {
	for _, kind := range []string{"new_api", "sub2api"} {
		for _, state := range []string{"creation-default", "performance-paused", "error", "global-cooldown", "temporary-cooldown", "overloaded", "model-cooldown", "expired"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				var hits atomic.Int64
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, gatewayTestStream("responses"))
				}))
				defer origin.Close()
				repo, cfg := gatewayDiagnosticFixture(origin.URL, service.PlatformOpenAI)
				repo.account.Credentials["upstream_kind"] = kind
				future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
				switch state {
				case "performance-paused":
					repo.account.PerformancePaused = true
				case "error":
					repo.account.Status, repo.account.ErrorMessage = service.StatusError, "synthetic existing error"
				case "global-cooldown":
					repo.account.RateLimitResetAt = &future
				case "temporary-cooldown":
					repo.account.TempUnschedulableUntil = &future
				case "overloaded":
					repo.account.OverloadUntil = &future
				case "model-cooldown":
					repo.account.Extra["model_rate_limits"] = map[string]any{"gpt-6.1-sol": map[string]any{"rate_limit_reset_at": future.Format(time.RFC3339)}}
				case "expired":
					repo.account.ExpiresAt, repo.account.AutoPauseOnExpired = &past, true
				}
				before := cloneGatewayTestAccount(repo.account)
				router, transport := gatewayDiagnosticRouter(repo, cfg, nil)
				recorder := gatewayTestRequest(router, context.Background(), `{"model_id":"gpt-6.1-sol"}`)
				require.Contains(t, recorder.Body.String(), `"success":true`)
				require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
				require.Equal(t, int64(1), hits.Load())
				require.Equal(t, int64(1), transport.calls.Load())
				require.Equal(t, int64(1), transport.closed.Load())
				require.Zero(t, repo.writes.Load())
				require.Equal(t, before, repo.account)
			})
		}
	}
}

type gatewayRecoverySpy struct {
	service.AccountRepository
	repo     *gatewayDiagnosticAccounts
	reads    atomic.Int64
	writes   atomic.Int64
	canceled atomic.Bool
}

func (r *gatewayRecoverySpy) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	r.reads.Add(1)
	if ctx.Err() != nil {
		r.canceled.Store(true)
	}
	return r.repo.GetByID(ctx, id)
}

func (r *gatewayRecoverySpy) clear(ctx context.Context, field string) error {
	r.writes.Add(1)
	if ctx.Err() != nil {
		r.canceled.Store(true)
	}
	r.repo.mu.Lock()
	defer r.repo.mu.Unlock()
	switch field {
	case "error":
		r.repo.account.Status, r.repo.account.ErrorMessage = service.StatusActive, ""
	case "global":
		r.repo.account.RateLimitedAt, r.repo.account.RateLimitResetAt, r.repo.account.OverloadUntil = nil, nil, nil
	case "quota":
		delete(r.repo.account.Extra, "antigravity_quota_scopes")
	case "model":
		delete(r.repo.account.Extra, "model_rate_limits")
	case "temporary":
		r.repo.account.TempUnschedulableUntil = nil
	}
	return nil
}

func (r *gatewayRecoverySpy) ClearError(ctx context.Context, _ int64) error {
	return r.clear(ctx, "error")
}

func (r *gatewayRecoverySpy) ClearRateLimit(ctx context.Context, _ int64) error {
	return r.clear(ctx, "global")
}

func (r *gatewayRecoverySpy) ClearAntigravityQuotaScopes(ctx context.Context, _ int64) error {
	return r.clear(ctx, "quota")
}

func (r *gatewayRecoverySpy) ClearModelRateLimits(ctx context.Context, _ int64) error {
	return r.clear(ctx, "model")
}

func (r *gatewayRecoverySpy) ClearTempUnschedulable(ctx context.Context, _ int64) error {
	return r.clear(ctx, "temporary")
}

func TestGatewayAccountConnectionSuccessSkipsRecoveryAfterKeepaliveCleanup(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "gateway-observation", true: "legacy-recovers"}[legacy], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var hits atomic.Int64
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, gatewayTestStream("responses"))
			}))
			defer origin.Close()
			repo, cfg := gatewayDiagnosticFixture(origin.URL, service.PlatformOpenAI)
			past := time.Now().Add(-time.Hour)
			repo.account.RateLimitResetAt = &past
			// Isolate cleanup from the separate default-false diagnostic case:
			// this gateway already passes the old forwarding health gate.
			repo.account.Schedulable = true
			if legacy {
				repo.account.Platform, repo.account.Type = service.PlatformOpenAI, service.AccountTypeAPIKey
				repo.account.Status, repo.account.ErrorMessage = service.StatusError, "synthetic preexisting error"
			}
			spy := &gatewayRecoverySpy{repo: repo}
			recovery := service.NewRateLimitService(spy, nil, cfg, nil, nil)
			router, transport := gatewayDiagnosticRouter(repo, cfg, recovery)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			finished := make(chan *httptest.ResponseRecorder, 1)
			go func() { finished <- gatewayTestRequest(router, ctx, `{"model_id":"gpt-6.1-sol"}`) }()
			timingWait(t, entered)
			// A cooldown added while the test is in flight must also survive an
			// observational gateway success, even when the old global reset expired.
			repo.mu.Lock()
			repo.account.Extra = maps.Clone(repo.account.Extra)
			repo.account.Extra["model_rate_limits"] = map[string]any{"unrelated-model": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}
			before := cloneGatewayTestAccount(repo.account)
			repo.mu.Unlock()
			close(release)
			select {
			case recorder := <-finished:
				require.Contains(t, recorder.Body.String(), `"success":true`)
				require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
			case <-ctx.Done():
				t.Fatal("handler did not finish after upstream completion")
			}
			require.Equal(t, int64(1), hits.Load())
			require.Equal(t, int64(1), transport.closed.Load())
			require.Zero(t, repo.writes.Load())
			require.False(t, spy.canceled.Load(), "legacy recovery must receive the original valid context")
			if legacy {
				require.Equal(t, int64(2), spy.reads.Load())
				require.Equal(t, int64(5), spy.writes.Load())
				require.Equal(t, service.StatusActive, repo.account.Status)
				require.Nil(t, repo.account.RateLimitResetAt)
				require.NotContains(t, repo.account.Extra, "model_rate_limits")
			} else {
				require.Zero(t, spy.reads.Load())
				require.Zero(t, spy.writes.Load())
				require.Equal(t, before, repo.account)
			}
		})
	}
}

func TestGatewayAccountConnectionAdminMultiGroupSelection(t *testing.T) {
	for _, kind := range []string{"new_api", "sub2api"} {
		for _, scenario := range []string{"unique-cross-platform", "observer-filter", "inactive-group", "removed-membership", "binding-filter", "allowlist-filter", "no-eligible-route", "same-protocol-ambiguous", "composite-private-alias", "storage-error", "explicit-route", "explicit-unauthorized"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				var hits atomic.Int64
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, gatewayTestStream("responses"))
				}))
				defer origin.Close()
				repo, cfg := gatewayDiagnosticFixture(origin.URL, service.PlatformOpenAI)
				repo.account.Credentials["upstream_kind"] = kind
				repo.account.GroupIDs = []int64{1011, 1010}
				repo.account.AccountGroups = []service.AccountGroup{{GroupID: 1011}, {GroupID: 1010}}
				repo.groups[1011] = &service.Group{ID: 1011, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true}
				ctx := context.Background()
				body := `{"model_id":"gpt-6.1-sol"}`
				wantSuccess := true
				switch scenario {
				case "observer-filter":
					repo.groups[1011].Platform = service.PlatformOpenAI
					ctx = service.WithObserverScope(ctx, []int64{1010})
				case "inactive-group":
					repo.groups[1011].Platform, repo.groups[1011].Status = service.PlatformOpenAI, service.StatusDisabled
				case "removed-membership":
					repo.groups[1011].Platform = service.PlatformOpenAI
					live := cloneGatewayTestAccount(repo.account)
					live.GroupIDs = []int64{1010}
					live.AccountGroups = []service.AccountGroup{{GroupID: 1010}}
					repo.live = map[int64]*service.Account{1011: live}
				case "binding-filter":
					repo.groups[1011].Platform = service.PlatformOpenAI
					repo.account.AccountGroups[0].AllowedModels = []string{"other-model"}
				case "allowlist-filter":
					repo.groups[1011].Platform = service.PlatformOpenAI
					repo.groups[1011].ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"other-model"}}
				case "no-eligible-route":
					repo.groups[1010].Platform = service.PlatformAnthropic
					wantSuccess = false
				case "same-protocol-ambiguous":
					repo.groups[1011].Platform = service.PlatformOpenAI
					wantSuccess = false
				case "composite-private-alias":
					repo.groups[1010].Platform, repo.groups[1011].Platform = service.PlatformComposite, service.PlatformComposite
					repo.account.Credentials["model_mapping"] = map[string]any{"public-selected": "private-wire-alias"}
					body, wantSuccess = `{"model_id":"public-selected"}`, false
				case "storage-error":
					repo.readErr = map[int64]error{1011: errors.New("synthetic read failure")}
					wantSuccess = false
				case "explicit-route":
					repo.groups[1011].Platform = service.PlatformOpenAI
					body = `{"model_id":"gpt-6.1-sol","group_id":1010,"platform":"openai"}`
				case "explicit-unauthorized":
					ctx = service.WithObserverScope(ctx, []int64{1011})
					body, wantSuccess = `{"model_id":"gpt-6.1-sol","group_id":1010,"platform":"openai"}`, false
				}
				before := cloneGatewayTestAccount(repo.account)
				router, transport := gatewayDiagnosticRouter(repo, cfg, nil)
				recorder := gatewayTestRequest(router, ctx, body)
				if wantSuccess {
					require.Contains(t, recorder.Body.String(), `"success":true`)
					require.Equal(t, int64(1), hits.Load())
					require.Equal(t, int64(1), transport.calls.Load())
					require.Equal(t, int64(1), transport.closed.Load())
				} else {
					require.Contains(t, recorder.Body.String(), `"type":"error"`)
					require.NotContains(t, recorder.Body.String(), `"success":true`)
					require.Zero(t, hits.Load())
					require.Zero(t, transport.calls.Load())
				}
				if scenario == "observer-filter" || scenario == "explicit-route" {
					require.Equal(t, []int64{1010}, repo.admitted)
				}
				require.Zero(t, repo.writes.Load())
				require.Equal(t, before, repo.account)
			})
		}
	}
}
