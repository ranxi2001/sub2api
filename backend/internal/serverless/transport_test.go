package serverless

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func managerWithHeaderBudget(t *testing.T, cache *redis.Client, store *memoryStore, budget time.Duration) *Manager {
	t.Helper()
	m, err := New(Runtime{Secret: strings.Repeat("k", 32), ResponseHeaderTimeout: budget}, cache, store)
	require.NoError(t, err)
	m.geo = mockCountry{}
	t.Cleanup(m.Stop)
	return m
}

func transferIngressRouter(m *Manager, primaryHits *atomic.Int32) http.Handler {
	r := gin.New()
	r.POST("/v1/responses", func(c *gin.Context) { m.Route(c, 42, "203.0.113.4") }, func(c *gin.Context) {
		primaryHits.Add(1)
		c.String(http.StatusOK, "primary")
	})
	return r
}

func waitForTransferSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func TestServerlessTransferResponseHeaderTimeoutDefaultsAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget time.Duration
	}{
		{name: "default has no extra header deadline"},
		{name: "explicit finite header deadline", budget: 80 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(Runtime{ResponseHeaderTimeout: tc.budget}, nil, nil)
			require.NoError(t, err)
			t.Cleanup(m.Stop)
			// Pin the default contract without making a regression take 60 seconds.
			require.Equal(t, tc.budget, m.transport.ResponseHeaderTimeout)
		})
	}
	_, err := New(Runtime{ResponseHeaderTimeout: -time.Nanosecond}, nil, nil)
	require.Error(t, err, "invalid budgets must also be rejected on the primary")
}

func TestServerlessTransferWaitsForDelayedHeadersWithoutBudget(t *testing.T) {
	cache := cacheFor(t)
	store := &memoryStore{}
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHeaders := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHeaders()
	var podHits, primaryHits atomic.Int32
	_, srv := registeredNode(t, cache, store, func(c *gin.Context) {
		_, _ = io.Copy(io.Discard, c.Request.Body)
		if podHits.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			c.String(http.StatusOK, "delayed pod response")
		case <-c.Request.Context().Done():
		}
	})
	store.config = routeConfig(srv.URL)
	store.config.FallbackToPrimary = true
	m := managerWithHeaderBudget(t, cache, store, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := testRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}")).WithContext(ctx)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		transferIngressRouter(m, &primaryHits).ServeHTTP(w, req)
		result <- w
	}()
	waitForTransferSignal(t, started, "business request did not reach Pod")
	select {
	case <-result:
		t.Fatal("transfer ended before the Pod released response headers")
	case <-time.After(100 * time.Millisecond):
	}
	releaseHeaders()
	select {
	case w := <-result:
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "delayed pod response", w.Body.String())
	case <-time.After(2 * time.Second):
		t.Fatal("transfer did not finish after Pod response headers were released")
	}
	require.Equal(t, int32(1), podHits.Load())
	require.Zero(t, primaryHits.Load())
}

func TestServerlessTransferFiniteHeaderBudgetDoesNotReplay(t *testing.T) {
	cache := cacheFor(t)
	store := &memoryStore{}
	started, cancelled := make(chan struct{}), make(chan struct{})
	var podHits, primaryHits atomic.Int32
	_, srv := registeredNode(t, cache, store, func(c *gin.Context) {
		_, _ = io.Copy(io.Discard, c.Request.Body)
		firstRequest := podHits.Add(1) == 1
		if firstRequest {
			close(started)
		}
		<-c.Request.Context().Done()
		if firstRequest {
			close(cancelled)
		}
	})
	store.config = routeConfig(srv.URL)
	store.config.FallbackToPrimary = true
	m := managerWithHeaderBudget(t, cache, store, 50*time.Millisecond)
	var logs bytes.Buffer
	m.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := testRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}")).WithContext(ctx)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		transferIngressRouter(m, &primaryHits).ServeHTTP(w, req)
		result <- w
	}()
	waitForTransferSignal(t, started, "business request did not reach Pod")
	select {
	case w := <-result:
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		require.Contains(t, w.Body.String(), `"type":"serverless_unavailable"`)
		require.Contains(t, w.Body.String(), "Pod transfer failed; request was not retried")
	case <-time.After(time.Second):
		t.Fatal("finite response-header budget did not end the transfer")
	}
	waitForTransferSignal(t, cancelled, "timed-out transfer did not cancel the Pod request")
	require.Equal(t, int32(1), podHits.Load(), "a transferred POST must be sent only once")
	require.Zero(t, primaryHits.Load(), "fallback must not replay a business request already sent to the Pod")
	var entry map[string]any
	decoder := json.NewDecoder(&logs)
	require.NoError(t, decoder.Decode(&entry))
	require.Equal(t, "serverless.forward_failed", entry["msg"])
	require.Equal(t, "response_header_timeout", entry["error_kind"])
	require.ErrorIs(t, decoder.Decode(&entry), io.EOF, "one failed transfer must emit one failure log")
}

func TestServerlessTransferCancellationWhileWaitingForHeadersReachesPod(t *testing.T) {
	cache := cacheFor(t)
	store := &memoryStore{}
	started, cancelled := make(chan struct{}), make(chan struct{})
	var podHits, primaryHits atomic.Int32
	_, srv := registeredNode(t, cache, store, func(c *gin.Context) {
		_, _ = io.Copy(io.Discard, c.Request.Body)
		firstRequest := podHits.Add(1) == 1
		if firstRequest {
			close(started)
		}
		<-c.Request.Context().Done()
		if firstRequest {
			close(cancelled)
		}
	})
	store.config = routeConfig(srv.URL)
	store.config.FallbackToPrimary = true
	m := managerWithHeaderBudget(t, cache, store, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := testRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}")).WithContext(ctx)
	finished := make(chan struct{})
	go func() {
		transferIngressRouter(m, &primaryHits).ServeHTTP(httptest.NewRecorder(), req)
		close(finished)
	}()
	waitForTransferSignal(t, started, "business request did not reach Pod")
	cancel()
	waitForTransferSignal(t, cancelled, "client cancellation did not reach Pod before response headers")
	waitForTransferSignal(t, finished, "transfer kept waiting after client cancellation")
	require.Equal(t, int32(1), podHits.Load())
	require.Zero(t, primaryHits.Load())
}

func TestServerlessProbeIsIndependentOfBusinessHeaderBudget(t *testing.T) {
	cache := cacheFor(t)
	store := &memoryStore{}
	node := managerFor(t, "us-pod", cache, store)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseHeaders := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHeaders()
	router := gin.New()
	router.GET("/internal/serverless/probe", func(c *gin.Context) {
		close(started)
		select {
		case <-release:
			node.ProbeHandler(c)
		case <-c.Request.Context().Done():
		}
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	m := managerWithHeaderBudget(t, cache, store, 20*time.Millisecond)
	probeTransport, ok := m.http.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotSame(t, m.transport, probeTransport)
	require.Zero(t, probeTransport.ResponseHeaderTimeout)
	require.Equal(t, 3*time.Second, m.http.Timeout, "probes must retain their short total budget")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan bool, 1)
	go func() {
		result <- m.Probe(ctx, Pod{ID: "us-pod", Boot: node.boot, Endpoint: srv.URL, Ready: true})
	}()
	waitForTransferSignal(t, started, "signed health probe did not reach Pod")
	select {
	case <-result:
		t.Fatal("probe inherited the shorter business response-header budget")
	case <-time.After(100 * time.Millisecond):
	}
	releaseHeaders()
	select {
	case healthy := <-result:
		require.True(t, healthy, "a signed probe within its own budget should succeed")
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not finish after its response headers were released")
	}
}

func TestServerlessTransferPreservesCanonicalRequestID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		contextID string
		headerID  string
		wantID    string
	}{
		{name: "generated ID", contextID: "generated-request-id", wantID: "generated-request-id"},
		{name: "canonical context overrides header", contextID: "canonical-request-id", headerID: "stale-header-id", wantID: "canonical-request-id"},
		{name: "existing header without context", headerID: "existing-request-id", wantID: "existing-request-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := cacheFor(t)
			store := &memoryStore{}
			received := make(chan http.Header, 1)
			_, srv := registeredNode(t, cache, store, func(c *gin.Context) {
				received <- c.Request.Header.Clone()
				c.String(http.StatusOK, "pod")
			})
			store.config = routeConfig(srv.URL)
			m := managerWithHeaderBudget(t, cache, store, 0)
			req := testRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}"))
			req.Header.Set("X-Client-Request-ID", "client-request-id")
			if tc.headerID != "" {
				req.Header.Set("X-Request-ID", tc.headerID)
			}
			if tc.contextID != "" {
				req = req.WithContext(context.WithValue(req.Context(), ctxkey.RequestID, tc.contextID))
			}
			w := httptest.NewRecorder()
			ingressRouter(m, 42, "203.0.113.4").ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			select {
			case headers := <-received:
				require.Equal(t, tc.wantID, headers.Get("X-Request-ID"))
				require.Equal(t, "client-request-id", headers.Get("X-Client-Request-ID"))
			case <-time.After(2 * time.Second):
				t.Fatal("business request did not reach Pod")
			}
		})
	}
}
