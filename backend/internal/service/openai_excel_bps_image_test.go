package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExcelBPSInlineImageForwardAndFetch(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	gin.SetMode(gin.TestMode)
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", path, stream), func(t *testing.T) {
				wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_image\",\"status\":\"completed\",\"model\":\"gpt-6-astra\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"image received\"}]}]}}\n\n"
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
				svc := openAIClientToolsTestService(upstream)
				t.Cleanup(func() { require.NoError(t, svc.CloseExcelBPSImages()) })
				router := gin.New()
				router.GET(basispoints.ImageRelayPath+":token", svc.ServeExcelBPSImage)
				server := httptest.NewTLSServer(router)
				defer server.Close()
				svc.settingService = NewSettingService(&excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageBaseURL: server.URL}}, svc.cfg)
				body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%v,"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":%q,"detail":"high"}]}]}`, stream, dataURL))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				c.Request.Header.Set("X-Forwarded-Host", "attacker.example")
				result, err := svc.Forward(context.Background(), c, excelAccount(), body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotContains(t, string(upstream.lastBody), "data:image")
				var imageURL string
				for _, item := range gjson.GetBytes(upstream.lastBody, "input").Array() {
					for _, part := range item.Get("content").Array() {
						if part.Get("type").String() == "input_image" {
							imageURL = part.Get("image_url").String()
						}
					}
				}
				require.True(t, strings.HasPrefix(imageURL, server.URL+basispoints.ImageRelayPath), imageURL)
				resp, err := server.Client().Get(imageURL)
				require.NoError(t, err)
				defer func() { _ = resp.Body.Close() }()
				fetched, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, pngBytes.Bytes(), fetched)
				require.Contains(t, rec.Body.String(), "image received")
			})
		}
	}
}

func TestExcelBPSImageRelayValidationDoesNotCallUpstream(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	for _, tt := range []struct {
		name, baseURL, dataURL string
		status                 int
	}{
		{"disabled", "", "data:image/png;base64,PRIVATE_PAYLOAD", 400},
		{"invalid data", "https://images.example", "data:image/png;base64,PRIVATE_PAYLOAD", 400},
		{"invalid origin", "http://images.example", "data:image/png;base64,PRIVATE_PAYLOAD", 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc := openAIClientToolsTestService(upstream)
			t.Cleanup(func() { require.NoError(t, svc.CloseExcelBPSImages()) })
			svc.settingService = NewSettingService(&excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: fmt.Sprint(tt.baseURL != ""), SettingKeyExcelBPSImageBaseURL: tt.baseURL}}, svc.cfg)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","input":[{"role":"user","content":[{"type":"input_image","image_url":%q}]}]}`, tt.dataURL))
			_, err := svc.Forward(context.Background(), c, excelAccount(), body)
			require.Error(t, err)
			require.Equal(t, tt.status, rec.Code)
			require.Empty(t, upstream.requests)
			require.NotContains(t, rec.Body.String(), "PRIVATE_PAYLOAD")
			require.True(t, IsResponseCommitted(c))
		})
	}
}

func TestExcelBPSImageCapabilityRedactedFromUpstreamError(t *testing.T) {
	raw := `{"error":{"message":"Cannot fetch https://images.example/api/bps-images/PRIVATE_CAPABILITY_123"}}`
	out := excelBPSSanitizeErrorBody(raw, "test-token", excelAccount())
	require.NotContains(t, out, "PRIVATE_CAPABILITY_123")
	require.Contains(t, out, "/api/bps-images/[redacted]")
}

func TestExcelBPS429ImageAndCompactKeepCodexSchedulable(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusTooManyRequests, Header: excelBPSQuotaHeaders("100", "100"), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"PRIVATE_UPSTREAM"}}`))}}
				svc := openAIClientToolsTestService(upstream)
				t.Cleanup(func() { require.NoError(t, svc.CloseExcelBPSImages()) })
				svc.settingService = NewSettingService(&excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageBaseURL: "https://images.example"}}, svc.cfg)
				repo := &excelBPSQuotaRepo{writes: make(chan excelBPSQuotaWrite, 4)}
				svc.accountRepo = repo
				svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
				svc.rateLimitService.SetAccountRuntimeBlocker(svc)
				body := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","stream":%t,"input":[{"role":"user","content":[{"type":"input_image","image_url":%q}]}]}`, stream, dataURL))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				account := excelAccount()

				_, err := svc.Forward(context.Background(), c, account, body)

				require.EqualError(t, err, "excel BPS: basispoints_rate_limited")
				require.Equal(t, http.StatusTooManyRequests, rec.Code)
				require.Len(t, upstream.requests, 1)
				require.NotContains(t, string(upstream.lastBody), "data:image")
				require.NotContains(t, rec.Body.String(), "PRIVATE_UPSTREAM")
				requireNoExcelBPSQuotaWrite(t, repo)
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
				require.True(t, account.IsSchedulable())
			})
		}
	}
}

func TestExcelBPSPrewarmFailureDoesNotSubmitUpstream(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("EXCEL_BPS_IMAGE_PREWARM", "true")
	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes.Bytes())
	server := httptest.NewTLSServer(http.NotFoundHandler())
	server.Close()
	upstream := &httpUpstreamRecorder{}
	svc := openAIClientToolsTestService(upstream)
	t.Cleanup(func() { require.NoError(t, svc.CloseExcelBPSImages()) })
	svc.settingService = NewSettingService(&excelBPSImageSettingsRepo{values: map[string]string{SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageBaseURL: server.URL}}, svc.cfg)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(fmt.Sprintf("{\"model\":\"gpt-6-astra\",\"input\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"image_url\":%q}]}]}", dataURL))
	_, err := svc.Forward(context.Background(), c, excelAccount(), body)
	require.Error(t, err)
	require.Equal(t, 503, rec.Code)
	require.Empty(t, upstream.requests)
	require.Contains(t, rec.Body.String(), "basispoints_image_prewarm_unavailable")
	require.NotContains(t, rec.Body.String(), "data:image")
	require.NotContains(t, rec.Body.String(), server.URL)
	require.True(t, IsResponseCommitted(c))
}
