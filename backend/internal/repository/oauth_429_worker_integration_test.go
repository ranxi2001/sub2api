//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

type oauth429Loopback struct {
	target   *url.URL
	accounts sync.Map
}

func (u *oauth429Loopback) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	if err := service.ValidateOAuth429Upstream(req, id); err != nil {
		return nil, err
	}
	u.accounts.Store(id, true)
	next := req.Clone(req.Context())
	target := *u.target
	target.Path = req.URL.Path
	next.URL = &target
	next.Host = ""
	// Only the explicitly created loopback target is reachable from this adapter.
	return (&http.Client{Timeout: time.Second * 3}).Do(next)
}
func (u *oauth429Loopback) DoWithTLS(req *http.Request, proxy string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, n)
}

func oauth429Fixture(t *testing.T, history []string) (*accountRepository, *service.Account, service.OAuthAutoConfig) {
	t.Helper()
	repo, c := degradationPolicyFixture(t)
	off := false
	c.DegradationAction.Upstream429Enabled = nil
	c.DegradationAction.ProbeEnabled = &off
	c.DegradationAction.ThresholdPercent = nil
	c.DegradationAction.Independent429 = service.OAuth429TestConfig{Enabled: true, MinSamples: 20}
	svc := service.ProvideAccountOpsService(NewSettingRepository(integrationEntClient), NewAccountOpsRepository(integrationDB), nil, repo, NewGroupRepository(integrationEntClient, integrationDB), &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}, nil, nil, nil, nil, nil)
	t.Cleanup(svc.Stop)
	c, err := svc.SaveOAuthAutoConfig(t.Context(), c)
	require.NoError(t, err)

	a := degradationAccountFixture(t, 50, map[string]any{"gpt-6-astra": "gpt-6-astra", "gpt-6-luna": "gpt-6-luna"}, history, time.Minute)
	_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=credentials||'{"access_token":"fixture-only"}'::jsonb WHERE id=$1`, a.ID)
	require.NoError(t, err)
	return repo, a, c
}
func oauth429Observe(t *testing.T, repo *accountRepository, id int64, model string, index int) bool {
	t.Helper()
	d := sha256.Sum256([]byte(fmt.Sprintf("business-%d-%s-%d", id, model, index)))
	var revision string
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT value::jsonb->>'revision' FROM settings WHERE key=$1`, service.SettingKeyOAuthAutoConfig).Scan(&revision))
	queued, err := repo.ObserveOAuth429(t.Context(), service.OAuth429Observation{AccountID: id, Model: model, Revision: revision, OAuth429Sample: service.OAuth429Sample{Request: hex.EncodeToString(d[:]), Status: 429, At: time.Now().UTC()}})
	require.NoError(t, err)
	return queued
}
func oauth429Prime(t *testing.T, repo *accountRepository, id int64, model string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		require.Equal(t, i == 19, oauth429Observe(t, repo, id, model, i))
	}
}
func oauth429Runner(t *testing.T, repo *accountRepository, answers []string, models ...string) (*service.ScheduledTestRunnerService, *atomic.Int32, *oauth429Loopback) {
	t.Helper()
	model := "gpt-6-astra"
	if len(models) > 0 {
		model = models[0]
	}
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil || input["model"] != model {
			http.Error(w, "wrong target model", 400)
			return
		}
		if index >= len(answers) {
			http.Error(w, "unexpected extra call", 500)
			return
		}
		answer := answers[index]
		if answer == "429" || answer == "401" {
			code := 429
			if answer == "401" {
				code = 401
			}
			http.Error(w, "fixture rejection", code)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if answer == "truncated" {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"23\"}\n\n")
			return
		}
		if answer == "incomplete_status" {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"23\"}\n\ndata: {\"type\":\"response.done\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")
			return
		}
		if answer == "wrapped_rate_limit" {
			answer = "429"
		}
		text, _ := json.Marshal(answer)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%s}\n\ndata: {\"type\":\"response.completed\"}\n\n", text)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	upstream := &oauth429Loopback{target: target}
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	tests := service.NewAccountTestService(repo, nil, nil, nil, nil, upstream, cfg, nil)
	runner := service.ProvideScheduledTestRunnerService(nil, nil, tests, nil, cfg, &service.QualityJudgeService{}, nil, nil, &service.ChannelMonitorV2Service{}, service.NewConcurrencyService(NewConcurrencyCache(testRedis(t), 15, 60)))
	t.Cleanup(runner.Stop)
	return runner, calls, upstream
}
func TestOAuth429WorkerIndependentPair(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
		history []string
		blocked bool
	}{
		{"qualityops_off_two_wrong", []string{"23", "27"}, nil, true},
		{"history_wrong_cannot_shortcut", []string{"23", "21"}, []string{"incorrect", "incorrect"}, false},
		{"wrong_pass", []string{"23", "21"}, nil, false},
		{"wrong_then_429_then_correct", []string{"23", "429", "21"}, nil, false},
		{"five_consecutive_429", []string{"429", "429", "429", "429", "429"}, nil, true},
		{"four_then_correct", []string{"429", "429", "429", "429", "21"}, nil, false},
		{"four_then_unconfirmed", []string{"429", "429", "429", "429", "wrapped_rate_limit"}, nil, false},
		{"five_after_wrong", []string{"23", "429", "429", "429", "429", "429"}, nil, true},
		{"auth_is_inconclusive", []string{"401", "401"}, nil, false},
		{"partial_integer_with_terminal_error", []string{"23", "incomplete_status"}, nil, false},
		{"rate_limit_delivered_as_text", []string{"wrapped_rate_limit", "wrapped_rate_limit"}, nil, false},
		{"formatted_correct", []string{"23", "答案是21颗糖果。"}, nil, false},
		{"wrong_empty", []string{"23", ""}, nil, false},
		{"wrong_truncated", []string{"23", "truncated"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, a, _ := oauth429Fixture(t, tc.history)
			runner, calls, upstream := oauth429Runner(t, repo, tc.answers)
			oauth429Prime(t, repo, a.ID, "gpt-6-astra")
			before, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.False(t, before.IsOAuth429ModelBlocked("gpt-6-astra"))
			require.Zero(t, calls.Load(), "history cannot stand in for an actual execution")
			cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
			secondTests := service.NewAccountTestService(repo, nil, nil, nil, nil, upstream, cfg, nil)
			second := service.ProvideScheduledTestRunnerService(nil, nil, secondTests, nil, cfg, &service.QualityJudgeService{}, nil, nil, &service.ChannelMonitorV2Service{}, service.NewConcurrencyService(NewConcurrencyCache(testRedis(t), 15, 60)))
			t.Cleanup(second.Stop)
			second.StartOAuth429Worker()
			runner.StartOAuth429Worker()
			require.Eventually(t, func() bool {
				var state string
				err := integrationDB.QueryRowContext(t.Context(), `SELECT state FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra'`, a.ID).Scan(&state)
				return err == nil && state == "complete"
			}, 8*time.Second, 20*time.Millisecond, "actual background worker must complete immediately without a quality plan or cooldown")
			runner.Stop()
			second.Stop()
			require.EqualValues(t, len(tc.answers), calls.Load())
			after, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.Equal(t, tc.blocked, after.IsOAuth429ModelBlocked("gpt-6-astra"))
			require.True(t, after.IsModelSupported("gpt-6-luna"))
			require.Equal(t, 50, after.Concurrency)
			require.True(t, after.Schedulable)
			require.Equal(t, service.StatusActive, after.Status)
			if tc.blocked {
				candidate := buildSchedulerMetadataAccount(*after)
				require.False(t, candidate.IsModelSupported("gpt-6-astra"))
				require.True(t, candidate.IsModelSupported("gpt-6-luna"))
				// Save a form loaded before the worker committed its block.
				require.NoError(t, repo.Update(t.Context(), before))
				reloaded, err := repo.GetByID(t.Context(), a.ID)
				require.NoError(t, err)
				require.True(t, reloaded.IsOAuth429ModelBlocked("gpt-6-astra"))
			}
			require.Equal(t, before.Credentials, after.Credentials)
			var data []byte
			var state string
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT data,state FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra'`, a.ID).Scan(&data, &state))
			var saved service.OAuth429Task
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, "complete", state)
			require.Len(t, saved.Runs, len(tc.answers))
			var logged service.AutoConfigEventDetails
			var event []byte
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT details FROM account_auto_config_events WHERE account_id=$1 AND kind='oauth429_review_completed' ORDER BY id DESC LIMIT 1`, a.ID).Scan(&event))
			require.NoError(t, json.Unmarshal(event, &logged))
			require.NotNil(t, logged.OAuth429)
			require.Equal(t, saved.Reason, logged.OAuth429.Reason)
			require.Equal(t, tc.blocked, logged.OAuth429.ModelBlocked)
			if tc.name == "five_consecutive_429" || tc.name == "five_after_wrong" {
				require.Equal(t, "five_consecutive_429", saved.Reason)
			}
			for i, answer := range tc.answers {
				if answer == "429" {
					require.Equal(t, 429, saved.Runs[i].HTTPStatus)
					require.Equal(t, "rate_limited", saved.Runs[i].Verdict)
				}
			}
			switch tc.name {
			case "auth_is_inconclusive", "rate_limit_delivered_as_text":
				for _, run := range saved.Runs {
					require.Equal(t, "inconclusive", run.Verdict)
				}
			case "partial_integer_with_terminal_error", "wrong_truncated", "wrong_empty":
				require.Equal(t, "inconclusive", saved.Runs[1].Verdict)
			case "formatted_correct", "wrong_pass":
				require.Equal(t, "correct", saved.Runs[1].Verdict)
			}
			require.NotEqual(t, saved.Runs[0].ID, saved.Runs[1].ID)
			require.NotEqual(t, saved.Runs[0].ResultID, saved.Runs[1].ResultID)
			require.NoError(t, repo.CompleteOAuth429(t.Context(), &saved))
			for i := 20; i < 60; i++ {
				require.False(t, oauth429Observe(t, repo, a.ID, "gpt-6-astra", i))
			}
			pending, err := repo.ClaimOAuth429(t.Context())
			require.NoError(t, err)
			require.Nil(t, pending)
			require.EqualValues(t, len(tc.answers), calls.Load())
			upstream.accounts.Range(func(key, value any) bool { require.Equal(t, a.ID, key); return true })
		})
	}
}
func TestOAuth429PersistentClaimRestartAndFences(t *testing.T) {
	for _, change := range []string{"restart", "disable", "revision", "stop_resume", "delete", "model_change", "stale_result"} {
		t.Run(change, func(t *testing.T) {
			repo, a, c := oauth429Fixture(t, nil)
			runner, calls, _ := oauth429Runner(t, repo, []string{"23", "27"})
			// Duplicate business messages and another model must not fill this window.
			for i := 0; i < 19; i++ {
				require.False(t, oauth429Observe(t, repo, a.ID, "gpt-6-astra", i))
				require.False(t, oauth429Observe(t, repo, a.ID, "gpt-6-astra", i))
			}
			require.False(t, oauth429Observe(t, repo, a.ID, "gpt-6-luna", 19))
			require.True(t, oauth429Observe(t, repo, a.ID, "gpt-6-astra", 19))
			var wg sync.WaitGroup
			claimed := make(chan *service.OAuth429Task, 2)
			errs := make(chan error, 2)
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					task, err := repo.ClaimOAuth429(t.Context())
					errs <- err
					if task != nil {
						claimed <- task
					}
				}()
			}
			wg.Wait()
			close(claimed)
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			require.Len(t, claimed, 1, "replicas must share the persistent lease")
			task := <-claimed
			first, err := repo.StartOAuth429Run(t.Context(), task)
			require.NoError(t, err)
			require.NotNil(t, first)
			switch change {
			case "restart":
				_, err = integrationDB.ExecContext(t.Context(), `UPDATE oauth_429_tasks SET data=jsonb_set(data,'{lease_until}',to_jsonb($2::text)) WHERE account_id=$1`, a.ID, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano))
			case "disable", "revision":
				if change == "disable" {
					c.DegradationAction.Independent429.Enabled = false
				} else {
					c.Revision += "-new"
				}
				raw, _ := json.Marshal(c)
				_, err = integrationDB.ExecContext(t.Context(), `UPDATE settings SET value=$2 WHERE key=$1`, service.SettingKeyOAuthAutoConfig, string(raw))
			case "stop_resume":
				_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET schedulable=false WHERE id=$1`, a.ID)
				require.NoError(t, err)
				_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET schedulable=true WHERE id=$1`, a.ID)
			case "delete":
				_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET deleted_at=clock_timestamp() WHERE id=$1`, a.ID)
			case "model_change":
				_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"gpt-6-astra":"gpt-6-luna"}') WHERE id=$1`, a.ID)
			case "stale_result":
				task.Lease = "obsolete"
			}
			require.NoError(t, err)
			first.Verdict = "incorrect"
			first.ResultID = "late-result"
			first.FinishedAt = time.Now().UTC()
			require.NoError(t, repo.FinishOAuth429Run(t.Context(), task, *first))
			require.NoError(t, repo.CompleteOAuth429(t.Context(), task))
			if change == "restart" {
				restarted := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
				recovered, err := restarted.ClaimOAuth429(t.Context())
				require.NoError(t, err)
				require.NotNil(t, recovered)
				require.Equal(t, "inconclusive", recovered.Runs[0].Verdict)
				require.NoError(t, runner.RunOAuth429Task(t.Context(), restarted, recovered))
				require.EqualValues(t, 1, calls.Load(), "crashed in-flight call must not be replayed")
			} else {
				require.Zero(t, calls.Load())
			}
			var extra []byte
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT extra FROM accounts WHERE id=$1`, a.ID).Scan(&extra))
			require.NotContains(t, string(extra), service.OAuth429ModelBlocksKey)
		})
	}
}

// Compile-time production repository contract.
var _ service.OAuth429Repository = (*accountRepository)(nil)

func TestOAuth429WorkerAdmitsEachModelIndependently(t *testing.T) {
	repo, a, _ := oauth429Fixture(t, nil)
	for _, model := range []string{"gpt-6-astra", "gpt-6-luna"} {
		oauth429Prime(t, repo, a.ID, model)
	}
	var pending int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT count(*) FROM oauth_429_tasks WHERE state='pending' AND account_id=$1`, a.ID).Scan(&pending))
	require.Equal(t, 2, pending)
}

// Business response ingress wakes the real durable observer without a quality plan.
func TestOAuth429HTTPTransportDeduplicatesAndWakesWorker(t *testing.T) {
	repo, a, _ := oauth429Fixture(t, nil)
	runner, calls, _ := oauth429Runner(t, repo, []string{"23", "27"})
	business := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	defer business.Close()
	transport := &accountResponseTransport{base: http.DefaultTransport}
	runner.StartOAuth429Worker()
	defer runner.Stop()
	for i := 0; i < 20; i++ {
		ctx := context.WithValue(t.Context(), ctxkey.ClientRequestID, fmt.Sprintf("business-http-%d-%d", a.ID, i))
		ctx = requestcapture.WithAccount(ctx, a.ID)
		for range 2 {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, business.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","input":"fixture"}`))
			require.NoError(t, err)
			response, err := transport.RoundTrip(req)
			require.NoError(t, err)
			_ = response.Body.Close()
		}
		if i < 19 {
			require.Zero(t, calls.Load(), "physical retries must not count as independent business samples")
		}
	}
	require.Eventually(t, func() bool {
		got, err := repo.GetByID(t.Context(), a.ID)
		return err == nil && got.IsOAuth429ModelBlocked("gpt-6-astra")
	}, 8*time.Second, 20*time.Millisecond)
	require.EqualValues(t, 2, calls.Load())
}

func TestOAuth429AliasAndLiveCancellation(t *testing.T) {
	t.Run("alias binds actual target", func(t *testing.T) {
		repo, a, _ := oauth429Fixture(t, nil)
		_, err := integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=jsonb_set(credentials,'{model_mapping}','{"alias":"gpt-6-astra","gpt-6-luna":"gpt-6-luna"}') WHERE id=$1`, a.ID)
		require.NoError(t, err)
		runner, calls, _ := oauth429Runner(t, repo, []string{"23", "27"})
		oauth429Prime(t, repo, a.ID, "gpt-6-astra")
		task, err := repo.ClaimOAuth429(t.Context())
		require.NoError(t, err)
		require.NotNil(t, task)
		require.Equal(t, "alias", task.RequestModel)
		require.NoError(t, runner.RunOAuth429Task(t.Context(), repo, task))
		require.EqualValues(t, 2, calls.Load())
		got, err := repo.GetByID(t.Context(), a.ID)
		require.NoError(t, err)
		require.False(t, got.IsModelSupported("alias"))
		require.True(t, got.IsModelSupported("gpt-6-luna"))
	})
	t.Run("disable aborts in flight", func(t *testing.T) {
		repo, a, c := oauth429Fixture(t, nil)
		started, aborted := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// HTTP/1 detects the peer closing after the request body reaches EOF.
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				return
			}
			close(started)
			select {
			case <-r.Context().Done():
				close(aborted)
			case <-time.After(5 * time.Second):
			}
		}))
		defer server.Close()
		target, err := url.Parse(server.URL)
		require.NoError(t, err)
		cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
		tests := service.NewAccountTestService(repo, nil, nil, nil, nil, &oauth429Loopback{target: target}, cfg, nil)
		runner := service.ProvideScheduledTestRunnerService(nil, nil, tests, nil, cfg, &service.QualityJudgeService{}, nil, nil, &service.ChannelMonitorV2Service{}, service.NewConcurrencyService(NewConcurrencyCache(testRedis(t), 15, 60)))
		defer runner.Stop()
		oauth429Prime(t, repo, a.ID, "gpt-6-astra")
		runner.StartOAuth429Worker()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not dispatch")
		}
		c.DegradationAction.Independent429.Enabled = false
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(t.Context(), `UPDATE settings SET value=$2 WHERE key=$1`, service.SettingKeyOAuthAutoConfig, string(raw))
		require.NoError(t, err)
		select {
		case <-aborted:
		case <-time.After(2 * time.Second):
			t.Fatal("disabled task did not cancel the in-flight provider call")
		}
		got, err := repo.GetByID(t.Context(), a.ID)
		require.NoError(t, err)
		require.False(t, got.IsOAuth429ModelBlocked("gpt-6-astra"))
	})
}

func TestOAuth429WorkerRollingWindowAndCapacity(t *testing.T) {
	repo, a, cfg := oauth429Fixture(t, nil)
	ctx := t.Context()
	// The cap includes live observations. Reclaim an expired tracker while a
	// recent tracker and active jobs remain protected.
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO oauth_429_tasks(account_id,model,state,data,updated_at)
 SELECT $1,'capacity-fixture-'||i,'observing','{}',clock_timestamp()-interval '2 hours'
 FROM generate_series(1,$2::integer) i`, a.ID, oauth429QueueLimit)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM oauth_429_tasks WHERE account_id=$1 AND model LIKE 'capacity-fixture-%'`, a.ID)
	})
	for i := 0; i < 360; i++ {
		status := 200
		if i >= 200 {
			status = 429
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("rolling-%d", i)))
		queued, err := repo.ObserveOAuth429(ctx, service.OAuth429Observation{AccountID: a.ID, Model: "gpt-6-astra", Revision: cfg.Revision, OAuth429Sample: service.OAuth429Sample{Request: hex.EncodeToString(digest[:]), Status: status, At: time.Now().UTC()}})
		require.NoError(t, err)
		require.Equal(t, i == 359, queued, "observation %d", i)
	}
	var samples, fixtures int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT jsonb_array_length(data->'samples') FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra'`, a.ID).Scan(&samples))
	require.Equal(t, 200, samples)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM oauth_429_tasks WHERE account_id=$1 AND model LIKE 'capacity-fixture-%'`, a.ID).Scan(&fixtures))
	require.Equal(t, oauth429QueueLimit-1, fixtures)
}
