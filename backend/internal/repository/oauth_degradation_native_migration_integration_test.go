//go:build integration

package repository

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationNativeControlsMigration(t *testing.T) {
	migration, err := dbmigrations.FS.ReadFile("338_oauth_degradation_native_controls.sql")
	require.NoError(t, err)

	t.Run("materializes exact and longest wildcard targets without widening either side", func(t *testing.T) {
		for _, tc := range []struct {
			name, credentials, action, expectedMapping string
		}{
			{
				name:            "exact then longest prefix",
				credentials:     `{"model_mapping":{"gpt-*":"gpt-6-luna","gpt-6*":"gpt-6.1-sol","gpt-6-astra":"gpt-6-astra","not-selected":"gpt-6-luna"}}`,
				action:          `{"models":["gpt-6-astra","gpt-6-luna","gpt-6.1-sol"]}`,
				expectedMapping: `{"gpt-6-astra":"gpt-6-astra","gpt-6-luna":"gpt-6.1-sol","gpt-6.1-sol":"gpt-6.1-sol"}`,
			},
			{
				name:            "public and outbound models both belong to scope",
				credentials:     `{"model_mapping":{"gpt-6-luna":"gpt-6-astra","gpt-6.1-sol":"gpt-6.1-sol","not-selected":"gpt-6.1-sol"}}`,
				action:          `{"models":["gpt-6-luna","gpt-6.1-sol"]}`,
				expectedMapping: `{"gpt-6.1-sol":"gpt-6.1-sol"}`,
			},
			{
				name:            "existing whitelist is not expanded",
				credentials:     `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`,
				action:          `{"models":["gpt-6-luna","gpt-6-astra"]}`,
				expectedMapping: `{"gpt-6-luna":"gpt-6-luna"}`,
			},
			{
				name:            "SQL pattern characters remain literal",
				credentials:     `{"model_mapping":{"a_b*":"a_b-target","a%b*":"a%b-target","*":"not-selected"}}`,
				action:          `{"models":["a_b-x","a_b-target","acb-x","a%b-x","a%b-target","axxb-x"]}`,
				expectedMapping: `{"a_b-x":"a_b-target","a_b-target":"a_b-target","a%b-x":"a%b-target","a%b-target":"a%b-target"}`,
			},
			{
				name:            "legacy implicit OpenAI mapping",
				credentials:     `{"model_mapping":null}`,
				action:          `{"models":["gpt-6-luna","gpt-6-astra"]}`,
				expectedMapping: `{"gpt-6-luna":"gpt-6-luna","gpt-6-astra":"gpt-6-astra"}`,
			},
			{
				name:            "explicit alias mode retains allowed default models",
				credentials:     `{"model_mapping":{"custom":"gpt-6-luna"},"model_mapping_mode":"aliases"}`,
				action:          `{"models":["gpt-6-luna","gpt-6-astra"]}`,
				expectedMapping: `{"gpt-6-luna":"gpt-6-luna","gpt-6-astra":"gpt-6-astra"}`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tx := testTx(t)
				id := insertOAuthNativeMigrationFixture(t, tx, "openai", "oauth", tc.credentials, tc.action, 10)
				beforeEvents := oauthNativeMigrationOutboxCount(t, tx, id)
				_, err := tx.ExecContext(t.Context(), string(migration))
				require.NoError(t, err)
				got := readOAuthNativeMigrationFixture(t, tx, id)
				var credentials, extra map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(got.credentials), &credentials))
				require.NoError(t, json.Unmarshal([]byte(got.extra), &extra))
				require.JSONEq(t, tc.expectedMapping, string(credentials["model_mapping"]))
				require.JSONEq(t, `"whitelist"`, string(credentials["model_mapping_mode"]))
				require.True(t, got.schedulable)
				require.Equal(t, 10, got.concurrency)
				require.Equal(t, beforeEvents+1, oauthNativeMigrationOutboxCount(t, tx, id))
				var action map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(extra["oauth_degradation_action"], &action))
				require.JSONEq(t, `true`, string(action["native_controls"]))
			})
		}
	})

	t.Run("empty and malformed scopes use the visible scheduling switch", func(t *testing.T) {
		for _, tc := range []struct{ name, credentials, action string }{
			{"empty scope", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `{"models":[]}`},
			{"null scope", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `{"models":null}`},
			{"object scope", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `{"models":{"gpt-6-luna":true}}`},
			{"mixed scope types", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `{"models":["gpt-6-luna",5]}`},
			{"null action", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `null`},
			{"string action", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `"invalid-legacy-state"`},
			{"invalid model flag", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `{"models":["gpt-6-luna"],"limit_models":"true"}`},
			{"invalid concurrency flag", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `{"models":["gpt-6-luna"],"limit_concurrency":10}`},
			{"disabled model action with invalid concurrency flag", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `{"models":["gpt-6-luna"],"limit_models":false,"limit_concurrency":10}`},
			{"array raw mapping", `{"model_mapping":["gpt-6-luna"]}`, `{"models":["gpt-6-luna"]}`},
			{"string raw mapping", `{"model_mapping":"gpt-6-luna"}`, `{"models":["gpt-6-luna"]}`},
			{"non-string mapping target", `{"model_mapping":{"gpt-6-luna":7}}`, `{"models":["gpt-6-luna"]}`},
			{"malformed credentials", `[]`, `{"models":["gpt-6-luna"]}`},
			{"no authorized outbound target", `{"model_mapping":{"gpt-6-luna":"gpt-6-astra"}}`, `{"models":["gpt-6-luna"]}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tx := testTx(t)
				id := insertOAuthNativeMigrationFixture(t, tx, "openai", "oauth", tc.credentials, tc.action, 10)
				before := readOAuthNativeMigrationFixture(t, tx, id)
				_, err := tx.ExecContext(t.Context(), string(migration))
				require.NoError(t, err)
				got := readOAuthNativeMigrationFixture(t, tx, id)
				require.False(t, got.schedulable, "unrepresentable legacy scope must not become default model support")
				require.JSONEq(t, before.credentials, got.credentials, "original mapping remains visible for administrator repair")
				require.Equal(t, before.concurrency, got.concurrency)
			})
		}
	})

	t.Run("model action off preserves native settings and unrelated state", func(t *testing.T) {
		tx := testTx(t)
		id := insertOAuthNativeMigrationFixture(t, tx, "openai", "oauth", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"},"fixture_credential":"preserve","compact_model_mapping":{"gpt-6-luna":"gpt-6-astra"}}`, `{"models":[],"limit_models":false,"limit_concurrency":false,"limit_groups":true,"group_ids":[33]}`, 17)
		before := readOAuthNativeMigrationFixture(t, tx, id)
		_, err := tx.ExecContext(t.Context(), string(migration))
		require.NoError(t, err)
		got := readOAuthNativeMigrationFixture(t, tx, id)
		require.JSONEq(t, before.credentials, got.credentials)
		require.True(t, got.schedulable)
		require.Equal(t, 17, got.concurrency)
		var extra map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(got.extra), &extra))
		require.JSONEq(t, `{"keep":true}`, string(extra["fixture_extra"]))
		require.JSONEq(t, `{"phase":"passed"}`, string(extra["openai_two_fa_admission"]))
		require.NotContains(t, extra, "auto_config_concurrency", "unselected concurrency action must not add an upgrade hold")
	})

	t.Run("implicit non-OpenAI provider catalogs pause conservatively", func(t *testing.T) {
		for _, platform := range []string{"antigravity", "gemini"} {
			t.Run(platform, func(t *testing.T) {
				tx := testTx(t)
				id := insertOAuthNativeMigrationFixture(t, tx, platform, "oauth", `{"fixture_credential":"preserve"}`, `{"models":["gemini-3.8-flash"],"limit_concurrency":false}`, 10)
				before := readOAuthNativeMigrationFixture(t, tx, id)
				_, err := tx.ExecContext(t.Context(), string(migration))
				require.NoError(t, err)
				got := readOAuthNativeMigrationFixture(t, tx, id)
				require.False(t, got.schedulable, "SQL must not invent a default provider mapping it cannot resolve")
				require.JSONEq(t, before.credentials, got.credentials)
				require.Equal(t, before.concurrency, got.concurrency)
			})
		}
	})

	t.Run("persisted cross-platform mappings become ordinary strict whitelists", func(t *testing.T) {
		for _, tc := range []struct{ platform, allowed, excluded string }{
			{"antigravity", "claude-opus-5-7", "gemini-3.8-flash"},
			{"gemini", "gemini-3.8-flash", "gemini-3.1-pro"},
			{"anthropic", "claude-sonnet-5", "claude-opus-5-7"},
		} {
			t.Run(tc.platform, func(t *testing.T) {
				tx := testTx(t)
				credentials, err := json.Marshal(map[string]any{"model_mapping": map[string]string{tc.allowed: tc.allowed, tc.excluded: tc.excluded}})
				require.NoError(t, err)
				action, err := json.Marshal(map[string]any{"models": []string{tc.allowed}, "limit_concurrency": false})
				require.NoError(t, err)
				id := insertOAuthNativeMigrationFixture(t, tx, tc.platform, "oauth", string(credentials), string(action), 10)
				_, err = tx.ExecContext(t.Context(), string(migration))
				require.NoError(t, err)
				got := readOAuthNativeMigrationFixture(t, tx, id)
				require.True(t, got.schedulable)
				account := service.Account{ID: id, Platform: tc.platform, Type: service.AccountTypeOAuth}
				require.NoError(t, json.Unmarshal([]byte(got.credentials), &account.Credentials))
				require.NoError(t, json.Unmarshal([]byte(got.extra), &account.Extra))
				require.Equal(t, map[string]string{tc.allowed: tc.allowed}, account.GetModelMapping(), "provider aliases must not widen the migrated native whitelist")
				require.True(t, account.IsModelSupported(tc.allowed))
				require.False(t, account.IsModelSupported(tc.excluded))
			})
		}
	})

	t.Run("OpenAI alias flags do not grant identity fallback on another platform", func(t *testing.T) {
		tx := testTx(t)
		id := insertOAuthNativeMigrationFixture(t, tx, "antigravity", "oauth", `{"model_mapping":{"claude-opus-5-7":"claude-opus-5-7"},"model_mapping_mode":"aliases"}`, `{"models":["claude-opus-5-7","unconfigured-model"],"limit_concurrency":false}`, 10)
		_, err := tx.ExecContext(t.Context(), `UPDATE accounts SET extra=extra || '{"openai_passthrough":true,"openai_oauth_passthrough":true}'::jsonb WHERE id=$1`, id)
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), string(migration))
		require.NoError(t, err)
		got := readOAuthNativeMigrationFixture(t, tx, id)
		var credentials map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(got.credentials), &credentials))
		require.JSONEq(t, `{"claude-opus-5-7":"claude-opus-5-7"}`, string(credentials["model_mapping"]))
		require.True(t, got.schedulable)
	})

	t.Run("limits model transport while preserving credentials and independent state", func(t *testing.T) {
		tx := testTx(t)
		id := insertOAuthNativeMigrationFixture(t, tx, "openai", "oauth", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"},"compact_model_mapping":{"gpt-6-luna":"gpt-6-astra"},"fixture_credential":"preserve","oauth_initial_selected_models":["gpt-6-luna","gpt-6-astra"]}`, `{"models":["gpt-6-luna"],"concurrency_cap":20}`, 7)
		_, err := tx.ExecContext(t.Context(), `UPDATE accounts SET extra=extra || '{"openai_passthrough":true,"openai_oauth_passthrough":true}'::jsonb WHERE id=$1`, id)
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), string(migration))
		require.NoError(t, err)
		got := readOAuthNativeMigrationFixture(t, tx, id)
		var credentials, extra map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(got.credentials), &credentials))
		require.NoError(t, json.Unmarshal([]byte(got.extra), &extra))
		require.JSONEq(t, `"preserve"`, string(credentials["fixture_credential"]))
		require.JSONEq(t, `["gpt-6-luna","gpt-6-astra"]`, string(credentials["oauth_initial_selected_models"]))
		require.NotContains(t, credentials, "compact_model_mapping")
		require.JSONEq(t, `false`, string(extra["openai_passthrough"]))
		require.JSONEq(t, `false`, string(extra["openai_oauth_passthrough"]))
		require.JSONEq(t, `{"keep":true}`, string(extra["fixture_extra"]))
		require.JSONEq(t, `{"phase":"passed"}`, string(extra["openai_two_fa_admission"]))
		require.JSONEq(t, `{"concurrency":7,"degradation_hold":true}`, string(extra["auto_config_concurrency"]), "hold must follow the native value rather than the stale audit cap")
		require.Equal(t, 7, got.concurrency)
	})

	t.Run("only OAuth accounts are converted", func(t *testing.T) {
		tx := testTx(t)
		id := insertOAuthNativeMigrationFixture(t, tx, "openai", "apikey", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, `{"models":[]}`, 19)
		before := readOAuthNativeMigrationFixture(t, tx, id)
		beforeEvents := oauthNativeMigrationOutboxCount(t, tx, id)
		_, err := tx.ExecContext(t.Context(), string(migration))
		require.NoError(t, err)
		require.Equal(t, before, readOAuthNativeMigrationFixture(t, tx, id))
		require.Equal(t, beforeEvents, oauthNativeMigrationOutboxCount(t, tx, id))
	})

	t.Run("replay preserves subsequent native model concurrency and group edits", func(t *testing.T) {
		tx := testTx(t)
		var groupID int64
		require.NoError(t, tx.QueryRowContext(t.Context(), `INSERT INTO groups(name,platform) VALUES('oauth-native-migration-manual-group','openai') RETURNING id`).Scan(&groupID))
		action, err := json.Marshal(map[string]any{"models": []string{"gpt-6-luna"}, "limit_groups": true, "group_ids": []int64{groupID}, "concurrency_cap": 10})
		require.NoError(t, err)
		id := insertOAuthNativeMigrationFixture(t, tx, "openai", "oauth", `{"model_mapping":{"gpt-6-luna":"gpt-6-luna"}}`, string(action), 10)
		_, err = tx.ExecContext(t.Context(), string(migration))
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), `INSERT INTO account_groups(account_id,group_id,priority,allowed_models) VALUES($1,$2,37,'["gpt-6-astra"]'::jsonb)`, id, groupID)
		require.NoError(t, err)
		_, err = tx.ExecContext(t.Context(), `UPDATE accounts SET concurrency=29,schedulable=true,credentials=jsonb_set(credentials,'{model_mapping}','{"gpt-6-luna":"gpt-6-luna","gpt-6-astra":"gpt-6-astra"}'::jsonb),updated_at=clock_timestamp() WHERE id=$1`, id)
		require.NoError(t, err)
		before := readOAuthNativeMigrationFixture(t, tx, id)
		beforeEvents := oauthNativeMigrationOutboxCount(t, tx, id)
		_, err = tx.ExecContext(t.Context(), string(migration))
		require.NoError(t, err)
		require.Equal(t, before, readOAuthNativeMigrationFixture(t, tx, id), "one-time conversion must not overwrite an administrator's native edits")
		require.Equal(t, beforeEvents, oauthNativeMigrationOutboxCount(t, tx, id), "replay must not emit redundant scheduler events")
		var priority int
		var allowedModels string
		require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT priority,allowed_models::text FROM account_groups WHERE account_id=$1 AND group_id=$2`, id, groupID).Scan(&priority, &allowedModels))
		require.Equal(t, 37, priority)
		require.JSONEq(t, `["gpt-6-astra"]`, allowedModels)
	})
}

type oauthNativeMigrationState struct {
	credentials string
	extra       string
	concurrency int
	schedulable bool
	updatedAt   time.Time
}

func insertOAuthNativeMigrationFixture(t *testing.T, tx *sql.Tx, platform, kind, credentials, action string, concurrency int) int64 {
	t.Helper()
	extra, err := json.Marshal(map[string]json.RawMessage{
		"oauth_degradation_action": json.RawMessage(action),
		"fixture_extra":            json.RawMessage(`{"keep":true}`),
		"openai_two_fa_admission":  json.RawMessage(`{"phase":"passed"}`),
	})
	require.NoError(t, err)
	var id int64
	require.NoError(t, tx.QueryRowContext(t.Context(), `INSERT INTO accounts(name,platform,type,credentials,extra,concurrency,schedulable) VALUES('oauth-native-migration-fixture',$1,$2,$3::jsonb,$4::jsonb,$5,true) RETURNING id`, platform, kind, credentials, string(extra), concurrency).Scan(&id))
	return id
}

func readOAuthNativeMigrationFixture(t *testing.T, tx *sql.Tx, id int64) oauthNativeMigrationState {
	t.Helper()
	var state oauthNativeMigrationState
	require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT credentials::text,extra::text,concurrency,schedulable,updated_at FROM accounts WHERE id=$1`, id).Scan(&state.credentials, &state.extra, &state.concurrency, &state.schedulable, &state.updatedAt))
	return state
}

func oauthNativeMigrationOutboxCount(t *testing.T, tx *sql.Tx, id int64) int {
	t.Helper()
	var count int
	require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type='account_changed'`, id).Scan(&count))
	return count
}
