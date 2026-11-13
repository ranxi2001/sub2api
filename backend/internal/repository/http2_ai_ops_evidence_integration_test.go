//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHTTP2AIOpsEvidenceIncludesCommittedTransportFailure(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	// Everything stays in this transaction: no global policy, cohorts or other
	// tests' observations are changed by this evidence-reader regression.
	suffix := uuid.NewString()
	var userID, keyID, accountID, otherAccountID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'synthetic') RETURNING id`, "http2-"+suffix+"@test.invalid").Scan(&userID))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'synthetic') RETURNING id`, userID, "http2-"+suffix).Scan(&keyID))
	for _, id := range []*int64{&accountID, &otherAccountID} {
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES($1,'openai','oauth','active',true) RETURNING id`, "http2-"+uuid.NewString()).Scan(id))
	}
	now := time.Now().UTC().Truncate(time.Second)
	stamp := now.Add(-time.Minute)
	policy := service.DefaultAIOpsPolicy()
	policy.ObservationSeconds = 300
	accounts := map[int64]aiOpsAccount{accountID: {quotaKnown: true}}
	model := "gpt-5.4"

	addUsage := func(requestID, outcome string, usageKeyID int64) int64 {
		t.Helper()
		var usageID int64
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,reasoning_effort,input_tokens,output_tokens,stream,created_at) VALUES($1,$2,$3,$4,$5,'high',100,10,true,$6) RETURNING id`, userID, usageKeyID, accountID, requestID, model, stamp).Scan(&usageID))
		trace := map[string]any{
			"version": 1, "started_at": stamp.Add(-time.Second).Format(time.RFC3339Nano),
			"outcome": outcome, "status": 200, "body_complete": outcome == "success",
			"canceled": false, "truncated": outcome != "success", "client_disconnect": false,
			"downstream_error": false, "terminal": "completed",
			"protocol_decision": map[string]string{"upstream": "http"},
		}
		raw, marshalErr := json.Marshal(trace)
		require.NoError(t, marshalErr)
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO request_timing_details(usage_log_id,trace_id,created_at,detail) VALUES($1,$2::uuid,$3,$4::jsonb)`, usageID, uuid.NewString(), stamp, string(raw))
		require.NoError(t, insertErr)
		return usageID
	}
	addError := func(requestID, phase, errorType, owner, source string, status int, upstream any, business, countTokens bool, account int64, at time.Time, errorModel string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO ops_error_logs(account_id,api_key_id,request_id,client_request_id,model,error_phase,error_type,error_owner,error_source,severity,status_code,upstream_status_code,is_business_limited,is_count_tokens,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'error',$10,$11,$12,$13,$14) RETURNING id`, account, keyID, requestID, requestID+"-client", errorModel, phase, errorType, owner, source, status, upstream, business, countTokens, at).Scan(&id))
		return id
	}
	capture := func() service.AIOpsEvidenceSnapshot {
		t.Helper()
		evidence, captureErr := captureAIOpsEvidence(ctx, tx, accountID, policy, now, accounts)
		require.NoError(t, captureErr)
		return evidence
	}
	assertResetPopulation := func() {
		t.Helper()
		evidence := capture()
		require.Equal(t, 2, evidence.Samples, "AIOps must count one success plus the committed HTTP200 transport failure")
		require.InDelta(t, 0.5, evidence.ErrorRate, 0.000001)
		require.True(t, evidence.ModelCoverageComplete, "same known model/effort/protocol cohort must remain complete")
		require.Equal(t, model, evidence.Model)
		require.Equal(t, "high", evidence.Effort)
		require.Equal(t, "http", evidence.Protocol)
	}

	addUsage("local:success-"+suffix, "success", keyID)
	resetRequestID := "reset-" + suffix
	resetID := addError(resetRequestID, "network", "transport_error", "provider", "upstream_transport", 200, nil, false, false, accountID, stamp, model)
	assertResetPopulation()
	var actualStatus int
	var upstreamStatus sql.NullInt64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT status_code,upstream_status_code FROM ops_error_logs WHERE id=$1`, resetID).Scan(&actualStatus, &upstreamStatus))
	require.Equal(t, 200, actualStatus)
	require.False(t, upstreamStatus.Valid, "evidence classification must not fabricate an upstream HTTP502")

	addError(resetRequestID, "network", "transport_error", "provider", "upstream_transport", 200, nil, false, false, accountID, stamp.Add(time.Second), model)
	partialUsageID := addUsage("client:"+resetRequestID+"-client", "failed", keyID) // billable partial output is not success
	assertResetPopulation()
	// Even a conflicting completed usage trace cannot count the same failed
	// request again as a successful sample in the error-rate denominator.
	_, err = tx.ExecContext(ctx, `UPDATE request_timing_details SET detail=detail||'{"outcome":"success","body_complete":true,"truncated":false}'::jsonb WHERE usage_log_id=$1`, partialUsageID)
	require.NoError(t, err)
	assertResetPopulation()
	for _, correlatedID := range []string{"local:" + resetRequestID, resetRequestID} {
		_, err = tx.ExecContext(ctx, `UPDATE usage_logs SET request_id=$1 WHERE id=$2`, correlatedID, partialUsageID)
		require.NoError(t, err)
		assertResetPopulation()
	}
	_, err = tx.ExecContext(ctx, `SAVEPOINT other_key_control`)
	require.NoError(t, err)
	var otherKeyID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'other synthetic key') RETURNING id`, userID, "other-"+suffix).Scan(&otherKeyID))
	addUsage("client:"+resetRequestID+"-client", "success", otherKeyID)
	otherKeyEvidence := capture()
	require.Equal(t, 3, otherKeyEvidence.Samples, "another API key's same client request ID must remain a separate success")
	require.InDelta(t, 1.0/3.0, otherKeyEvidence.ErrorRate, 0.000001)
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT other_key_control`)
	require.NoError(t, err)
	assertResetPopulation()

	for _, control := range []struct {
		name, phase, errorType, owner string
		status                       int
		upstream                     any
		business, countTokens        bool
	}{
		{name: "recovered_failover", phase: "upstream", errorType: "upstream_error", owner: "provider", status: 200, upstream: 502},
		{name: "business", phase: "upstream", errorType: "upstream_error", owner: "provider", status: 503, upstream: 503, business: true},
		{name: "business_transport", phase: "network", errorType: "transport_error", owner: "provider", status: 200, business: true},
		{name: "cancellation", phase: "network", errorType: "client_canceled", owner: "provider", status: 499},
		{name: "rate_limit", phase: "upstream", errorType: "upstream_error", owner: "provider", status: 429, upstream: 429},
		{name: "overload", phase: "upstream", errorType: "upstream_error", owner: "provider", status: 529, upstream: 529},
		{name: "wrapped_rate_limit", phase: "upstream", errorType: "upstream_error", owner: "provider", status: 502, upstream: 429},
		{name: "wrapped_overload", phase: "upstream", errorType: "upstream_error", owner: "provider", status: 502, upstream: 529},
		{name: "count_tokens", phase: "network", errorType: "transport_error", owner: "provider", status: 200, countTokens: true},
		{name: "client_owned", phase: "network", errorType: "transport_error", owner: "client", status: 200},
	} {
		t.Logf("excluded control: %s", control.name)
		addError(control.name+suffix, control.phase, control.errorType, control.owner, "upstream_transport", control.status, control.upstream, control.business, control.countTokens, accountID, stamp, model)
		assertResetPopulation()
	}
	addError("other-account"+suffix, "network", "transport_error", "provider", "upstream_transport", 200, nil, false, false, otherAccountID, stamp, model)
	addError("outside-window"+suffix, "network", "transport_error", "provider", "upstream_transport", 200, nil, false, false, accountID, now.Add(-time.Hour), model)
	assertResetPopulation()

	unknownID := addError("unknown-model"+suffix, "network", "transport_error", "provider", "upstream_transport", 200, nil, false, false, accountID, stamp, "unknown-model")
	require.False(t, capture().ModelCoverageComplete, "transport inclusion must preserve unknown-model cohort blocking")
	_, err = tx.ExecContext(ctx, `DELETE FROM ops_error_logs WHERE id=$1`, unknownID)
	require.NoError(t, err)
	addError("http503"+suffix, "upstream", "upstream_error", "provider", "upstream_http", 503, 503, false, false, accountID, stamp, model)
	evidence := capture()
	require.Equal(t, 3, evidence.Samples, "genuine upstream HTTP failures remain eligible alongside transport failures")
	require.InDelta(t, 2.0/3.0, evidence.ErrorRate, 0.000001)
	require.True(t, evidence.ModelCoverageComplete)
}
