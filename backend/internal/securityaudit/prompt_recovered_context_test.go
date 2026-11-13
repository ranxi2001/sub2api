//go:build integration

package securityaudit

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const recoveredPromptCanary = "fixture restored private paragraph 734829"

type recoveredPromptScanner struct {
	calls int
	block bool
}

func (s *recoveredPromptScanner) Scan(_ context.Context, _ ActiveEndpoint, text string, _ []string) (*NormalizedResult, error) {
	if !strings.Contains(text, recoveredPromptCanary) {
		return nil, &GuardError{Code: ErrorCodeInvalidResponse}
	}
	s.calls++
	result := integrationResult(EventPass)
	if s.block {
		result = integrationResult(EventCritical)
	}
	result.ScannerEvidence = map[string]string{"fixture": recoveredPromptCanary}
	return result, nil
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
