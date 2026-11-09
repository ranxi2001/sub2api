package repository

import (
	"runtime"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// A repository fixture must coexist with background clocks (including ants'
// package-init pool). Serial tests alone cannot protect process-wide time.Local.
// This test is also copied unchanged into the isolated pre-fix checkout in CI.
func TestRepositoryTimezoneIsolation(t *testing.T) {
	previousLocal, previousLocation, previousName := time.Local, timezone.Location(), timezone.Name()
	started, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				_ = time.Now()
				runtime.Gosched()
			}
		}
	}()
	<-started
	defer func() {
		close(stop)
		<-done
	}()
	for range 64 {
		useGroupUsageRepositoryTestTimezone(t, "America/New_York")
		useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	}
	require.Same(t, previousLocal, time.Local, "repository fixtures must leave the process clock timezone unchanged")
	require.Same(t, previousLocation, timezone.Location(), "repository fixtures must leave the application timezone unchanged")
	require.Equal(t, previousName, timezone.Name())
}
