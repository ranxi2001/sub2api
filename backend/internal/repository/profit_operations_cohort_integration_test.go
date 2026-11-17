//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type profitCohortAmounts struct {
	Requests        int      `json:"requests"`
	Standard        float64  `json:"standard_charge"`
	Income          float64  `json:"request_income"`
	Cost            *float64 `json:"estimated_cost"`
	Profit          *float64 `json:"estimated_profit"`
	CoveredCost     float64  `json:"covered_estimated_cost"`
	CoveredIncome   float64  `json:"covered_income"`
	CoveredRequests int      `json:"cost_covered_requests"`
	UnknownCost     int      `json:"unknown_cost_requests"`
	Attributed      int      `json:"attributed_requests"`
	UnknownUpstream int      `json:"unknown_upstream_requests"`
}

func TestProfitOperationsMixedCohortMissingAccountDrilldown(t *testing.T) {
	ctx := context.Background()
	db, _ := repository.UpstreamChannelsTestDatabase(t)
	repo := repository.NewChannelRepository(db)
	entities, ok := repo.(service.UpstreamChannelRepository)
	require.True(t, ok)
	a, err := entities.CreateUpstreamChannel(ctx, service.UpstreamChannelMutation{Code: "mixed-a", Name: "Mixed A", Confirm: true})
	require.NoError(t, err)
	b, err := entities.CreateUpstreamChannel(ctx, service.UpstreamChannelMutation{Code: "mixed-b", Name: "Mixed B", Confirm: true})
	require.NoError(t, err)
	var user, key int64
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash) VALUES('mixed-cohort@test.invalid','fixture') RETURNING id`).Scan(&user))
	require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,key,name) VALUES($1,'mixed-cohort-key','fixture') RETURNING id`, user).Scan(&key))
	sharedURL := "https://shared.example:8443/Tenant/v1"
	otherURL := "https://shared.example:8443/Other/v1"
	type sample struct {
		platform, kind, endpoint string
		channel                  int64
		multiplier               float64
		requests                 int
		standard, income         float64
	}
	samples := []sample{
		{"openai", "apikey", sharedURL, a.ID, .25, 2, 10, 4},
		{"gemini", "oauth", "", a.ID, .5, 1, 6, 5},
		{"openai", "apikey", sharedURL, b.ID, .2, 1, 20, 9},
		{"openai", "apikey", sharedURL, 0, .1, 2, 30, 12},
		{"openai", "apikey", otherURL, 0, .4, 1, 5, 3},
		{"openai", "apikey", "https://user:private@secret.example/v1?token=private", 0, .3, 1, 10, 7},
	}
	insertUsage := func(account int64, count int, standard, income float64) {
		t.Helper()
		for i := 0; i < count; i++ {
			_, e := db.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,created_at) VALUES($1,$2,$3,$4,'mixed-cohort-model',$5,$6,clock_timestamp())`, user, key, account, fmt.Sprintf("mixed-%d-%d", account, i), standard, income)
			require.NoError(t, e)
		}
	}
	ids := make([]int64, len(samples))
	for i, s := range samples {
		credentials, e := json.Marshal(map[string]string{"base_url": s.endpoint})
		require.NoError(t, e)
		require.NoError(t, db.QueryRow(`INSERT INTO accounts(name,platform,type,credentials,extra) VALUES($1,$2,$3,$4::jsonb,jsonb_build_object('cost_multiplier',$5::numeric)) RETURNING id`, fmt.Sprintf("mixed-%d", i), s.platform, s.kind, string(credentials), s.multiplier).Scan(&ids[i]))
		if s.channel != 0 {
			_, e = entities.BindUpstreamChannelAccounts(ctx, service.UpstreamChannelBindingsMutation{ChannelID: s.channel, Bindings: []service.UpstreamChannelBinding{{AccountID: ids[i]}}, Confirm: true})
			require.NoError(t, e)
		}
		insertUsage(ids[i], s.requests, s.standard, s.income)
	}
	// Model an orphaned historical/imported usage row ONLY in this private UUID
	// database. The application schema and the shared integration DB are untouched.
	constraints, err := db.Query(`SELECT conname FROM pg_constraint WHERE contype='f' AND conrelid='usage_logs'::regclass AND confrelid='accounts'::regclass`)
	require.NoError(t, err)
	var names []string
	for constraints.Next() {
		var name string
		require.NoError(t, constraints.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, constraints.Err())
	require.NoError(t, constraints.Close())
	require.NotEmpty(t, names)
	for _, name := range names {
		_, err = db.Exec(`ALTER TABLE usage_logs DROP CONSTRAINT ` + pq.QuoteIdentifier(name))
		require.NoError(t, err)
	}
	const missingAccount = int64(999999999)
	var missingCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts WHERE id=$1`, missingAccount).Scan(&missingCount))
	require.Zero(t, missingCount)
	insertUsage(missingAccount, 2, 7, 6)
	var now time.Time
	require.NoError(t, db.QueryRow(`SELECT clock_timestamp() + interval '1 second'`).Scan(&now))
	now = now.UTC()
	var before string
	require.NoError(t, db.QueryRow(`SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM usage_logs u`).Scan(&before))

	h := admin.NewChannelHandler(service.NewChannelService(repo, nil, nil, nil, nil), nil, nil)
	r := gin.New()
	// Authorization is covered by the entity HTTP suite; exercise the real read
	// handler and persisted repository here without replacing either with a mock.
	r.GET("/profit-operations/snapshot", h.ProfitOperations)
	type bucket struct {
		profitCohortAmounts
		Key string `json:"key"`
	}
	type usage struct {
		Summary profitCohortAmounts `json:"summary"`
		Rows    []bucket            `json:"rows"`
		Total   int                 `json:"total"`
	}
	read := func(upstream, dimension string, account, channel int64, page, size int) usage {
		t.Helper()
		q := url.Values{"from": {now.Add(-2 * time.Hour).Format(time.RFC3339)}, "until": {now.Format(time.RFC3339)}, "upstream": {upstream}, "dimension": {dimension}, "account_id": {fmt.Sprint(account)}, "channel_id": {fmt.Sprint(channel)}, "page": {fmt.Sprint(page)}, "page_size": {fmt.Sprint(size)}, "sort": {"name"}, "direction": {"asc"}}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/profit-operations/snapshot?"+q.Encode(), nil))
		require.Equal(t, 200, w.Code, w.Body.String())
		require.NotContains(t, w.Body.String(), "private")
		require.NotContains(t, w.Body.String(), "secret.example")
		var out struct {
			Data struct{ Usage usage }
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out.Data.Usage
	}
	all := map[string]profitCohortAmounts{}
	var summary profitCohortAmounts
	for page := 1; page <= 3; page++ {
		out := read("", "upstream", 0, 0, page, 2)
		require.Equal(t, 6, out.Total)
		require.Len(t, out.Rows, 2)
		if page == 1 {
			summary = out.Summary
		} else {
			require.Equal(t, summary, out.Summary, "summary is independent of row pagination")
		}
		for _, row := range out.Rows {
			all[row.Key] = row.profitCohortAmounts
		}
	}
	require.Equal(t, 10, summary.Requests)
	require.Equal(t, 135.0, summary.Standard)
	require.Equal(t, 68.0, summary.Income)
	require.Nil(t, summary.Cost)
	require.Nil(t, summary.Profit)
	require.Equal(t, 23.0, summary.CoveredCost)
	require.Equal(t, 56.0, summary.CoveredIncome)
	require.Equal(t, 8, summary.CoveredRequests)
	require.Equal(t, 2, summary.UnknownCost)
	require.Equal(t, 7, summary.Attributed)
	require.Equal(t, 3, summary.UnknownUpstream)
	missing := read("unknown:account_missing", "upstream", 0, 0, 1, 20)
	require.Equal(t, 2, missing.Summary.Requests, "missing-account drill-down must exclude configured accounts outside the selected cohort")
	for _, scope := range []struct {
		key         string
		requests    int
		standard    float64
		income      float64
		cost        *float64
		unknownCost int
		unknownURL  int
	}{
		{fmt.Sprintf("channel:%d", a.ID), 3, 26, 13, profitCohortFloat(8), 0, 0},
		{fmt.Sprintf("channel:%d", b.ID), 1, 20, 9, profitCohortFloat(4), 0, 0},
		{sharedURL, 2, 60, 24, profitCohortFloat(6), 0, 0},
		{otherURL, 1, 5, 3, profitCohortFloat(2), 0, 0},
		{"unknown:invalid_url", 1, 10, 7, profitCohortFloat(3), 0, 1},
		{"unknown:account_missing", 2, 14, 12, nil, 2, 2},
	} {
		t.Run(scope.key, func(t *testing.T) {
			for _, dimension := range []string{"upstream", "account", "model"} {
				out := read(scope.key, dimension, 0, 0, 1, 20)
				require.Equal(t, all[scope.key], out.Summary, "bucket and drill-down must have identical amounts and unknown coverage: %s", dimension)
				require.Equal(t, scope.requests, out.Summary.Requests)
				require.Equal(t, scope.standard, out.Summary.Standard)
				require.Equal(t, scope.income, out.Summary.Income)
				require.Equal(t, scope.cost, out.Summary.Cost)
				require.Equal(t, scope.unknownCost, out.Summary.UnknownCost)
				require.Equal(t, scope.unknownURL, out.Summary.UnknownUpstream)
				if scope.cost != nil {
					require.NotNil(t, out.Summary.Profit)
					require.Equal(t, scope.income-*scope.cost, *out.Summary.Profit)
				}
			}
		})
	}
	byID := read("", "upstream", 0, a.ID, 1, 20)
	require.Equal(t, all[fmt.Sprintf("channel:%d", a.ID)], byID.Summary)
	byAccount := read("unknown:account_missing", "account", missingAccount, 0, 1, 20)
	require.Equal(t, all["unknown:account_missing"], byAccount.Summary)
	// A valid account excluded by the unknown filter must not become missing.
	require.Zero(t, read("unknown:account_missing", "account", ids[3], 0, 1, 20).Summary.Requests)
	require.Zero(t, read("unknown:account_missing", "upstream", 0, a.ID, 1, 20).Summary.Requests)
	require.Zero(t, read(sharedURL, "upstream", 0, a.ID, 1, 20).Summary.Requests)
	var after string
	require.NoError(t, db.QueryRow(`SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM usage_logs u`).Scan(&after))
	require.Equal(t, before, after, "profit reads must never rewrite historical usage")
}

func profitCohortFloat(v float64) *float64 { return &v }
