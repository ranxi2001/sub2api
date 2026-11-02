package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type performanceSettingsRepo struct {
	settingHandlerRepoStub
	getErr error
	setErr error
}

func (r *performanceSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *performanceSettingsRepo) Set(_ context.Context, key, value string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.values[key] = value
	return nil
}

func performanceSettingsRequest(h *SettingHandler, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, "/api/v1/admin/settings/account-performance", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if method == http.MethodGet {
		h.GetAccountPerformanceSettings(c)
	} else {
		h.UpdateAccountPerformanceSettings(c)
	}
	return rec
}

func TestAccountPerformanceSettingsDefaultValidationAndPersistence(t *testing.T) {
	repo := &performanceSettingsRepo{settingHandlerRepoStub: settingHandlerRepoStub{values: map[string]string{}}}
	handler := func() *SettingHandler {
		return NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil)
	}
	h := handler()
	rec := performanceSettingsRequest(h, http.MethodGet, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.EqualValues(t, 60, gjson.GetBytes(rec.Body.Bytes(), "data.window_minutes").Int())
	require.Empty(t, repo.values, "reading an unconfigured installation does not write settings")

	for _, body := range []string{
		`{}`, `null`, `{"window_minutes":null}`, `{"window_minutes":0}`,
		`{"window_minutes":-1}`, `{"window_minutes":1441}`, `{"window_minutes":1.5}`,
		`{"window_minutes":"60"}`, `{"window_minutes":999999999999999999999999}`,
	} {
		rec = performanceSettingsRequest(h, http.MethodPut, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Empty(t, repo.values, "invalid payload must not persist")
	}
	for _, minutes := range []int{1, 15, 60, 120, 1440} {
		rec = performanceSettingsRequest(h, http.MethodPut, fmt.Sprintf(`{"window_minutes":%d}`, minutes))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, strconv.Itoa(minutes), repo.values[service.SettingKeyAccountPerformanceWindowMinutes])
		// A new service instance observes the saved global value without a restart/cache.
		rec = performanceSettingsRequest(handler(), http.MethodGet, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.EqualValues(t, minutes, gjson.GetBytes(rec.Body.Bytes(), "data.window_minutes").Int())
	}
}

func TestAccountPerformanceSettingsStorageErrorsAreNotSuccessfulDefaults(t *testing.T) {
	repo := &performanceSettingsRepo{settingHandlerRepoStub: settingHandlerRepoStub{values: map[string]string{}}}
	h := NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil)
	repo.getErr = errors.New("settings unavailable")
	require.Equal(t, http.StatusInternalServerError, performanceSettingsRequest(h, http.MethodGet, "").Code)
	repo.getErr = nil
	repo.setErr = errors.New("write unavailable")
	require.Equal(t, http.StatusInternalServerError, performanceSettingsRequest(h, http.MethodPut, `{"window_minutes":30}`).Code)
	require.Empty(t, repo.values)

	for _, value := range []string{"0", "1441", "1.5", "invalid"} {
		repo.values[service.SettingKeyAccountPerformanceWindowMinutes] = value
		require.Equal(t, http.StatusBadRequest, performanceSettingsRequest(h, http.MethodGet, "").Code, value)
	}
}
