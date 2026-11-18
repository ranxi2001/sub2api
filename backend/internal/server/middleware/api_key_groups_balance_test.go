//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type balanceGroupRouter struct {
	available bool
	calls     int
}

func (r *balanceGroupRouter) ProbeAPIKeyGroup(context.Context, *service.Group, string, string) (service.APIKeyGroupAvailability, error) {
	r.calls++
	return service.APIKeyGroupAvailability{Available: r.available}, nil
}

func TestMultiGroupInsufficientBalanceResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		balance   float64
		mode      string
		available bool
		status    int
		code      string
	}{
		{"negative", -0.005, "", true, 403, "INSUFFICIENT_BALANCE"},
		{"zero", 0, "", true, 403, "INSUFFICIENT_BALANCE"},
		{"below_reserve", 0.005, "", true, 403, "INSUFFICIENT_BALANCE"},
		{"funded", 1, "", true, 204, ""},
		{"funded_empty_pool", 1, "", false, 503, "NO_AVAILABLE_API_KEY_GROUP"},
		{"model_forbidden", -0.005, "model", true, 403, "NO_ELIGIBLE_API_KEY_GROUP"},
		{"stream_required", -0.005, "stream", true, 403, "NO_ELIGIBLE_API_KEY_GROUP"},
		{"access_revoked", -0.005, "access", true, 403, "NO_ELIGIBLE_API_KEY_GROUP"},
		{"inactive", -0.005, "inactive", true, 403, "NO_ELIGIBLE_API_KEY_GROUP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Billing.MinimumBalanceReserve = 0.01
			id := int64(1)
			groups := map[int64]*service.Group{
				1: {ID: 1, Status: service.StatusActive, Platform: service.PlatformOpenAI, RateMultiplier: 0.1},
				2: {ID: 2, Status: service.StatusActive, Platform: service.PlatformOpenAI, RateMultiplier: 0.2},
			}
			for _, g := range groups {
				switch tc.mode {
				case "model":
					g.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"fixture-allowed"}}
				case "stream":
					g.StreamOnly = true
				case "access":
					g.IsExclusive = true
				case "inactive":
					g.Status = "inactive"
				}
			}
			key := &service.APIKey{ID: 7, UserID: 8, Status: service.StatusActive, GroupID: &id, GroupIDs: []int64{1, 2}, Group: groups[1], User: &service.User{ID: 8, Status: service.StatusActive, Balance: tc.balance}}
			svc := service.NewAPIKeyService(nil, nil, creditFallbackGroups{groups: groups}, nil, nil, nil, cfg)
			probe := &balanceGroupRouter{available: tc.available}
			svc.SetGroupRouter(probe)
			r := gin.New()
			r.POST("/v1/responses", func(c *gin.Context) {
				selected, err := selectAPIKeyRequestGroup(c, svc, nil, cfg, key, false)
				if tc.code != "" {
					require.Error(t, err)
					require.Equal(t, tc.code, infraerrors.Reason(err))
					require.Nil(t, selected)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, selected)
				c.Status(http.StatusNoContent)
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"fixture-requested","input":"fixture","stream":false}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code)
			if tc.code != "" {
				var body map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				require.Equal(t, tc.code, body["code"])
				if tc.code == "INSUFFICIENT_BALANCE" {
					require.Equal(t, service.InsufficientUserBalanceMessage, body["message"])
				}
			}
			if tc.status == 403 {
				require.Zero(t, probe.calls, "ineligible requests must not probe or dispatch upstream")
			}
			require.Equal(t, tc.balance, key.User.Balance, "classification must not mutate billing")
			require.Equal(t, id, *key.GroupID)
		})
	}
}
