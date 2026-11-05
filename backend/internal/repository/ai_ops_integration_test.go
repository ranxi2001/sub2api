//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func aiOpsIntegrationAccount(t *testing.T) int64 {
	t.Helper()
	var id int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,concurrency,load_factor) VALUES($1,'openai','oauth','active',true,20,10) RETURNING id`, fmt.Sprintf("ai-ops-%d", time.Now().UnixNano())).Scan(&id))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, id)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, id)
	})
	return id
}

func TestAIOpsPostgresRevisionSeparatesConfigurationFromObservation(t *testing.T) {
	id := aiOpsIntegrationAccount(t)
	ctx := context.Background()
	read := func() int64 {
		var n int64
		require.NoError(t, integrationDB.QueryRow(`SELECT ai_ops_revision FROM accounts WHERE id=$1`, id).Scan(&n))
		return n
	}
	before := read()
	_, err := integrationDB.Exec(`UPDATE accounts SET last_used_at=NOW(),extra=extra||jsonb_build_object('codex_primary_used_percent',31,'codex_usage_updated_at',NOW(),'codex_turn_ticket:fixture','redacted') WHERE id=$1`, id)
	require.NoError(t, err)
	require.Equal(t, before, read(), "quota/ticket/last-used observations must not invalidate ownership")
	_, err = integrationDB.Exec(`UPDATE accounts SET concurrency=19 WHERE id=$1`, id)
	require.NoError(t, err)
	require.Equal(t, before+1, read())
	var group int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO groups(name,platform) VALUES($1,'openai') RETURNING id`, fmt.Sprintf("ai-scope-%d", id)).Scan(&group))
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM groups WHERE id=$1`, group) })
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, id, group)
	require.NoError(t, err)
	require.Equal(t, before+2, read())
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_groups SET priority=priority+1 WHERE account_id=$1 AND group_id=$2`, id, group)
	require.NoError(t, err)
	require.Equal(t, before+3, read())
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, id, group)
	require.NoError(t, err)
	require.Equal(t, before+4, read())
}

func TestAIOpsPostgresRevisionPreservesLegacyExtraShapes(t *testing.T) {
	for _, value := range []string{`null`, `[]`, `[1]`, `"legacy scalar"`, `42`, `true`, `{}`} {
		t.Run(value, func(t *testing.T) {
			id := aiOpsIntegrationAccount(t)
			// OpenAI has a separate object-only long-context billing trigger;
			// legacy quota accounts on other platforms allow all JSON shapes.
			_, err := integrationDB.Exec(`UPDATE accounts SET platform='anthropic' WHERE id=$1`, id)
			require.NoError(t, err)
			read := func() (int64, string) {
				var revision int64
				var extra string
				require.NoError(t, integrationDB.QueryRow(`SELECT ai_ops_revision,extra::text FROM accounts WHERE id=$1`, id).Scan(&revision, &extra))
				return revision, extra
			}
			_, err = integrationDB.Exec(`UPDATE accounts SET extra='{"fixture":true}' WHERE id=$1`, id)
			require.NoError(t, err)
			before, _ := read()
			_, err = integrationDB.Exec(`UPDATE accounts SET extra=$2::jsonb WHERE id=$1`, id, value)
			require.NoError(t, err)
			after, extra := read()
			require.Equal(t, before+1, after, "legacy shape changes must invalidate proposals")
			require.JSONEq(t, value, extra, "revision checks must not rewrite legacy data")
			_, err = integrationDB.Exec(`UPDATE accounts SET extra=$2::jsonb,last_used_at=NOW() WHERE id=$1`, id, value)
			require.NoError(t, err)
			unchanged, _ := read()
			require.Equal(t, after, unchanged, "identical legacy values are not configuration changes")
			_, err = integrationDB.Exec(`UPDATE accounts SET extra='{"fixture":true}' WHERE id=$1`, id)
			require.NoError(t, err)
			restored, _ := read()
			require.Equal(t, after+1, restored, "legacy-to-object transitions must invalidate proposals")
		})
	}
}

func TestAIOpsPostgresRuntimePauseDoesNotOverwriteManualSchedulable(t *testing.T) {
	id := aiOpsIntegrationAccount(t)
	repo := &accountRepository{sql: integrationDB}
	_, err := integrationDB.Exec(`INSERT INTO ai_ops_controls(account_id,ai_paused) VALUES($1,true)`, id)
	require.NoError(t, err)
	got, err := repo.AIOpsRuntime(context.Background(), []int64{id})
	require.NoError(t, err)
	require.True(t, got[id].Paused)
	require.Equal(t, 20, got[id].Concurrency)
	var schedulable bool
	require.NoError(t, integrationDB.QueryRow(`SELECT schedulable FROM accounts WHERE id=$1`, id).Scan(&schedulable))
	require.True(t, schedulable)
	_, err = integrationDB.Exec(`UPDATE accounts SET schedulable=false WHERE id=$1`, id)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE ai_ops_controls SET ai_paused=false WHERE account_id=$1`, id)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRow(`SELECT schedulable FROM accounts WHERE id=$1`, id).Scan(&schedulable))
	require.False(t, schedulable, "AI resume cannot clear a manual restriction")
}

func TestAIOpsPostgresNoEvidenceNeverBecomesZeroErrorHealthy(t *testing.T) {
	id := aiOpsIntegrationAccount(t)
	repo := &accountRepository{sql: integrationDB}
	tx, err := repo.aiOpsTx(context.Background())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	p := aiOpsTestPolicy()
	p.Accounts = []service.AIOpsAccountScope{{AccountID: id, CapacityDomain: "fixture"}}
	accounts, err := readAIOpsAccounts(context.Background(), tx, []int64{id})
	require.NoError(t, err)
	e, err := captureAIOpsEvidence(context.Background(), tx, id, p, time.Now().UTC(), accounts)
	require.NoError(t, err)
	require.Equal(t, 0, e.Samples)
	require.False(t, e.ModelCoverageComplete)
	require.Equal(t, -1, e.Inflight)
}

type aiOpsPGFixture struct {
	repo                *accountRepository
	policy              service.AIOpsPolicy
	target, peer, actor int64
	model               string
}

// All records are synthetic and committed only in the disposable integration
// database. These tests are deliberately serial because policy is a singleton.
// Cleanup only removes this fixture's ledger and restores its policy snapshot;
// it never truncates a shared budget or deletes another test's actions.
func aiOpsFullPGFixture(t *testing.T) aiOpsPGFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()
	var originalConfig []byte
	var originalRevision int64
	var originalActor sql.NullInt64
	var originalUpdated time.Time
	require.NoError(t, integrationDB.QueryRow(`SELECT revision,config,actor_id,updated_at FROM ai_ops_policy WHERE id=1`).Scan(&originalRevision, &originalConfig, &originalActor, &originalUpdated))
	var userID, keyID, groupID, targetID, peerID int64
	t.Cleanup(func() {
		for _, id := range []int64{targetID, peerID} {
			if id == 0 {
				continue
			}
			for _, query := range []string{
				`DELETE FROM ai_ops_probes WHERE account_id=$1`,
				`DELETE FROM ai_ops_controls WHERE account_id=$1`,
				`DELETE FROM ai_ops_actions WHERE account_id=$1`,
				`DELETE FROM ai_ops_proposals WHERE account_id=$1`,
				`DELETE FROM scheduler_outbox WHERE account_id=$1`,
				`DELETE FROM request_timing_details WHERE usage_log_id IN (SELECT id FROM usage_logs WHERE account_id=$1)`,
				`DELETE FROM usage_logs WHERE account_id=$1`,
				`DELETE FROM accounts WHERE id=$1`,
			} {
				_, err := integrationDB.Exec(query, id)
				require.NoError(t, err)
			}
		}
		if keyID != 0 {
			_, err := integrationDB.Exec(`DELETE FROM api_keys WHERE id=$1`, keyID)
			require.NoError(t, err)
		}
		if userID != 0 {
			_, err := integrationDB.Exec(`DELETE FROM users WHERE id=$1`, userID)
			require.NoError(t, err)
		}
		if groupID != 0 {
			_, err := integrationDB.Exec(`DELETE FROM groups WHERE id=$1`, groupID)
			require.NoError(t, err)
		}
		_, err := integrationDB.Exec(`UPDATE ai_ops_policy SET revision=$1,config=$2,actor_id=$3,updated_at=$4 WHERE id=1`, originalRevision, string(originalConfig), originalActor, originalUpdated)
		require.NoError(t, err)
	})
	user := mustCreateUser(t, integrationEntClient, &service.User{Email: "ai-ops-" + suffix + "@test.invalid"})
	userID = user.ID
	group := mustCreateGroup(t, integrationEntClient, &service.Group{Name: "ai-ops-" + suffix, Platform: service.PlatformOpenAI, ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}})
	groupID = group.ID
	key := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: user.ID, Key: "ai-fixture-" + suffix, Name: "synthetic"})
	keyID = key.ID
	model := "gpt-5.4"
	for i, destination := range []*int64{&targetID, &peerID} {
		account := mustCreateAccount(t, integrationEntClient, &service.Account{Name: fmt.Sprintf("ai-ops-%s-%d", suffix, i), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 20})
		*destination = account.ID
		credentials := map[string]any{"chatgpt_account_id": fmt.Sprintf("synthetic-%s-%d", suffix, i), "model_mapping": map[string]string{model: model}}
		extra := map[string]any{"codex_primary_used_percent": 20, "codex_7d_used_percent": 30, "codex_usage_updated_at": time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)}
		_, err := integrationDB.Exec(`UPDATE accounts SET credentials=$2,extra=$3,load_factor=10 WHERE id=$1`, account.ID, aiOpsJSON(credentials), aiOpsJSON(extra))
		require.NoError(t, err)
		_, err = integrationDB.Exec(`INSERT INTO account_groups(account_id,group_id,priority,allowed_models) VALUES($1,$2,1,$3)`, account.ID, group.ID, aiOpsJSON([]string{model}))
		require.NoError(t, err)
		for n := 0; n < 12; n++ {
			stamp := time.Now().UTC().Add(-30 * time.Second)
			var usageID int64
			require.NoError(t, integrationDB.QueryRow(`INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,request_id,model,reasoning_effort,input_tokens,output_tokens,stream,created_at) VALUES($1,$2,$3,$4,$5,$6,'high',100,10,true,$7) RETURNING id`, user.ID, key.ID, account.ID, group.ID, uuid.NewString(), model, stamp).Scan(&usageID))
			trace := map[string]any{"version": 1, "started_at": stamp.Add(-time.Second).Format(time.RFC3339Nano), "outcome": "success", "status": 200, "body_complete": true, "canceled": false, "truncated": false, "client_disconnect": false, "downstream_error": false, "terminal": "completed", "protocol_decision": map[string]string{"upstream": "http"}}
			raw, err := json.Marshal(trace)
			require.NoError(t, err)
			_, err = integrationDB.Exec(`INSERT INTO request_timing_details(usage_log_id,trace_id,created_at,detail) VALUES($1,$2::uuid,$3,$4::jsonb)`, usageID, uuid.NewString(), stamp, string(raw))
			require.NoError(t, err)
		}
	}
	p := service.DefaultAIOpsPolicy()
	p.Mode = "approve_each"
	p.Halted = false
	p.GroupID = group.ID
	p.MinSamples = 10
	p.ObservationSeconds = 60
	p.EvidenceMaxAgeSeconds = 120
	p.DailyActionLimit = 100
	p.MinTotalConcurrency = 1
	p.Accounts = []service.AIOpsAccountScope{{AccountID: targetID, CapacityDomain: "fixture-a"}, {AccountID: peerID, CapacityDomain: "fixture-b"}}
	repo := &accountRepository{sql: integrationDB}
	var err error
	p, err = repo.SaveAIOpsPolicy(ctx, p, originalRevision, user.ID)
	require.NoError(t, err)
	return aiOpsPGFixture{repo: repo, policy: p, target: targetID, peer: peerID, actor: user.ID, model: model}
}

func TestAIOpsPostgresApproveApplyIdempotentAndManualRollbackConflict(t *testing.T) {
	f := aiOpsFullPGFixture(t)
	ctx := context.Background()
	proposal, err := f.repo.PreviewAIOps(ctx, service.AIOpsPreviewInput{RunID: uuid.NewString(), Action: "set_concurrency", AccountID: f.target, ProposedValue: aiOpsFloat(18), EvidenceObservedAt: time.Now().UTC()}, f.actor)
	require.NoError(t, err)
	require.Equal(t, "ready", proposal.Status, "blocked reasons: %v", proposal.BlockedReasons)
	require.Equal(t, 12, proposal.Evidence.Samples)
	require.True(t, proposal.Evidence.ModelCoverageComplete)
	require.Equal(t, f.model, proposal.Evidence.Model)
	require.Equal(t, "high", proposal.Evidence.Effort)
	require.Equal(t, "http", proposal.Evidence.Protocol)
	action, err := f.repo.ApplyAIOps(ctx, proposal.ID, proposal.Digest, f.actor, false)
	require.NoError(t, err)
	repeat, err := f.repo.ApplyAIOps(ctx, proposal.ID, proposal.Digest, f.actor, false)
	require.NoError(t, err)
	require.Equal(t, action.ID, repeat.ID)
	var concurrency, actions, outbox int
	require.NoError(t, integrationDB.QueryRow(`SELECT concurrency FROM accounts WHERE id=$1`, f.target).Scan(&concurrency))
	require.Equal(t, 18, concurrency)
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM ai_ops_actions WHERE proposal_id=$1`, proposal.ID).Scan(&actions))
	require.Equal(t, 1, actions)
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 AND payload->>'ai_ops'='true'`, f.target).Scan(&outbox))
	require.Equal(t, 1, outbox)
	_, err = integrationDB.Exec(`UPDATE accounts SET concurrency=17 WHERE id=$1`, f.target)
	require.NoError(t, err)
	rollback, err := f.repo.RollbackAIOps(ctx, action.ID, action.OwnerRevision, f.actor)
	require.NoError(t, err)
	require.Equal(t, "conflict", rollback.Status)
	require.NoError(t, integrationDB.QueryRow(`SELECT concurrency FROM accounts WHERE id=$1`, f.target).Scan(&concurrency))
	require.Equal(t, 17, concurrency)
	p, err := f.repo.AIOpsPolicy(ctx)
	require.NoError(t, err)
	require.True(t, p.Halted)
}

func TestAIOpsPostgresFailedRecoveryProbeCannotResumePausedAccount(t *testing.T) {
	f := aiOpsFullPGFixture(t)
	ctx := context.Background()
	proposal, err := f.repo.PreviewAIOps(ctx, service.AIOpsPreviewInput{RunID: uuid.NewString(), Action: "pause_account", AccountID: f.target, EvidenceObservedAt: time.Now().UTC()}, f.actor)
	require.NoError(t, err)
	require.Equal(t, "ready", proposal.Status, "blocked reasons: %v", proposal.BlockedReasons)
	_, err = f.repo.ApplyAIOps(ctx, proposal.ID, proposal.Digest, f.actor, false)
	require.NoError(t, err)
	_, err = f.repo.ReserveAIOpsProbe(ctx, f.target, "different-model", f.actor)
	require.ErrorIs(t, err, service.ErrAIOpsBlocked)
	probe, err := f.repo.ReserveAIOpsProbe(ctx, f.target, f.model, f.actor)
	require.NoError(t, err)
	require.Equal(t, "high", probe.Effort)
	require.Equal(t, "http", probe.Protocol)
	completed, err := f.repo.CompleteAIOpsProbe(ctx, probe.ID, false, f.actor)
	require.NoError(t, err)
	require.Equal(t, "failed", completed.Status)
	resume, err := f.repo.PreviewAIOps(ctx, service.AIOpsPreviewInput{RunID: uuid.NewString(), Action: "resume_account", AccountID: f.target, EvidenceObservedAt: time.Now().UTC()}, f.actor)
	require.NoError(t, err)
	require.Equal(t, "blocked", resume.Status)
	require.Contains(t, resume.BlockedReasons, "recovery_probe_required")
	_, err = f.repo.ApplyAIOps(ctx, resume.ID, resume.Digest, f.actor, false)
	require.Error(t, err)
	runtime, err := f.repo.AIOpsRuntime(ctx, []int64{f.target})
	require.NoError(t, err)
	require.True(t, runtime[f.target].Paused)
}

// Only GetAccount is used for propagation confirmation; embedding the interface
// makes any unexpected cache operation fail instead of silently succeeding.
type aiOpsPropagationCache struct {
	service.SchedulerCache
	account *service.Account
}

func (c *aiOpsPropagationCache) GetAccount(context.Context, int64) (*service.Account, error) {
	return c.account, nil
}

func TestAIOpsPostgresPropagationTimeoutKeepsReservationUntilConfirmed(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			f := aiOpsFullPGFixture(t)
			ctx := context.Background()
			proposal, err := f.repo.PreviewAIOps(ctx, service.AIOpsPreviewInput{RunID: uuid.NewString(), Action: "set_concurrency", AccountID: f.target, ProposedValue: aiOpsFloat(18), EvidenceObservedAt: time.Now().UTC()}, f.actor)
			require.NoError(t, err)
			require.Equal(t, "ready", proposal.Status)
			action, err := f.repo.ApplyAIOps(ctx, proposal.ID, proposal.Digest, f.actor, false)
			require.NoError(t, err)
			action.ObserveUntil = time.Now().UTC().Add(-time.Minute)
			if legacy {
				action.Status = "inconclusive"
				action.ReasonCode = "propagation_unconfirmed"
			}
			_, err = integrationDB.Exec(`UPDATE ai_ops_actions SET status=$2,data=$3 WHERE id=$1`, action.ID, action.Status, aiOpsJSON(action))
			require.NoError(t, err)
			require.NoError(t, f.repo.ReconcileAIOps(ctx))
			read := func() service.AIOpsAction {
				var raw []byte
				require.NoError(t, integrationDB.QueryRow(`SELECT data FROM ai_ops_actions WHERE id=$1`, action.ID).Scan(&raw))
				var got service.AIOpsAction
				require.NoError(t, json.Unmarshal(raw, &got))
				return got
			}
			got := read()
			require.Equal(t, "propagation_pending", got.Status)
			require.Equal(t, "propagation_unconfirmed", got.ReasonCode)
			policy, err := f.repo.AIOpsPolicy(ctx)
			require.NoError(t, err)
			require.True(t, policy.Halted)
			require.Equal(t, f.policy.Revision+1, policy.Revision)
			require.NoError(t, f.repo.ReconcileAIOps(ctx))
			again, err := f.repo.AIOpsPolicy(ctx)
			require.NoError(t, err)
			require.Equal(t, policy.Revision, again.Revision, "repeated timeout checks must not churn policy revisions")

			// Re-enabling the policy cannot release the unresolved action's capacity
			// reservation, including records produced by the old controller.
			policy.Halted = false
			_, err = f.repo.SaveAIOpsPolicy(ctx, policy, policy.Revision, f.actor)
			require.NoError(t, err)
			if legacy {
				_, err = integrationDB.Exec(`UPDATE ai_ops_actions SET status='inconclusive',data=jsonb_set(data,'{status}','"inconclusive"') WHERE id=$1`, action.ID)
				require.NoError(t, err)
			}
			peer, err := f.repo.PreviewAIOps(ctx, service.AIOpsPreviewInput{RunID: uuid.NewString(), Action: "set_concurrency", AccountID: f.peer, ProposedValue: aiOpsFloat(18), EvidenceObservedAt: time.Now().UTC()}, f.actor)
			require.NoError(t, err)
			require.Equal(t, "ready", peer.Status)
			_, err = f.repo.ApplyAIOps(ctx, peer.ID, peer.Digest, f.actor, false)
			require.ErrorIs(t, err, service.ErrAIOpsBlocked, "another capacity domain must not bypass the unresolved reservation")

			f.repo.schedulerCache = &aiOpsPropagationCache{account: &service.Account{ID: f.target, Concurrency: 18}}
			confirmedAt := time.Now().UTC()
			require.NoError(t, f.repo.ReconcileAIOps(ctx))
			got = read()
			require.Equal(t, "observing", got.Status)
			require.Empty(t, got.ReasonCode)
			require.False(t, got.ObservationStartedAt.Before(confirmedAt))
			require.Equal(t, time.Duration(f.policy.ObservationSeconds)*time.Second, got.ObserveUntil.Sub(got.ObservationStartedAt))
		})
	}
}

func TestAIOpsPostgresObservationRequiresSameCompleteCohort(t *testing.T) {
	for _, kind := range []string{"matching", "matching_regression", "model", "effort", "protocol", "input", "cache", "output", "legacy_buckets", "before_propagation", "legacy_observation"} {
		t.Run(kind, func(t *testing.T) {
			f := aiOpsFullPGFixture(t)
			ctx := context.Background()
			proposal, err := f.repo.PreviewAIOps(ctx, service.AIOpsPreviewInput{RunID: uuid.NewString(), Action: "set_concurrency", AccountID: f.target, ProposedValue: aiOpsFloat(18), EvidenceObservedAt: time.Now().UTC()}, f.actor)
			require.NoError(t, err)
			require.Equal(t, "ready", proposal.Status)
			require.Equal(t, "small", proposal.Evidence.InputBucket)
			require.Equal(t, "uncached", proposal.Evidence.CacheBucket)
			require.Equal(t, "small", proposal.Evidence.OutputBucket)
			action, err := f.repo.ApplyAIOps(ctx, proposal.ID, proposal.Digest, f.actor, false)
			require.NoError(t, err)
			// Simulate a persisted baseline from a prior request cohort. Today's
			// complete PG trace population remains unchanged, isolating comparison
			// from eligibility and ownership checks.
			switch kind {
			case "model":
				proposal.Evidence.Model = "prior-model"
			case "effort":
				proposal.Evidence.Effort = "low"
			case "protocol":
				proposal.Evidence.Protocol = "ws"
			case "input":
				proposal.Evidence.InputBucket = "large"
			case "cache":
				proposal.Evidence.CacheBucket = "cached"
			case "output":
				proposal.Evidence.OutputBucket = "large"
			case "legacy_buckets":
				proposal.Evidence.InputBucket = ""
				proposal.Evidence.CacheBucket = ""
				proposal.Evidence.OutputBucket = ""
			}
			_, err = integrationDB.Exec(`UPDATE ai_ops_proposals SET data=$2 WHERE id=$1`, proposal.ID, aiOpsJSON(proposal))
			require.NoError(t, err)
			if kind != "matching" {
				// Large enough to cross the rollback threshold: a cohort
				// mismatch must suppress rollback even when error rate rises.
				t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM ops_error_logs WHERE account_id=$1`, f.target) })
				for range 12 {
					_, err = integrationDB.Exec(`INSERT INTO ops_error_logs(account_id,group_id,request_id,model,error_phase,error_type,error_owner,error_source,severity,status_code,upstream_status_code,is_count_tokens,created_at) VALUES($1,$2,$3,$4,'upstream','upstream_error','provider','upstream_http','error',503,503,false,NOW()-interval '10 seconds')`, f.target, f.policy.GroupID, uuid.NewString(), f.model)
					require.NoError(t, err)
				}
			}
			action.Status = "observing"
			action.CreatedAt = time.Now().UTC().Add(-3 * time.Minute)
			action.ObservationStartedAt = time.Now().UTC().Add(-2 * time.Minute)
			action.ObserveUntil = time.Now().UTC().Add(-time.Second)
			if kind == "before_propagation" {
				action.ObservationStartedAt = time.Now().UTC().Add(-10 * time.Second)
			} else if kind == "legacy_observation" {
				action.ObservationStartedAt = time.Time{}
			}
			_, err = integrationDB.Exec(`UPDATE ai_ops_actions SET status=$2,data=$3 WHERE id=$1`, action.ID, action.Status, aiOpsJSON(action))
			require.NoError(t, err)
			require.NoError(t, f.repo.ReconcileAIOps(ctx))
			var raw []byte
			require.NoError(t, integrationDB.QueryRow(`SELECT data FROM ai_ops_actions WHERE id=$1`, action.ID).Scan(&raw))
			var got service.AIOpsAction
			require.NoError(t, json.Unmarshal(raw, &got))
			if kind == "matching" {
				require.Equal(t, "succeeded", got.Status)
				require.Equal(t, "post_change_verified", got.ReasonCode)
			} else if kind == "matching_regression" {
				require.Equal(t, "rolled_back", got.Status)
				require.Equal(t, "error_rate_regressed", got.ReasonCode)
			} else {
				require.Equal(t, "inconclusive", got.Status, "non-comparable evidence must not prove success or trigger rollback")
				reason := "post_change_cohort_changed"
				if kind == "before_propagation" || kind == "legacy_observation" {
					reason = "insufficient_post_change_evidence"
				}
				require.Equal(t, reason, got.ReasonCode)
			}
			var concurrency int
			require.NoError(t, integrationDB.QueryRow(`SELECT concurrency FROM accounts WHERE id=$1`, f.target).Scan(&concurrency))
			expected := 18
			if kind == "matching_regression" {
				expected = 20
			}
			require.Equal(t, expected, concurrency, "only a comparable regression can restore old configuration")
		})
	}
}

func TestAIOpsPostgresApprovalRejectsChangedRequestBuckets(t *testing.T) {
	for _, kind := range []string{"input", "cache", "output"} {
		t.Run(kind, func(t *testing.T) {
			f := aiOpsFullPGFixture(t)
			ctx := context.Background()
			proposal, err := f.repo.PreviewAIOps(ctx, service.AIOpsPreviewInput{RunID: uuid.NewString(), Action: "set_concurrency", AccountID: f.target, ProposedValue: aiOpsFloat(18), EvidenceObservedAt: time.Now().UTC()}, f.actor)
			require.NoError(t, err)
			require.Equal(t, "ready", proposal.Status)
			switch kind {
			case "input":
				_, err = integrationDB.Exec(`UPDATE usage_logs SET input_tokens=50000 WHERE account_id=$1`, f.target)
			case "cache":
				_, err = integrationDB.Exec(`UPDATE usage_logs SET cache_read_tokens=100 WHERE account_id=$1`, f.target)
			case "output":
				_, err = integrationDB.Exec(`UPDATE usage_logs SET output_tokens=9000 WHERE account_id=$1`, f.target)
			}
			require.NoError(t, err)
			_, err = f.repo.ApplyAIOps(ctx, proposal.ID, proposal.Digest, f.actor, false)
			require.ErrorIs(t, err, service.ErrAIOpsConflict)
			var actions, concurrency int
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM ai_ops_actions WHERE proposal_id=$1`, proposal.ID).Scan(&actions))
			require.Zero(t, actions)
			require.NoError(t, integrationDB.QueryRow(`SELECT concurrency FROM accounts WHERE id=$1`, f.target).Scan(&concurrency))
			require.Equal(t, 20, concurrency)
		})
	}
}
