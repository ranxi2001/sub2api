//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHTTP2AIOpsFailureIdentityScopedToAPIKey(t *testing.T) {
	for _, scenario := range []string{"shared_request", "unknown_model", "conflicting_completed_usage"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			tx, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			suffix := uuid.NewString()
			var userID, accountID int64
			var keys [3]int64
			require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'synthetic') RETURNING id`, "http2-identity-"+suffix+"@test.invalid").Scan(&userID))
			require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES($1,'openai','oauth','active',true) RETURNING id`, "http2-identity-"+suffix).Scan(&accountID))
			for i := range keys {
				require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'synthetic') RETURNING id`, userID, uuid.NewString()).Scan(&keys[i]))
			}
			now := time.Now().UTC().Truncate(time.Second)
			stamp := now.Add(-time.Minute)
			model := "gpt-5.4"
			requestID := "shared-" + suffix // Ingress accepts a supplied X-Request-ID.
			addUsage := func(key int64, id string) {
				t.Helper()
				var usageID int64
				require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,reasoning_effort,input_tokens,output_tokens,stream,created_at) VALUES($1,$2,$3,$4,$5,'high',100,10,true,$6) RETURNING id`, userID, key, accountID, id, model, stamp).Scan(&usageID))
				trace, marshalErr := json.Marshal(map[string]any{
					"version": 1, "started_at": stamp.Add(-time.Second).Format(time.RFC3339Nano),
					"outcome": "success", "status": 200, "body_complete": true,
					"canceled": false, "truncated": false, "client_disconnect": false,
					"downstream_error": false, "terminal": "completed",
					"protocol_decision": map[string]string{"upstream": "http"},
				})
				require.NoError(t, marshalErr)
				_, err = tx.ExecContext(ctx, `INSERT INTO request_timing_details(usage_log_id,trace_id,created_at,detail) VALUES($1,$2::uuid,$3,$4::jsonb)`, usageID, uuid.NewString(), stamp, string(trace))
				require.NoError(t, err)
			}
			addFailure := func(key int64, failureModel string, at time.Time) {
				t.Helper()
				_, err = tx.ExecContext(ctx, `INSERT INTO ops_error_logs(account_id,api_key_id,request_id,client_request_id,model,error_phase,error_type,error_owner,error_source,severity,status_code,created_at) VALUES($1,$2,$3,$4,$5,'network','transport_error','provider','upstream_transport','error',200,$6)`, accountID, key, requestID, requestID+"-client", failureModel, at)
				require.NoError(t, err)
			}
			capture := func() service.AIOpsEvidenceSnapshot {
				t.Helper()
				policy := service.DefaultAIOpsPolicy()
				policy.ObservationSeconds = 300
				evidence, captureErr := captureAIOpsEvidence(ctx, tx, accountID, policy, now, map[int64]aiOpsAccount{accountID: {quotaKnown: true}})
				require.NoError(t, captureErr)
				return evidence
			}
			addUsage(keys[0], "local:success-"+suffix)
			failureModel := model
			if scenario == "unknown_model" {
				failureModel = "unknown-model"
			}
			// Key A is older, so request-only DISTINCT ON incorrectly discards it.
			addFailure(keys[0], failureModel, stamp)
			addFailure(keys[1], model, stamp.Add(time.Second))
			if scenario == "conflicting_completed_usage" {
				addUsage(keys[0], "local:"+requestID)
				addUsage(keys[1], "client:"+requestID+"-client")
				// A key with no failure must retain its matching-ID success.
				addUsage(keys[2], "local:"+requestID)
			}
			assertPopulation := func() {
				t.Helper()
				evidence := capture()
				if scenario == "conflicting_completed_usage" {
					require.Equal(t, 4, evidence.Samples, "F3 conflicting completed usage must be excluded only for the same API key")
					require.InDelta(t, 0.5, evidence.ErrorRate, 0.000001, "F3 both scoped failures must survive success exclusion")
				} else if scenario == "unknown_model" {
					require.False(t, evidence.ModelCoverageComplete, "F3 an older different-key unknown-model failure must remain a cohort blocker")
					require.Equal(t, 3, evidence.Samples)
					require.InDelta(t, 2.0/3.0, evidence.ErrorRate, 0.000001)
				} else {
					require.Equal(t, 3, evidence.Samples, "F3 one success plus two different-key resets sharing X-Request-ID must be three samples")
					require.InDelta(t, 2.0/3.0, evidence.ErrorRate, 0.000001)
				}
				if scenario != "unknown_model" {
					require.True(t, evidence.ModelCoverageComplete)
				}
			}
			assertPopulation()
			// Duplicate logs for either key still contribute only one failure.
			addFailure(keys[0], failureModel, stamp.Add(2*time.Second))
			addFailure(keys[1], model, stamp.Add(3*time.Second))
			assertPopulation()
		})
	}
}
