package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestAdminUsagePlatformFilter(t *testing.T) {
	for _, path := range []string{"/admin/usage", "/admin/usage/stats"} {
		for _, platform := range append(domain.ConcretePlatformIDs(), "", "unknown", "composite", "invented") {
			t.Run(path+"/"+platform, func(t *testing.T) {
				repo := &adminUsageRepoCapture{}
				router := newAdminUsageRequestTypeTestRouter(repo)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?platform="+platform+"&group_id=7&start_date=2026-11-01&end_date=2026-11-07", nil))
				if platform == "composite" || platform == "invented" {
					require.Equal(t, http.StatusBadRequest, rec.Code)
					return
				}
				require.Equal(t, http.StatusOK, rec.Code)
				filters := repo.listFilters
				if path == "/admin/usage/stats" {
					filters = repo.statsFilters
				}
				require.Equal(t, int64(7), filters.GroupID)
				raw, err := json.Marshal(filters)
				require.NoError(t, err)
				var fields map[string]any
				require.NoError(t, json.Unmarshal(raw, &fields))
				require.Equal(t, platform, fields["Platform"], "platform must reach the service query")
			})
		}
	}
}

func TestDashboardPlatformFiltersAndCaches(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	for _, endpoint := range []string{"trend", "models", "groups", "snapshot"} {
		t.Run(endpoint, func(t *testing.T) {
			resetDashboardReadCachesForTest()
			repo := &dashboardUsageRepoCapture{}
			router := newDashboardRequestTypeTestRouter(repo)
			for _, platform := range []string{"", "openai", "gemini", "unknown"} {
				url := "/admin/dashboard/" + endpoint + "?start_date=2026-11-01&end_date=2026-11-07&include_stats=false&include_group_stats=true&platform=" + platform
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				switch endpoint {
				case "trend":
					require.Equal(t, platform, repo.trendPlatform)
				case "models":
					require.Equal(t, platform, repo.modelPlatform)
				case "groups":
					require.Equal(t, platform, repo.groupPlatform)
				case "snapshot":
					require.Equal(t, platform, repo.trendPlatform)
					require.Equal(t, platform, repo.modelPlatform)
					require.Equal(t, platform, repo.groupPlatform)
				}
				header := "X-Snapshot-Cache"
				// The response is cached independently for each platform.
				require.Equal(t, "miss", rec.Header().Get(header))
				repeated := httptest.NewRecorder()
				router.ServeHTTP(repeated, httptest.NewRequest(http.MethodGet, url, nil))
				require.Equal(t, http.StatusOK, repeated.Code)
				require.Equal(t, "hit", repeated.Header().Get(header))
			}
		})
	}
}

func TestDashboardPlatformModelCacheScope(t *testing.T) {
	t.Cleanup(resetDashboardReadCachesForTest)
	for _, endpoint := range []string{"trend", "models", "groups", "snapshot"} {
		t.Run(endpoint, func(t *testing.T) {
			resetDashboardReadCachesForTest()
			repo := &dashboardUsageRepoCapture{}
			router := newDashboardRequestTypeTestRouter(repo)
			for _, platform := range []string{"openai", "gemini"} {
				for _, model := range []string{"requested-a", "requested-b"} {
					url := "/admin/dashboard/" + endpoint + "?start_date=2026-11-01&end_date=2026-11-07&include_stats=false&include_group_stats=true&platform=" + platform + "&model=" + model
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Equal(t, "miss", rec.Header().Get("X-Snapshot-Cache"))
					filters := []usagestats.UsageLogFilters{}
					switch endpoint {
					case "trend":
						filters = append(filters, repo.trendFilters)
					case "models":
						filters = append(filters, repo.modelFilters)
					case "groups":
						filters = append(filters, repo.groupFilters)
					case "snapshot":
						filters = append(filters, repo.trendFilters, repo.modelFilters, repo.groupFilters)
					}
					for _, filter := range filters {
						require.Equal(t, platform, filter.Platform)
						require.Equal(t, model, filter.Model)
						require.Equal(t, usagestats.ModelSourceRequested, filter.ModelFilterSource)
					}
					repeated := httptest.NewRecorder()
					router.ServeHTTP(repeated, httptest.NewRequest(http.MethodGet, url, nil))
					require.Equal(t, "hit", repeated.Header().Get("X-Snapshot-Cache"))
				}
			}
		})
	}
}
