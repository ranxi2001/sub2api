//go:build integration

package securityaudit

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const recoveredPromptCanary = "fixture restored private paragraph 734829"

type recoveredPromptScanner struct {
	calls   int
	block   bool
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *recoveredPromptScanner) Scan(ctx context.Context, _ ActiveEndpoint, text string, _ []string) (*NormalizedResult, error) {
	if !strings.Contains(text, recoveredPromptCanary) {
		return nil, &GuardError{Code: ErrorCodeInvalidResponse}
	}
	s.calls++
	if s.entered != nil {
		s.once.Do(func() { close(s.entered) })
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result := integrationResult(EventPass)
	if s.block {
		result = integrationResult(EventCritical)
	}
	result.ScannerEvidence = map[string]string{"fixture": recoveredPromptCanary}
	return result, nil
}

func TestRecoveredContextAsyncEnforcement(t *testing.T) {
	for _, scenario := range []string{"admission", "penalty"} {
		t.Run(scenario, func(t *testing.T) {
			require.NotEmpty(t, os.Getenv(promptAuditPostgresTestEnv))
			require.NotEmpty(t, os.Getenv(promptAuditRedisTestEnv))
			db := openPromptAuditIntegrationDB(t)
			client := redis.NewClient(&redis.Options{Addr: os.Getenv(promptAuditRedisTestEnv), DB: 14})
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			require.NoError(t, client.FlushDB(ctx).Err())
			defer client.FlushDB(context.Background())
			user, key := insertIdentity(t, db, "users"), insertIdentity(t, db, "api_keys")
			penaltyConfig := service.DefaultSecurityPenaltyConfig()
			penaltyConfig.Enabled = true
			penaltyConfig.ObserveOnly = false
			penaltyConfig.Threshold = 1
			encoded, err := json.Marshal(penaltyConfig)
			require.NoError(t, err)
			_, err = db.Exec("INSERT INTO settings(key,value) VALUES($1,$2),($3,'true')", service.SettingKeySecurityPenalty, string(encoded), service.SettingKeyRiskControlEnabled)
			require.NoError(t, err)
			penalties := service.NewSecurityPenaltyService(db, postgresPromptAuditSettingRepository{db: db}, nil)
			cfg := guardConfig(ActiveEndpoint{ID: "fixture", Enabled: true, TimeoutMS: 5000, InputLimit: 10000})
			cfg.BlockingEnabled = false
			cfg.AllGroups = true
			cfg.StorePassEvents = true
			repo := NewPostgreSQLRepository(db)
			svc := NewPromptService(&fakeConfigStore{cfg: cfg, active: true}, repo, NewRedisPayloadStore(client), nil, NewAtomicMetrics())
			svc.background = ctx
			scanner := &recoveredPromptScanner{block: true}
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			if scenario == "admission" {
				scanner.entered = make(chan struct{})
				scanner.release = release
			}
			svc.evaluator = NewGuardEvaluator(scanner, repo, NewAtomicMetrics())
			coordinator := ProvidePenaltyCoordinator(nil, svc, penalties)
			body, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": recoveredPromptCanary}}})
			require.NoError(t, err)
			req := Request{RequestID: "async-recovered-" + scenario, UserID: user, APIKeyID: key, Protocol: "openai_responses", Model: "fixture", Body: body,
				PenaltyContext: PenaltyRequestContext{SessionKey: "fixture-original-session", RequestID: "async-recovered-" + scenario}}
			if field := reflect.ValueOf(&req).Elem().FieldByName("NoBodyRetention"); field.IsValid() {
				field.SetBool(true)
			}
			admitted := make(chan Decision, 1)
			checkDone := make(chan struct{})
			go func() { defer close(checkDone); admitted <- coordinator.Check(ctx, req) }()
			defer func() { unblock(); cancel(); <-checkDone; svc.enqueueWG.Wait() }()
			if scenario == "admission" {
				select {
				case <-scanner.entered:
				case <-time.After(2 * time.Second):
					t.Fatal("async scanner barrier not reached")
				}
				payloadKeys, err := client.Keys(ctx, PayloadKeyPrefix+"*").Result()
				require.NoError(t, err)
				require.Empty(t, payloadKeys, "scanner-held recovered text must not be staged in durable Redis")
				select {
				case decision := <-admitted:
					require.True(t, decision.AllowNextStage, "async risk must preserve request admission semantics")
				case <-time.After(time.Second):
					t.Fatal("RECOVERED_ASYNC_ADMISSION_BLOCKED: gateway admission waited for an unreleased scanner")
				}
				unblock()
			} else {
				select {
				case decision := <-admitted:
					require.True(t, decision.AllowNextStage)
				case <-time.After(2 * time.Second):
					t.Fatal("async admission did not finish")
				}
			}
			svc.enqueueWG.Wait()
			page, err := penalties.ListEvents(ctx, user, 1, 10)
			require.NoError(t, err)
			require.EqualValues(t, 1, page.Total, "RECOVERED_ASYNC_PENALTY_MISSING: actual async risk must commit its configured violation")
			detail, err := penalties.GetEvent(ctx, user, page.Items[0].ID)
			require.NoError(t, err)
			detailRaw, err := json.Marshal(detail)
			require.NoError(t, err)
			require.NotContains(t, string(detailRaw), recoveredPromptCanary)
			var session string
			require.NoError(t, db.QueryRow("SELECT session_key FROM security_penalty_events WHERE id=$1", detail.ID).Scan(&session))
			require.Equal(t, "fixture-original-session", session, "the penalty must retain its original session scope")
			blocked, err := penalties.Admission(ctx, user, session)
			require.NoError(t, err)
			require.True(t, blocked.Blocked, "async risk must enforce the configured threshold")
			other, err := penalties.Admission(ctx, user, "fixture-other-session")
			require.NoError(t, err)
			require.False(t, other.Blocked, "session policy must not widen to other work")
			var jobs, events string
			require.NoError(t, db.QueryRow("SELECT string_agg(row_to_json(t)::text,'') FROM prompt_audit_jobs t").Scan(&jobs))
			require.NoError(t, db.QueryRow("SELECT string_agg(row_to_json(t)::text,'') FROM prompt_audit_events t").Scan(&events))
			require.Contains(t, jobs, `"execution_mode":"async_audit"`)
			require.Contains(t, jobs, `"status":"done"`)
			require.NotContains(t, jobs+events, recoveredPromptCanary)
			keys, err := client.Keys(ctx, PayloadKeyPrefix+"*").Result()
			require.NoError(t, err)
			require.Empty(t, keys)
			require.Equal(t, 1, scanner.calls)
			// A failed penalty write must roll back the metadata/event too.
			_, err = db.Exec(`CREATE FUNCTION fixture_reject_async_penalty() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture penalty unavailable'; END $$`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE TRIGGER fixture_reject_async_penalty BEFORE INSERT ON security_penalty_events FOR EACH ROW EXECUTE FUNCTION fixture_reject_async_penalty()`)
			require.NoError(t, err)
			defer db.Exec(`DROP TRIGGER IF EXISTS fixture_reject_async_penalty ON security_penalty_events; DROP FUNCTION IF EXISTS fixture_reject_async_penalty()`)
			req.RequestID += "-rollback"
			req.PenaltyContext.RequestID = req.RequestID
			req.PenaltyContext.SessionKey = "fixture-rollback-session"
			require.True(t, coordinator.Check(ctx, req).AllowNextStage)
			svc.enqueueWG.Wait()
			var jobCount, eventCount int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM prompt_audit_jobs").Scan(&jobCount))
			require.NoError(t, db.QueryRow("SELECT count(*) FROM prompt_audit_events").Scan(&eventCount))
			require.Equal(t, 1, jobCount, "failed penalty cannot leave a successful async job")
			require.Equal(t, 1, eventCount, "event and penalty must commit in one transaction")
		})
	}
}

// Uses real PostgreSQL event/job persistence and real Redis payload storage.
// Reflection only lets the unchanged test compile on the pre-provenance RED;
// that baseline actually evaluates/persists the canary and must fail below.
func TestRecoveredContextAuditPersistence(t *testing.T) {
	for _, mode := range []string{"blocking_allow", "blocking_risk", "async"} {
		t.Run(mode, func(t *testing.T) {
			require.NotEmpty(t, os.Getenv(promptAuditPostgresTestEnv), "isolated PostgreSQL is required; cannot skip evidence")
			address := os.Getenv(promptAuditRedisTestEnv)
			require.NotEmpty(t, address, "isolated Redis is required; cannot skip evidence")
			db := openPromptAuditIntegrationDB(t)
			client := redis.NewClient(&redis.Options{Addr: address, DB: 14})
			t.Cleanup(func() { _ = client.Close() })
			ctx := context.Background()
			require.NoError(t, client.FlushDB(ctx).Err())
			defer client.FlushDB(ctx)
			user, key := insertIdentity(t, db, "users"), insertIdentity(t, db, "api_keys")
			repo := NewPostgreSQLRepository(db)
			cfg := guardConfig(ActiveEndpoint{ID: "fixture", Enabled: true, TimeoutMS: 1000, InputLimit: 10000})
			cfg.AllGroups, cfg.StorePassEvents, cfg.QueueCapacity = true, true, 8
			cfg.BlockingEnabled = mode != "async"
			config := &fakeConfigStore{cfg: cfg, active: true}
			scanner := &recoveredPromptScanner{block: mode == "blocking_risk"}
			svc := NewPromptService(config, repo, NewRedisPayloadStore(client), nil, NewAtomicMetrics())
			svc.evaluator = NewGuardEvaluator(scanner, repo, NewAtomicMetrics())
			svc.background = ctx
			body, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": recoveredPromptCanary}}})
			require.NoError(t, err)
			req := Request{RequestID: "recovered-" + mode, UserID: user, APIKeyID: key, Protocol: "openai_responses", Model: "fixture", Body: body}
			field := reflect.ValueOf(&req).Elem().FieldByName("NoBodyRetention")
			if field.IsValid() {
				field.SetBool(true)
			}
			if mode == "async" {
				require.NoError(t, svc.Enqueue(ctx, req))
				svc.enqueueWG.Wait()
			} else {
				decision, err := svc.Evaluate(ctx, req)
				require.NoError(t, err)
				require.NotNil(t, decision)
				require.Equal(t, mode != "blocking_risk", decision.AllowNextStage, "privacy must not weaken moderation")
			}
			var jobs, events string
			require.NoError(t, db.QueryRow("SELECT COALESCE(string_agg(row_to_json(t)::text, ''), '') FROM prompt_audit_jobs t").Scan(&jobs))
			require.NoError(t, db.QueryRow("SELECT COALESCE(string_agg(row_to_json(t)::text, ''), '') FROM prompt_audit_events t").Scan(&events))
			keys, err := client.Keys(ctx, PayloadKeyPrefix+"*").Result()
			require.NoError(t, err)
			persisted := jobs + events
			for _, key := range keys {
				value, err := client.Get(ctx, key).Result()
				require.NoError(t, err)
				persisted += value
			}
			require.NotContains(t, persisted, recoveredPromptCanary, "SHADOW_AUDIT_BODY_RETAINED: recovered text must not leave its bounded shadow store")
			require.Equal(t, 1, scanner.calls, "recovered text must still be evaluated")
			require.NotEmpty(t, events, "metadata decision must remain auditable")
			require.Empty(t, keys, "recovered text must never enter the durable asynchronous payload queue")
		})
	}
}
