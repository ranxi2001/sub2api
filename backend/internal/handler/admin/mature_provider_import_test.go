package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMatureProviderImportRejectsMixedManualCredential(t *testing.T) {
	svc := newStubAdminService()
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.POST("/accounts", h.Create)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/accounts", strings.NewReader(`{"name":"Synthetic","platform":"openai","type":"apikey","provider_id":1,"provider_key_id":"7","credentials":{"api_key":"manual-synthetic-key"}}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 400, w.Code, "provider imports must reject mixed manual credentials before account creation")
	require.Empty(t, svc.createdAccounts)
}
