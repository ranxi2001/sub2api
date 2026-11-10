package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.QualityOAuth401Repository = (*scheduledTestPlanRepository)(nil)

var qualityAccount401Pattern = regexp.MustCompile(`(^|[^0-9])401([^0-9]|$)`)

// Claim captures the credential identity before the request. A changed captured
// identity or saved rule version starts a fresh streak after a process restart.
// Equality markers stay in this runtime table, never in snapshots or responses.
func claimQualityOAuth401Tx(ctx context.Context, tx *sql.Tx, plan *service.ScheduledTestPlan, until time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO quality_oauth_401_streaks
 (plan_id,config_updated_at,config_revision,credentials_revision,claimed_until)
 SELECT $1,$2,md5(jsonb_build_array(p.pelican_config,p.model_id,p.cron_expression)::text),
 md5(COALESCE(a.credentials,'{}'::jsonb)::text),$3 FROM accounts a JOIN scheduled_test_plans p ON p.id=$1
 WHERE a.id=$4 AND a.type='oauth' AND a.deleted_at IS NULL
 ON CONFLICT(plan_id) DO UPDATE SET
 failures=CASE WHEN quality_oauth_401_streaks.config_revision=EXCLUDED.config_revision
 AND quality_oauth_401_streaks.config_updated_at=EXCLUDED.config_updated_at
 AND quality_oauth_401_streaks.credentials_revision=EXCLUDED.credentials_revision
 AND quality_oauth_401_streaks.last_round_until=quality_oauth_401_streaks.claimed_until
 THEN quality_oauth_401_streaks.failures ELSE 0 END,
 own_revoked_401=CASE WHEN quality_oauth_401_streaks.config_revision=EXCLUDED.config_revision
 AND quality_oauth_401_streaks.config_updated_at=EXCLUDED.config_updated_at
 AND quality_oauth_401_streaks.credentials_revision=EXCLUDED.credentials_revision
 AND quality_oauth_401_streaks.last_round_until=quality_oauth_401_streaks.claimed_until
 THEN quality_oauth_401_streaks.own_revoked_401 ELSE false END,
 config_updated_at=EXCLUDED.config_updated_at,config_revision=EXCLUDED.config_revision,credentials_revision=EXCLUDED.credentials_revision,
 claimed_until=EXCLUDED.claimed_until`, plan.ID, plan.UpdatedAt, until, plan.AccountID)
	return err
}

func (r *scheduledTestPlanRepository) CompleteQualityOAuth401Round(ctx context.Context, plan *service.ScheduledTestPlan, until time.Time, evidence *service.QualityOAuth401Round) (bool, error) {
	if plan == nil || plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil || plan.PelicanConfig.Quality.OAuth401MaxRounds <= 0 {
		return false, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	deleted, err := completeQualityOAuth401RoundTx(ctx, tx, plan, until, evidence)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	if deleted {
		logQualityOAuth401Deletion(plan)
	}
	return deleted, nil
}

func logQualityOAuth401Deletion(plan *service.ScheduledTestPlan) {
	logger.LegacyPrintf("service.scheduled_test_runner", "quality OAuth 401 rule deleted plan=%d account=%d max_rounds=%d", plan.ID, plan.AccountID, plan.PelicanConfig.Quality.OAuth401MaxRounds)
}

// The plan/account locks fence manual edits, pauses, account-type changes and
// credential replacement against both counting and deletion. This runs outside
// quality-action savepoints and never changes account data or ownership.
func completeQualityOAuth401RoundTx(ctx context.Context, tx *sql.Tx, plan *service.ScheduledTestPlan, until time.Time, evidence *service.QualityOAuth401Round) (bool, error) {
	if plan == nil || plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil || plan.PelicanConfig.Quality.OAuth401MaxRounds <= 0 || evidence == nil {
		return false, nil
	}
	var accountID int64
	var enabled, liveLease bool
	var version time.Time
	var cfgRaw []byte
	err := tx.QueryRowContext(ctx, `SELECT account_id,enabled,updated_at,
 COALESCE(running_until=$2 AND running_until>clock_timestamp(),false),pelican_config
 FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, plan.ID, until).
		Scan(&accountID, &enabled, &version, &liveLease, &cfgRaw)
	if err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if accountID != plan.AccountID || !enabled || !liveLease || !version.Equal(plan.UpdatedAt) {
		return false, nil
	}
	var cfg service.PelicanTestConfig
	if err = json.Unmarshal(cfgRaw, &cfg); err != nil {
		return false, err
	}
	if cfg.Quality == nil || cfg.Quality.OAuth401MaxRounds < 1 || cfg.Quality.OAuth401MaxRounds > 100 || cfg.Quality.OAuth401MaxRounds != plan.PelicanConfig.Quality.OAuth401MaxRounds || cfg.Quality.Action == service.QualityActionOpenAITwoFAAdmission {
		return false, nil
	}
	var accountType, status, errorMessage, revision string
	var deletedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT type,status,COALESCE(error_message,''),deleted_at,
 md5(COALESCE(credentials,'{}'::jsonb)::text) FROM accounts WHERE id=$1 FOR UPDATE`, accountID).
		Scan(&accountType, &status, &errorMessage, &deletedAt, &revision)
	if err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var failures int
	var claimedVersion, claimedUntil time.Time
	var claimedRevision string
	var last sql.NullTime
	var ownRevoked401 bool
	err = tx.QueryRowContext(ctx, `SELECT failures,config_updated_at,credentials_revision,claimed_until,last_round_until,own_revoked_401
 FROM quality_oauth_401_streaks WHERE plan_id=$1 FOR UPDATE`, plan.ID).
		Scan(&failures, &claimedVersion, &claimedRevision, &claimedUntil, &last, &ownRevoked401)
	if err == sql.ErrNoRows {
		return false, nil // No pre-request credential identity was captured.
	} else if err != nil {
		return false, err
	}
	if !claimedUntil.Equal(until) || !claimedVersion.Equal(version) || last.Valid && !last.Time.Before(until) {
		return false, nil
	}
	// The persisted evidence belongs to this rule's captured credential hash.
	// Generic account error text can never grant revocation trust to skipped rounds.
	trustedRevocation := ownRevoked401 || evidence.HasDirectRevoked401
	qualifies := accountType == service.AccountTypeOAuth && !deletedAt.Valid && status == "error" &&
		qualityAccount401Pattern.MatchString(errorMessage) && claimedRevision == revision &&
		cfg.TestChannel != "bps" && (evidence.AllDirect401 || evidence.All401OrUnavailable && trustedRevocation)
	if qualifies {
		failures = min(failures+1, 100)
	} else {
		failures = 0
	}
	if _, err = tx.ExecContext(ctx, `UPDATE quality_oauth_401_streaks SET failures=$2,last_round_until=$3,own_revoked_401=$4 WHERE plan_id=$1`, plan.ID, failures, until, qualifies && trustedRevocation); err != nil {
		return false, err
	}
	if failures < cfg.Quality.OAuth401MaxRounds {
		return false, nil
	}
	if err = deleteScheduledTestPlanTx(ctx, tx, plan.ID); err != nil {
		if infraerrors.Reason(err) == service.QualityScopeConflictReason {
			// Preserve the threshold and the rule for operator reconciliation.
			// No restoration, ownership bypass or replay of this round occurs.
			return false, nil
		}
		return false, err
	}
	return true, nil
}
