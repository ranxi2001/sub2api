package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPR714RegionalAndDirectHeaderWait(t *testing.T) {
	for _, regional := range []bool{false, true} {
		for _, relaxed := range []bool{false, true} {
			name := "direct"
			if regional {
				name = "regional proxy"
			}
			if relaxed {
				name += "/relaxed"
			} else {
				name += "/global"
			}
			t.Run(name, func(t *testing.T) {
				var submissions atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					submissions.Add(1)
					select {
					case <-time.After(1500 * time.Millisecond):
						_, _ = io.WriteString(w, "synthetic completion")
					case <-r.Context().Done():
					}
				}))
				defer server.Close()
				cfg := &config.Config{}
				target := server.URL
				if regional {
					cfg = regionalHTTPConfig(t, server.URL, "images.example.test")
					target = "http://images.example.test/v1/images/generations"
				}
				upstream := newHTTPUpstream(cfg, fakeOpenAIResponseHeaderTimeoutProvider{seconds: 1, enabled: true})
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				ctx = service.WithHTTPUpstreamProfile(ctx, service.HTTPUpstreamProfileOpenAI)
				if relaxed {
					ctx = service.WithHTTPUpstreamResponseHeaderTimeout(ctx, 3*time.Second)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(`{"synthetic":true}`))
				require.NoError(t, err)
				resp, err := upstream.Do(req, "", 101, 1)
				if relaxed {
					require.NoError(t, err)
					body, readErr := io.ReadAll(resp.Body)
					require.NoError(t, readErr)
					require.NoError(t, resp.Body.Close())
					require.Equal(t, "synthetic completion", string(body))
				} else {
					require.Error(t, err)
				}
				require.EqualValues(t, 1, submissions.Load())
				requireNoUpstreamInFlight(t, pr612NativeTransport(t, upstream))
			})
		}
	}
}

func TestPR714HeaderWaitDoesNotTimeOutAnEstablishedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: start\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush synthetic SSE headers: %v", err)
			return
		}
		select {
		case <-time.After(1500 * time.Millisecond):
			_, _ = io.WriteString(w, "data: complete\n\n")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	upstream := newHTTPUpstream(&config.Config{}, fakeOpenAIResponseHeaderTimeoutProvider{seconds: 1, enabled: true})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(service.WithHTTPUpstreamProfile(ctx, service.HTTPUpstreamProfileOpenAI), http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := upstream.Do(req, "", 102, 1)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Contains(t, string(body), "data: complete")
	requireNoUpstreamInFlight(t, pr612NativeTransport(t, upstream))
}
