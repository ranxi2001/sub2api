package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type roleMatrixConcurrencyCache struct {
	ConcurrencyCache
	startupScans   atomic.Int64
	periodicScans  atomic.Int64
	runtimeStarted chan context.Context
}

func (c *roleMatrixConcurrencyCache) CleanupStaleProcessSlots(context.Context, string) error {
	c.startupScans.Add(1)
	return nil
}
func (c *roleMatrixConcurrencyCache) CleanupExpiredAccountSlotKeys(context.Context) error {
	c.periodicScans.Add(1)
	return nil
}
func (c *roleMatrixConcurrencyCache) RunRegionalRuntime(ctx context.Context) {
	c.runtimeStarted <- ctx
}

func TestRuntimeRoleGatesGlobalMaintenanceAndPreservesRegionalRuntime(t *testing.T) {
	for _, role := range []string{"", config.RuntimeRoleFull, config.RuntimeRoleGateway} {
		t.Run(role, func(t *testing.T) {
			cfg := &config.Config{Runtime: config.RuntimeConfig{Role: role}}
			cfg.Gateway.Scheduling.SlotCleanupInterval = time.Hour
			cache := &roleMatrixConcurrencyCache{runtimeStarted: make(chan context.Context, 1)}
			svc := ProvideConcurrencyService(cache, nil, cfg, nil)
			defer svc.StopAPIKeyQueue()
			select {
			case ctx := <-cache.runtimeStarted:
				require.NotNil(t, ctx)
				require.NoError(t, ctx.Err())
			case <-time.After(time.Second):
				t.Fatal("regional admission runtime must remain active on every role")
			}
			if role == config.RuntimeRoleGateway {
				require.Zero(t, cache.startupScans.Load())
				require.Never(t, func() bool { return cache.periodicScans.Load() > 0 }, 30*time.Millisecond, time.Millisecond)
			} else {
				require.EqualValues(t, 1, cache.startupScans.Load())
				require.Eventually(t, func() bool { return cache.periodicScans.Load() > 0 }, time.Second, time.Millisecond)
			}

			collector := ProvideOpsMetricsCollector(nil, nil, nil, nil, nil, nil, cfg)
			defer collector.Stop()
			if role == config.RuntimeRoleGateway {
				require.Nil(t, collector.stopCh, "gateway must not enter collector polling/leader-lock contention")
			} else {
				require.NotNil(t, collector.stopCh)
			}

			// Embedded unused methods panic if the provider unexpectedly performs
			// request work while deciding whether to start the recovery loop.
			gemini := ProvideGeminiCachedContentService(&roleMatrixGeminiRepository{}, nil, cfg)
			defer gemini.Stop()
			recoveryNotStarted := false
			gemini.startOnce.Do(func() { recoveryNotStarted = true })
			require.Equal(t, role == config.RuntimeRoleGateway, recoveryNotStarted)
		})
	}
}

type roleMatrixGeminiRepository struct{ GeminiCachedContentRepository }

type roleMatrixAuditRepository struct {
	AuditLogRepository
	written chan []*AuditLog
}

func (r *roleMatrixAuditRepository) BatchInsert(_ context.Context, entries []*AuditLog) (int64, error) {
	r.written <- append([]*AuditLog(nil), entries...)
	return int64(len(entries)), nil
}

func TestRuntimeRoleKeepsLocalAuditWritesAndGatesSharedRetention(t *testing.T) {
	for _, role := range []string{"", config.RuntimeRoleFull, config.RuntimeRoleGateway} {
		t.Run(role, func(t *testing.T) {
			cfg := &config.Config{Runtime: config.RuntimeConfig{Role: role}}
			repo := &roleMatrixAuditRepository{written: make(chan []*AuditLog, 1)}
			svc := ProvideAuditLogService(repo, nil, cfg)
			defer svc.Stop()
			retentionNotStarted := false
			svc.retentionOnce.Do(func() { retentionNotStarted = true })
			require.Equal(t, role == config.RuntimeRoleGateway, retentionNotStarted)
			svc.Record(&AuditLog{Action: "role_matrix.audit"})
			svc.Stop()
			select {
			case entries := <-repo.written:
				require.Len(t, entries, 1)
				require.Equal(t, "role_matrix.audit", entries[0].Action)
			case <-time.After(time.Second):
				t.Fatal("local audit entry was not persisted on shutdown")
			}
		})
	}
}
