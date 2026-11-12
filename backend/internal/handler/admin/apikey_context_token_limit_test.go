package admin

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminAPIKeyContextTokenLimitPatch(t *testing.T) {
	for _, tc := range []struct {
		body         string
		status, want int
	}{
		{`{"context_token_limit":1000}`, 200, 1000},
		{`{"context_token_limit":0}`, 200, 0},
		{`{}`, 200, 1234},
		{`{"context_token_limit":null}`, 200, 1234},
		{`{"context_token_limit":2147483647}`, 200, 2147483647},
		{`{"context_token_limit":-1}`, 400, 1234},
		{`{"context_token_limit":2147483648}`, 400, 1234},
		{`{"context_token_limit":1.5}`, 400, 1234},
		{`{"context_token_limit":"1000"}`, 400, 1234},
	} {
		t.Run(tc.body, func(t *testing.T) {
			svc := newStubAdminService()
			svc.apiKeys = []service.APIKey{{ID: 10, AllowLongContext: true, ContextTokenLimit: 1234, Usage5h: 8}}
			router := setupAPIKeyHandler(svc)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/api-keys/10", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == 200 {
				require.Contains(t, rec.Body.String(), fmt.Sprintf(`"context_token_limit":%d`, tc.want))
				require.Contains(t, rec.Body.String(), `"allow_long_context":true`)
			}
			require.Equal(t, 8.0, svc.apiKeys[0].Usage5h)
		})
	}
}
