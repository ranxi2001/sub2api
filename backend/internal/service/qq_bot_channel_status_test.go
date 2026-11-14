package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/stretchr/testify/require"
)

type qqStatusRuntimeStub struct{ runtime ChannelMonitorRuntime }

func (s qqStatusRuntimeStub) GetChannelMonitorRuntime(context.Context) ChannelMonitorRuntime {
	return s.runtime
}

type qqStatusGroupStub struct {
	groups []Group
	err    error
	calls  int
}

func (s *qqStatusGroupStub) ListActive(context.Context) ([]Group, error) {
	s.calls++
	return s.groups, s.err
}

type qqStatusRepoStub struct {
	ChannelMonitorV2Repository
	config      *ChannelMonitorV2Config
	snapshot    *ChannelMonitorV2Snapshot
	matrix      *ChannelMonitorV2Matrix
	fail        string
	configCalls int
	filters     []ChannelMonitorV2Filter
	configs     []ChannelMonitorV2Config
	admins      []bool
	groupBy     ChannelMonitorV2GroupBy
}

func (s *qqStatusRepoStub) GetConfig(context.Context) (*ChannelMonitorV2Config, error) {
	s.configCalls++
	if s.fail == "config" {
		return nil, errors.New("config failed")
	}
	return s.config, nil
}
func (s *qqStatusRepoStub) GetSnapshot(_ context.Context, filter ChannelMonitorV2Filter, cfg ChannelMonitorV2Config, admin bool) (*ChannelMonitorV2Snapshot, error) {
	s.filters = append(s.filters, filter)
	s.configs = append(s.configs, cfg)
	s.admins = append(s.admins, admin)
	if s.fail == "snapshot" {
		return nil, errors.New("snapshot failed")
	}
	return s.snapshot, nil
}
func (s *qqStatusRepoStub) GetMatrix(_ context.Context, filter ChannelMonitorV2Filter, cfg ChannelMonitorV2Config, groupBy ChannelMonitorV2GroupBy, admin bool) (*ChannelMonitorV2Matrix, error) {
	s.filters = append(s.filters, filter)
	s.configs = append(s.configs, cfg)
	s.admins = append(s.admins, admin)
	s.groupBy = groupBy
	if s.fail == "matrix" {
		return nil, errors.New("matrix failed")
	}
	return s.matrix, nil
}

func qqStatusAdapterFixture() (*qqBotChannelStatus, *qqStatusRepoStub, *qqStatusGroupStub) {
	updated := time.Date(2026, 10, 16, 6, 30, 0, 0, time.UTC)
	ttft := int64(1280)
	tps := 42.5
	metric := ChannelMonitorV2Metric{OutputTPS: &tps, RequestCount: 100, SuccessRequests: 90, ErrorRequests: 10, SuccessRate: .9, ErrorRate: .0032, CacheRate: .7625, CacheRateDenominator: 1000, RPM: 12, TPM: 3600, TTFT: ChannelMonitorV2Latency{SampleCount: 90, P50Ms: &ttft}}
	id := int64(1)
	repo := &qqStatusRepoStub{
		config:   &ChannelMonitorV2Config{Enabled: true, GroupIDs: []int64{1, 5}, IgnoredErrorCategories: []string{"client_cancelled"}, Platforms: []ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true, Models: []string{"model"}}}},
		snapshot: &ChannelMonitorV2Snapshot{Metrics: metric, Coverage: ChannelMonitorV2Coverage{DataThrough: updated, CoverageComplete: true}},
		matrix:   &ChannelMonitorV2Matrix{Coverage: ChannelMonitorV2Coverage{DataThrough: updated, CoverageComplete: true}, Items: []ChannelMonitorV2MatrixRow{{GroupID: &id, GroupName: "public", Platform: "openai", Metrics: ChannelMonitorV2Metric{RequestCount: 2, ErrorRate: .5}}}},
	}
	groups := &qqStatusGroupStub{groups: []Group{
		{ID: 1, Name: "公开分组", Status: StatusActive},
		{ID: 2, Name: "专属分组", Status: StatusActive, IsExclusive: true},
		{ID: 3, Name: "订阅分组", Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
		{ID: 4, Name: "停用分组", Status: StatusDisabled},
		{ID: 5, Name: "混合分组", Status: StatusActive},
	}}
	monitor := NewChannelMonitorV2Service(repo)
	monitor.now = func() time.Time { return updated.Add(time.Minute) }
	adapter := &qqBotChannelStatus{monitor: monitor, groups: groups, settings: qqStatusRuntimeStub{runtime: ChannelMonitorRuntime{Enabled: true, Mode: ChannelMonitorModeV2, HideThroughput: true}}}
	return adapter, repo, groups
}

func TestQQBotChannelStatusUsesMonitorAggregatesAndConfiguredGroups(t *testing.T) {
	p, repo, _ := qqStatusAdapterFixture()
	report, err := p.ChannelStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, repo.configCalls)
	require.Len(t, repo.filters, 2)
	require.Equal(t, repo.filters[0], repo.filters[1])
	require.Equal(t, "90m", repo.filters[0].Range)
	require.Equal(t, 90*time.Minute, repo.filters[0].End.Sub(repo.filters[0].Start))
	require.True(t, repo.filters[0].RestrictGroups)
	require.Equal(t, []int64{1, 2, 3, 5}, repo.filters[0].AllowedGroupIDs)
	require.Equal(t, *repo.config, repo.configs[0])
	require.Equal(t, repo.configs[0], repo.configs[1])
	require.Equal(t, []bool{false, false}, repo.admins)
	require.Equal(t, ChannelMonitorV2GroupByPlatformGroup, repo.groupBy)
	require.Equal(t, repo.snapshot.Coverage.DataThrough, report.UpdatedAt)
	require.True(t, report.Overall.HasRequests)
	require.False(t, report.Overall.InsufficientSamples)
	require.InDelta(t, .0032, report.Overall.ErrorRate, 1e-9)
	require.InDelta(t, .7625, *report.Overall.CacheRate, 1e-9)
	require.EqualValues(t, 1280, *report.Overall.TTFTP50Ms)
	require.False(t, report.ShowThroughput)
	require.Zero(t, report.Overall.RPM)
	require.Zero(t, report.Overall.TokensPerSecond)
	require.NotNil(t, report.Overall.OutputTPS)
	require.Equal(t, 42.5, *report.Overall.OutputTPS)
	require.Len(t, report.Groups, 1)
	require.Equal(t, "公开分组", report.Groups[0].Name)
	require.True(t, report.Groups[0].Metrics.InsufficientSamples)
	// Overall must use the original weighted snapshot, not average group rates.
	require.NotEqual(t, report.Overall.ErrorRate, report.Groups[0].Metrics.ErrorRate)
	require.Nil(t, report.Groups[0].Metrics.CacheRate)
	require.Nil(t, report.Groups[0].Metrics.TTFTP50Ms)
	require.Nil(t, report.Groups[0].Metrics.OutputTPS)
	require.EqualValues(t, 100, repo.snapshot.Metrics.RequestCount)

	p.settings = qqStatusRuntimeStub{runtime: ChannelMonitorRuntime{Enabled: true, Mode: ChannelMonitorModeV2}}
	report, err = p.ChannelStatus(context.Background())
	require.NoError(t, err)
	require.True(t, report.ShowThroughput)
	require.Equal(t, 12.0, report.Overall.RPM)
	require.Equal(t, 60.0, report.Overall.TokensPerSecond)
}

func TestQQBotHealthUsesFiveCompleteMinutesAndMonitorPolicy(t *testing.T) {
	p, repo, _ := qqStatusAdapterFixture()
	at := time.Date(2026, 10, 21, 3, 10, 29, 0, time.UTC)
	report, err := p.HealthSnapshot(context.Background(), at)
	require.NoError(t, err)
	require.Len(t, repo.filters, 1)
	f := repo.filters[0]
	require.Equal(t, 5*time.Minute, f.End.Sub(f.Start))
	require.Equal(t, 0, f.End.Second())
	require.Equal(t, time.Minute, f.Bucket)
	require.Equal(t, []int64{1, 2, 3, 5}, f.AllowedGroupIDs)
	require.Equal(t, *repo.config, repo.configs[0])
	require.True(t, report.Complete)
	require.Len(t, report.Groups, 1)
	require.Equal(t, "1:openai", report.Groups[0].Key)
	require.EqualValues(t, 2, report.Groups[0].Requests)
	require.Equal(t, .5, report.Groups[0].ErrorRate)
}

func TestQQBotChannelStatusNoTrafficAndMixedPlatforms(t *testing.T) {
	p, repo, _ := qqStatusAdapterFixture()
	id, disabledID := int64(5), int64(4)
	repo.matrix.Items = append(repo.matrix.Items,
		ChannelMonitorV2MatrixRow{GroupID: &id, Platform: "openai"},
		ChannelMonitorV2MatrixRow{GroupID: &id, Platform: "anthropic"},
		ChannelMonitorV2MatrixRow{GroupID: &disabledID, GroupName: "disabled"},
		ChannelMonitorV2MatrixRow{GroupName: "ungrouped"})
	repo.matrix.Coverage.DataThrough = repo.snapshot.Coverage.DataThrough.Add(-time.Minute)
	repo.matrix.Coverage.CoverageComplete = false
	repo.snapshot.Coverage.Bootstrap = &ChannelMonitorV2Bootstrap{Active: true}
	report, err := p.ChannelStatus(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Groups, 3)
	require.Equal(t, "混合分组（openai）", report.Groups[1].Name)
	require.Equal(t, "混合分组（anthropic）", report.Groups[2].Name)
	require.False(t, report.Groups[1].Metrics.HasRequests)
	require.Equal(t, repo.matrix.Coverage.DataThrough, report.UpdatedAt)
	require.True(t, report.PartialCoverage)
	require.True(t, report.Backfilling)
	metric := qqBotChannelStatusMetric(ChannelMonitorV2Metric{RequestCount: 1, CacheRateDenominator: 5}, false, 50)
	require.NotNil(t, metric.CacheRate)
	require.Zero(t, *metric.CacheRate)
}

func TestQQBotChannelStatusRespectsConfiguredMinimumSamples(t *testing.T) {
	for _, threshold := range []int64{1, 5, 50} {
		for _, count := range []int64{0, threshold - 1, threshold, threshold + 1} {
			p, repo, _ := qqStatusAdapterFixture()
			repo.config.HealthThresholds.MinimumSample = threshold
			metric := ChannelMonitorV2Metric{RequestCount: count, ErrorRequests: count, ErrorRate: 1}
			repo.snapshot.Metrics = metric
			repo.matrix.Items[0].Metrics = metric
			report, err := p.ChannelStatus(context.Background())
			require.NoError(t, err)
			for _, got := range []qqbot.ChannelStatusMetric{report.Overall, report.Groups[0].Metrics} {
				require.Equal(t, count > 0, got.HasRequests)
				require.Equal(t, count > 0 && count < threshold, got.InsufficientSamples)
				require.Equal(t, 1.0, got.ErrorRate) // Preserve the raw statistic.
			}
		}
	}
}

func TestQQBotChannelStatusIncludesAllMonitoredGroupTypes(t *testing.T) {
	p, repo, _ := qqStatusAdapterFixture()
	repo.config.GroupIDs = nil // Empty monitor configuration means all active groups.
	exclusiveID, subscriptionID := int64(2), int64(3)
	repo.matrix.Items = append(repo.matrix.Items,
		ChannelMonitorV2MatrixRow{GroupID: &exclusiveID, Platform: "openai"},
		ChannelMonitorV2MatrixRow{GroupID: &subscriptionID, Platform: "openai"})
	report, err := p.ChannelStatus(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Groups, 3)
	require.Equal(t, "专属分组", report.Groups[1].Name)
	require.Equal(t, "订阅分组", report.Groups[2].Name)
	require.Empty(t, repo.configs[0].GroupIDs)
	require.Equal(t, repo.filters[0], repo.filters[1])
}

func TestQQBotChannelStatusDisabledAndFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*qqBotChannelStatus, *qqStatusRepoStub, *qqStatusGroupStub)
		want   error
	}{
		{"disabled", func(p *qqBotChannelStatus, _ *qqStatusRepoStub, _ *qqStatusGroupStub) {
			p.settings = qqStatusRuntimeStub{}
		}, qqbot.ErrChannelStatusDisabled},
		{"v1", func(p *qqBotChannelStatus, _ *qqStatusRepoStub, _ *qqStatusGroupStub) {
			p.settings = qqStatusRuntimeStub{runtime: ChannelMonitorRuntime{Enabled: true, Mode: ChannelMonitorModeV1}}
		}, qqbot.ErrChannelStatusUnsupported},
		{"config disabled", func(_ *qqBotChannelStatus, r *qqStatusRepoStub, _ *qqStatusGroupStub) { r.config.Enabled = false }, qqbot.ErrChannelStatusDisabled},
		{"nil config", func(_ *qqBotChannelStatus, r *qqStatusRepoStub, _ *qqStatusGroupStub) { r.config = nil }, qqbot.ErrChannelStatusDisabled},
		{"config failure", func(_ *qqBotChannelStatus, r *qqStatusRepoStub, _ *qqStatusGroupStub) { r.fail = "config" }, nil},
		{"groups failure", func(_ *qqBotChannelStatus, _ *qqStatusRepoStub, g *qqStatusGroupStub) {
			g.err = errors.New("groups failed")
		}, nil},
		{"snapshot failure", func(_ *qqBotChannelStatus, r *qqStatusRepoStub, _ *qqStatusGroupStub) { r.fail = "snapshot" }, nil},
		{"matrix failure", func(_ *qqBotChannelStatus, r *qqStatusRepoStub, _ *qqStatusGroupStub) { r.fail = "matrix" }, nil},
		{"nil snapshot", func(_ *qqBotChannelStatus, r *qqStatusRepoStub, _ *qqStatusGroupStub) { r.snapshot = nil }, nil},
		{"nil matrix", func(_ *qqBotChannelStatus, r *qqStatusRepoStub, _ *qqStatusGroupStub) { r.matrix = nil }, nil},
		{"missing settings", func(p *qqBotChannelStatus, _ *qqStatusRepoStub, _ *qqStatusGroupStub) { p.settings = nil }, nil},
		{"missing groups", func(p *qqBotChannelStatus, _ *qqStatusRepoStub, _ *qqStatusGroupStub) { p.groups = nil }, nil},
		{"missing monitor", func(p *qqBotChannelStatus, _ *qqStatusRepoStub, _ *qqStatusGroupStub) { p.monitor = nil }, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, r, g := qqStatusAdapterFixture()
			test.change(p, r, g)
			report, err := p.ChannelStatus(context.Background())
			require.Nil(t, report)
			require.Error(t, err)
			if test.want != nil {
				require.ErrorIs(t, err, test.want)
				require.Empty(t, r.filters)
			}
		})
	}
}

func TestQQBotChannelStatusEmptyActiveScopeNeverQueriesAllGroups(t *testing.T) {
	p, r, g := qqStatusAdapterFixture()
	g.groups = []Group{{ID: 4, Status: StatusDisabled}}
	report, err := p.ChannelStatus(context.Background())
	require.NoError(t, err)
	require.Empty(t, r.filters)
	require.Empty(t, report.Groups)
	require.False(t, report.Overall.HasRequests)
}
