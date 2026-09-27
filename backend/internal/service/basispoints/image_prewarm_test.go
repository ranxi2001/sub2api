package basispoints

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestImagePrewarmRequiresCompleteReadyResponse(t *testing.T) {
	data := relayTestPNG(t)
	for _, tc := range []struct {
		name   string
		status int
		ready  string
		size   int
		good   bool
	}{
		{"ready", 200, "1", len(data), true}, {"old origin", 200, "", len(data), false},
		{"partial", 200, "1", len(data) - 1, false}, {"failure", 503, "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "HEAD", r.Method)
				require.Empty(t, r.Header.Get("Authorization"))
				w.Header().Set("Content-Length", strconv.Itoa(tc.size))
				w.Header().Set("X-BPS-Image-Ready", tc.ready)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			relay, err := newTestImageRelay(t, srv.URL)
			require.NoError(t, err)
			body, err := relay.Rewrite(relayTestRequest(t, data), "scope")
			require.NoError(t, err)
			err = relay.prewarm(context.Background(), body, srv.Client())
			if tc.good {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrImageRelayPrewarm)
				require.NotContains(t, err.Error(), srv.URL)
			}
		})
	}
}

func TestImagePrewarmDeduplicatesToolImagesAndIgnoresForeignURLs(t *testing.T) {
	data := relayTestPNG(t)
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("X-BPS-Image-Ready", "1")
	}))
	defer srv.Close()
	relay, err := newTestImageRelay(t, srv.URL)
	require.NoError(t, err)
	body, err := relay.Rewrite(relayTestRequest(t, data), "scope")
	require.NoError(t, err)
	u := relayTestURL(t, body)
	request := object{"input": []any{object{"type": "custom_tool_call_output", "output": []any{object{"type": "input_image", "image_url": u}, object{"type": "input_image", "image_url": u}, object{"type": "input_image", "image_url": "https://untrusted.invalid/secret"}, object{"type": "input_image", "image_url": srv.URL + ImageRelayPath + strings.Repeat("x", 43)}}}}}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, relay.prewarm(context.Background(), raw, srv.Client()))
	require.EqualValues(t, 1, calls.Load())
}

func TestImagePrewarmHonorsCancellationAndRejectsRedirect(t *testing.T) {
	data := relayTestPNG(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	relay, err := newTestImageRelay(t, srv.URL)
	require.NoError(t, err)
	body, err := relay.Rewrite(relayTestRequest(t, data), "scope")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, relay.prewarm(ctx, body, srv.Client()), ErrImageRelayPrewarm)
	require.Equal(t, http.ErrUseLastResponse, imagePrewarmClient.CheckRedirect(nil, nil))
}
