package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type pr612AdminSettingRepo struct{ settingHandlerRepoStub }

func (r *pr612AdminSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func TestOpenAIResponseHeaderTimeoutAdminValidationAndDisable(t *testing.T) {
	repo := &pr612AdminSettingRepo{settingHandlerRepoStub{values: map[string]string{}}}
	h := NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil)
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/openai-response-header-timeout", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.UpdateOpenAIResponseHeaderTimeoutSettings(c)
		return rec
	}
	for _, body := range []string{
		`{}`, `null`, `{"seconds":20}`, `{"enabled":true}`, `{"enabled":null,"seconds":20}`,
		`{"enabled":true,"seconds":0}`, `{"enabled":false,"seconds":0}`,
		`{"enabled":true,"seconds":-1}`, `{"enabled":true,"seconds":601}`,
		`{"enabled":true,"seconds":1.5}`, `{"enabled":true,"seconds":999999999999999999999999}`,
	} {
		rec := put(body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
	for _, seconds := range []int{1, 600} {
		rec := put(fmt.Sprintf(`{"enabled":true,"seconds":%d}`, seconds))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, gjson.GetBytes(rec.Body.Bytes(), "data.enabled").Bool())
		require.EqualValues(t, seconds, gjson.GetBytes(rec.Body.Bytes(), "data.seconds").Int())
	}
	rec := put(`{"enabled":false,"seconds":20}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, gjson.GetBytes(rec.Body.Bytes(), "data.enabled").Bool())
	_, enabled := h.settingService.GetOpenAIResponseHeaderTimeoutCached(t.Context())
	require.False(t, enabled)
}

func TestOpenAIResponseHeaderTimeoutAdminImageGroups(t *testing.T) {
	repo := &pr612AdminSettingRepo{settingHandlerRepoStub{values: map[string]string{}}}
	h := NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil)
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/openai-response-header-timeout", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.UpdateOpenAIResponseHeaderTimeoutSettings(c)
		return rec
	}

	rec := put(`{"enabled":true,"seconds":60,"image_group_ids":[7,3,7],"image_seconds":150}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `[3,7]`, gjson.GetBytes(rec.Body.Bytes(), "data.image_group_ids").Raw)
	require.EqualValues(t, 150, gjson.GetBytes(rec.Body.Bytes(), "data.image_seconds").Int())

	rec = put(`{"enabled":true,"seconds":45}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `[3,7]`, gjson.GetBytes(rec.Body.Bytes(), "data.image_group_ids").Raw, "old clients keep saved groups")
	require.EqualValues(t, 150, gjson.GetBytes(rec.Body.Bytes(), "data.image_seconds").Int())

	rec = put(`{"enabled":true,"seconds":45,"image_group_ids":[],"image_seconds":150}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `[]`, gjson.GetBytes(rec.Body.Bytes(), "data.image_group_ids").Raw)

	require.Equal(t, http.StatusBadRequest, put(`{"enabled":true,"seconds":45,"image_seconds":601}`).Code)
}
