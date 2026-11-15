package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/qqbot"
)

func (p *qqBotChannelStatus) HealthSnapshot(ctx context.Context, now time.Time) (qqbot.HealthSnapshot, error) {
	out := qqbot.HealthSnapshot{}
	runtime := p.settings.GetChannelMonitorRuntime(ctx)
	if !runtime.Enabled || !runtime.PassiveAggregationAllowed() {
		return out, nil
	}
	cfg, err := p.monitor.getEnabledConfig(ctx)
	if err != nil {
		return out, err
	}
	groups, err := p.groups.ListActive(ctx)
	if err != nil {
		return out, err
	}
	filter := ChannelMonitorV2Filter{Range: "5m", Start: now.UTC().Add(-5 * time.Minute).Truncate(time.Minute), End: now.UTC().Truncate(time.Minute), Bucket: time.Minute, RestrictGroups: true, AllowedGroupIDs: []int64{}}
	names := map[int64]string{}
	for _, g := range groups {
		if g.ID > 0 && g.IsActive() {
			filter.AllowedGroupIDs = append(filter.AllowedGroupIDs, g.ID)
			names[g.ID] = g.Name
		}
	}
	if len(names) == 0 {
		return out, nil
	}
	matrix, err := p.monitor.repo.GetMatrix(ctx, filter, *cfg, ChannelMonitorV2GroupByPlatformGroup, false)
	if err != nil {
		return out, err
	}
	if matrix == nil {
		return out, fmt.Errorf("empty health matrix")
	}
	out.UpdatedAt = matrix.Coverage.DataThrough
	out.Complete = matrix.Coverage.CoverageComplete && (matrix.Coverage.Bootstrap == nil || !matrix.Coverage.Bootstrap.Active)
	for _, row := range matrix.Items {
		if row.GroupID == nil {
			continue
		}
		name, ok := names[*row.GroupID]
		if !ok {
			continue
		}
		out.Groups = append(out.Groups, qqbot.HealthMetric{Key: fmt.Sprintf("%d:%s", *row.GroupID, row.Platform), Name: name + "（" + row.Platform + "）", Requests: row.Metrics.RequestCount, ErrorRate: row.Metrics.ErrorRate, TTFTMs: row.Metrics.TTFT.P50Ms})
	}
	return out, nil
}
