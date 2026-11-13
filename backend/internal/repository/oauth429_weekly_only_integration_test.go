//go:build integration

package repository

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
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
