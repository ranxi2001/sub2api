//go:build integration

package qqbot_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestQQBotFullMigrationChain(t *testing.T) {
	dsn := os.Getenv("QQBOT_TEST_DSN")
	if dsn == "" {
		t.Skip("QQBOT_TEST_DSN is required")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer admin.Close()
	schema := "qqbot_mig_" + uuid.New().String()[:8]
	quoted := `"` + schema + `"`
	_, err = admin.Exec(`CREATE DATABASE ` + quoted)
	require.NoError(t, err)
	defer admin.Exec(`DROP DATABASE ` + quoted + ` WITH (FORCE)`)
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.Path = "/" + schema
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	require.NoError(t, repository.ApplyMigrations(context.Background(), db))
	require.NoError(t, repository.ApplyMigrations(context.Background(), db))
	var rewardTables int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('qq_bot_reward_codes','qq_bot_reward_attempts','qq_bot_reward_limits')`).Scan(&rewardTables))
	require.Equal(t, 3, rewardTables)
	var exists bool
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='qq_bot_rewards')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='qq_bot_lottery_draws')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='qq_bot_message_references')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='qq_bot_group_messages')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='qq_bot_group_messages' AND column_name='member_id')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='qq_bot_message_references' AND column_name='shareable')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='qq_bot_price_snapshots')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='qq_bot_periodic_growth_progress')`).Scan(&exists))
	require.True(t, exists)
	var autonomousTables int
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='qq_bot_incoming_images')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='qq_bot_message_references' AND column_name='image_job_id')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('qq_bot_autonomous_state','qq_bot_autonomous_seen','qq_bot_autonomous_decisions')`).Scan(&autonomousTables))
	require.Equal(t, 3, autonomousTables)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='qq_bot_pending_image_plans')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='qq_bot_jobs' AND column_name='image_decision')`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='qq_bot_message_references' AND column_name='source_job_id')`).Scan(&exists))
	require.True(t, exists)
	var autonomousColumns int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='qq_bot_jobs' AND column_name IN ('trigger_reason','decision_reason','model_reserved_at','reply_reserved_at','reply_sent_at')`).Scan(&autonomousColumns))
	require.Equal(t, 5, autonomousColumns)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='qq_bot_jobs' AND column_name IN ('autonomous_ready_at','autonomous_data')`).Scan(&autonomousColumns))
	require.Equal(t, 2, autonomousColumns)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='qq_bot_autonomous_seen' AND column_name IN ('member_id','job_id','ends_conversation')`).Scan(&autonomousColumns))
	require.Equal(t, 3, autonomousColumns)

}
