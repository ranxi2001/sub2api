//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type gatewayPreviewAdminService struct {
	*availableModelsAdminService
}

func (s *gatewayPreviewAdminService) GetProxy(context.Context, int64) (*service.Proxy, error) {
	return nil, nil
}

func TestAccountHandlerGatewayModelsDraftAndSavedConnection(t *testing.T) {
	for _, kind := range []string{"new_api", "sub2api"} {
		for _, draft := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/saved", true: "/draft"}[draft], func(t *testing.T) {
				svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{
					ID: 1003, Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
					Credentials: map[string]any{"upstream_kind": kind, "base_url": "https://saved.example/v1", "api_key": "saved-synthetic-key"},
				}}
				upstream := &syncUpstreamHTTPUpstream{resp: &http.Response{StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"live"}]}`))}}
				router := setupSyncUpstreamModelsRouter(svc, upstream)
				path, payload := "/api/v1/admin/accounts/1003/models/sync-upstream", map[string]any{}
				if draft {
					path = "/api/v1/admin/accounts/models/sync-upstream-preview"
					payload = map[string]any{"platform": "gateway", "type": "gateway", "upstream_kind": kind,
						"account_id": 1003, "base_url": "https://draft.example/tenant", "api_key": ""}
				}
				body, err := json.Marshal(payload)
				require.NoError(t, err)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Len(t, upstream.requests, 1)
				wantURL := "https://saved.example/v1/models"
				if draft {
					wantURL = "https://draft.example/tenant/v1/models"
				}
				require.Equal(t, wantURL, upstream.requests[0].URL.String())
				require.Equal(t, "Bearer saved-synthetic-key", upstream.requests[0].Header.Get("Authorization"))
				require.Contains(t, rec.Body.String(), `"discovery_source":"upstream"`)
				require.NotContains(t, rec.Body.String(), "saved-synthetic-key")
			})
		}
	}
}

func TestAccountHandlerGatewayModelsPreviewRejectsObserverAndMissingProxy(t *testing.T) {
	for _, scenario := range []string{"observer", "proxy", "invalid-kind", "missing-key"} {
		t.Run(scenario, func(t *testing.T) {
			svc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{
				ID: 1003, Platform: service.PlatformGateway, Type: service.AccountTypeGateway,
				Credentials: map[string]any{"upstream_kind": "new_api", "base_url": "https://saved.example", "api_key": "synthetic-key"},
			}}
			payload := map[string]any{"platform": "gateway", "type": "gateway", "upstream_kind": "new_api",
				"base_url": "https://draft.example", "api_key": "synthetic-key"}
			want := http.StatusBadRequest
			switch scenario {
			case "observer":
				payload["account_id"] = 1003
				want = http.StatusForbidden
			case "proxy":
				payload["proxy_id"] = 7
			case "invalid-kind":
				payload["upstream_kind"] = "other"
			case "missing-key":
				delete(payload, "api_key")
			}
			upstream := &syncUpstreamHTTPUpstream{}
			router := setupSyncUpstreamModelsRouter(&gatewayPreviewAdminService{availableModelsAdminService: svc}, upstream)
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/models/sync-upstream-preview", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			if scenario == "observer" {
				req = req.WithContext(service.WithObserverScope(req.Context(), []int64{1}))
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, want, rec.Code, rec.Body.String())
			require.Empty(t, upstream.requests)
		})
	}
}
