//go:build integration

package repository

import (
	"database/sql"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// UserLiveLoadTestCache exposes the isolated integration fixture to external
// HTTP tests without importing handlers into the repository package.
type UserLiveLoadTestCache struct{ *regionalConcurrencyCache }

func NewUserLiveLoadTestCache(t *testing.T, local bool) *UserLiveLoadTestCache {
	return &UserLiveLoadTestCache{userLiveLoadRegionalCache(t, local)}
}

func NewUserLiveLoadTestRequest(t *testing.T, c *UserLiveLoadTestCache) (service.LiveLeaseTransferRequest, *service.AcquireResult) {
	return userLiveLoadUnlimitedRequest(t, c.regionalConcurrencyCache)
}

func (c *UserLiveLoadTestCache) Database() *sql.DB { return c.database() }
func (c *UserLiveLoadTestCache) Region() string    { return c.region() }

func (c *UserLiveLoadTestCache) PauseNextPipeline(t *testing.T) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	base, ok := c.ConcurrencyCache.(*concurrencyCache)
	require.True(t, ok)
	hook := &userLiveLoadPauseHook{started: make(chan struct{}), resume: make(chan struct{})}
	base.rdb.AddHook(hook)
	return hook.started, hook.resume
}
