//go:build integration

package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationNativeModelEditRestoresAccount(t *testing.T) {
	ctx := t.Context()
	repo, cfg := degradationPolicyFixture(t)
	cfg.DegradationAction.Models = []string{"custom", "gpt-6-luna", "blocked"}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	original := map[string]any{"custom": "gpt-6-luna", "gpt-6-luna": "gpt-6-luna", "blocked": "gpt-6-astra"}
	a := degradationAccountFixture(t, 30, original, []string{"incorrect"}, time.Minute)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=credentials||'{"compact_model_mapping":{"custom":"gpt-6-astra"}}'::jsonb,extra=extra||'{"openai_passthrough":true,"openai_oauth_passthrough":true}'::jsonb WHERE id=$1`, a.ID)
	require.NoError(t, err)
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"custom": "gpt-6-luna", "gpt-6-luna": "gpt-6-luna"}, got.Credentials["model_mapping"], "the account editor sees the actual restricted mapping, preserving custom targets")
	require.Equal(t, "whitelist", got.Credentials["model_mapping_mode"])
	require.NotContains(t, got.Credentials, "compact_model_mapping")
	require.False(t, got.IsOpenAIPassthroughEnabled(), "native whitelist must not be bypassed by a prior passthrough setting")
	require.False(t, got.IsModelSupported("gpt-6-astra"))
	require.False(t, got.IsModelSupported("blocked"), "target outside the selected scope cannot survive via an alias")
	require.True(t, got.IsModelSupported("custom"))
	require.Equal(t, 10, got.Concurrency)

	// The regular account editor is sufficient: no marker-specific unlock API.
	got.Credentials["model_mapping"] = map[string]any{"gpt-6-astra": "gpt-6-astra"}
	got.Concurrency = 30
	got.Schedulable = true
	require.NoError(t, repo.Update(ctx, got))
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	restored, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, restored.IsModelSupported("gpt-6-astra"), "manual native mapping change must remove the effective restriction")
	require.True(t, restored.Schedulable)
	require.Equal(t, 30, restored.Concurrency)
	require.True(t, restored.HasOAuthDegradationAction(), "history remains recorded without reapplying its old model scope")
	var hold bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE((extra->'auto_config_concurrency'->>'degradation_hold')::boolean,false) FROM accounts WHERE id=$1", a.ID).Scan(&hold))
	require.False(t, hold, "manual concurrency save releases automatic-upgrade hold")
}

func TestOAuthDegradationEmptyIntersectionUsesNativePause(t *testing.T) {
	ctx := t.Context()
	repo, _ := degradationPolicyFixture(t)
	original := map[string]any{"gpt-6-astra": "gpt-6-astra"}
	a := degradationAccountFixture(t, 30, original, []string{"incorrect"}, time.Minute)
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable, "empty intersection is visible as disabled scheduling")
	require.Equal(t, original, got.Credentials["model_mapping"], "do not store an empty map that would grant every model")
	got.Schedulable = true
	require.NoError(t, repo.Update(ctx, got))
	require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, time.Now()))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.Schedulable, "administrator can recover through the existing scheduling field")
}
