// Package redissession provides isolated Redis-backed session stores for tests.
package redissession

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/redissession"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// New keeps Redis client setup outside service packages while exercising the
// production session store against an isolated server with the requested TTL.
func New(t *testing.T, prefix string, ttl time.Duration) *redissession.Store {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return redissession.New(client, prefix, ttl)
}
