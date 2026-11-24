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
	client   *http.Client
	target   *url.URL
	negative bool
}

func (u *issue817GrokHTTP) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	local := req.Clone(req.Context())
	endpoint := *req.URL
	endpoint.Scheme, endpoint.Host = u.target.Scheme, u.target.Host
	local.URL = &endpoint
	if u.negative {
		local.Header.Set("X-Issue817-Negative", "true")
	}
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
	type scenario struct {
		name        string
		response    string
		fault       string
		autoRecover bool
		newer       bool
		owned       bool
		negative    string
	}
	cases := []scenario{
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
	}
	for _, negative := range []string{"shorter_429", "equal_429", "later_429", "exhausted_200"} {
		for _, auto := range []bool{false, true} {
			mode := "auto_off"
			if auto {
				mode = "auto_on"
			}
			for _, fault := range []string{"saved", "create", "prune"} {
				cases = append(cases, scenario{name: "negative_" + negative + "_" + mode + "_" + fault,
					response: "available", negative: negative, autoRecover: auto, fault: fault, owned: true})
			}
		}
	}
	for _, tc := range cases {
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
				if r.Header.Get("X-Issue817-Negative") == "true" {
					reset := resetAt
					switch tc.negative {
					case "shorter_429":
						reset = now.Add(time.Minute)
						w.Header().Set("Retry-After", "60")
					case "later_429":
						reset = resetAt.Add(time.Hour)
					}
					w.Header().Set("X-Ratelimit-Limit-Requests", "10")
					w.Header().Set("X-Ratelimit-Remaining-Requests", "0")
					w.Header().Set("X-Ratelimit-Reset-Requests", reset.Format(time.RFC3339Nano))
					if tc.negative != "exhausted_200" {
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = w.Write([]byte(`{"error":{"message":"actual concurrent negative"}}`))
						return
					}
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
					return
				}
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
			var negativeExecutor *service.AccountTestService
			var negativeLimitedAt, negativeResetAt time.Time
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
				if tc.negative != "" {
					// A second executor/repository performs an actual negative HTTP
					// observation while the scheduled positive is awaiting Create.
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/offline/concurrent-test", nil)
					negativeErr := negativeExecutor.TestAccountConnection(c, accountID, "grok-4.5", "", service.AccountTestModeDefault)
					if tc.negative == "exhausted_200" {
						require.NoError(t, negativeErr)
					} else {
						require.ErrorContains(t, negativeErr, "429")
					}
					negative, err := accounts.GetByID(ctx, accountID)
					require.NoError(t, err)
					require.NotNil(t, negative.RateLimitedAt)
					require.NotNil(t, negative.RateLimitResetAt)
					negativeLimitedAt, negativeResetAt = *negative.RateLimitedAt, *negative.RateLimitResetAt
					if tc.negative == "later_429" {
						require.True(t, negativeResetAt.After(resetAt))
					} else {
						require.True(t, resetAt.Equal(negativeResetAt), "negative observation must not shorten the existing reset")
					}
				}
			}
			scheduled := service.ProvideScheduledTestService(plans, results, nil, nil)
			runner, _ := service.NewGrokConnectivityRunnerForTesting(scheduled, accounts, &issue817GrokHTTP{client: server.Client(), target: target})
			_, negativeExecutor = service.NewGrokConnectivityRunnerForTesting(scheduled, NewAccountRepository(integrationEntClient, integrationDB, nil),
				&issue817GrokHTTP{client: server.Client(), target: target, negative: true})
			require.Equal(t, tc.fault != "create", runner.RunQuotaScheduledPlanForTesting(ctx, plan))
			expectedCalls := int32(1)
			if tc.negative != "" {
				expectedCalls++
			}
			require.Equal(t, expectedCalls, calls.Load(), "only the planned probe and explicit negative may run")
			account, err := accounts.GetByID(ctx, accountID)
			require.NoError(t, err)
			positive := tc.response == "headerless" || tc.response == "available"
			if tc.negative != "" {
				require.NotNil(t, account.RateLimitedAt, "actual negative observation must survive deferred positive recovery")
				require.NotNil(t, account.RateLimitResetAt)
				require.True(t, negativeLimitedAt.Equal(*account.RateLimitedAt))
				require.True(t, negativeResetAt.Equal(*account.RateLimitResetAt))
				require.True(t, negativeLimitedAt.After(limitedAt), "every actual negative must advance the recovery generation")
			} else if positive && tc.fault != "create" && !tc.newer {
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

func TestIssue817GrokNegativePreservesAdaptiveCooldown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		previous time.Duration
		next     time.Duration
	}{
		{name: "forty_minutes", previous: 40 * time.Minute, next: time.Hour},
		{name: "thirty_minutes_exact", previous: 30 * time.Minute, next: time.Hour},
		{name: "thirty_minutes_below", previous: 30*time.Minute - time.Microsecond, next: 30 * time.Minute},
		{name: "ten_minutes_exact", previous: 10 * time.Minute, next: 30 * time.Minute},
		{name: "ten_minutes_below", previous: 10*time.Minute - time.Microsecond, next: 10 * time.Minute},
	} {
		for _, negative := range []string{"shorter_429", "equal_429", "exhausted_200"} {
			t.Run(tc.name+"/"+negative, func(t *testing.T) {
				ctx := context.Background()
				now := time.Now().UTC().Truncate(time.Microsecond)
				reset := now.Add(5 * time.Minute)
				started := reset.Add(-tc.previous)
				credentials, err := json.Marshal(map[string]any{"access_token": "offline-access", "refresh_token": "offline-refresh", "expires_at": now.Add(time.Hour).Format(time.RFC3339)})
				require.NoError(t, err)
				var id int64
				require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,credentials,rate_limited_at,rate_limit_reset_at)
 VALUES('issue817 backoff','grok','oauth','active',true,$1::jsonb,$2,$3) RETURNING id`, string(credentials), started, reset).Scan(&id))
				t.Cleanup(func() {
					_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, id)
					_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, id)
				})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					boundary := reset
					if negative == "shorter_429" {
						boundary = now.Add(time.Minute)
						w.Header().Set("Retry-After", "60")
					}
					w.Header().Set("X-Ratelimit-Limit-Requests", "10")
					w.Header().Set("X-Ratelimit-Remaining-Requests", "0")
					w.Header().Set("X-Ratelimit-Reset-Requests", boundary.Format(time.RFC3339Nano))
					if negative != "exhausted_200" {
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = w.Write([]byte(`{"error":{"message":"offline negative backoff observation"}}`))
						return
					}
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
				}))
				t.Cleanup(server.Close)
				target, err := url.Parse(server.URL)
				require.NoError(t, err)
				accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
				scheduled := service.NewScheduledTestService(NewScheduledTestPlanRepository(integrationDB), NewScheduledTestResultRepository(integrationDB))
				_, executor := service.NewGrokConnectivityRunnerForTesting(scheduled, accounts, &issue817GrokHTTP{client: server.Client(), target: target})
				previousGeneration := started
				var staleEdit *service.Account
				var staleBillingEdit *service.Account
				for observation := range 2 {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/offline/backoff-test", nil)
					err = executor.TestAccountConnection(c, id, "grok-4.5", "", service.AccountTestModeDefault)
					if negative == "exhausted_200" {
						require.NoError(t, err)
					} else {
						require.ErrorContains(t, err, "429")
					}
					account, err := accounts.GetByID(ctx, id)
					require.NoError(t, err)
					require.NotNil(t, account.RateLimitedAt)
					require.NotNil(t, account.RateLimitResetAt)
					require.True(t, account.RateLimitedAt.After(previousGeneration))
					require.True(t, reset.Equal(*account.RateLimitResetAt))
					afterExpiry := reset.Add(time.Second)
					next, limited := service.GrokRepeated429ResetForTesting(account, afterExpiry)
					require.True(t, limited)
					require.True(t, afterExpiry.Add(tc.next).Equal(next), "actual negative must preserve the exact adaptive cooldown tier after expiry")
					previousGeneration = *account.RateLimitedAt
					if observation == 0 {
						// An actual name edit reads G1 before the next HTTP negative
						// commits G2. Saving this snapshot must not resurrect G1.
						staleEdit = account
						billingSnapshot := *account
						staleBillingEdit = &billingSnapshot
					}
				}
				account, err := accounts.GetByID(ctx, id)
				require.NoError(t, err)
				generation := *account.RateLimitedAt
				history := account.Extra[service.GrokRateLimitHistoryExtraKey]
				staleGeneration := *staleEdit.RateLimitedAt
				staleEdit.Name = "stale name edit after G2"
				require.NoError(t, accounts.Update(ctx, staleEdit))
				account, err = accounts.GetByID(ctx, id)
				require.NoError(t, err)
				require.NotNil(t, account.RateLimitedAt)
				require.NotNil(t, account.RateLimitResetAt)
				require.True(t, generation.Equal(*account.RateLimitedAt), "stale configuration edit must preserve the latest negative generation and history")
				require.Equal(t, "stale name edit after G2", account.Name)
				require.True(t, reset.Equal(*account.RateLimitResetAt))
				require.Equal(t, history, account.Extra[service.GrokRateLimitHistoryExtraKey])
				require.True(t, generation.Equal(*staleEdit.RateLimitedAt), "the returned edit snapshot must reflect the committed generation")
				cleared, err := accounts.(*accountRepository).ClearRateLimitIfObserved(ctx, id, staleGeneration, reset)
				require.NoError(t, err)
				require.False(t, cleared, "a positive that observed G1 cannot clear G2 after a name edit")
				// The admin path normally uses the billing-settings capability.
				staleBillingEdit.Name = "stale billing-settings name edit"
				require.NoError(t, accounts.(service.AccountBillingSettingsRepository).UpdateWithAccountBillingSettings(ctx, staleBillingEdit, service.AccountBillingSettingsUpdate{SetName: true, SetNotes: true}))
				account, err = accounts.GetByID(ctx, id)
				require.NoError(t, err)
				require.True(t, generation.Equal(*account.RateLimitedAt))
				require.Equal(t, history, account.Extra[service.GrokRateLimitHistoryExtraKey])
				afterEditExpiry := reset.Add(time.Second)
				afterEditNext, limited := service.GrokRepeated429ResetForTesting(account, afterEditExpiry)
				require.True(t, limited)
				require.True(t, afterEditExpiry.Add(tc.next).Equal(afterEditNext), "stale configuration edit must preserve the adaptive cooldown tier after expiry")
				// The scheduler projection and an ordinary account edit must retain
				// the history bound to the current recovery generation.
				account.Extra = filterSchedulerExtra(account.Extra)
				afterExpiry := reset.Add(time.Second)
				next, _ := service.GrokRepeated429ResetForTesting(account, afterExpiry)
				require.True(t, afterExpiry.Add(tc.next).Equal(next))
				delete(account.Extra, service.GrokRateLimitHistoryExtraKey)
				require.NoError(t, accounts.Update(ctx, account))
				account, err = accounts.GetByID(ctx, id)
				require.NoError(t, err)
				next, _ = service.GrokRepeated429ResetForTesting(account, afterExpiry)
				require.True(t, afterExpiry.Add(tc.next).Equal(next))
				// Reset extension starts a new window, so old tier evidence must
				// not leak into that window or survive a clear/re-arm generation.
				newBoundary := reset.Add(time.Minute)
				require.NoError(t, accounts.(*accountRepository).SetRateLimitedIfLater(ctx, id, newBoundary))
				account, err = accounts.GetByID(ctx, id)
				require.NoError(t, err)
				next, _ = service.GrokRepeated429ResetForTesting(account, newBoundary.Add(time.Second))
				require.True(t, newBoundary.Add(time.Second+10*time.Minute).Equal(next))
				beforeClear := account
				require.NoError(t, accounts.ClearRateLimit(ctx, id))
				require.NoError(t, accounts.Update(ctx, beforeClear))
				account, err = accounts.GetByID(ctx, id)
				require.NoError(t, err)
				require.Nil(t, account.RateLimitedAt, "an ordinary stale edit cannot undo an explicit clear")
				require.Nil(t, account.RateLimitResetAt)
				require.NoError(t, accounts.(*accountRepository).SetRateLimitedIfLater(ctx, id, reset))
				account, err = accounts.GetByID(ctx, id)
				require.NoError(t, err)
				rearmedGeneration := *account.RateLimitedAt
				require.NoError(t, accounts.Update(ctx, beforeClear))
				account, err = accounts.GetByID(ctx, id)
				require.NoError(t, err)
				require.True(t, rearmedGeneration.Equal(*account.RateLimitedAt), "an ordinary stale edit cannot undo an explicit re-arm")
				next, _ = service.GrokRepeated429ResetForTesting(account, afterExpiry)
				require.True(t, afterExpiry.Add(10*time.Minute).Equal(next))
			})
		}
	}
}
