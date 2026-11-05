package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminAPIKeyLongContextBooleanPatch(t *testing.T) {
	for _, tc := range []struct {
		name, body        string
		initial, expected bool
		status            int
	}{
		{"disable", `{"allow_long_context":false}`, true, false, http.StatusOK},
		{"enable", `{"allow_long_context":true}`, false, true, http.StatusOK},
		{"omitted preserves restricted", `{}`, false, false, http.StatusOK},
		{"omitted preserves allowed", `{}`, true, true, http.StatusOK},
		{"other policy preserves restriction", `{"reasoning_effort":"high"}`, false, false, http.StatusOK},
		{"string rejected", `{"allow_long_context":"false"}`, true, true, http.StatusBadRequest},
		{"number rejected", `{"allow_long_context":0}`, true, true, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newStubAdminService()
			svc.apiKeys = []service.APIKey{{ID: 10, AllowLongContext: tc.initial}}
			router := setupAPIKeyHandler(svc)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == http.StatusOK {
				if tc.expected {
					require.Contains(t, rec.Body.String(), `"allow_long_context":true`)
				} else {
					require.Contains(t, rec.Body.String(), `"allow_long_context":false`)
				}
			}
		})
	}
}

func TestAdminAPIKeyLongContextFailureDoesNotPartiallyResetUsage(t *testing.T) {
	svc := &failingAPIKeyModelPolicyService{stubAdminService: newStubAdminService()}
	svc.apiKeys = []service.APIKey{{ID: 10, AllowLongContext: true, Usage5h: 8}}
	router := setupAPIKeyHandler(svc)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(`{"allow_long_context":false,"reset_rate_limit_usage":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, 8.0, svc.apiKeys[0].Usage5h)
	require.True(t, svc.apiKeys[0].AllowLongContext)
}
