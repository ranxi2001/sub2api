package handler

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelPolicyUserKeyRepository struct {
	service.APIKeyRepository
	key    *service.APIKey
	fields service.APIKeyUpdateFields
}

func (r *modelPolicyUserKeyRepository) Create(_ context.Context, key *service.APIKey) error {
	key.ID = 10
	copy := *key
	r.key = &copy
	return nil
}

func (r *modelPolicyUserKeyRepository) GetByID(context.Context, int64) (*service.APIKey, error) {
	copy := *r.key
	copy.ModelMapping = maps.Clone(r.key.ModelMapping)
	return &copy, nil
}

func (r *modelPolicyUserKeyRepository) Update(_ context.Context, key *service.APIKey, fields service.APIKeyUpdateFields) error {
	copy := *key
	r.key, r.fields = &copy, fields
	return nil
}

type modelPolicyUserRepository struct{ service.UserRepository }

func (*modelPolicyUserRepository) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 2, Status: service.StatusActive, Role: service.RoleUser}, nil
}

// Exercise the actual JSON binding and user service, not just request structs:
// privileged-looking fields must never reach a user-controlled write mask.
func TestAPIKeyUserJSONCannotWriteAdministratorModelPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &modelPolicyUserKeyRepository{}
	svc := service.NewAPIKeyService(repo, &modelPolicyUserRepository{}, nil, nil, nil, nil, &config.Config{})
	h := NewAPIKeyHandler(svc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 2})
		c.Next()
	})
	router.POST("/api/v1/api-keys", h.Create)
	router.PUT("/api/v1/api-keys/:id", h.Update)

	post := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", bytes.NewBufferString(`{"name":"created","model_mapping":{"gpt-6.1-sol":"gpt-5.6-sol"},"reasoning_effort":"ultra","allow_long_context":false}`))
	post.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	router.ServeHTTP(created, post)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	require.Empty(t, repo.key.ModelMapping)
	require.Empty(t, repo.key.ReasoningEffort)
	require.True(t, repo.key.AllowLongContext, "user creation keeps the server default")
	require.Contains(t, created.Body.String(), `"model_mapping":null`)
	require.Contains(t, created.Body.String(), `"reasoning_effort":""`)

	for _, body := range []string{
		`{"name":"renamed","model_mapping":{"gpt-6.1-sol":"different"},"reasoning_effort":"ultra","allow_long_context":true}`,
		`{"name":"renamed","model_mapping":{},"reasoning_effort":"","allow_long_context":false}`,
		`{"name":"renamed","ModelMapping":{},"ReasoningEffort":"ultra","AllowLongContext":true}`,
	} {
		repo.key.ModelMapping = map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}
		repo.key.ReasoningEffort = "high"
		repo.key.AllowLongContext = false
		put := httptest.NewRequest(http.MethodPut, "/api/v1/api-keys/10", bytes.NewBufferString(body))
		put.Header.Set("Content-Type", "application/json")
		updated := httptest.NewRecorder()
		router.ServeHTTP(updated, put)
		require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
		require.Equal(t, "renamed", repo.key.Name)
		require.Equal(t, map[string]string{"gpt-6.1-sol": "gpt-5.6-sol"}, repo.key.ModelMapping)
		require.Equal(t, "high", repo.key.ReasoningEffort)
		require.False(t, repo.key.AllowLongContext)
		require.Equal(t, service.APIKeyUpdateFields{Name: true}, repo.fields)
		require.Contains(t, updated.Body.String(), `"model_mapping":{"gpt-6.1-sol":"gpt-5.6-sol"}`)
		require.Contains(t, updated.Body.String(), `"reasoning_effort":"high"`)
	}
}
