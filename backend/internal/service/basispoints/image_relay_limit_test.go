package basispoints

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func configureImageCount(t *testing.T, relay *ImageRelay, count int) {
	t.Helper()
	limits := relay.limits
	limits.MaxImages = count
	if limits.StorageEntries < count {
		limits.StorageEntries = count
	}
	require.NoError(t, relay.Configure(relay.baseURL, limits))
}

func TestImageRelayConfiguredCountIncludesHistoryAndTools(t *testing.T) {
	relay, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(relayTestPNG(t))
	imagePart := object{"type": "input_image", "image_url": dataURL}
	images := make([]any, 20)
	for i := range images {
		images[i] = imagePart
	}
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		t.Run(kind, func(t *testing.T) {
			configureImageCount(t, relay, 20)
			body, err := json.Marshal(object{"input": []any{
				object{"role": "user", "content": images},
				object{"type": kind, "output": []any{imagePart}},
			}})
			require.NoError(t, err)
			_, err = relay.Rewrite(body, "history")
			require.ErrorContains(t, err, "at most 20")
			configureImageCount(t, relay, 64)
			rewritten, err := relay.Rewrite(body, "history")
			require.NoError(t, err)
			require.NotContains(t, string(rewritten), "data:image")
			require.Equal(t, 21, bytes.Count(rewritten, []byte(ImageRelayPath)))
			configureImageCount(t, relay, 20)
			_, err = relay.Rewrite(body, "history")
			require.ErrorContains(t, err, "at most 20")
			relay.mu.Lock()
			require.Zero(t, relay.reservedEntries)
			require.Zero(t, relay.reservedBytes)
			relay.mu.Unlock()
		})
	}
}

func TestImageRelayCountSettingBounds(t *testing.T) {
	relay, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	require.Equal(t, 20, relay.limits.MaxImages)
	for _, limit := range []int{-1, 0, 4097} {
		limits := relay.limits
		limits.MaxImages = limit
		require.Error(t, relay.Configure(relay.baseURL, limits))
		require.Equal(t, 20, relay.limits.MaxImages)
	}
	for _, limit := range []int{1, 64, 512, 4096} {
		configureImageCount(t, relay, limit)
		require.Equal(t, limit, relay.limits.MaxImages)
	}
}

func TestImageRelayMaximumCountRepeatedRequests(t *testing.T) {
	relay, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	configureImageCount(t, relay, 4096)
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(relayTestPNG(t))
	for _, count := range []int{1, 512, 4096, 4096, 4097} {
		parts := make([]any, count)
		for i := range parts {
			parts[i] = object{"type": "input_image", "image_url": url}
		}
		raw, err := json.Marshal(object{"input": []any{object{"role": "user", "content": parts}}})
		require.NoError(t, err)
		rewritten, err := relay.Rewrite(raw, "repeated-images")
		if count > 4096 {
			require.ErrorContains(t, err, "at most 4096")
		} else {
			require.NoError(t, err)
			require.Equal(t, count, bytes.Count(rewritten, []byte(ImageRelayPath)))
		}
		require.Len(t, relay.entries, 1)
		require.Zero(t, relay.reservedEntries)
		require.Zero(t, relay.reservedBytes)
	}
}

func TestImagePrewarmOverTwentyDistinctImagesAfterCountChange(t *testing.T) {
	var relay *ImageRelay
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-BPS-Image-Ready", "1")
		relay.ServeHTTP(w, req)
	}))
	defer server.Close()
	var err error
	relay, err = newTestImageRelay(t, server.URL)
	require.NoError(t, err)
	configureImageCount(t, relay, 64)
	parts := make([]any, 21)
	for i := range parts {
		img := image.NewRGBA(image.Rect(0, 0, 2, 3))
		img.SetRGBA(0, 0, color.RGBA{R: byte(i), A: 255})
		var encoded bytes.Buffer
		require.NoError(t, png.Encode(&encoded, img))
		parts[i] = object{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())}
	}
	raw, err := json.Marshal(object{"input": []any{object{"role": "user", "content": parts}}})
	require.NoError(t, err)
	rewritten, err := relay.Rewrite(raw, "unique-images")
	require.NoError(t, err)
	require.Len(t, relay.entries, 21)
	// Lowering the limit must not invalidate a batch already admitted above it.
	configureImageCount(t, relay, 20)
	require.NoError(t, relay.prewarm(context.Background(), rewritten, server.Client()))
}

func TestImageRelayExpiryHeaderForEdgeCache(t *testing.T) {
	relay, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	body, err := relay.Rewrite(relayTestRequest(t, relayTestPNG(t)), "scope")
	require.NoError(t, err)
	u := relayTestURL(t, body)
	rec := httptest.NewRecorder()
	relay.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, u, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	expires, err := strconv.ParseInt(rec.Header().Get("X-BPS-Image-Expires"), 10, 64)
	require.NoError(t, err)
	for _, img := range relay.entries {
		require.Equal(t, img.expires.Unix(), expires)
	}
}

func TestImageRelayMaximumDistinctBatch(t *testing.T) {
	relay, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	limits := DefaultImageRelayLimits()
	limits.MaxImages, limits.StorageEntries = 4096, 8192
	require.NoError(t, relay.Configure(relay.baseURL, limits))
	parts := make([]any, 4096)
	for i := range parts {
		img := image.NewRGBA(image.Rect(0, 0, 2, 3))
		img.SetRGBA(0, 0, color.RGBA{R: byte(i), G: byte(i >> 8), A: 255})
		var data bytes.Buffer
		require.NoError(t, png.Encode(&data, img))
		parts[i] = object{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes())}
	}
	for _, count := range []int{20, 21, 64, 512, 4096, 4096} {
		raw, err := json.Marshal(object{"input": []any{object{"role": "user", "content": parts[:count]}}})
		require.NoError(t, err)
		start := time.Now()
		out, err := relay.Rewrite(raw, "distinct-batch")
		require.NoError(t, err)
		require.Equal(t, count, bytes.Count(out, []byte(ImageRelayPath)))
		require.Len(t, relay.entries, count)
		require.Zero(t, relay.reservedEntries)
		require.Zero(t, relay.reservedBytes)
		t.Logf("images=%d request_bytes=%d elapsed_ms=%d", count, len(raw), time.Since(start).Milliseconds())
	}
}
