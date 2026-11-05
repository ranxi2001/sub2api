package admin

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type groupProtectionSettings struct {
	service.SettingRepository
	values map[string]string
}

func (s *groupProtectionSettings) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}

func (s *groupProtectionSettings) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

type groupProtectionGroups struct{ service.GroupRepository }

func (groupProtectionGroups) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	if id != 7 {
		return nil, service.ErrGroupNotFound
	}
	return &service.Group{ID: id, Platform: service.PlatformOpenAI}, nil
}

func TestGroupAntiDistillationIndependentSettingsAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &groupProtectionSettings{values: map[string]string{}}
	protection := service.NewGroupAntiDistillationService(settings, groupProtectionGroups{}, nil, nil, nil)
	h := ProvideGroupHandlerWithProtection(newStubAdminService(), nil, nil, &config.Config{}, protection)
	r := gin.New()
	r.GET("/groups/:id/anti-distillation", h.GetAntiDistillation)
	r.PUT("/groups/:id/anti-distillation", h.SaveAntiDistillation)
	res := serveGroupRequest(r, "GET", "/groups/7/anti-distillation", "")
	require.Equal(t, 200, res.Code)
	require.Contains(t, res.Body.String(), `"enabled":false`)
	require.Contains(t, res.Body.String(), `"pause_minutes":5`)
	res = serveGroupRequest(r, "PUT", "/groups/7/anti-distillation", `{"enabled":true,"pause_minutes":10}`)
	require.Equal(t, 200, res.Code)
	res = serveGroupRequest(r, "GET", "/groups/7/anti-distillation", "")
	require.Contains(t, res.Body.String(), `"enabled":true`)
	require.Contains(t, res.Body.String(), `"pause_minutes":10`)
	for _, body := range []string{`{}`, `null`, `{"enabled":false}`, `{"pause_minutes":5}`, `{"enabled":null,"pause_minutes":5}`, `{"enabled":true,"pause_minutes":0}`, `{"enabled":true,"pause_minutes":1441}`} {
		res = serveGroupRequest(r, "PUT", "/groups/7/anti-distillation", body)
		require.Equal(t, 400, res.Code, body)
	}
	res = serveGroupRequest(r, "GET", "/groups/8/anti-distillation", "")
	require.Equal(t, 404, res.Code)
}
