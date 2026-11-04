package serverless

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestServerlessKeyIPUsesIndependentBindingsAndSharedCountryCache(t *testing.T) {
	cache := cacheFor(t)
	store := &memoryStore{}
	_, srv := registeredNode(t, cache, store, func(c *gin.Context) { c.String(200, "pod") })
	store.config = routeConfig(srv.URL)
	store.config.AffinityScope = AffinityKeyIP
	store.config.Regions[0].Country = "CN"
	m := managerFor(t, "", cache, store)
	geo := NewGeoJSResolver(cache)
	var lookups atomic.Int32
	geo.client.Transport = geoTransport(func(r *http.Request) (*http.Response, error) {
		lookups.Add(1)
		ip, country := "8.8.8.8", "CN"
		if strings.Contains(r.URL.Path, "1.1.1.1") {
			ip, country = "1.1.1.1", "US"
		}
		body, _ := json.Marshal(map[string]string{"ip": ip, "country_code": country})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	m.geo = geo
	// The country responses are mock fixtures, not the real countries of these IPs.
	for _, tc := range []struct {
		key  int64
		ip   string
		want string
	}{
		{42, "8.8.8.8", "pod"},
		{42, "1.1.1.1", "primary"},
		{42, "8.8.8.8", "pod"},
		{42, "::ffff:8.8.8.8", "pod"},
		{43, "8.8.8.8", "pod"},
	} {
		response := request(t, ingressRouter(m, tc.key, tc.ip), "/v1/responses")
		require.Equal(t, 200, response.Code)
		require.Equal(t, tc.want, response.Body.String())
	}
	require.Equal(t, int32(2), lookups.Load(), "new keys must reuse Redis IP-country results")
	keys, err := cache.Keys(context.Background(), prefix+"affinity_ip:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 3)
	for _, key := range keys {
		require.NotContains(t, key, "8.8.8.8")
		require.NotContains(t, key, "1.1.1.1")
	}
}

func TestServerlessLegacyAffinityStillPinsKeyAcrossIPs(t *testing.T) {
	cache := cacheFor(t)
	store := &memoryStore{}
	_, srv := registeredNode(t, cache, store, func(c *gin.Context) { c.String(200, "pod") })
	store.config = routeConfig(srv.URL)
	m := managerFor(t, "", cache, store)
	require.Equal(t, "pod", request(t, ingressRouter(m, 42, "203.0.113.1"), "/v1/responses").Body.String())
	require.Equal(t, "pod", request(t, ingressRouter(m, 42, "198.51.100.1"), "/v1/responses").Body.String())
	keys, err := cache.Keys(context.Background(), prefix+"affinity:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)
}

func TestServerlessOptInFallbackMovesUnavailableBindingBeforeForward(t *testing.T) {
	for _, failure := range []string{"restarted", "probe_failed"} {
		t.Run(failure, func(t *testing.T) {
			cache := cacheFor(t)
			store := &memoryStore{}
			var hits atomic.Int32
			node, srv := registeredNode(t, cache, store, func(c *gin.Context) { hits.Add(1); c.String(200, "pod") })
			store.config = routeConfig(srv.URL)
			store.config.FallbackToPrimary = true
			m := managerFor(t, "", cache, store)
			router := ingressRouter(m, 42, "203.0.113.1")
			require.Equal(t, "pod", request(t, router, "/v1/responses").Body.String())
			if failure == "restarted" {
				node.boot = randomID()
				require.NoError(t, cache.Del(context.Background(), prefix+"pod:us-pod").Err())
				node.heartbeat(context.Background())
			} else {
				srv.Close()
				m.health = map[string]healthResult{}
			}
			response := request(t, router, "/v1/responses")
			require.Equal(t, 200, response.Code)
			require.Equal(t, "primary", response.Body.String())
			require.Equal(t, "primary", response.Header().Get("X-Sub2api-Pod"))
			require.Equal(t, "primary", request(t, router, "/v1/responses").Body.String())
			require.Equal(t, int32(1), hits.Load())
			stats, err := m.Stats(context.Background())
			require.NoError(t, err)
			require.Equal(t, "1", stats["primary|US|binding_fallback|requests"])
		})
	}
}

func TestServerlessFallbackDoesNotOverwriteConcurrentBinding(t *testing.T) {
	cache := cacheFor(t)
	m := managerFor(t, "", cache, &memoryStore{})
	key := m.affinityKey(Config{}, 42, "203.0.113.1")
	previous, _ := json.Marshal(Binding{ID: "old", Country: "US"})
	newer, _ := json.Marshal(Binding{ID: "new", Country: "CN"})
	require.NoError(t, cache.Set(context.Background(), key, string(newer), affinityTTL).Err())
	require.False(t, m.bindPrimary(context.Background(), key, string(previous), "US"))
	got, err := cache.Get(context.Background(), key).Result()
	require.NoError(t, err)
	require.Equal(t, string(newer), got)
	require.NoError(t, cache.Set(context.Background(), key, string(previous), affinityTTL).Err())
	require.True(t, m.bindPrimary(context.Background(), key, string(previous), "US"))
	require.True(t, m.bindPrimary(context.Background(), key, string(previous), "US"), "concurrent identical fallbacks may proceed locally")
}

func TestServerlessPrimaryFallbackNeverReplaysAForwardedRequest(t *testing.T) {
	for _, failure := range []string{"http_error", "disconnect_after_flush"} {
		t.Run(failure, func(t *testing.T) {
			cache := cacheFor(t)
			store := &memoryStore{}
			var hits atomic.Int32
			_, srv := registeredNode(t, cache, store, func(c *gin.Context) {
				hits.Add(1)
				if failure == "http_error" {
					c.String(503, "pod error")
					return
				}
				conn, buffer, err := c.Writer.Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = buffer.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 100\r\n\r\ndata: first\n\n")
				_ = buffer.Flush()
				_ = conn.Close()
			})
			store.config = routeConfig(srv.URL)
			store.config.FallbackToPrimary = true
			m := managerFor(t, "", cache, store)
			response := request(t, ingressRouter(m, 42, "203.0.113.1"), "/v1/responses")
			require.Equal(t, int32(1), hits.Load())
			require.NotEqual(t, "primary", response.Body.String())
			raw, err := cache.Get(context.Background(), m.affinityKey(store.config, 42, "203.0.113.1")).Result()
			require.NoError(t, err)
			var binding Binding
			require.NoError(t, json.Unmarshal([]byte(raw), &binding))
			require.Equal(t, "us-pod", binding.ID)
		})
	}
}

func TestServerlessAffinitySettingsRejectUnknownScope(t *testing.T) {
	for _, scope := range []string{"", AffinityAPIKey, AffinityKeyIP} {
		require.NoError(t, Validate(Config{AffinityScope: scope}))
	}
	require.Error(t, Validate(Config{AffinityScope: "ip"}))
}
