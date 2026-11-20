//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/requestcapture"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// This entire oracle compiles against 8678e88 using only its public API. The
// provider arity adapter below is setup compatibility, not an implementation
// substitute: every execution still uses the checkout's real worker/executor.
type immediateOracleHTTP struct {
	target  *url.URL
	calls   atomic.Int32
	account int64
}

func (u *immediateOracleHTTP) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	if id != u.account {
		return nil, fmt.Errorf("alternate account")
	}
	next := req.Clone(req.Context())
	target := *u.target
	target.Path = req.URL.Path
	next.URL = &target
	next.Host = ""
	return (&http.Client{Timeout: 3 * time.Second}).Do(next)
}
func (u *immediateOracleHTTP) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}
func TestOAuth429ImmediateWithoutQualityOpsBehaviorOracle(t *testing.T) {
	ctx := t.Context()
	repo, c := degradationPolicyFixture(t)
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var policy map[string]any
	require.NoError(t, json.Unmarshal(raw, &policy))
	policy["degradation_action"] = map[string]any{"enabled": true, "probe_enabled": false, "upstream_429_enabled": true, "limit_models": true, "target_concurrency": 10, "models": []string{"gpt-6-luna"}, "independent_429": map[string]any{"enabled": true, "min_samples": 20}}
	raw, err = json.Marshal(policy)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE settings SET value=$2 WHERE key=$1`, service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	a := degradationAccountFixture(t, 50, map[string]any{"gpt-6-astra": "gpt-6-astra", "gpt-6-luna": "gpt-6-luna"}, nil, time.Minute)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=credentials||'{"access_token":"loopback-only"}'::jsonb,rate_limit_reset_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	upstream := &immediateOracleHTTP{account: a.ID}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "gpt-6-astra" {
			http.Error(w, "wrong model", 400)
			return
		}
		upstream.calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer provider.Close()
	upstream.target, err = url.Parse(provider.URL)
	require.NoError(t, err)
	runtime := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	ops := service.ProvideAccountOpsService(NewSettingRepository(integrationEntClient), NewAccountOpsRepository(integrationDB), nil, repo, NewGroupRepository(integrationEntClient, integrationDB), runtime, nil, nil, nil, nil, nil)
	defer ops.Stop()
	// Save through the existing configuration service so the cached observer and
	// persisted revision agree before the first business response is recorded.
	var enabled service.OAuthAutoConfig
	require.NoError(t, json.Unmarshal(raw, &enabled))
	_, err = ops.SaveOAuthAutoConfig(ctx, enabled)
	require.NoError(t, err)
	tests := service.NewAccountTestService(repo, nil, nil, nil, nil, upstream, runtime, nil)
	factory := reflect.ValueOf(service.ProvideScheduledTestRunnerService)
	args := []reflect.Value{reflect.Zero(factory.Type().In(0)), reflect.Zero(factory.Type().In(1)), reflect.ValueOf(tests), reflect.Zero(factory.Type().In(3)), reflect.ValueOf(runtime), reflect.ValueOf(&service.QualityJudgeService{}), reflect.Zero(factory.Type().In(6)), reflect.Zero(factory.Type().In(7)), reflect.ValueOf(&service.ChannelMonitorV2Service{})}
	if factory.Type().NumIn() == 10 {
		args = append(args, reflect.ValueOf(service.NewConcurrencyService(NewConcurrencyCache(testRedis(t), 15, 60))))
	}
	runner := factory.Call(args)[0].Interface().(*service.ScheduledTestRunnerService)
	defer runner.Stop()
	// Older production has no independent worker. Its missing capability must fail
	// the same named business assertion below, not an undefined-symbol compiler.
	if start := reflect.ValueOf(runner).MethodByName("StartOAuth429Worker"); start.IsValid() {
		start.Call(nil)
	}
	business := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); w.WriteHeader(429) }))
	defer business.Close()
	transport := &accountResponseTransport{base: http.DefaultTransport}
	for i := 0; i < 20; i++ {
		requestCtx := context.WithValue(ctx, ctxkey.ClientRequestID, fmt.Sprintf("oracle-%d-%d", a.ID, i))
		requestCtx = context.WithValue(requestCtx, ctxkey.Model, "gpt-6-astra")
		requestCtx = requestcapture.WithAccount(requestCtx, a.ID)
		req, e := http.NewRequestWithContext(requestCtx, http.MethodPost, business.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","stream":true}`))
		require.NoError(t, e)
		resp, e := transport.RoundTrip(req)
		require.NoError(t, e)
		_ = resp.Body.Close()
	}
	require.Eventually(t, func() bool { return upstream.calls.Load() == 2 }, 8*time.Second, 20*time.Millisecond, "threshold must immediately execute two independent provider tests with qualityops disabled and no plan")
	// SQL/JSON only: this oracle is copied unchanged to old production, which
	// has neither the task Go type nor the worker API. Calls alone cannot prove
	// that two complete answers were graded or that completion was committed.
	var terminal struct {
		AccountID int64  `json:"account_id"`
		Model     string `json:"model"`
		TriggerID string `json:"trigger_id"`
		Runs      []struct {
			ID             string    `json:"id"`
			ResultID       string    `json:"result_id"`
			Verdict        string    `json:"verdict"`
			AnswerComplete bool      `json:"answer_complete"`
			StartedAt      time.Time `json:"started_at"`
			FinishedAt     time.Time `json:"finished_at"`
		} `json:"runs"`
	}
	require.Eventually(t, func() bool {
		var state string
		var data []byte
		if integrationDB.QueryRowContext(ctx, `SELECT state,data FROM oauth_429_tasks WHERE account_id=$1 AND model=$2`, a.ID, "gpt-6-astra").Scan(&state, &data) != nil || json.Unmarshal(data, &terminal) != nil {
			return false
		}
		return state == "complete" && len(terminal.Runs) == 2
	}, 8*time.Second, 20*time.Millisecond, "two complete correct tests must commit a terminal task with two persisted results")
	require.Equal(t, a.ID, terminal.AccountID)
	require.Equal(t, "gpt-6-astra", terminal.Model)
	require.NotEmpty(t, terminal.TriggerID)
	identities := map[string]bool{}
	for _, run := range terminal.Runs {
		require.Equal(t, "correct", run.Verdict)
		require.True(t, run.AnswerComplete)
		require.False(t, run.StartedAt.IsZero())
		require.False(t, run.FinishedAt.Before(run.StartedAt))
		for _, id := range []string{run.ID, run.ResultID} {
			require.NotEmpty(t, id)
			require.False(t, identities[id], "each execution and result must have a distinct persisted identity")
			identities[id] = true
		}
	}
	require.EqualValues(t, 2, upstream.calls.Load(), "exactly two complete correct tests terminate this trigger")
	var plans int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_test_plans WHERE account_id=$1`, a.ID).Scan(&plans))
	require.Zero(t, plans)
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 50, got.Concurrency)
	require.True(t, got.IsModelSupported("gpt-6-luna"))
}
