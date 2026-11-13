//go:build integration

package repository

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

// Real PostgreSQL scheduling writes and the real queued rotation lifecycle.
// Upstream enrollment is fictional: no worker contacts an external service.
func TestOAuth429WeeklyOnlyRotationPreservesScheduling(t *testing.T) {
	for _, mode := range []string{"permanent_disable", "manual_disable", "manual_enable"} {
		for _, success := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_success_%t", mode, success), func(t *testing.T) {
				ctx, rotation, cfg := totpRotationFixture(t)
				accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=true,extra=$2::jsonb WHERE id=$1`, cfg.AccountID, `{"openai_disable_scheduling_on_429":true,"openai_totp_rotate_on_429":true}`)
				require.NoError(t, err)
				a, err := accounts.GetByID(ctx, cfg.AccountID)
				require.NoError(t, err)
				h := http.Header{}
				h.Set("x-codex-primary-window-minutes", "10080")
				h.Set("x-codex-primary-used-percent", "20")
				h.Set("x-codex-secondary-window-minutes", "0")
				if mode == "permanent_disable" {
					h.Set("x-codex-primary-used-percent", "99")
				}
				rates := service.NewRateLimitService(accounts, nil, &config.Config{}, nil, nil)
				rates.HandleUpstreamError(ctx, a, 429, h, nil)
				task, err := rotation.CreateTOTPRotationOn429(ctx, a.ID, "fictional-snapshot", cfg.UpdatedAt)
				require.NoError(t, err)
				require.NotNil(t, task)
				claimed, err := rotation.ClaimTOTPRotation(ctx, "fictional-worker")
				require.NoError(t, err)
				require.Equal(t, task.TaskID, claimed.TaskID)
				want := mode == "manual_enable"
				if mode != "permanent_disable" {
					// Administrator changes the durable switch after the task was queued.
					require.NoError(t, accounts.SetSchedulable(ctx, a.ID, want))
				}
				before, err := accounts.GetByID(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, want, before.Schedulable)
				if success {
					for _, step := range [][3]string{{"running", "enrolling", ""}, {"enrolling", "prepared", "enc:fictional-new"}, {"prepared", "activating", ""}, {"activating", "verifying", ""}} {
						require.NoError(t, rotation.TransitionTOTPRotation(ctx, task.TaskID, "fictional-worker", step[0], step[1], step[2]))
					}
				}
				require.NoError(t, rotation.FinishTOTPRotation(ctx, task.TaskID, "fictional-worker", success, ""))
				after, err := accounts.GetByID(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, want, after.Schedulable, "rotation completion must preserve the current manual/permanent switch")
				_, err = rates.RecoverAccountAfterSuccessfulTest(ctx, a.ID)
				require.NoError(t, err)
				recovered, err := accounts.GetByID(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, want, recovered.Schedulable, "quota recovery must not reopen permanent/manual disable")
			})
		}
	}
}

func TestOAuth429WeeklyOnlyInvalidWeeklyPersistence(t *testing.T) {
	for _, usage := range []string{"NaN", "+Inf", "duplicate"} {
		t.Run(usage, func(t *testing.T) {
			ctx, _, cfg := totpRotationFixture(t)
			accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
			require.NoError(t, accounts.SetSchedulable(ctx, cfg.AccountID, true))
			old := map[string]any{
				service.OpenAIDisableSchedulingOn429ExtraKey: true,
				"codex_7d_used_percent":                      99.0, "codex_7d_window_minutes": 10080,
				"codex_usage_updated_at": time.Now().Add(-time.Minute).Format(time.RFC3339Nano),
			}
			require.NoError(t, accounts.UpdateExtra(ctx, cfg.AccountID, old))
			a, err := accounts.GetByID(ctx, cfg.AccountID)
			require.NoError(t, err)
			h := http.Header{}
			h.Set("x-codex-primary-window-minutes", "10080")
			h.Set("x-codex-primary-used-percent", usage)
			if usage == "duplicate" {
				h["X-Codex-Primary-Used-Percent"] = []string{"99", "20"}
			}
			rates := service.NewRateLimitService(accounts, nil, &config.Config{}, nil, nil)
			rates.HandleUpstreamError(ctx, a, 429, h, nil)
			a, err = accounts.GetByID(ctx, cfg.AccountID)
			require.NoError(t, err)
			require.True(t, a.Schedulable)
			require.Contains(t, a.Extra, "codex_7d_permanent_disable_invalid_at", "invalid weekly provenance must survive real JSONB persistence")
			// A delayed earlier valid packet cannot clear newer invalid provenance.
			require.NoError(t, accounts.UpdateExtra(ctx, a.ID, old))
			a, err = accounts.GetByID(ctx, a.ID)
			require.NoError(t, err)
			rates.HandleUpstreamError(ctx, a, 429, nil, nil)
			a, err = accounts.GetByID(ctx, cfg.AccountID)
			require.NoError(t, err)
			require.True(t, a.Schedulable, "a rejected weekly packet must not poison a later headerless 429")
		})
	}
}

func TestOAuth429WeeklyOnlyNonweeklyPersistenceRetainsEvidence(t *testing.T) {
	ctx, _, cfg := totpRotationFixture(t)
	accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
	require.NoError(t, accounts.SetSchedulable(ctx, cfg.AccountID, true))
	rates := service.NewRateLimitService(accounts, nil, &config.Config{}, nil, nil)
	a, err := accounts.GetByID(ctx, cfg.AccountID)
	require.NoError(t, err)
	// Feature is initially off: persist a real explicit weekly observation
	// through the parser/repository without a permanent scheduling write.
	h := http.Header{}
	h.Set("x-codex-primary-window-minutes", "10080")
	h.Set("x-codex-primary-used-percent", "99")
	rates.HandleUpstreamError(ctx, a, 429, h, nil)
	a, err = accounts.GetByID(ctx, cfg.AccountID)
	require.NoError(t, err)
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-primary-used-percent", "100")
	h.Set("x-codex-secondary-window-minutes", "0")
	h.Set("x-codex-secondary-used-percent", "0")
	rates.HandleUpstreamError(ctx, a, 429, h, nil)
	require.NoError(t, accounts.UpdateExtra(ctx, a.ID, map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}))
	a, err = accounts.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, a.Schedulable)
	rates.HandleUpstreamError(ctx, a, 429, nil, nil)
	a, err = accounts.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, a.Schedulable, "nonweekly JSONB updates must retain the valid weekly observation")
}

func TestOAuth429WeeklyOnlyQueryUsageSupersedesEvidence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		used        int
		newerHeader bool
	}{
		{"new_weekly_zero", 0, false}, {"new_weekly_low", 20, false}, {"new_weekly_high", 99, false},
		{"malformed_weekly_usage", 20, false}, {"malformed_nonweekly_window", 20, false},
		{"malformed_weekly_reset", 99, false},
		{"invalid_weekly_reset", 99, false}, {"expired_weekly_reset", 99, false},
		{"invalid_weekly_usage", 101, false}, {"negative_weekly_usage", -1, false}, {"unrelated_zero_window", 20, false},
		{"older_query_cannot_replace_newer_high", 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, cfg := totpRotationFixture(t)
			accounts := NewAccountRepository(integrationEntClient, integrationDB, nil).(*accountRepository)
			require.NoError(t, accounts.SetSchedulable(ctx, cfg.AccountID, true))
			require.NoError(t, accounts.UpdateCredentials(ctx, cfg.AccountID, map[string]any{"email": "owner@example.test", "chatgpt_account_id": "fictional-owner", "access_token": "fictional-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}))
			rates := service.NewRateLimitService(accounts, nil, &config.Config{}, nil, nil)
			h := http.Header{}
			h.Set("x-codex-primary-window-minutes", "10080")
			h.Set("x-codex-primary-used-percent", "99")
			a, err := accounts.GetByID(ctx, cfg.AccountID)
			require.NoError(t, err)
			rates.HandleUpstreamError(ctx, a, 429, h, nil)
			old, err := accounts.GetByID(ctx, a.ID)
			require.NoError(t, err)
			var peers atomic.Int64
			peerErrors := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/backend-api/wham/usage" {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				peers.Add(1)
				if tc.newerHeader {
					latest, readErr := accounts.GetByID(ctx, a.ID)
					if readErr != nil {
						peerErrors <- readErr
						w.WriteHeader(500)
						return
					}
					rates.HandleUpstreamError(ctx, latest, 429, h, nil)
				}
				shortSeconds := 18000
				if tc.name == "unrelated_zero_window" {
					shortSeconds = 0
				}
				resetSeconds, resetAt := 7200, ""
				if tc.name == "invalid_weekly_reset" {
					resetSeconds = -1
				}
				if tc.name == "expired_weekly_reset" {
					resetAt = fmt.Sprintf(",\"reset_at\":%d", time.Now().Add(-time.Minute).Unix())
				}
				usedJSON, shortJSON, resetJSON := fmt.Sprint(tc.used), fmt.Sprint(shortSeconds), fmt.Sprint(resetSeconds)
				if tc.name == "malformed_weekly_usage" {
					usedJSON = `"invalid"`
				}
				if tc.name == "malformed_nonweekly_window" {
					shortJSON = `"invalid"`
				}
				if tc.name == "malformed_weekly_reset" {
					resetJSON = `"invalid"`
				}
				_, _ = fmt.Fprintf(w, `{"account_id":"fictional-owner","email":"owner@example.test","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":%s,"limit_window_seconds":604800,"reset_after_seconds":%s%s},"secondary_window":{"used_percent":0,"limit_window_seconds":%s,"reset_after_seconds":3600}}}`, usedJSON, resetJSON, resetAt, shortJSON)
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			factory := func(string) (*req.Client, error) {
				client := req.C().WrapRoundTripFunc(func(rt req.RoundTripper) req.RoundTripFunc {
					return func(request *req.Request) (*req.Response, error) {
						request.URL.Scheme = target.Scheme
						request.URL.Host = target.Host
						return rt.RoundTrip(request)
					}
				})
				return client, nil
			}
			quota := service.NewOpenAIQuotaService(accounts, nil, service.NewOpenAITokenProvider(accounts, nil, nil), factory, nil)
			_, err = quota.QueryUsage(ctx, a.ID)
			require.NoError(t, err)
			select {
			case peerErr := <-peerErrors:
				require.NoError(t, peerErr)
			default:
			}
			require.Positive(t, peers.Load())
			// Simulate a delayed pre-query observation after query persistence.
			delayed := map[string]any{}
			for key, value := range old.Extra {
				if service.IsCodexUsageExtraKey(key) {
					delayed[key] = value
				}
			}
			require.NoError(t, accounts.UpdateExtra(ctx, a.ID, delayed))
			require.NoError(t, accounts.UpdateExtra(ctx, a.ID, map[string]any{service.OpenAIDisableSchedulingOn429ExtraKey: true}))
			a, err = accounts.GetByID(ctx, a.ID)
			require.NoError(t, err)
			rates.HandleUpstreamError(ctx, a, 429, nil, nil)
			a, err = accounts.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, !(tc.newerHeader || (tc.used > 98 && tc.used <= 100 && tc.name != "invalid_weekly_reset" && tc.name != "expired_weekly_reset" && tc.name != "malformed_weekly_reset")), a.Schedulable, "actual QueryUsage persistence must supersede older weekly high without overwriting a newer observation")
		})
	}
}
