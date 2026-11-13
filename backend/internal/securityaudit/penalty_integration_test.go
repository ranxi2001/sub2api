package securityaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSecurityPenaltyConcurrentStrikesPreserveSessionIsolationAndLogin(t *testing.T) {
	for _, policy := range []string{"session_block", "user_block", "session_route_then_block"} {
		t.Run(policy, func(t *testing.T) {
			db := openPromptAuditIntegrationDB(t)
			user := insertIdentity(t, db, "users")
			cfg := service.DefaultSecurityPenaltyConfig()
			cfg.Enabled = true
			cfg.ObserveOnly = false
			cfg.Policy = policy
			target := int64(99)
			if policy == "session_route_then_block" {
				cfg.TargetGroupID = &target
			}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			_, err = db.Exec("INSERT INTO settings(key,value) VALUES($1,$2),($3,'true')", service.SettingKeySecurityPenalty, string(raw), service.SettingKeyRiskControlEnabled)
			require.NoError(t, err)
			penalties := service.NewSecurityPenaltyService(db, postgresPromptAuditSettingRepository{db: db}, nil)
			var wg sync.WaitGroup
			errors := make(chan error, 12)
			// Concurrent delivery of the same finding must produce one event.
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errors <- penalties.RecordViolation(context.Background(), service.SecurityPenaltyViolation{UserID: user, APIKeyID: 7, SessionKey: "session-A", RequestID: "same-request", Source: "content_moderation", Reason: "sexual", FullPrompt: "SYNTHETIC_OWNER_PROMPT"})
				}()
			}
			wg.Wait()
			close(errors)
			for err := range errors {
				require.NoError(t, err)
			}
			page, err := penalties.ListEvents(context.Background(), user, 1, 10)
			require.NoError(t, err)
			require.EqualValues(t, 1, page.Total)
			before, err := penalties.Admission(context.Background(), user, "session-A")
			require.NoError(t, err)
			require.False(t, before.Blocked)
			if policy == "session_route_then_block" {
				require.Equal(t, &target, before.TargetGroupID)
			} else {
				require.Nil(t, before.TargetGroupID)
			}
			for i := 0; i < 2; i++ {
				require.NoError(t, penalties.RecordViolation(context.Background(), service.SecurityPenaltyViolation{UserID: user, APIKeyID: 7, SessionKey: "session-A", RequestID: fmt.Sprintf("distinct-%d", i), Source: "prompt_audit", Reason: "jailbreak", FullPrompt: "SYNTHETIC_OWNER_PROMPT"}))
			}
			a, err := penalties.Admission(context.Background(), user, "session-A")
			require.NoError(t, err)
			require.True(t, a.Blocked)
			b, err := penalties.Admission(context.Background(), user, "session-B")
			require.NoError(t, err)
			require.Equal(t, policy == "user_block", b.Blocked)
			// This minimal users table has only id: successful penalties prove
			// that enforcement never writes login status or user credentials.
			detail, err := penalties.GetEvent(context.Background(), user, page.Items[0].ID)
			require.NoError(t, err)
			require.Equal(t, "SYNTHETIC_OWNER_PROMPT", detail.FullPrompt)
			other := insertIdentity(t, db, "users")
			_, err = penalties.GetEvent(context.Background(), other, page.Items[0].ID)
			require.Error(t, err)
			require.NoError(t, penalties.Release(context.Background(), user, other, "reviewed false positive"))
			a, err = penalties.Admission(context.Background(), user, "session-A")
			require.NoError(t, err)
			require.False(t, a.Blocked)
			require.Nil(t, a.TargetGroupID)
			page, err = penalties.ListEvents(context.Background(), user, 1, 10)
			require.NoError(t, err)
			require.EqualValues(t, 3, page.Total)
		})
	}
}

func TestSecurityPenaltyAsyncFindingCommitsWithLeaseFencedEvent(t *testing.T) {
	db := openPromptAuditIntegrationDB(t)
	user := insertIdentity(t, db, "users")
	cfg := service.DefaultSecurityPenaltyConfig()
	cfg.Enabled = true
	cfg.ObserveOnly = false
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO settings(key,value) VALUES($1,$2),($3,'true')", service.SettingKeySecurityPenalty, string(raw), service.SettingKeyRiskControlEnabled)
	require.NoError(t, err)
	penalties := service.NewSecurityPenaltyService(db, postgresPromptAuditSettingRepository{db: db}, nil)
	repo := NewPostgreSQLRepository(db)
	ProvidePenaltyCoordinator(nil, &PromptService{repo: repo}, penalties)
	snapshot := integrationSnapshot("penalty-async")
	snapshot.UserID = user
	snapshot.APIKeyID = 0
	snapshot.GroupID = nil
	snapshot.PenaltyContext = PenaltyRequestContext{SessionKey: "session-A", RequestID: "request-async"}
	job, err := repo.CreateStagingWithCapacity(context.Background(), snapshot, 1, 3, 100)
	require.NoError(t, err)
	require.NoError(t, repo.PublishQueued(context.Background(), job.ID))
	job, claimed, err := repo.ClaimNextJob(context.Background(), repo.clock.Now())
	require.NoError(t, err)
	require.True(t, claimed)
	job.Snapshot.FullPrompt = "SYNTHETIC_ASYNC_PROMPT"
	result := &NormalizedResult{
		Action: ActionBlock, Decision: EventCritical, RiskLevel: RiskCritical, Safety: "Unsafe",
		Categories: []string{"jailbreak"}, MatchedScanners: []string{"jailbreak"},
		ScannerScores: map[string]float64{"jailbreak": 1}, ScannerEvidence: map[string]string{"jailbreak": "Jailbreak"},
	}
	_, err = repo.Complete(context.Background(), job, result, false)
	require.NoError(t, err)
	_, err = repo.Complete(context.Background(), job, result, false)
	require.ErrorIs(t, err, ErrLeaseLost)
	page, err := penalties.ListEvents(context.Background(), user, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	full, err := penalties.GetEvent(context.Background(), user, page.Items[0].ID)
	require.NoError(t, err)
	require.Equal(t, "SYNTHETIC_ASYNC_PROMPT", full.FullPrompt)
}
