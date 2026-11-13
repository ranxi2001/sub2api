//go:build integration

package repository

import (
	"fmt"
	"net/http"
	"testing"

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
