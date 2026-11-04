package serverless

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type geoTransport func(*http.Request) (*http.Response, error)

func (f geoTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestServerlessGeoJSUsesExistingProviderAndSharedCache(t *testing.T) {
	cache := cacheFor(t)
	g := NewGeoJSResolver(cache)
	var calls atomic.Int32
	g.client.Transport = geoTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, "https://get.geojs.io/v1/ip/geo/8.8.8.8.json", r.URL.String())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ip":"8.8.8.8","country_code":"US"}`))}, nil
	})
	require.Equal(t, "US", g.Country(context.Background(), "8.8.8.8"))
	other := NewGeoJSResolver(cache)
	other.client = g.client
	require.Equal(t, "US", other.Country(context.Background(), "8.8.8.8"))
	require.Equal(t, int32(1), calls.Load())
	keys, err := cache.Keys(context.Background(), prefix+"geojs:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	ttl, err := cache.TTL(context.Background(), keys[0]).Result()
	require.NoError(t, err)
	require.InDelta(t, (24 * time.Hour).Seconds(), ttl.Seconds(), 2)
	require.Empty(t, g.Country(context.Background(), "127.0.0.1"))
	require.Empty(t, g.Country(context.Background(), "10.2.3.4"))
	require.Empty(t, g.Country(context.Background(), "not-ip"))
	require.Equal(t, int32(1), calls.Load())
}
func TestServerlessGeoJSRejectsMismatchedIPAndCachesUnknown(t *testing.T) {
	g := NewGeoJSResolver(cacheFor(t))
	calls := 0
	g.client.Transport = geoTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ip":"1.1.1.1","country_code":"US"}`))}, nil
	})
	require.Empty(t, g.Country(context.Background(), "8.8.4.4"))
	require.Empty(t, g.Country(context.Background(), "8.8.4.4"))
	require.Equal(t, 1, calls)
}
