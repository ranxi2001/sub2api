//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Reproducible isolated SQL comparison. Timings are observations, not pass/fail
// latency thresholds or a claim about the production million-request window.
func TestProfitOperationsCachedWindowTiming(t *testing.T) {
	db, _ := repository.UpstreamChannelsTestDatabase(t)
	id := func(query string, args ...any) int64 {
		t.Helper()
		var v int64
		require.NoError(t, db.QueryRow(query, args...).Scan(&v))
		return v
	}
	user := id(`INSERT INTO users(email,password_hash,balance) VALUES('cache@test.invalid','synthetic',0) RETURNING id`)
	key := id(`INSERT INTO api_keys(user_id,key,name) VALUES($1,'cache-fixture','fixture') RETURNING id`, user)
	a := id(`INSERT INTO accounts(name,platform,type,credentials) VALUES('alpha','openai','apikey','{"base_url":"https://fixture.invalid"}') RETURNING id`)
	b := id(`INSERT INTO accounts(name,platform,type,credentials) VALUES('beta','openai','apikey','{"base_url":"https://fixture.invalid"}') RETURNING id`)
	until := time.Now().UTC().Truncate(time.Second)
	from := until.Add(-time.Hour)
	_, err := db.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,total_cost,actual_cost,created_at)
 SELECT $1,$2,CASE WHEN n%2=0 THEN $3::bigint ELSE $4::bigint END,'cache-'||n,'fixture-model',10,4,$5::timestamptz FROM generate_series(1,10000)n`, user, key, a, b, from.Add(time.Minute))
	require.NoError(t, err)
	svc := service.NewChannelService(repository.NewChannelRepository(db), nil, nil, nil, nil)
	f := service.UpstreamOperationsFilter{From: from, Until: until, Dimension: "account", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 1}
	var baseline []time.Duration
	for i := 0; i < 3; i++ {
		f.Page = 1 + i%2
		if i == 2 {
			f.Sort = "name"
		}
		at := time.Now()
		_, err := svc.ProfitOperations(context.Background(), f)
		require.NoError(t, err)
		baseline = append(baseline, time.Since(at))
	}
	handler := admin.NewChannelHandler(svc, nil, nil)
	r := gin.New()
	r.GET("/snapshot", handler.ProfitOperations)
	var cached []time.Duration
	var observed time.Time
	var summary json.RawMessage
	for i := 0; i < 4; i++ {
		sort := "requests"
		if i >= 2 {
			sort = "name"
		}
		target := fmt.Sprintf("/snapshot?from=%s&until=%s&dimension=account&sort=%s&page=%d&page_size=1", from.Format(time.RFC3339), until.Format(time.RFC3339), sort, 1+i%2)
		w := httptest.NewRecorder()
		at := time.Now()
		r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		cached = append(cached, time.Since(at))
		require.Equal(t, 200, w.Code, w.Body.String())
		var envelope struct {
			Data service.ProfitOperationsSnapshot
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		var usage struct {
			Summary json.RawMessage
			Rows    []json.RawMessage
		}
		require.NoError(t, json.Unmarshal(envelope.Data.Usage, &usage))
		require.Len(t, usage.Rows, 1)
		if i == 0 {
			observed = envelope.Data.QueriedAt
			summary = usage.Summary
		} else {
			require.Equal(t, observed, envelope.Data.QueriedAt)
			require.JSONEq(t, string(summary), string(usage.Summary))
		}
	}
	t.Logf("synthetic PostgreSQL rows=10000 timezone=UTC window=[%s,%s) uncached(first,page,sort)=%v cached(first,page,sort,repeat)=%v", from.Format(time.RFC3339), until.Format(time.RFC3339), baseline, cached)
}
