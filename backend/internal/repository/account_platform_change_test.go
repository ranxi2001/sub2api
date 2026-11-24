package repository

import (
	"context"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPlatformChangeDropsLockedProviderMetadataAndKeepsCurrentQuota(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectQuery(`(?s)`+regexp.QuoteMeta("SELECT")+`.*`+regexp.QuoteMeta("FOR NO KEY UPDATE")).
		WithArgs(int64(27), service.PlatformAnthropic, service.AccountTypeAPIKey, `{"api_key":"fixture-key"}`, nil).
		WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "ollama_group_unchanged", "ollama_proxy_unchanged", "enabled", "rate_sync_enabled", "snapshot", "ollama_session", "ollama_auto", "ollama_snapshot", "opencode_group_unchanged", "opencode_auto", "opencode_snapshot", "current_extra", "concurrency"}).
			AddRow(false, false, true, []byte(`true`), []byte(`true`), []byte(`{"status":"ok"}`), nil, nil, nil, false, nil, nil,
				[]byte(`{"quota_used":42,"cost_multiplier":0.5,"grok_observed_models":["old"],"upstream_model_metadata":{"old":{}},"openai_codex_ticket":"old"}`), 3))
	account := &service.Account{ID: 27, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "fixture-key"},
		Extra:       map[string]any{"quota_used": 12.0, "anthropic_mimic_claude_code": true}}
	got, err := lockAndMergeAccountProbeExtra(service.WithAccountPlatformChange(context.Background()), client, account, service.AccountBillingSettingsUpdate{})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"quota_used": 42.0, "cost_multiplier": 0.5, "anthropic_mimic_claude_code": true}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
