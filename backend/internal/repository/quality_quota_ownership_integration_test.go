//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIssue817QuotaSkipRetainsActualIsolationOwnership(t *testing.T) {
	f, _, _ := newQualityIsolationFixture(t)
	require.Equal(t, "models_isolated", isolationRound(f, "failed", "passed"))
	ctx := context.Background()
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
	f.claim()
	var stateBefore, accountBefore []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM account_quality_states WHERE plan_id=$1`, f.plan.ID).Scan(&stateBefore))
	require.NotEqual(t, "{}", string(stateBefore))
	require.NoError(t, integrationDB.QueryRow(`SELECT to_jsonb(a) FROM accounts a WHERE id=$1`, f.account).Scan(&accountBefore))
	require.NoError(t, f.plans.(service.QualityQuotaSkipRepository).FinishQuotaSkippedPelican(ctx, f.plan, f.until, time.Now()))
	var stateAfter, accountAfter []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM account_quality_states WHERE plan_id=$1`, f.plan.ID).Scan(&stateAfter))
	require.NoError(t, integrationDB.QueryRow(`SELECT to_jsonb(a) FROM accounts a WHERE id=$1`, f.account).Scan(&accountAfter))
	require.JSONEq(t, string(stateBefore), string(stateAfter), "quota skip must retain actual model isolation ownership")
	require.JSONEq(t, string(accountBefore), string(accountAfter), "skip cannot mutate the penalized account")
	// The same ownership can still restore after quota recovery.
	f.claim()
	require.Equal(t, "restored", isolationRound(f, "passed", "passed"))
}
