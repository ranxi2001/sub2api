package repository

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/generationwrite"
	"github.com/stretchr/testify/require"
)

func TestPriorityLifecycleHTTPPostDialPhysicalZero(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer provider.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	defer transport.CloseIdleConnections()
	blocked := errors.New("synthetic new stop after dial wait")
	var stopped atomic.Bool
	req, err := http.NewRequest("POST", provider.URL, strings.NewReader("synthetic generation"))
	require.NoError(t, err)
	req = generationwrite.With(req, func(context.Context) error {
		if stopped.Load() {
			return blocked
		}
		return nil
	})
	done := make(chan error, 1)
	go func() {
		resp, err := doUpstreamRequest(&http.Client{Transport: transport}, req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("dial boundary not entered")
	}
	stopped.Store(true)
	close(release)
	select {
	case err := <-done:
		require.ErrorIs(t, err, blocked)
	case <-time.After(3 * time.Second):
		t.Fatal("postdial guard did not settle")
	}
	require.Zero(t, writes.Load(), "blocked generation must never reach provider after dial/TLS waiting")
}
