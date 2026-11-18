//go:build integration

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"database/sql"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

type batchWhitelistAdmin struct {
	service.AdminService
	repo service.AccountRepository
}

func (s *batchWhitelistAdmin) GetAccount(ctx context.Context, id int64) (*service.Account, error) {
	return s.repo.GetByID(ctx, id)
}

type batchWhitelistHTTP struct {
	t      *testing.T
	failID int64
}

func (u *batchWhitelistHTTP) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	require.Equal(u.t, http.MethodGet, req.Method)
	require.Equal(u.t, fmt.Sprintf("https://up-%d.example/v1/models", id), req.URL.String())
	body := fmt.Sprintf(`{"data":[{"id":"live-%d"}]}`, id)
	code := 200
	if id == u.failID {
		body = `{"error":"unavailable"}`
		code = 503
	}
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func (u *batchWhitelistHTTP) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestBatchWhitelistPostgresHandlerReadback(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("whitelist"), tcpostgres.WithUsername("fixture"), tcpostgres.WithPassword("fixture"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	repo := repository.NewAccountRepository(client, db, nil)
	ids := make([]int64, 0, 4)
	for range 4 {
		a := &service.Account{
			Name: "synthetic-whitelist", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Credentials: map[string]any{"api_key": "synthetic", "model_mapping": map[string]any{"old": "old"}},
			Extra:       map[string]any{"keep": "unchanged"}, Concurrency: 3, Priority: 19,
		}
		require.NoError(t, repo.Create(ctx, a))
		a.Credentials["base_url"] = fmt.Sprintf("https://up-%d.example/v1", a.ID)
		require.NoError(t, repo.Update(ctx, a))
		ids = append(ids, a.ID)
	}
	svc := service.NewAccountTestService(repo, nil, nil, nil, nil, &batchWhitelistHTTP{t: t, failID: ids[1]}, &config.Config{}, nil)
	h := NewAccountHandler(&batchWhitelistAdmin{repo: repo}, nil, nil, nil, nil, nil, nil, nil, svc, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/accounts/:id/models/sync-upstream", h.SyncUpstreamModels)
	r.GET("/accounts/:id", h.GetByID)
	read := func(id int64) map[string]any {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/accounts/%d", id), nil))
		require.Equal(t, 200, w.Code, w.Body.String())
		var response struct{ Data map[string]any }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		return response.Data
	}
	before := make(map[int64]map[string]any)
	for _, id := range ids {
		before[id] = read(id)
	}
	for _, id := range ids[:3] {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/accounts/%d/models/sync-upstream", id), strings.NewReader(`{"apply":true,"whitelist_only":true}`)))
		if id == ids[1] {
			require.Equal(t, 502, w.Code)
		} else {
			require.Equal(t, 200, w.Code, w.Body.String())
		}
	}
	for _, id := range ids {
		after := read(id)
		if id == ids[0] || id == ids[2] {
			model := fmt.Sprintf("live-%d", id)
			require.Equal(t, map[string]any{model: model}, after["credentials"].(map[string]any)["model_mapping"])
			after["credentials"].(map[string]any)["model_mapping"] = before[id]["credentials"].(map[string]any)["model_mapping"]
			// The existing CAS intentionally bumps the account revision.
			after["updated_at"] = before[id]["updated_at"]
		}
		require.Equal(t, before[id], after, "only selected successful white lists may change")
	}
}
