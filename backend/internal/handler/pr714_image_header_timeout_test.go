//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPR714ImagesWaitOnlyForSelectedGroups(t *testing.T) {
	for _, tc := range []struct {
		name    string
		groupID int64
		enabled bool
		success bool
	}{
		{"selected group", 7, true, true},
		{"other group", 8, true, false},
		{"global disabled", 7, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var submissions atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Errorf("read complete synthetic POST: %v", err)
					return
				}
				submissions.Add(1)
				select {
				case <-time.After(1500 * time.Millisecond):
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"created":1710000000,"data":[{"b64_json":"aW1hZ2U="}]}`)
				case <-r.Context().Done():
				}
			}))
			defer upstream.Close()
			cfg := pr612HTTPConfig()
			settings := service.NewSettingService(&toggleSettingRepo{values: map[string]string{}}, cfg)
			require.NoError(t, settings.SetOpenAIResponseHeaderTimeoutSettings(t.Context(), &service.OpenAIResponseHeaderTimeoutSettings{
				Enabled: tc.enabled, Seconds: 1, ImageGroupIDs: []int64{7}, ImageSeconds: 3,
			}))
			account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true,
				Credentials: map[string]any{"api_key": "fixture-only", "base_url": upstream.URL}, GroupIDs: []int64{tc.groupID}}
			repo := openAIImagesFailoverAccountRepo{accounts: []service.Account{*account}}
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, nil,
				cfg, nil, nil, nil, nil, nil, repository.ProvideHTTPUpstream(cfg, settings), nil, nil, nil, nil, nil, nil, settings, nil)
			t.Cleanup(gateway.CloseOpenAIWSPool)
			body := []byte(`{"model":"gpt-image-2","prompt":"synthetic","response_format":"b64_json"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("api_key", &service.APIKey{ID: 42, GroupID: &tc.groupID})
			parsed, err := gateway.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			_, err = gateway.ForwardImages(ctx, c, account, body, parsed, "")
			if tc.success {
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, rec.Code)
				require.Contains(t, rec.Body.String(), "aW1hZ2U=")
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), "timeout")
			}
			require.EqualValues(t, 1, submissions.Load(), "a ForwardImages call must make one physical submission")
			require.NoError(t, ctx.Err(), "the transport deadline must not exhaust the caller budget")
		})
	}
}
