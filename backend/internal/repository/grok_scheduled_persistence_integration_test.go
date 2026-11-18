//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Replace only the remote endpoint, keeping real HTTP, SSE parsing, observer,
// repository CAS and result insertion. No provider or OAuth refresh is called.
type issue817GrokHTTP struct {
	service.HTTPUpstream
	client *http.Client
	target *url.URL
}

func (u *issue817GrokHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	local := req.Clone(req.Context())
	endpoint := *req.URL
	endpoint.Scheme, endpoint.Host = u.target.Scheme, u.target.Host
	local.URL = &endpoint
	return u.client.Do(local)
}

type issue817GrokResults struct {
	service.ScheduledTestResultRepository
	beforeCreate func()
	failCreate   bool
	failPrune    bool
}

func (r *issue817GrokResults) Create(ctx context.Context, result *service.ScheduledTestResult) (*service.ScheduledTestResult, error) {
	r.beforeCreate()
	if r.failCreate {
		return nil, errors.New("issue817 Grok injected Create failure")
	}
	return r.ScheduledTestResultRepository.Create(ctx, result)
}

func (r *issue817GrokResults) PruneOldResults(ctx context.Context, planID int64, keep int) error {
	if r.failPrune {
		return errors.New("issue817 Grok injected prune failure")
	}
	return r.ScheduledTestResultRepository.PruneOldResults(ctx, planID, keep)
}

func TestIssue817GrokScheduledRecoveryRequiresSavedResult(t *testing.T) {
	for _, tc := range []struct {
		name        string
		response    string
		fault       string
		autoRecover bool
		newer       bool
		owned       bool
	}{
		{name: "headerless_create", response: "headerless", fault: "create", autoRecover: true},
		{name: "available_create", response: "available", fault: "create", autoRecover: true},
		{name: "headerless_saved", response: "headerless", autoRecover: true},
		{name: "available_saved", response: "available", autoRecover: true},
		{name: "headerless_prune", response: "headerless", fault: "prune", autoRecover: true},
		{name: "available_prune", response: "available", fault: "prune", autoRecover: true},
		{name: "auto_recover_off_create", response: "available", fault: "create"},
		{name: "auto_recover_off_saved", response: "available"},
		{name: "429_create", response: "429", fault: "create", autoRecover: true},
		{name: "429_saved", response: "429", autoRecover: true},
		{name: "exhausted_200_create", response: "exhausted", fault: "create", autoRecover: true},
		{name: "exhausted_200_saved", response: "exhausted", autoRecover: true},
		{name: "failed_stream_create", response: "failed_stream", fault: "create", autoRecover: true},
		{name: "failed_stream_saved", response: "failed_stream", autoRecover: true},
		{name: "newer_generation", response: "available", autoRecover: true, newer: true},
		{name: "owned_pause_and_model", response: "available", autoRecover: true, owned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
			plans := NewScheduledTestPlanRepository(integrationDB)
			now := time.Now().UTC().Truncate(time.Microsecond)
			limitedAt, resetAt := now.Add(-time.Minute), now.Add(10*time.Minute)
			credentials, err := json.Marshal(map[string]any{"access_token": "offline-access", "refresh_token": "offline-refresh", "expires_at": now.Add(time.Hour).Format(time.RFC3339)})
			require.NoError(t, err)
			var accountID int64
			require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,credentials,rate_limited_at,rate_limit_reset_at)
 VALUES('issue817 Grok offline','grok','oauth','active',true,$1::jsonb,$2,$3) RETURNING id`, string(credentials), limitedAt, resetAt).Scan(&accountID))
			t.Cleanup(func() {
				_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, accountID)
				_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, accountID)
			})
			due, previous := now.Add(-time.Minute), now.Add(-time.Hour)
			plan, err := plans.Create(ctx, &service.ScheduledTestPlan{AccountID: accountID, ModelID: "grok-4.5", CronExpression: "0 * * * *", Enabled: true, MaxResults: 10, AutoRecover: tc.autoRecover, NextRunAt: &due})
			require.NoError(t, err)
			_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET last_run_at=$2 WHERE id=$1`, plan.ID, previous)
			require.NoError(t, err)
			if tc.owned {
				require.NoError(t, accounts.SetTempUnschedulable(ctx, accountID, now.Add(time.Hour), "quality_rule:817"))
				require.NoError(t, accounts.SetModelRateLimit(ctx, accountID, "grok-heavy", now.Add(time.Hour), "quality_rule:817"))
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
					http.Error(w, "unexpected offline route", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.response == "available" || tc.response == "exhausted" {
					w.Header().Set("X-Ratelimit-Limit-Requests", "10")
					remaining := "9"
					if tc.response == "exhausted" {
						remaining = "0"
					}
					w.Header().Set("X-Ratelimit-Remaining-Requests", remaining)
					w.Header().Set("X-Ratelimit-Reset-Requests", "3600")
				}
				if tc.response == "429" {
					w.Header().Set("Retry-After", "3600")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
					return
				}
				if tc.response == "failed_stream" {
					_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"offline stream failed\"}}}\n\n"))
					return
				}
				_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))
			}))
			t.Cleanup(server.Close)
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			results := &issue817GrokResults{ScheduledTestResultRepository: NewScheduledTestResultRepository(integrationDB), failCreate: tc.fault == "create", failPrune: tc.fault == "prune"}
			results.beforeCreate = func() {
				require.Equal(t, int32(1), calls.Load(), "real HTTP must reach the observer before Create")
				account, err := accounts.GetByID(ctx, accountID)
				require.NoError(t, err)
				require.NotNil(t, account.RateLimitedAt, "Grok observer must keep the observed limit until result Create succeeds")
				require.NotNil(t, account.RateLimitResetAt)
				if tc.response != "429" && tc.response != "exhausted" {
					require.True(t, limitedAt.Equal(*account.RateLimitedAt))
					require.True(t, resetAt.Equal(*account.RateLimitResetAt))
				} else {
					require.True(t, account.RateLimitResetAt.After(resetAt), "negative quota observation must persist before Create")
				}
				if tc.response == "available" || tc.response == "exhausted" {
					require.NotNil(t, account.Extra["grok_usage_snapshot"], "quota observation remains enabled")
				}
				if tc.newer {
					// Clear/re-arm with a shorter reset: both timestamps fence stale success.
					limitedAt, resetAt = now.Add(time.Second), now.Add(5*time.Minute)
					_, err = integrationDB.Exec(`UPDATE accounts SET rate_limited_at=$2,rate_limit_reset_at=$3 WHERE id=$1`, accountID, limitedAt, resetAt)
					require.NoError(t, err)
				}
			}
			scheduled := service.ProvideScheduledTestService(plans, results, nil, nil)
			runner, _ := service.NewGrokConnectivityRunnerForTesting(scheduled, accounts, &issue817GrokHTTP{client: server.Client(), target: target})
			require.Equal(t, tc.fault != "create", runner.RunQuotaScheduledPlanForTesting(ctx, plan))
			require.Equal(t, int32(1), calls.Load(), "persistence failure must not replay the HTTP probe")
			account, err := accounts.GetByID(ctx, accountID)
			require.NoError(t, err)
			positive := tc.response == "headerless" || tc.response == "available"
			if positive && tc.fault != "create" && !tc.newer {
				require.Nil(t, account.RateLimitedAt)
				require.Nil(t, account.RateLimitResetAt)
			} else {
				require.NotNil(t, account.RateLimitedAt)
				require.NotNil(t, account.RateLimitResetAt)
				if tc.response != "429" && tc.response != "exhausted" {
					require.True(t, limitedAt.Equal(*account.RateLimitedAt))
					require.True(t, resetAt.Equal(*account.RateLimitResetAt))
				}
			}
			if tc.owned {
				require.NotNil(t, account.TempUnschedulableUntil)
				require.Equal(t, "quality_rule:817", account.TempUnschedulableReason)
				var owner string
				require.NoError(t, integrationDB.QueryRow(`SELECT extra->'model_rate_limits'->'grok-heavy'->>'reason' FROM accounts WHERE id=$1`, accountID).Scan(&owner))
				require.Equal(t, "quality_rule:817", owner)
			}
			stored, err := results.ListByPlanID(ctx, plan.ID, 10)
			require.NoError(t, err)
			current, err := plans.GetByID(ctx, plan.ID)
			require.NoError(t, err)
			if tc.fault == "create" {
				require.Empty(t, stored)
				require.True(t, previous.Equal(*current.LastRunAt))
				require.True(t, due.Equal(*current.NextRunAt))
			} else {
				require.Len(t, stored, 1)
				expectedStatus := "success"
				if tc.response == "429" || tc.response == "failed_stream" {
					expectedStatus = "failed"
				}
				require.Equal(t, expectedStatus, stored[0].Status)
				encoded, err := json.Marshal(stored[0])
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "grokRecovery", "runtime recovery evidence is not an API/history field")
				require.True(t, current.LastRunAt.After(previous))
			}
		})
	}
}

func TestIssue817GrokManualRecoveryRemainsImmediate(t *testing.T) {
	// The same real transport and observer retain default/manual recovery; no
	// scheduled context or SaveResult gate is injected into the admin path.
	for _, mode := range []string{service.AccountTestModeDefault, service.AccountTestModeGrokText} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			var id int64
			credentials, err := json.Marshal(map[string]any{"access_token": "offline-access", "refresh_token": "offline-refresh", "expires_at": now.Add(time.Hour).Format(time.RFC3339)})
			require.NoError(t, err)
			require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,credentials,rate_limited_at,rate_limit_reset_at)
 VALUES('issue817 manual','grok','oauth','active',true,$1::jsonb,$2,$3) RETURNING id`, string(credentials), now.Add(-time.Minute), now.Add(time.Hour)).Scan(&id))
			t.Cleanup(func() {
				_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, id)
				_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, id)
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
			}))
			t.Cleanup(server.Close)
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
			scheduled := service.NewScheduledTestService(NewScheduledTestPlanRepository(integrationDB), NewScheduledTestResultRepository(integrationDB))
			_, executor := service.NewGrokConnectivityRunnerForTesting(scheduled, accounts, &issue817GrokHTTP{client: server.Client(), target: target})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/offline/admin/test", nil)
			require.NoError(t, executor.TestAccountConnection(c, id, "grok-4.5", "", mode))
			account, err := accounts.GetByID(ctx, id)
			require.NoError(t, err)
			require.Nil(t, account.RateLimitedAt)
			require.Nil(t, account.RateLimitResetAt)
		})
	}
}
