//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type issue1081HandlerPolicy struct{}

func (issue1081HandlerPolicy) APIKeyMultiGroupEnabled(context.Context) (bool, error) {
	return false, nil
}

type issue1081HandlerUsers struct{ service.UserRepository }

func (issue1081HandlerUsers) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 7}, nil
}

type issue1081HandlerGroups struct{ service.GroupRepository }

func (issue1081HandlerGroups) GetByID(_ context.Context, id int64) (*service.Group, error) {
	return &service.Group{ID: id, Status: service.StatusActive}, nil
}

type issue1081HandlerKeys struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *issue1081HandlerKeys) GetByID(context.Context, int64) (*service.APIKey, error) {
	k := *r.key
	return &k, nil
}
func (r *issue1081HandlerKeys) Create(_ context.Context, k *service.APIKey) error {
	k.ID = 11
	r.key = k
	return nil
}
func (r *issue1081HandlerKeys) Update(_ context.Context, k *service.APIKey, _ service.APIKeyUpdateFields) error {
	r.key = k
	return nil
}

func TestIssue1081HTTPCreateUpdateRejectAndLegacyOmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := int64(1)
	repo := &issue1081HandlerKeys{key: &service.APIKey{ID: 11, UserID: 7, GroupID: &id, GroupIDs: []int64{1, 2}, Status: service.StatusActive, QuotaUsed: 12}}
	svc := service.NewAPIKeyService(repo, issue1081HandlerUsers{}, issue1081HandlerGroups{}, nil, nil, nil, &config.Config{})
	svc.SetGroupPolicy(issue1081HandlerPolicy{})
	h := NewAPIKeyHandler(svc)
	r := gin.New()
	actor := int64(7)
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: actor})
		c.Next()
	})
	r.POST("/keys", h.Create)
	r.PUT("/keys/:id", h.Update)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, body := range []string{`{"name":"fixture","group_id":1,"group_ids":[1,2]}`, `{"name":"fixture","group_id":1,"group_ids":[2]}`} {
		for _, method := range []string{"POST", "PUT"} {
			path := "/keys"
			if method == "PUT" {
				path += "/11"
			}
			w := request(method, path, body)
			require.Equal(t, 400, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "API_KEY_MULTI_GROUP_DISABLED")
		}
	}
	for _, body := range []string{`{"name":"renamed"}`, `{"name":"renamed","group_id":1}`} {
		w := request("PUT", "/keys/11", body)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Equal(t, []int64{1, 2}, repo.key.GroupIDs)
		require.Equal(t, float64(12), repo.key.QuotaUsed)
	}
	actor = 8
	w := request("PUT", "/keys/11", `{"name":"unauthorized"}`)
	require.Equal(t, http.StatusForbidden, w.Code)
	actor = 7
	w = request("POST", "/keys", `{"name":"legacy","group_id":1}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, []int64{1}, repo.key.GroupIDs)
}
