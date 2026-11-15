package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/stretchr/testify/require"
)

type qqPriceChannelStub struct {
	channels []Channel
	err      error
}

func (s *qqPriceChannelStub) ListAll(context.Context) ([]Channel, error) { return s.channels, s.err }

type qqPriceGroupStub struct {
	groups []Group
	err    error
	failAt int
	params []pagination.PaginationParams
}

func (s *qqPriceGroupStub) List(_ context.Context, params pagination.PaginationParams) ([]Group, *pagination.PaginationResult, error) {
	s.params = append(s.params, params)
	if s.err != nil && (s.failAt == 0 || s.failAt == params.Page) {
		return nil, nil, s.err
	}
	start := min(params.Offset(), len(s.groups))
	end := min(start+params.Limit(), len(s.groups))
	return s.groups[start:end], &pagination.PaginationResult{Total: int64(len(s.groups))}, nil
}

func priceField(t *testing.T, fields []qqbot.PriceField, key string) qqbot.PriceField {
	t.Helper()
	for _, f := range fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("missing field %s", key)
	return qqbot.PriceField{}
}

func TestQQPriceSnapshotUnitsAndTimeBoundaries(t *testing.T) {
	input, perRequest := 0.000002, 0.05
	at := time.Date(2026, 10, 20, 0, 59, 59, 0, time.UTC)
	pricing := ChannelModelPricing{Platform: "openai", Models: []string{"example"}, InputPrice: &input, PerRequestPrice: &perRequest, TimePricing: &ChannelTimePricing{Timezone: "Asia/Shanghai", WeekdaysOnly: true, Periods: []ChannelTimePricingPeriod{{StartTime: "09:00:00", EndTime: "09:00:01", Multiplier: .5}}}}
	p := qqBotPriceSource{channels: &qqPriceChannelStub{channels: []Channel{{ID: 1, Status: StatusActive, Name: "channel", ModelPricing: []ChannelModelPricing{pricing}}, {ID: 2, Status: StatusDisabled}}}, groups: &qqPriceGroupStub{groups: []Group{{ID: 1, Name: "group", Status: StatusActive, RateMultiplier: .8, SubscriptionType: SubscriptionTypeSubscription, PeakRateEnabled: true, PeakStart: "09:00", PeakEnd: "10:00", PeakRateMultiplier: 2}}}}
	before, err := p.PriceSnapshot(context.Background(), at)
	require.NoError(t, err)
	require.Len(t, before.States, 3)
	require.Equal(t, StatusActive, before.States[0].Status)
	require.Equal(t, StatusDisabled, before.States[1].Status)
	require.Equal(t, "USD 2", priceField(t, before.States[0].Fields, "openai/example/01_input").Value)
	require.Equal(t, "USD 0.05", priceField(t, before.States[0].Fields, "openai/example/08_request").Value)
	require.WithinDuration(t, at.Add(time.Second), before.NextCheck, time.Nanosecond)
	inside, err := p.PriceSnapshot(context.Background(), at.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, "0.5×", priceField(t, inside.States[0].Fields, "openai/example/31_time_current").Value)
	require.WithinDuration(t, at.Add(2*time.Second), inside.NextCheck, time.Nanosecond)
	after, err := p.PriceSnapshot(context.Background(), at.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, "1×", priceField(t, after.States[0].Fields, "openai/example/31_time_current").Value)
	saturday := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	weekend, err := p.PriceSnapshot(context.Background(), saturday)
	require.NoError(t, err)
	require.Equal(t, "1×", priceField(t, weekend.States[0].Fields, "openai/example/31_time_current").Value)
}

func TestQQPriceSnapshotIgnoresInternalCostAndRowIDs(t *testing.T) {
	price := 1e-6
	pricing := ChannelModelPricing{Platform: "openai", Models: []string{"example"}, InputPrice: &price}
	repo := &qqPriceChannelStub{channels: []Channel{{ID: 1, Name: "channel", Status: StatusActive, ModelPricing: []ChannelModelPricing{pricing}}}}
	p := qqBotPriceSource{channels: repo, groups: &qqPriceGroupStub{}}
	at := time.Now()
	before, err := p.PriceSnapshot(context.Background(), at)
	require.NoError(t, err)
	repo.channels[0].ModelPricing[0].ID = 999
	repo.channels[0].ModelPricing[0].UpdatedAt = time.Now()
	repo.channels[0].AccountStatsPricingRules = []AccountStatsPricingRule{{Name: "internal cost", Pricing: []ChannelModelPricing{{InputPrice: &price}}}}
	after, err := p.PriceSnapshot(context.Background(), at)
	require.NoError(t, err)
	require.Equal(t, before, after)
	repo.err = errors.New("unavailable")
	_, err = p.PriceSnapshot(context.Background(), at)
	require.Error(t, err)
}

func TestQQNextBoundaryMidnightAndGroupPeak(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	at := time.Date(2026, 10, 23, 23, 59, 59, 500000000, loc)
	require.Equal(t, time.Date(2026, 10, 24, 0, 0, 0, 0, loc), qqNextBoundary(at, at.Add(15*time.Second), loc, "00:00"))
	group := Group{ID: 1, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription, RateMultiplier: .8, PeakRateEnabled: true, PeakStart: "09:00", PeakEnd: "10:00", PeakRateMultiplier: 2}
	p := qqBotPriceSource{channels: &qqPriceChannelStub{}, groups: &qqPriceGroupStub{groups: []Group{group}}}
	for _, now := range []time.Time{at, time.Date(2026, 10, 20, 9, 0, 0, 0, loc), time.Date(2026, 10, 20, 10, 0, 0, 0, loc)} {
		got, err := p.PriceSnapshot(context.Background(), now)
		require.NoError(t, err)
		require.Equal(t, qqMultiplier(group.RateMultiplier*group.PeakMultiplierAt(now)), priceField(t, got.States[0].Fields, "05_current").Value)
	}
}

func TestQQPriceSnapshotIncludesInactiveGroupsAcrossPages(t *testing.T) {
	groups := make([]Group, 1001)
	for i := range groups {
		groups[i] = Group{ID: int64(i + 1), Name: "group", Status: StatusDisabled, RateMultiplier: .8}
	}
	repo := &qqPriceGroupStub{groups: groups}
	p := qqBotPriceSource{channels: &qqPriceChannelStub{}, groups: repo}
	got, err := p.PriceSnapshot(context.Background(), time.Now())
	require.NoError(t, err)
	require.Len(t, got.States, len(groups))
	require.Len(t, repo.params, 2)
	for _, params := range repo.params {
		require.Equal(t, "id", params.SortBy)
		require.Equal(t, pagination.SortOrderAsc, params.SortOrder)
	}
	require.Equal(t, "group:1001", got.States[1000].Key)
	require.Equal(t, StatusDisabled, got.States[1000].Status)
	require.Equal(t, "0.8×", priceField(t, got.States[1000].Fields, "01_text").Value)

	// A partial read must fail instead of making later pages look deleted.
	repo.failAt, repo.err = 2, errors.New("page unavailable")
	got, err = p.PriceSnapshot(context.Background(), time.Now())
	require.ErrorIs(t, err, repo.err)
	require.Empty(t, got.States)
}

func TestQQPriceSnapshotIncludesGroupModelPricing(t *testing.T) {
	input := 0.000002
	group := Group{
		ID:             7,
		Name:           "group",
		Platform:       PlatformOpenAI,
		Status:         StatusActive,
		RateMultiplier: 1,
		ModelPricing: []ChannelModelPricing{{
			Models:     []string{"gpt-5"},
			InputPrice: &input,
		}},
	}
	p := qqBotPriceSource{channels: &qqPriceChannelStub{}, groups: &qqPriceGroupStub{groups: []Group{group}}}

	snapshot, err := p.PriceSnapshot(context.Background(), time.Now())
	require.NoError(t, err)
	require.Len(t, snapshot.States, 1)
	field := priceField(t, snapshot.States[0].Fields, "model/openai/gpt-5/01_input")
	require.Equal(t, "gpt-5（openai）", field.Model)
	require.Equal(t, "USD 2", field.Value)
}
