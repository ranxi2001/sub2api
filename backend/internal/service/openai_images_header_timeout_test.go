package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenAIImageTimeoutSettingService(t *testing.T, settings OpenAIResponseHeaderTimeoutSettings) *SettingService {
	t.Helper()
	service := NewSettingService(&panelRateLimitSettingRepo{values: map[string]string{}}, &config.Config{})
	require.NoError(t, service.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &settings))
	return service
}

func TestOpenAIImageResponseHeaderTimeoutOnlyRelaxesSelectedGroups(t *testing.T) {
	ctx := context.Background()
	groupID, otherGroupID := int64(7), int64(8)
	settings := OpenAIResponseHeaderTimeoutSettings{Enabled: true, Seconds: 60, ImageGroupIDs: []int64{7}, ImageSeconds: 150}

	service := newOpenAIImageTimeoutSettingService(t, settings)
	require.Equal(t, 150*time.Second, service.OpenAIImageResponseHeaderTimeout(ctx, &groupID))
	require.Zero(t, service.OpenAIImageResponseHeaderTimeout(ctx, &otherGroupID))
	require.Zero(t, service.OpenAIImageResponseHeaderTimeout(ctx, nil))

	disabled := settings
	disabled.Enabled = false
	require.Zero(t, newOpenAIImageTimeoutSettingService(t, disabled).OpenAIImageResponseHeaderTimeout(ctx, &groupID))

	notLonger := settings
	notLonger.ImageSeconds = 60
	require.Zero(t, newOpenAIImageTimeoutSettingService(t, notLonger).OpenAIImageResponseHeaderTimeout(ctx, &groupID))
}

func TestOpenAIImageResponseHeaderTimeoutSettingsNormalization(t *testing.T) {
	ctx := context.Background()
	service := NewSettingService(&panelRateLimitSettingRepo{values: map[string]string{}}, &config.Config{})

	defaults, err := service.GetOpenAIResponseHeaderTimeoutSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{}, defaults.ImageGroupIDs)
	require.Equal(t, 180, defaults.ImageSeconds)

	require.NoError(t, service.SetOpenAIResponseHeaderTimeoutSettings(ctx, &OpenAIResponseHeaderTimeoutSettings{
		Enabled: true, Seconds: 60, ImageGroupIDs: []int64{9, 0, 3, 9, -1},
	}))
	saved, err := service.GetOpenAIResponseHeaderTimeoutSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{3, 9}, saved.ImageGroupIDs)
	require.Equal(t, 180, saved.ImageSeconds)

	require.Error(t, service.SetOpenAIResponseHeaderTimeoutSettings(ctx, &OpenAIResponseHeaderTimeoutSettings{
		Enabled: true, Seconds: 60, ImageSeconds: 601,
	}))
}

func TestForwardImagesAppliesResponseHeaderTimeoutForSelectedGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := newOpenAIImageTimeoutSettingService(t, OpenAIResponseHeaderTimeoutSettings{
		Enabled: true, Seconds: 60, ImageGroupIDs: []int64{7}, ImageSeconds: 150,
	})
	account := func(accountType string, groupID int64) *Account {
		credentials := map[string]any{"api_key": "sk-test", "base_url": "https://image.example.test"}
		if accountType == AccountTypeOAuth {
			credentials = map[string]any{"access_token": "token", "chatgpt_account_id": "account"}
		}
		return &Account{
			ID: 1, Name: "image", Platform: PlatformOpenAI, Type: accountType, Credentials: credentials,
			GroupIDs: []int64{groupID},
		}
	}

	for _, test := range []struct {
		name        string
		accountType string
		groupID     int64
		want        time.Duration
	}{
		{"API Key 账号在放宽分组", AccountTypeAPIKey, 7, 150 * time.Second},
		{"API Key 账号在其他分组", AccountTypeAPIKey, 8, 0},
		{"OAuth 账号在放宽分组", AccountTypeOAuth, 7, 150 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-1","prompt":"draw a cat"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			groupID := test.groupID
			c.Set("api_key", &APIKey{ID: 42, GroupID: &groupID})

			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"stop here"}}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: settings, httpUpstream: upstream}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)

			_, forwardErr := svc.ForwardImages(context.Background(), c, account(test.accountType, groupID), body, parsed, "")
			require.NotNil(t, upstream.lastReq, "forward error: %v", forwardErr)
			require.Equal(t, test.want, HTTPUpstreamResponseHeaderTimeoutFromContext(upstream.lastReq.Context()))
		})
	}
}
