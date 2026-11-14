//go:build integration

package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Only existing production constructors are used, including in the RED run.
// The PostgreSQL fixture is isolated and never uses application configuration.
func matureProviderDatabase(t *testing.T) *sql.DB {
	dsn := os.Getenv("MATURE_TEST_POSTGRES")
	if dsn == "" {
		// The general integration suite has no feature-specific DSN. Launch
		// an isolated fixture rather than consulting application settings.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		container, err := tcpostgres.Run(ctx, "postgres:16-alpine", tcpostgres.WithDatabase("mature_test"), tcpostgres.WithUsername("mature_fixture"), tcpostgres.WithPassword("synthetic-fixture-only"), tcpostgres.BasicWaitStrategies())
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			_ = container.Terminate(cleanup)
		})
		dsn, err = container.ConnectionString(ctx, "sslmode=disable")
		require.NoError(t, err)
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
	schema := fmt.Sprintf("mature_%d", time.Now().UnixNano())
	_, err = db.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	isolatedDSN := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		isolatedDSN = parsed.String()
	}
	isolated, err := sql.Open("postgres", isolatedDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = isolated.Close() })
	// The frozen baseline has no provider schema; it still reaches the HTTP
	// assertion and fails as 404, rather than failing fixture setup.
	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	for _, f := range entries {
		if strings.Contains(f.Name(), "mature_providers") && strings.HasSuffix(f.Name(), ".sql") {
			raw, e := migrations.FS.ReadFile(f.Name())
			require.NoError(t, e)
			for i := 0; i < 2; i++ {
				_, e = isolated.Exec(string(raw))
				require.NoError(t, e, "additive migration must support replay")
			}
		}
	}
	return isolated
}

func TestMatureProvidersPostgresCRUD(t *testing.T) {
	isolated := matureProviderDatabase(t)
	newRouter := func() *gin.Engine {
		r := gin.New()
		ch := admin.NewChannelHandler(service.NewChannelService(repository.NewChannelRepository(isolated), nil, nil, nil, nil), nil, nil)
		registerChannelRoutes(r.Group("/api/v1/admin"), &handler.Handlers{Admin: &handler.AdminHandlers{Channel: ch}})
		return r
	}
	a, b := newRouter(), newRouter()
	request := func(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/admin/providers"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		require.Equal(t, 200, w.Code, "provider records must persist through the real HTTP API: %s", w.Body.String())
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		return envelope.Data
	}
	body := `{"name":"Synthetic provider","site_type":"sub2api","base_url":"https://provider.example.invalid","notes":"fixture","cost_multiplier":0.25}`
	p := decode(request(a, "POST", "", body))
	require.NotEmpty(t, p["id"])
	require.Equal(t, "unknown", p["status"], "saving a record is not a connection or generation test")
	metrics := p["metrics"].(map[string]any)
	for _, field := range []string{"balance", "today_spend", "total_spend", "total_recharge"} {
		require.Nil(t, metrics[field], field+" must remain unknown")
	}
	require.Equal(t, "", metrics["currency"], "unobserved currency must not be guessed")
	path := fmt.Sprintf("/%.0f", p["id"])
	require.Equal(t, p, decode(request(b, "GET", path, "")), "a second instance reads the persisted provider")
	list := decode(request(b, "GET", "?search=Synthetic&page=1&page_size=1", ""))
	require.EqualValues(t, 1, list["total"])
	require.Len(t, list["items"], 1)
	page := decode(request(b, "GET", "?search=Synthetic&page=2&page_size=1", ""))
	require.EqualValues(t, 1, page["total"])
	require.Empty(t, page["items"])
	update := strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"revision":%.0f}`, p["revision"])
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, r := range []*gin.Engine{a, b} {
		wg.Add(1)
		go func(r *gin.Engine) { defer wg.Done(); codes <- request(r, "PUT", path, update).Code }(r)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	require.Equal(t, map[int]int{200: 1, 409: 1}, counts, "two instances must not overwrite the same revision")
	p = decode(request(a, "GET", path, ""))
	require.Equal(t, 400, request(a, "DELETE", path, fmt.Sprintf(`{"revision":%.0f}`, p["revision"])).Code)
	decode(request(a, "DELETE", path, fmt.Sprintf(`{"revision":%.0f,"confirm":true}`, p["revision"])))
	require.Equal(t, 404, request(b, "GET", path, "").Code)
	for _, bad := range []string{"http://example.invalid", "https://127.0.0.1", "https://[::1]", "https://user:password@example.invalid", "https://example.invalid?token=secret"} {
		raw := strings.Replace(body, "https://provider.example.invalid", bad, 1)
		require.Equal(t, 400, request(a, "POST", "", raw).Code, bad)
	}
}
