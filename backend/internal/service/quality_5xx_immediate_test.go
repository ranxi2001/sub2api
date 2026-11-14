//go:build unit

package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/qualityqueue/queuetest"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type immediateQualityAccountRepo struct {
	AccountRepository
	calls int
}

func (r *immediateQualityAccountRepo) ApplyQuality5xx(ctx context.Context, id int64) error {
	r.calls++
	return nil
}
func TestQuality5xxProductionProviderAppliesBeforeReturning(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := queuetest.NewClient(mr.Addr())
	defer func() { _ = rdb.Close() }()
	repo := &immediateQualityAccountRepo{}
	svc := ProvideRateLimitService(repo, nil, &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, rdb)
	require.NotNil(t, svc.qualityTrigger)
	defer func() { _ = svc.qualityTrigger.queue.Close() }()
	svc.observeQualityStatus(context.Background(), &Account{ID: 12, Type: AccountTypeOAuth}, 503)
	require.Equal(t, 1, repo.calls, "protection must finish before observer returns")
	members, err := mr.ZMembers(quality5xxPendingKey)
	require.NoError(t, err)
	require.NotEmpty(t, members)
	svc.observeQualityStatus(context.WithValue(context.Background(), qualityProbeContextKey{}, true), &Account{ID: 12, Type: AccountTypeOAuth}, 503)
	require.Equal(t, 1, repo.calls, "probes must not recursively start episodes")
	svc.observeQualityStatus(context.Background(), &Account{ID: 12, Type: AccountTypeOAuth}, 429)
	require.Equal(t, 1, repo.calls)
}
func TestQuality5xxPendingRemoveModelsNotCoveredByOlderFinish(t *testing.T) {
	p := pelicanPlan()
	now := time.Now()
	finished := now.Add(time.Second)
	p.LastRunAt = &finished
	p.PelicanConfig.Quality = &QualityPolicy{TriggerOnUpstream5xx: true, Action: QualityActionRemoveModel, RemoveModels: []string{"target"}}
	require.False(t, qualitySignalCovered(p, now), "old probe finishing after new signal must not acknowledge it")
}

func TestQuality5xxWorkerProviderHasQueue(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := queuetest.NewClient(mr.Addr())
	defer func() { _ = rdb.Close() }()
	// Exercise provider wiring without starting singleton cron jobs with nil
	// repositories. Such jobs can outlive this test and panic on a later tick.
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	svc := ProvideScheduledTestRunnerService(nil, nil, &AccountTestService{}, &RateLimitService{}, cfg, &QualityJudgeService{}, rdb, nil, &ChannelMonitorV2Service{})
	t.Cleanup(svc.Stop)
	require.NotNil(t, svc.qualityTrigger, "worker provider must connect the cross-instance queue")
	require.Nil(t, svc.cron)
	require.Nil(t, svc.triggerCancel)
}
