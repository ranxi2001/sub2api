//go:build integration

package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
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
	c.DegradationAction.ProbeEnabled = &off
	c.DegradationAction.ThresholdPercent = nil
	c.DegradationAction.Independent429 = service.OAuth429TestConfig{Enabled: true, DailyTestBudget: 1000, MinSamples: 20}
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(t.Context(), `UPDATE settings SET value=$2 WHERE key=$1`, service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(t.Context(), `DELETE FROM oauth_429_daily_budget`)
	require.NoError(t, err)
	a := degradationAccountFixture(t, 50, map[string]any{"gpt-6-astra": "gpt-6-astra", "gpt-6-luna": "gpt-6-luna"}, history, time.Minute)
	_, err = integrationDB.ExecContext(t.Context(), `UPDATE accounts SET credentials=credentials||'{"access_token":"fixture-only"}'::jsonb WHERE id=$1`, a.ID)
	require.NoError(t, err)
	return repo, a, c
}
func oauth429Observe(t *testing.T, repo *accountRepository, id int64, model string, index int) bool {
	t.Helper()
	d := sha256.Sum256([]byte(fmt.Sprintf("business-%d-%s-%d", id, model, index)))
	queued, err := repo.ObserveOAuth429(t.Context(), id, model, hex.EncodeToString(d[:]), 429)
	require.NoError(t, err)
	return queued
}
func oauth429Prime(t *testing.T, repo *accountRepository, id int64, model string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		require.Equal(t, i == 19, oauth429Observe(t, repo, id, model, i))
	}
}
func oauth429Runner(t *testing.T, repo *accountRepository, answers []string) (*service.ScheduledTestRunnerService, *atomic.Int32, *oauth429Loopback) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil || input["model"] != "gpt-6-astra" {
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
		{"wrong_unknown_429", []string{"23", "429"}, nil, false},
		{"two_rejections_terminate", []string{"429", "429"}, nil, false},
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
			task, err := repo.ClaimOAuth429(t.Context())
			require.NoError(t, err)
			require.NotNil(t, task)
			require.NoError(t, runner.RunOAuth429Task(t.Context(), repo, task))
			require.EqualValues(t, 2, calls.Load(), "qualityops off must still actively execute two complete independent calls")
			after, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.Equal(t, tc.blocked, after.IsOAuth429ModelBlocked("gpt-6-astra"))
			require.True(t, after.IsModelSupported("gpt-6-luna"))
			require.Equal(t, 50, after.Concurrency)
			require.True(t, after.Schedulable)
			require.Equal(t, before.Credentials, after.Credentials)
			var data []byte
			var state string
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT data,state FROM oauth_429_tasks WHERE account_id=$1 AND model='gpt-6-astra'`, a.ID).Scan(&data, &state))
			var saved service.OAuth429Task
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, "complete", state)
			require.Len(t, saved.Runs, 2)
			require.NotEqual(t, saved.Runs[0].ID, saved.Runs[1].ID)
			require.NotEqual(t, saved.Runs[0].ResultID, saved.Runs[1].ResultID)
			require.NoError(t, repo.CompleteOAuth429(t.Context(), task))
			for i := 20; i < 60; i++ {
				require.False(t, oauth429Observe(t, repo, a.ID, "gpt-6-astra", i))
			}
			pending, err := repo.ClaimOAuth429(t.Context())
			require.NoError(t, err)
			require.Nil(t, pending)
			require.EqualValues(t, 2, calls.Load())
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
