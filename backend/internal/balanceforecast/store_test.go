package balanceforecast

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// A private schema on the task-owned loopback server; never accept a remote DSN.
func fixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	dsn := os.Getenv("FORECAST_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set FORECAST_TEST_POSTGRES_DSN to task-owned loopback PostgreSQL")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost"}, u.Hostname())
	require.Equal(t, "/balance_forecast_task", u.Path)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("forecast_%d", time.Now().UnixNano())
	_, err = db.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	private, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, private.Close())
		_, cleanupErr := db.Exec("DROP SCHEMA " + schema + " CASCADE")
		require.NoError(t, cleanupErr)
		require.NoError(t, db.Close())
	})
	// Projection of the existing migration columns used by this SQL. Full schema
	// + real billing repository coverage is a separate integration gate.
	schemaSQL, err := os.ReadFile("testdata/schema.sql")
	require.NoError(t, err)
	_, err = private.Exec(string(schemaSQL))
	require.NoError(t, err)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	_, err = private.Exec(`INSERT INTO balance_forecast_coverage VALUES(true,$1)`, now.Add(-48*time.Hour))
	require.NoError(t, err)
	_, err = private.Exec(`INSERT INTO users(id,balance,created_at) VALUES(1,30,$1)`, now.Add(-48*time.Hour))
	require.NoError(t, err)
	return &Store{DB: private}, now
}

func event(t *testing.T, s *Store, id string, user int64, cost float64, state string, at time.Time) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"command": map[string]any{"UserID": user, "BalanceCost": cost, "SubscriptionCost": 99, "AccountQuotaCost": 999}})
	require.NoError(t, err)
	_, err = s.DB.Exec(`INSERT INTO usage_billing_events(request_id,api_key_id,payload,state,result,applied_at) VALUES($1,1,$2,$3,'{"Applied":true,"NewBalance":30}',$4)`, id, string(raw), state, at)
	require.NoError(t, err)
}

func TestSettledRateSQL(t *testing.T) {
	s, now := fixture(t)
	for i, h := range []int{3, 2, 1} {
		event(t, s, fmt.Sprint(i), 1, 2.5, "complete", now.Add(-time.Duration(h)*time.Hour))
	}
	event(t, s, "pending", 1, 100, "pending", now.Add(-time.Hour))
	event(t, s, "subscription", 1, 0, "applied", now.Add(-time.Hour))
	event(t, s, "old", 1, 100, "complete", now.Add(-25*time.Hour))
	event(t, s, "exclusive-end", 1, 100, "complete", now)
	c, err := s.Candidates(context.Background(), now, 0, 200)
	require.NoError(t, err)
	require.Len(t, c, 1, "a reliable user must be returned by the settled SQL aggregation")
	require.Equal(t, 3, c[0].Samples)
	require.Equal(t, 7.5, c[0].Spent, "only settled user balance cost, including the actual multiplier")
	require.Equal(t, 24.0, c[0].ObservationHours)
	require.InDelta(t, .3125, c[0].Hourly, 1e-10)
	require.InDelta(t, 96, c[0].Hours, 1e-10)
}

func TestSQLCaptureRegionalReliabilityAndLegacyGuard(t *testing.T) {
	s, now := fixture(t)
	// One real billed request plus a capture and a settled delegated charge.
	event(t, s, "regular", 1, 2.5, "applied", now.Add(-3*time.Hour))
	_, err := s.DB.Exec(`INSERT INTO batch_image_money_holds VALUES('batch',1,1,3,'captured'),('released',1,1,900,'released'),('held',1,1,NULL,'held')`)
	require.NoError(t, err)
	for _, table := range []string{"usage_billing_dedup", "usage_billing_dedup_archive"} {
		_, err = s.DB.Exec("INSERT INTO "+table+" VALUES('batch_image_capture:batch',1,$1)", now.Add(-2*time.Hour))
		require.NoError(t, err)
	}
	_, err = s.DB.Exec(`INSERT INTO regional_quota_leases VALUES('region',1),('subscription',1); INSERT INTO regional_budget_allocations VALUES('grant:region',0),('grant:subscription',1)`)
	require.NoError(t, err)
	raw := fmt.Sprintf(`{"usage":{"api_key_id":1,"charge_nano_usd":1500000000,"completed_at":%q}}`, now.Add(-time.Hour).Format(time.RFC3339))
	_, err = s.DB.Exec(`INSERT INTO regional_settled_events VALUES('remote','region',$1,$2),('sub','subscription',$1,$2)`, raw, now.Add(-time.Hour))
	require.NoError(t, err)
	_, err = s.DB.Exec(`INSERT INTO usage_logs VALUES(1,'batch_image_capture:batch',1,0,3,$1),(1,'remote',1,0,1.5,$1),(1,'sub',1,1,999,$1)`, now.Add(-time.Hour))
	require.NoError(t, err)
	rows, err := s.Candidates(t.Context(), now, 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Covered)
	require.Equal(t, 7.0, rows[0].Spent)
	require.Equal(t, 3, rows[0].Samples)
	require.Greater(t, rows[0].Hours, 0.0, "canonical usage and hot/archive capture copies must not duplicate money")
	_, err = s.DB.Exec(`INSERT INTO usage_logs VALUES(1,'unproven-manual',1,0,500,$1)`, now.Add(-time.Hour))
	require.NoError(t, err)
	rows, err = s.Candidates(t.Context(), now, 0, 200)
	require.NoError(t, err)
	require.False(t, rows[0].Covered)
	require.Zero(t, rows[0].Hours, "incomplete accounting suppresses the estimate")
}

func TestSQLWindowCutoffNewUserAndNoNoise(t *testing.T) {
	s, now := fixture(t)
	_, err := s.DB.Exec(`UPDATE users SET created_at=$1`, now.Add(-2*time.Hour))
	require.NoError(t, err)
	for i, minutes := range []int{110, 60, 10} {
		event(t, s, fmt.Sprint(i), 1, 2, "complete", now.Add(-time.Duration(minutes)*time.Minute))
	}
	rows, err := s.Candidates(t.Context(), now, 0, 200)
	require.NoError(t, err)
	require.Equal(t, 2.0, rows[0].ObservationHours)
	require.Equal(t, 3.0, rows[0].Hourly)
	require.Equal(t, 10.0, rows[0].Hours)
	_, err = s.DB.Exec(`UPDATE balance_forecast_coverage SET started_at=$1`, now.Add(-30*time.Minute))
	require.NoError(t, err)
	rows, err = s.Candidates(t.Context(), now, 0, 200)
	require.NoError(t, err)
	require.Equal(t, 1, rows[0].Samples)
	require.Zero(t, rows[0].Hours)
	rows, err = s.Candidates(t.Context(), now, 1, 200)
	require.NoError(t, err)
	require.Empty(t, rows, "keyset excludes processed ID")
	_, err = s.Candidates(t.Context(), now, 0, 201)
	require.Error(t, err)
}
