package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSConfiguredImageCountGateway(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("EXCEL_BPS_IMAGE_PREWARM", "false")
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	for _, tc := range []struct {
		limit, count int
		accepted     bool
	}{
		{20, 20, true}, {20, 21, false}, {64, 21, true}, {64, 64, true}, {64, 65, false},
	} {
		t.Run(fmt.Sprintf("limit=%d/images=%d", tc.limit, tc.count), func(t *testing.T) {
			wire := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_count\",\"status\":\"completed\",\"model\":\"gpt-6-astra\",\"output\":[]}}\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			t.Cleanup(func() { require.NoError(t, svc.CloseExcelBPSImages()) })
			svc.settingService = NewSettingService(&excelBPSImageSettingsRepo{values: map[string]string{
				SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageBaseURL: "https://images.example",
				SettingKeyExcelBPSImageMaxImagesPerRequest: strconv.Itoa(tc.limit),
			}}, svc.cfg)
			parts := make([]any, tc.count)
			for i := range parts {
				parts[i] = map[string]any{"type": "input_image", "image_url": dataURL}
			}
			body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "stream": true, "input": []any{map[string]any{"role": "user", "content": parts}}})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			account := excelAccount()
			_, err = svc.Forward(context.Background(), c, account, body)
			require.True(t, account.IsExcelBPSEnabledForModel("gpt-6-astra"))
			require.Empty(t, rec.Header().Get("X-Codex2API-Basispoints-Bypass"))
			if !tc.accepted {
				require.ErrorContains(t, err, "basispoints_request_invalid")
				require.Equal(t, 400, rec.Code)
				require.Empty(t, upstream.requests)
				require.Contains(t, rec.Body.String(), fmt.Sprintf("at most %d", tc.limit))
				return
			}
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
			require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
			count := 0
			for _, item := range gjson.GetBytes(upstream.lastBody, "input").Array() {
				for _, part := range item.Get("content").Array() {
					if part.Get("type").String() == "input_image" {
						count++
					}
				}
			}
			require.Equal(t, tc.count, count)
		})
	}
}
