package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func aiOpsTestPolicy() service.AIOpsPolicy {
	p := service.DefaultAIOpsPolicy()
	p.Mode = "approve_each"
	p.Halted = false
	p.Revision = 3
	p.GroupID = 7
	p.Accounts = []service.AIOpsAccountScope{{AccountID: 11, CapacityDomain: "upstream-a"}, {AccountID: 12, CapacityDomain: "upstream-b"}}
	return p
}
func aiOpsJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func aiOpsExpectTx(m sqlmock.Sqlmock) {
	m.ExpectBegin()
	m.ExpectExec("SET LOCAL statement_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(662662)")).WillReturnResult(sqlmock.NewResult(0, 0))
}
func aiOpsAccountRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "revision", "concurrency", "load_factor", "healthy", "managed", "paused", "control_revision", "owner", "groups", "provider_fingerprint", "quota_known"}).
		AddRow(11, 5, 20, 10, true, false, false, 0, "", "{7}", "identity-a", true).AddRow(12, 6, 20, 10, true, false, false, 0, "", "{7}", "identity-b", true)
}

func TestAIOpsPolicyRejectsUnknownScopeAndUnlimitedCapacity(t *testing.T) {
	p := aiOpsTestPolicy()
	require.NoError(t, service.ValidateAIOpsPolicy(p))
	for _, mutate := range []func(*service.AIOpsPolicy){func(p *service.AIOpsPolicy) { p.GroupID = 0 }, func(p *service.AIOpsPolicy) { p.Accounts[0].CapacityDomain = "" }, func(p *service.AIOpsPolicy) { p.MinConcurrency = 0 }, func(p *service.AIOpsPolicy) { p.MaxStepPercent = 26 }, func(p *service.AIOpsPolicy) { p.AutoActions = []string{"set_load_factor"} }} {
		c := aiOpsTestPolicy()
		mutate(&c)
		require.ErrorIs(t, service.ValidateAIOpsPolicy(c), service.ErrAIOpsBlocked)
	}
}

func TestAIOpsCapacityFloorDoesNotCountCorrelatedPeerAsFallback(t *testing.T) {
	p := aiOpsTestPolicy()
	p.MinHealthyAccounts = 2
	p.Accounts[1].CapacityDomain = p.Accounts[0].CapacityDomain
	now := time.Now().UTC()
	accounts := map[int64]aiOpsAccount{11: {id: 11, revision: 5, concurrency: 20, healthy: true, quotaKnown: true, groups: []int64{7}}, 12: {id: 12, revision: 5, concurrency: 20, healthy: true, quotaKnown: true, groups: []int64{7}}}
	e := service.AIOpsEvidenceSnapshot{Samples: 30, ModelCoverageComplete: true}
	in := service.AIOpsPreviewInput{Action: "pause_account", AccountID: 11, EvidenceObservedAt: now}
	require.Contains(t, aiOpsReasons(p, in, accounts[11], e, accounts, now), "capacity_floor")
	in.Action = "set_concurrency"
	in.ProposedValue = aiOpsFloat(18)
	require.Contains(t, aiOpsReasons(p, in, accounts[11], e, accounts, now), "capacity_floor")
}

func TestAIOpsPreviewRefusesStaleUnknownModelAndManagedOwnership(t *testing.T) {
	p := aiOpsTestPolicy()
	now := time.Now().UTC()
	a := aiOpsAccount{id: 11, revision: 5, concurrency: 20, healthy: true, managed: true, groups: []int64{7, 8}}
	in := service.AIOpsPreviewInput{Action: "set_concurrency", AccountID: 11, ProposedValue: aiOpsFloat(19), EvidenceObservedAt: now.Add(-time.Hour)}
	reasons := aiOpsReasons(p, in, a, service.AIOpsEvidenceSnapshot{Samples: 100}, map[int64]aiOpsAccount{11: a}, now)
	for _, reason := range []string{"managed_by_other_controller", "shared_group_scope", "stale_evidence", "insufficient_model_evidence"} {
		require.Contains(t, reasons, reason)
	}
}

func TestAIOpsApplyAccountAuditAndOutboxAreAtomic(t *testing.T) {
	for _, failOutbox := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "outbox_failure_rolls_back"}[failOutbox], func(t *testing.T) {
			db, m, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			r := &accountRepository{sql: db}
			now := time.Now().UTC()
			p := aiOpsTestPolicy()
			proposal := service.AIOpsProposal{ID: "4447dc20-c3eb-4f5a-87ba-adfc91509b8c", RunID: "run-fixed", Action: "set_concurrency", AccountID: 11, ProposedValue: aiOpsFloat(18), BeforeValue: aiOpsFloat(20), PolicyRevision: 3, TargetRevision: 5, Digest: "frozen", CreatedAt: now, ExpiresAt: now.Add(time.Minute), Status: "ready"}
			aiOpsExpectTx(m)
			m.ExpectQuery("SELECT data FROM ai_ops_proposals.*FOR UPDATE").WithArgs(proposal.ID).WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(aiOpsJSON(proposal)))
			m.ExpectQuery("SELECT data FROM ai_ops_actions WHERE proposal_id").WithArgs(proposal.ID).WillReturnError(sql.ErrNoRows)
			m.ExpectQuery("SELECT revision,config FROM ai_ops_policy").WillReturnRows(sqlmock.NewRows([]string{"revision", "config"}).AddRow(p.Revision, aiOpsJSON(p)))
			m.ExpectQuery("SELECT EXISTS.*serverless_routing_v1").WillReturnRows(sqlmock.NewRows([]string{"unsupported"}).AddRow(false))
			m.ExpectQuery("SELECT a.id,a.ai_ops_revision").WithArgs("{11,12}").WillReturnRows(aiOpsAccountRows())
			m.ExpectQuery("WITH successes").WillReturnRows(sqlmock.NewRows([]string{"successes", "failures", "known", "cohorts", "unknown_failures", "model"}).AddRow(30, 0, 30, 1, 0, "gpt-test"))
			for i := 0; i < 2; i++ {
				m.ExpectQuery("SELECT platform,type").WillReturnRows(sqlmock.NewRows([]string{"platform", "type", "credentials", "extra"}).AddRow("openai", "oauth", `{"model_mapping":{"gpt-test":"gpt-test"}}`, `{}`))
				m.ExpectQuery("SELECT COALESCE.*allowed_models").WillReturnRows(sqlmock.NewRows([]string{"models", "group_models"}).AddRow(`[]`, `{}`))
				m.ExpectQuery("SELECT EXISTS.*usage_logs peer").WillReturnRows(sqlmock.NewRows([]string{"compatible"}).AddRow(true))
			}
			m.ExpectQuery("SELECT COUNT.*FILTER").WillReturnRows(sqlmock.NewRows([]string{"active", "daily", "last"}).AddRow(0, 0, nil))
			m.ExpectExec("INSERT INTO ai_ops_actions").WillReturnResult(sqlmock.NewResult(0, 1))
			m.ExpectQuery("UPDATE accounts SET concurrency").WithArgs(int64(11), 18, int64(5)).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(6))
			m.ExpectExec("INSERT INTO ai_ops_controls").WillReturnResult(sqlmock.NewResult(0, 1))
			m.ExpectExec("UPDATE ai_ops_actions").WillReturnResult(sqlmock.NewResult(0, 1))
			m.ExpectExec("UPDATE ai_ops_proposals").WillReturnResult(sqlmock.NewResult(0, 1))
			if failOutbox {
				m.ExpectExec("INSERT INTO scheduler_outbox").WillReturnError(errors.New("outbox unavailable"))
				m.ExpectRollback()
			} else {
				m.ExpectExec("INSERT INTO scheduler_outbox").WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectCommit()
			}
			a, err := r.ApplyAIOps(context.Background(), proposal.ID, proposal.Digest, 9, false)
			if failOutbox {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, "propagation_pending", a.Status)
				require.Equal(t, int64(6), a.TargetRevision)
				require.Equal(t, int64(1), a.OwnerRevision)
			}
			require.NoError(t, m.ExpectationsWereMet())
		})
	}
}

func TestAIOpsRetryReturnsExistingActionWithoutAnotherBudgetOrWrite(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	r := &accountRepository{sql: db}
	p := service.AIOpsProposal{ID: "4447dc20-c3eb-4f5a-87ba-adfc91509b8c", Digest: "fixed", Status: "applied"}
	a := service.AIOpsAction{ID: "8a3845fb-6ffa-4b21-8a73-4f73b95e04cb", ProposalID: p.ID, Status: "observing"}
	aiOpsExpectTx(m)
	m.ExpectQuery("SELECT data FROM ai_ops_proposals").WithArgs(p.ID).WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(aiOpsJSON(p)))
	m.ExpectQuery("SELECT data FROM ai_ops_actions WHERE proposal_id").WithArgs(p.ID).WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(aiOpsJSON(a)))
	m.ExpectCommit()
	got, err := r.ApplyAIOps(context.Background(), p.ID, "fixed", 9, false)
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestAIOpsRollbackNeverOverwritesManualRevision(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	r := &accountRepository{sql: db}
	a := service.AIOpsAction{ID: "8a3845fb-6ffa-4b21-8a73-4f73b95e04cb", AccountID: 11, Action: "set_concurrency", BeforeValue: aiOpsFloat(20), AfterValue: aiOpsFloat(18), OwnerRevision: 1, TargetRevision: 4, Status: "observing"}
	p := aiOpsTestPolicy()
	aiOpsExpectTx(m)
	m.ExpectQuery("SELECT data FROM ai_ops_actions WHERE id").WithArgs(a.ID).WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow(aiOpsJSON(a)))
	m.ExpectQuery("SELECT revision,config FROM ai_ops_policy").WillReturnRows(sqlmock.NewRows([]string{"revision", "config"}).AddRow(p.Revision, aiOpsJSON(p)))
	m.ExpectQuery("SELECT a.id,a.ai_ops_revision").WithArgs("{11,12}").WillReturnRows(aiOpsAccountRows())
	m.ExpectExec("UPDATE ai_ops_policy").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("UPDATE ai_ops_actions").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit()
	got, err := r.RollbackAIOps(context.Background(), a.ID, 1, 9)
	require.NoError(t, err)
	require.Equal(t, "conflict", got.Status)
	require.Equal(t, "ownership_changed", got.ReasonCode)
	require.NoError(t, m.ExpectationsWereMet())
}
