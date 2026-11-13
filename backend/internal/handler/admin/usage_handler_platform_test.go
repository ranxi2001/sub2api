package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestAdminUsagePlatformFilter(t *testing.T) {
	for _, path := range []string{"/admin/usage", "/admin/usage/stats"} {
		for _, platform := range append(domain.ConcretePlatformIDs(), "", "unknown", "composite", "invented") {
			t.Run(path+"/"+platform, func(t *testing.T) {
				repo := &adminUsageRepoCapture{}
				router := newAdminUsageRequestTypeTestRouter(repo)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?platform="+platform+"&group_id=7", nil))
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
