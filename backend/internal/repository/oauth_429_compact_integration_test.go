//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type compactForwardHTTP struct {
	target *url.URL
}

func (u *compactForwardHTTP) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	next := req.Clone(requestcapture.WithAccount(req.Context(), id))
	target := *u.target
	target.Path = req.URL.Path
	next.URL, next.Host = &target, ""
	return (&http.Client{Transport: &accountResponseTransport{base: http.DefaultTransport}, Timeout: 3 * time.Second}).Do(next)
}
func (u *compactForwardHTTP) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}

func TestOAuth429CompactGlobalForwardWorker(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, accountRule := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough_%t/account_rule_%t", passthrough, accountRule), func(t *testing.T) {
				repo, a, policy := oauth429Fixture(t, nil)
				cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}, Gateway: config.GatewayConfig{OpenAICompactModel: "gpt-6-luna"}}
				routing, err := repo.ConfigureOAuth429Routing(t.Context(), cfg.Gateway.OpenAICompactModel)
				require.NoError(t, err)
				// Replace the fixture observer with one initialized from this exact
				// runtime selector, as production ProvideAccountOpsService does.
				ops := service.ProvideAccountOpsService(NewSettingRepository(integrationEntClient), NewAccountOpsRepository(integrationDB), nil, repo, NewGroupRepository(integrationEntClient, integrationDB), cfg, nil, nil, nil, nil, nil)
				t.Cleanup(ops.Stop)
				policy.DegradationAction.Models = []string{"gpt-6.1-terra"}
				_, err = ops.SaveOAuthAutoConfig(t.Context(), policy)
				require.NoError(t, err)
				credentials := map[string]any{"access_token": "fixture-only", "model_mapping": map[string]any{"alias-one": "gpt-6-astra", "other-alias": "gpt-6-luna"}}
				target := "gpt-6-luna"
				if accountRule {
					credentials["compact_model_mapping"] = map[string]any{"alias-one": "gpt-6-astra"}
					target = "gpt-6-astra"
				}
				raw, err := json.Marshal(credentials)
				require.NoError(t, err)
				_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=$2,extra=extra||jsonb_build_object('openai_passthrough',$3::boolean,'openai_passthrough_model_filter',true) WHERE id=$1`, a.ID, string(raw), passthrough)
				require.NoError(t, err)
				a, err = repo.GetByID(t.Context(), a.ID)
				require.NoError(t, err)
				ordinary := service.ResolveOAuth429TargetModel(a, "alias-one", "/responses", cfg.Gateway.OpenAICompactModel)
				// Allow sampling until an explicit worker start; assertions below
				// cannot accidentally pass before the twentieth observation drains.
				calls := atomic.Int32{}
				business := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					expected := ordinary
					if strings.HasSuffix(r.URL.Path, "/compact") {
						expected = target
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != expected {
						http.Error(w, "wrong actual compact wire", 400)
						return
					}
					calls.Add(1)
					w.WriteHeader(429)
				}))
				defer business.Close()
				peer, err := url.Parse(business.URL)
				require.NoError(t, err)
				gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, &compactForwardHTTP{target: peer}, nil, nil, nil, nil, nil, nil, nil, nil)
				t.Cleanup(gateway.CloseOpenAIWSPool)
				t.Cleanup(gateway.StopOpenAICodexTicketHarvester)
				forward := func(index int, endpoint string) error {
					ctx := context.WithValue(t.Context(), ctxkey.Model, "alias-one")
					ctx = context.WithValue(ctx, ctxkey.ClientRequestID, fmt.Sprintf("compact-%d-%d", a.ID, index))
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, endpoint, nil).WithContext(ctx)
					_, err := gateway.Forward(ctx, c, a, []byte(`{"model":"alias-one","stream":false,"instructions":"fixture","input":[]}`))
					return err
				}
				for i := 0; i < 20; i++ {
					_ = forward(i, "/v1/responses/compact")
				}
				require.EqualValues(t, 20, calls.Load())
				var task service.OAuth429Task
				require.Eventually(t, func() bool {
					var data []byte
					return integrationDB.QueryRowContext(t.Context(), `SELECT data FROM oauth_429_tasks WHERE account_id=$1 AND model=$2 AND state='pending'`, a.ID, target).Scan(&data) == nil && json.Unmarshal(data, &task) == nil
				}, 5*time.Second, 20*time.Millisecond, "real compact Forward and transport must enqueue the globally resolved wire model")
				require.Equal(t, routing.Revision, task.RoutingRevision)
				require.Equal(t, "alias-one", task.RequestModel)
				runner, probes, _ := oauth429Runner(t, repo, []string{"23", "27"}, target)
				runner.StartOAuth429Worker()
				defer runner.Stop()
				require.Eventually(t, func() bool {
					var data []byte
					return integrationDB.QueryRowContext(t.Context(), `SELECT data FROM oauth_429_tasks WHERE account_id=$1 AND model=$2 AND state='complete'`, a.ID, target).Scan(&data) == nil && json.Unmarshal(data, &task) == nil
				}, 8*time.Second, 20*time.Millisecond)
				require.Len(t, task.Runs, 2)
				require.Equal(t, a.ID, task.AccountID)
				require.Equal(t, target, task.Model)
				require.Equal(t, "alias-one", task.RequestModel)
				require.Equal(t, "two_wrong_answers", task.Reason)
				require.True(t, task.ModelBlocked)
				require.NotEmpty(t, task.TriggerID)
				require.EqualValues(t, 2, probes.Load())
				identities := map[string]bool{}
				for _, run := range task.Runs {
					require.True(t, run.AnswerComplete)
					require.Equal(t, "incorrect", run.Verdict)
					require.False(t, run.StartedAt.IsZero())
					require.False(t, run.FinishedAt.Before(run.StartedAt))
					for _, id := range []string{run.ID, run.ResultID} {
						require.NotEmpty(t, id)
						require.False(t, identities[id], "provider runs and persisted results must be independent")
						identities[id] = true
					}
				}
				a, err = repo.GetByID(t.Context(), a.ID)
				require.NoError(t, err)
				blocks := a.Extra[service.OAuth429ModelBlocksKey].(map[string]any)
				require.Len(t, blocks, 1)
				require.NotNil(t, blocks[target])
				require.Error(t, forward(21, "/v1/responses/compact"))
				require.EqualValues(t, 20, calls.Load(), "blocked compact request must stop before HTTP")
				if ordinary != target {
					require.True(t, service.OAuth429RequestModelSupported(a, "alias-one", "/responses", cfg.Gateway.OpenAICompactModel))
					_ = forward(22, "/v1/responses")
					require.EqualValues(t, 21, calls.Load(), "healthy ordinary route must still reach its actual wire")
					inverse, err := json.Marshal(map[string]any{ordinary: map[string]any{}})
					require.NoError(t, err)
					_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET extra=jsonb_set(extra,'{oauth_429_model_blocks}',$2::jsonb) WHERE id=$1`, a.ID, string(inverse))
					require.NoError(t, err)
					a, err = repo.GetByID(t.Context(), a.ID)
					require.NoError(t, err)
					_ = forward(23, "/v1/responses/compact")
					require.EqualValues(t, 22, calls.Load(), "healthy compact route must survive a different ordinary block")
					require.Error(t, forward(24, "/v1/responses"))
					require.EqualValues(t, 22, calls.Load(), "blocked ordinary route must stop before HTTP")
				}
			})
		}
	}
}

func TestOAuth429CompactRoutingChangeFencesEveryStage(t *testing.T) {
	repo, a, c := oauth429Fixture(t, nil)
	first, err := repo.ConfigureOAuth429Routing(t.Context(), "gpt-6-luna")
	require.NoError(t, err)
	observe := func(i int, policy service.OAuth429RoutingPolicy, input, wire string) bool {
		v := oauth429ObservationFixture(a.ID, wire, c.Revision, i, 429)
		v.RequestModel, v.Endpoint, v.RoutingRevision, v.CompactModel = input, "/responses/compact", policy.Revision, policy.CompactModel
		queued, err := repo.ObserveOAuth429(t.Context(), v)
		require.NoError(t, err)
		return queued
	}
	for i := 0; i < 20; i++ {
		require.False(t, observe(i, first, "illegal-input", "gpt-6-luna"))
		require.False(t, observe(i+20, first, "gpt-6-astra", "gpt-6-astra"), "allowed original must not authorize arbitrary wire")
	}
	for i := 0; i < 20; i++ {
		require.Equal(t, i == 19, observe(i+40, first, "gpt-6-astra", "gpt-6-luna"))
	}
	claimed, err := repo.claimOAuth429(t.Context(), a.ID, "gpt-6-luna")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	started, err := repo.StartOAuth429Run(t.Context(), claimed)
	require.NoError(t, err)
	require.NotNil(t, started)
	_, err = repo.ConfigureOAuth429Routing(t.Context(), "gpt-6-astra")
	require.NoError(t, err)
	restored, err := repo.ConfigureOAuth429Routing(t.Context(), "gpt-6-luna")
	require.NoError(t, err)
	require.NotEqual(t, first.Revision, restored.Revision)
	valid, err := repo.OAuth429Current(t.Context(), claimed)
	require.NoError(t, err)
	require.False(t, valid)
	restarted, err := repo.StartOAuth429Run(t.Context(), claimed)
	require.NoError(t, err)
	require.Nil(t, restarted)
	started.ResultID, started.Verdict, started.FinishedAt, started.AnswerComplete = "stale-result", "incorrect", time.Now(), true
	require.NoError(t, repo.FinishOAuth429Run(t.Context(), claimed, *started))
	require.NoError(t, repo.CompleteOAuth429(t.Context(), claimed))
	require.False(t, observe(80, first, "gpt-6-astra", "gpt-6-luna"), "queued old routing revision cannot revive after change-back")
	inflight := oauth429ObservationFixture(a.ID, "gpt-6-luna", c.Revision, 81, 429)
	inflight.RequestModel, inflight.Endpoint = "gpt-6-astra", "/responses/compact"
	inflight.RoutingRevision, inflight.CompactModel, inflight.StartedAt = restored.Revision, restored.CompactModel, first.ValidSince
	queued, err := repo.ObserveOAuth429(t.Context(), inflight)
	require.NoError(t, err)
	require.False(t, queued, "request started before global change must not inherit the new observer revision")
	for i := 0; i < 20; i++ {
		require.Equal(t, i == 19, observe(i+100, restored, "gpt-6-astra", "gpt-6-luna"))
	}
	fresh, err := repo.claimOAuth429(t.Context(), a.ID, "gpt-6-luna")
	require.NoError(t, err)
	require.NotNil(t, fresh)
	require.NoError(t, repo.CompleteOAuth429(t.Context(), claimed))
	var state string
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT state FROM oauth_429_tasks WHERE account_id=$1 AND model=$2`, a.ID, fresh.Model).Scan(&state))
	require.Equal(t, "running", state)
	_, err = repo.ConfigureOAuth429Routing(t.Context(), "gpt-6-astra")
	require.NoError(t, err)
	recovered, err := repo.claimOAuth429(t.Context(), a.ID, fresh.Model)
	require.NoError(t, err)
	require.Nil(t, recovered)
}
