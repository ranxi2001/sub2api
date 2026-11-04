package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminAPIKeyModelPolicyUpdate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		mapping map[string]string
		effort  string
	}{
		{"set both", `{"model_mapping":{"gpt-6.1-sol":"gpt-5.6-sol"},"reasoning_effort":"high"}`, map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, "high"},
		{"omit preserves", `{}`, map[string]string{"old": "target"}, "low"},
		{"effort only", `{"reasoning_effort":"high"}`, map[string]string{"old": "target"}, "high"},
		{"clear both", `{"model_mapping":{},"reasoning_effort":""}`, map[string]string{}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := newStubAdminService()
			svc.apiKeys = []service.APIKey{{ID: 10, ModelMapping: map[string]string{"old": "target"}, ReasoningEffort: "low"}}
			router := setupAPIKeyHandler(svc)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var response struct {
				Data struct {
					APIKey struct {
						ModelMapping    map[string]string `json:"model_mapping"`
						ReasoningEffort string            `json:"reasoning_effort"`
					} `json:"api_key"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, tt.mapping, response.Data.APIKey.ModelMapping)
			require.Equal(t, tt.effort, response.Data.APIKey.ReasoningEffort)
		})
	}
}

type failingAPIKeyModelPolicyService struct {
	*stubAdminService
}

func (s *failingAPIKeyModelPolicyService) AdminUpdateAPIKeySettings(context.Context, int64, service.AdminUpdateAPIKeySettingsInput) (*service.AdminUpdateAPIKeyGroupIDResult, error) {
	return nil, errors.New("policy persistence failed")
}

func TestAdminAPIKeyPolicyFailureDoesNotPartiallyResetUsage(t *testing.T) {
	svc := &failingAPIKeyModelPolicyService{stubAdminService: newStubAdminService()}
	svc.apiKeys = []service.APIKey{{ID: 10, Usage5h: 8}}
	router := setupAPIKeyHandler(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(`{"reasoning_effort":"high","reset_rate_limit_usage":true}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, 8.0, svc.apiKeys[0].Usage5h)
}

func TestAdminAPIKeyInvalidPolicyDoesNotResetUsage(t *testing.T) {
	for _, body := range []string{
		`{"reasoning_effort":"invalid","reset_rate_limit_usage":true}`,
		`{"model_mapping":{"source":""},"reset_rate_limit_usage":true}`,
		`{"model_mapping":{"source":42},"reset_rate_limit_usage":true}`,
	} {
		svc := newStubAdminService()
		svc.apiKeys = []service.APIKey{{ID: 10, Usage5h: 8}}
		router := setupAPIKeyHandler(svc)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, 8.0, svc.apiKeys[0].Usage5h)
	}
}
