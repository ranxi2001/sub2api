package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountHTTPResponseNativeAndTLSFilters(t *testing.T) {
	original := upstreamstats.Default
	upstreamstats.Default = upstreamstats.NewStore(100, time.Now())
	defer func() { upstreamstats.Default = original }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer server.Close()
	client := NewHTTPUpstream(nil)
	since := time.Now()
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		for _, path := range []string{"/v1/messages", "/v1/models", "/v1/responses", "/v1/chat/completions", "/v1beta/models/synthetic:streamGenerateContent"} {
			req, err := http.NewRequest(method, server.URL+path, nil)
			require.NoError(t, err)
			resp, err := client.DoWithTLS(req, "", 90001, 1, &tlsfingerprint.Profile{Name: "http-fixture"})
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
		}
	}
	got := upstreamstats.Default.Snapshot([]int64{90001, 90002}, since, time.Now())
	require.Equal(t, int64(4), got[90001].Total, "only inference POSTs; upstream HTTP auth failures count because they reached upstream")
	require.Equal(t, int64(4), got[90001].Failures)
	require.Nil(t, got[90002].Percent)
}

func TestAccountHTTPResponseExcludesLocalAndNetworkFailures(t *testing.T) {
	original := upstreamstats.Default
	upstreamstats.Default = upstreamstats.NewStore(100, time.Now())
	defer func() { upstreamstats.Default = original }()
	client := NewHTTPUpstream(nil)
	since := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("local host rejection must not send")
		w.WriteHeader(500)
	}))
	defer server.Close()
	ctx := service.WithHTTPUpstreamPublicHostsOnly(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/messages", nil)
	require.NoError(t, err)
	_, err = client.Do(req, "", 90003, 1)
	require.Error(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	req, err = http.NewRequestWithContext(canceled, http.MethodPost, server.URL+"/v1/messages", nil)
	require.NoError(t, err)
	_, err = client.Do(req, "", 90003, 1)
	require.Error(t, err)
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	req, err = http.NewRequest(http.MethodPost, closed.URL+"/v1/responses", nil)
	require.NoError(t, err)
	_, err = client.Do(req, "", 90003, 1)
	require.Error(t, err)
	got := upstreamstats.Default.Snapshot([]int64{90003}, since, time.Now())[90003]
	require.Zero(t, got.Total)
	require.Nil(t, got.Percent)
}

func TestAccountHTTPResponseFingerprintedTLS(t *testing.T) {
	original := upstreamstats.Default
	upstreamstats.Default = upstreamstats.NewStore(100, time.Now())
	defer func() { upstreamstats.Default = original }()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	_, send := lifecycleClient(t, server, true)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", nil)
	require.NoError(t, err)
	since := time.Now()
	resp, err := send(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	got := upstreamstats.Default.Snapshot([]int64{1}, since, time.Now())[1]
	require.Equal(t, int64(1), got.Total)
	require.NotNil(t, got.Percent)
	require.Zero(t, *got.Percent)
}

func TestAccountHTTPResponseCancellationHeaderBoundary(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		sendHeaders bool
		status      int
	}{
		{name: "before_headers", status: http.StatusServiceUnavailable},
		{name: "after_success_headers", sendHeaders: true, status: http.StatusOK},
		{name: "after_error_headers", sendHeaders: true, status: http.StatusServiceUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			original := upstreamstats.Default
			upstreamstats.Default = upstreamstats.NewStore(100, time.Now())
			defer func() { upstreamstats.Default = original }()
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				if scenario.sendHeaders {
					// Keep the body incomplete so cancellation causes a real read
					// failure after RoundTrip has returned the response headers.
					w.Header().Set("Content-Length", "32")
					w.WriteHeader(scenario.status)
					_ = http.NewResponseController(w).Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/messages", nil)
			require.NoError(t, err)
			client := NewHTTPUpstream(nil)
			since := time.Now()
			type response struct {
				resp *http.Response
				err  error
			}
			done := make(chan response, 1)
			go func() {
				resp, err := client.Do(req, "", 90004, 1)
				done <- response{resp, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("loopback request did not reach the server before cancellation")
			}
			if !scenario.sendHeaders {
				cancel()
			}
			result := <-done
			if scenario.sendHeaders {
				require.NoError(t, result.err)
				require.Equal(t, scenario.status, result.resp.StatusCode)
				before := upstreamstats.Default.Snapshot([]int64{90004}, since, time.Now())[90004]
				require.Equal(t, int64(1), before.Total)
				cancel()
				_, readErr := io.ReadAll(result.resp.Body)
				require.ErrorIs(t, readErr, context.Canceled)
				require.NoError(t, result.resp.Body.Close())
				after := upstreamstats.Default.Snapshot([]int64{90004}, since, time.Now())[90004]
				require.Equal(t, before, after, "cancellation/body failure must not retract an observed HTTP status")
				if scenario.status >= 400 {
					require.Equal(t, int64(1), after.Failures)
				} else {
					require.Zero(t, after.Failures)
				}
			} else {
				require.ErrorIs(t, result.err, context.Canceled)
				require.Nil(t, result.resp)
				got := upstreamstats.Default.Snapshot([]int64{90004}, since, time.Now())[90004]
				require.Zero(t, got.Total)
				require.Nil(t, got.Percent)
			}
		})
	}
}

var _ service.HTTPUpstream = (*httpUpstreamService)(nil)
