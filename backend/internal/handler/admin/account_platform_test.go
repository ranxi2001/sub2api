package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type platformEditAdmin struct {
	service.AdminService
	input *service.UpdateAccountInput
}

func (s *platformEditAdmin) UpdateAccount(_ context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	s.input = input
	return &service.Account{ID: id, Platform: input.Platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
		Credentials: map[string]any{"api_key": "fixture-private-key"}}, nil
}

func TestAccountPlatformEditHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &platformEditAdmin{}
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.PUT("/accounts/:id", handler.Update)
	request := httptest.NewRequest(http.MethodPut, "/accounts/7", strings.NewReader(`{"platform":"anthropic","group_ids":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, service.PlatformAnthropic, svc.input.Platform)
	require.NotNil(t, svc.input.GroupIDs)
	require.Contains(t, response.Body.String(), `"platform":"anthropic"`)
	require.NotContains(t, response.Body.String(), "fixture-private-key")
}
