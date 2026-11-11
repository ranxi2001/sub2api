package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func imageFormContext(t *testing.T, endpoint, contentType string, body []byte) (*gin.Context, *OpenAIImagesRequest) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", contentType)
	svc := &OpenAIGatewayService{}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	return c, parsed
}

func imageFormJSON(t *testing.T, refs ...string) []byte {
	t.Helper()
	images := make([]map[string]string, 0, len(refs))
	for _, ref := range refs {
		images = append(images, map[string]string{"image_url": ref})
	}
	body, err := json.Marshal(map[string]any{
		"model": "gpt-image-2", "prompt": "原样保留\nblue", "stream": false,
		"size": "2048x2048", "output_compression": 0, "images": images,
	})
	require.NoError(t, err)
	return body
}

func imageFormUpstream() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: b64BackfillImageResponse(http.StatusOK, "application/json",
		[]byte(`{"data":[{"b64_json":"b2s="}],"usage":{"input_tokens":3,"output_tokens":4}}`))}
}

func TestImageEditFormForwardingPreservesMappingAndUploads(t *testing.T) {
	t.Setenv("SUB2API_IMAGE_EDIT_FORM_ACCOUNT_IDS", "7")
	ref := "data:image/png;base64," + base64.StdEncoding.EncodeToString(b64BackfillPNGBytes)
	for _, input := range []string{"json", "image", "image[]", "image[0]"} {
		t.Run(input, func(t *testing.T) {
			body, contentType := imageFormJSON(t, ref), "application/json"
			if input != "json" {
				var buf bytes.Buffer
				w := multipart.NewWriter(&buf)
				for k, v := range map[string]string{"model": "gpt-image-2", "prompt": "原样保留\nblue", "stream": "false", "size": "2048x2048", "output_compression": "0"} {
					require.NoError(t, w.WriteField(k, v))
				}
				for _, name := range []string{input, "mask"} {
					part, err := w.CreateFormFile(name, "test.png")
					require.NoError(t, err)
					_, err = part.Write(b64BackfillPNGBytes)
					require.NoError(t, err)
				}
				require.NoError(t, w.Close())
				body, contentType = buf.Bytes(), w.FormDataContentType()
			}
			c, parsed := imageFormContext(t, openAIImagesEditsEndpoint, contentType, body)
			account := b64BackfillAccount(false)
			account.Credentials["base_url"] = "https://rpm.tosky.io/v1"
			account.Credentials["model_mapping"] = map[string]any{"gpt-image-2": "gpt-image-1"}
			upstream := imageFormUpstream()
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			require.NoError(t, err)
			require.Equal(t, 1, result.ImageCount)
			require.Len(t, upstream.requests, 1, "an edit is submitted only once")
			req := upstream.requests[0]
			require.Equal(t, "https://rpm.tosky.io/v1/images/edits", req.URL.String())
			require.Equal(t, "Bearer sk-test", req.Header.Get("Authorization"))
			require.EqualValues(t, len(upstream.bodies[0]), req.ContentLength)
			require.NoError(t, req.ParseMultipartForm(1<<20))
			defer func() { _ = req.MultipartForm.RemoveAll() }()
			for k, want := range map[string]string{"model": "gpt-image-1", "prompt": "原样保留\nblue", "stream": "false", "size": "2048x2048", "output_compression": "0"} {
				require.Equal(t, []string{want}, req.MultipartForm.Value[k], k)
			}
			require.Len(t, req.MultipartForm.File["image"], 1)
			for name, files := range req.MultipartForm.File {
				require.Contains(t, []string{"image", "mask"}, name)
				for _, file := range files {
					reader, err := file.Open()
					require.NoError(t, err)
					got, err := io.ReadAll(reader)
					require.NoError(t, reader.Close())
					require.NoError(t, err)
					require.Equal(t, b64BackfillPNGBytes, got)
				}
			}
		})
	}
}

func TestImageEditFormForwardingScope(t *testing.T) {
	for _, tc := range []struct{ name, ids, base, endpoint string }{
		{"disabled", "", "https://rpm.tosky.io", openAIImagesEditsEndpoint},
		{"other account", "17", "https://rpm.tosky.io", openAIImagesEditsEndpoint},
		{"other host", "7", "https://relay.example.com", openAIImagesEditsEndpoint},
		{"generations", "7", "https://rpm.tosky.io", openAIImagesGenerationsEndpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SUB2API_IMAGE_EDIT_FORM_ACCOUNT_IDS", tc.ids)
			// A private reference must not be downloaded when the adapter is disabled.
			body := imageFormJSON(t, "http://127.0.0.1/private.png")
			c, parsed := imageFormContext(t, tc.endpoint, "application/json", body)
			account := b64BackfillAccount(false)
			account.Credentials["base_url"] = tc.base
			upstream := imageFormUpstream()
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			_, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "application/json", upstream.requests[0].Header.Get("Content-Type"))
			require.JSONEq(t, string(body), string(upstream.bodies[0]))
		})
	}
}

func TestImageEditFormRemoteDownloadPolicyAndFailures(t *testing.T) {
	t.Setenv("SUB2API_IMAGE_EDIT_FORM_ACCOUNT_IDS", "7")
	for _, tc := range []struct {
		name      string
		refs      []string
		fetchErr  error
		wantErr   bool
		wantCalls int
	}{
		{"public", []string{"https://cdn.example.com/test.png"}, nil, false, 2},
		{"private", []string{"http://127.0.0.1/private.png"}, nil, true, 0},
		{"multiple", []string{"https://cdn.example.com/a.png", "https://cdn.example.com/b.png"}, nil, true, 0},
		{"fetch error", []string{"https://cdn.example.com/test.png?signature=private-value"}, errors.New("Get https://cdn.example.com/test.png?signature=private-value: timeout"), true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := imageFormJSON(t, tc.refs...)
			c, parsed := imageFormContext(t, openAIImagesEditsEndpoint, "application/json", body)
			account := b64BackfillAccount(false)
			account.Credentials["base_url"] = "https://rpm.tosky.io/v1"
			proxyID := int64(1)
			account.ProxyID = &proxyID
			account.Proxy = &Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080}
			upstream := imageFormUpstream()
			upstream.err = tc.fetchErr
			upstream.responses = []*http.Response{b64BackfillImageResponse(http.StatusOK, "image/png", b64BackfillPNGBytes), upstream.resp}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			_, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			if tc.wantErr {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private-value")
				require.False(t, strings.Contains(err.Error(), "https://"))
			} else {
				require.NoError(t, err)
			}
			require.Len(t, upstream.requests, tc.wantCalls)
			if tc.wantCalls > 0 {
				download := upstream.requests[0]
				require.Equal(t, http.MethodGet, download.Method)
				require.Empty(t, download.Header.Get("Authorization"))
				require.True(t, HTTPUpstreamPublicHostsOnly(download.Context()))
				require.Equal(t, "http://proxy.example.com:8080", upstream.lastProxyURL)
			}
		})
	}
}
