//go:build unit

package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/regional"
	"github.com/stretchr/testify/require"
)

type authRuntimeAuthorityStub struct {
	mu       sync.Mutex
	wire     []byte
	revision uint64
	records  []RegionalAuthRecord
}

func (a *authRuntimeAuthorityStub) Restore(context.Context, string, bool) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.wire...), nil
}
func (a *authRuntimeAuthorityStub) Publish(ctx context.Context, _ string, build func(context.Context, uint64, RegionalAuthSource) ([]byte, error)) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	wire, err := build(ctx, a.revision+1, regionalAuthSourceStub{records: a.records})
	if err == nil {
		a.revision++
		a.wire = wire
	}
	return wire, err
}
func writeAuthRuntimeTestConfig(t *testing.T, c RegionalSchedulerRuntimeConfig) *config.Config {
	cfg := writeRuntimeTestConfig(t, c)
	cfg.Runtime.RegionalAuthFile = cfg.Runtime.RegionalSchedulerFile
	cfg.Runtime.RegionalSchedulerFile = ""
	return cfg
}
func TestRegionalAuthRuntimeMTLSPullRevocationAndRestore(t *testing.T) {
	ctx := context.Background()
	c := runtimeTestConfig(t)
	c.Kind = regional.SnapshotAuth
	c.AuthUserIDs = []int64{2}
	c.AuthLookupKey = c.Keys[c.KeyID]
	c.AllowedIDs = nil
	c.Buckets = nil
	authority := &authRuntimeAuthorityStub{records: []RegionalAuthRecord{authTestRecord()}}
	pub, err := startRegionalAuthRuntime(writeAuthRuntimeTestConfig(t, c), authority)
	require.NoError(t, err)
	defer pub.stop()
	server := httptest.NewUnstartedServer(pub.server.Handler)
	server.TLS = pub.server.TLSConfig.Clone()
	server.StartTLS()
	defer server.Close()
	c.Mode = "subscriber"
	c.Endpoint = server.URL + "/internal/regional/auth"
	c.StateFile = filepath.Join(t.TempDir(), "auth")
	c.Bootstrap = true
	cfg := writeAuthRuntimeTestConfig(t, c)
	svc, err := ProvideAPIKeyService(nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil)
	require.NoError(t, err)
	defer svc.StopAuthCacheInvalidationSubscriber()
	require.Eventually(t, func() bool { _, err := svc.GetByKey(ctx, authTestRecord().APIKey.Key); return err == nil }, 5*time.Second, 10*time.Millisecond)
	// Both kinds are isolated even though they share transport implementation.
	require.Equal(t, regional.SnapshotAuth, svc.regionalAuthRuntime.cfg.snapshotKind())
	authority.mu.Lock()
	authority.records = nil
	authority.mu.Unlock()
	require.Eventually(t, func() bool {
		_, err := svc.GetByKeyUncached(ctx, authTestRecord().APIKey.Key)
		return errors.Is(err, ErrAPIKeyNotFound)
	}, 5*time.Second, 10*time.Millisecond)
	svc.StopAuthCacheInvalidationSubscriber()
	pub.stop()
	server.Close()
	c.Bootstrap = false
	restored, err := ProvideAPIKeyService(nil, nil, nil, nil, nil, nil, writeAuthRuntimeTestConfig(t, c), nil, nil, nil, nil)
	require.NoError(t, err)
	defer restored.StopAuthCacheInvalidationSubscriber()
	_, err = restored.GetByKey(ctx, authTestRecord().APIKey.Key)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	// Explicit kind is mandatory on the auth runtime file.
	c.Kind = ""
	_, err = startRegionalAuthRuntime(writeAuthRuntimeTestConfig(t, c), nil)
	require.Error(t, err)
}
