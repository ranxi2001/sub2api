package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProfitOperationsCostMultiplierSourcesAndIndependentValidation(t *testing.T) {
	for _, tc := range []struct {
		raw   any
		want  *float64
		basis string
	}{{nil, profitPtr(.1), "current_default_0.1"}, {0.0, profitPtr(0), "current_saved_multiplier"}, {.25, profitPtr(.25), "current_saved_multiplier"}, {"bad", nil, "invalid_multiplier"}, {-1, nil, "invalid_multiplier"}} {
		v, b := OperationsCostMultiplier(&Account{Extra: map[string]any{"cost_multiplier": tc.raw}})
		require.Equal(t, tc.want, v)
		require.Equal(t, tc.basis, b)
	}
	now := time.Now()
	f := UpstreamOperationsFilter{From: now.Add(-time.Hour), Until: now, Dimension: "upstream", ModelScope: "public", Sort: "estimated_profit", Direction: "desc", Page: 1, PageSize: 20}
	require.NoError(t, ValidateProfitOperationsFilter(f))
	require.Error(t, ValidateUpstreamOperationsFilter(f), "financial sorting is confined to profit API")
	f.Sort = "name;DROP TABLE usage_logs"
	require.Error(t, ValidateProfitOperationsFilter(f))
}
func profitPtr(v float64) *float64 { return &v }

func TestProfitOperationsCalendarMonthWindow(t *testing.T) {
	// October has 31 days plus a repeated hour in a UTC+2 to UTC+1 transition.
	from := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.FixedZone("summer", 2*3600))
	until := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.FixedZone("winter", 3600))
	f := UpstreamOperationsFilter{From: from, Until: until, Dimension: "upstream", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	require.Equal(t, 31*24*time.Hour+time.Hour, until.Sub(from))
	require.NoError(t, ValidateProfitOperationsFilter(f), "profit presets must accept a full local calendar month")
	require.Error(t, ValidateUpstreamOperationsFilter(f), "the legacy upstream API keeps its existing 31-day limit")
	f.From = f.Until.Add(-32 * 24 * time.Hour)
	require.NoError(t, ValidateProfitOperationsFilter(f))
	f.From = f.From.Add(-time.Nanosecond)
	require.Error(t, ValidateProfitOperationsFilter(f), "profit queries remain bounded to 32 days")
}

type profitOperationsDeadlineRepo struct {
	ChannelRepository
	read func(context.Context, UpstreamOperationsFilter) (*ProfitOperationsSnapshot, error)
}

func (r *profitOperationsDeadlineRepo) ReadProfitOperations(ctx context.Context, f UpstreamOperationsFilter) (*ProfitOperationsSnapshot, error) {
	return r.read(ctx, f)
}

func TestProfitOperationsQueryBudgetPreservesParentDeadline(t *testing.T) {
	now := time.Now()
	f := UpstreamOperationsFilter{From: now.Add(-24 * time.Hour), Until: now, Dimension: "upstream", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	want := &ProfitOperationsSnapshot{}
	var queryDeadline time.Time
	repo := &profitOperationsDeadlineRepo{read: func(ctx context.Context, got UpstreamOperationsFilter) (*ProfitOperationsSnapshot, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		queryDeadline = deadline
		require.Equal(t, f, got)
		return want, nil
	}}
	s := NewChannelService(repo, nil, nil, nil, nil)
	started := time.Now()
	got, err := s.ProfitOperations(context.Background(), f)
	require.NoError(t, err)
	require.Same(t, want, got)
	require.WithinDuration(t, started.Add(5*time.Minute), queryDeadline, time.Second, "large reports receive five minutes instead of the old 25-second deadline")
	parentDeadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), parentDeadline)
	defer cancel()
	_, err = s.ProfitOperations(ctx, f)
	require.NoError(t, err)
	require.Equal(t, parentDeadline, queryDeadline, "a shorter caller deadline must still cancel expensive reads")
}

func TestProfitOperationsNormalizesDriverCancellation(t *testing.T) {
	now := time.Now()
	f := UpstreamOperationsFilter{From: now.Add(-time.Hour), Until: now, Dimension: "upstream", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	driverError := errors.New("driver canceled query")
	repo := &profitOperationsDeadlineRepo{read: func(context.Context, UpstreamOperationsFilter) (*ProfitOperationsSnapshot, error) {
		return nil, driverError
	}}
	s := NewChannelService(repo, nil, nil, nil, nil)
	_, err := s.ProfitOperations(context.Background(), f)
	require.ErrorIs(t, err, driverError, "ordinary repository failures keep their identity")
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), now.Add(-time.Second))
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			defer cancel()
			out, err := s.ProfitOperations(ctx, f)
			require.Nil(t, out)
			require.ErrorIs(t, err, want, "a driver-specific cancellation must expose the request context cause")
		})
	}
}
