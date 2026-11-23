package service

import (
	"context"
	"time"
)

type KeyGroupHealth struct {
	GeneratedAt time.Time             `json:"generated_at"`
	DataThrough *time.Time            `json:"data_through"`
	WindowStart time.Time             `json:"window_start"`
	WindowEnd   time.Time             `json:"window_end"`
	Fresh       bool                  `json:"fresh"`
	Groups      []KeyGroupHealthEntry `json:"groups"`
}

type KeyGroupHealthEntry struct {
	GroupID   int64    `json:"group_id"`
	Name      string   `json:"group_name"`
	Status    string   `json:"status"`
	Requests  int64    `json:"requests"`
	Errors    int64    `json:"errors"`
	ErrorRate *float64 `json:"error_rate"`
	TTFTP50Ms *int64   `json:"ttft_p50_ms"`
}

// KeyGroupHealth reads five completed minutes from the passive monitor.
// Never report an operational group from a stalled aggregation watermark.
func (s *ChannelMonitorV3Service) KeyGroupHealth(ctx context.Context, groups []*Group) (*KeyGroupHealth, error) {
	cfg, err := s.repo.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	through, err := s.repo.DataThrough(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	end := now.Truncate(time.Minute)
	if through != nil && through.Before(end) {
		end = through.UTC().Truncate(time.Minute)
	}
	start := end.Add(-5 * time.Minute)
	fresh := through != nil && !through.After(now) && now.Sub(*through) <= 5*time.Minute
	out := &KeyGroupHealth{GeneratedAt: now, DataThrough: through, WindowStart: start, WindowEnd: end, Fresh: fresh, Groups: make([]KeyGroupHealthEntry, 0, len(groups))}
	ids := make([]int64, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	var facts *ChannelMonitorV3Facts
	if fresh {
		facts, err = s.repo.SlotFacts(ctx, ids, start, end, 5*time.Minute, true)
		if err != nil {
			return nil, err
		}
	}
	for _, group := range groups {
		entry := KeyGroupHealthEntry{GroupID: group.ID, Name: group.Name, Status: ChannelMonitorV3StatusUnknown}
		acc := channelMonitorV3Accumulate(*cfg, ChannelMonitorV3Component{GroupID: group.ID}, facts, time.Time{}, 0)[0]
		entry.Requests, entry.Errors = acc.counted(), acc.errors
		if entry.Requests > 0 {
			rate := float64(entry.Errors) / float64(entry.Requests)
			entry.ErrorRate = &rate
			entry.TTFTP50Ms = channelMonitorV3P50(acc.ttft)
			entry.Status = ChannelMonitorV3StatusInsufficient
			if entry.Requests >= int64(cfg.MinRequests) {
				entry.Status = channelMonitorV3Judge(*cfg, rate, entry.TTFTP50Ms, cfg.DegradedTTFTMs)
			}
		}
		out.Groups = append(out.Groups, entry)
	}
	return out, nil
}
