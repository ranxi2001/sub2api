package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKeyGroupHealthCountsFinalFailuresAndFreshness(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 3, 30, 0, time.UTC)
	through := now.Truncate(time.Minute)
	repo := &monitorV3RepoFake{through: &through, config: ChannelMonitorV3Config{MinRequests: 10, DownErrorRate: .2, DegradedErrorRate: .05, DegradedTTFTMs: 2000, IgnoredErrorCategories: []string{"authentication"}},
		slotFacts: ChannelMonitorV3Facts{
			Metrics: []ChannelMonitorV3Fact{{Slot: through.Add(-time.Minute), GroupID: 1, Success: 9, Errors: 3}, {Slot: through.Add(-time.Minute), GroupID: 2, Errors: 15}},
			Errors:  []ChannelMonitorV3ErrorFact{{Slot: through.Add(-time.Minute), GroupID: 1, Category: "authentication", Errors: 2}},
			Latency: []ChannelMonitorV3LatencyFact{{Slot: through.Add(-time.Minute), GroupID: 1, UpperBound: 1000, Count: 6}, {Slot: through.Add(-time.Minute), GroupID: 1, UpperBound: 5000, Count: 3}},
		}}
	svc := NewChannelMonitorV3Service(repo, nil)
	svc.now = func() time.Time { return now }
	groups := []*Group{{ID: 1}, {ID: 3}}
	result, err := svc.KeyGroupHealth(context.Background(), groups)
	require.NoError(t, err)
	require.True(t, result.Fresh)
	require.Equal(t, through.Add(-5*time.Minute), result.WindowStart)
	require.Equal(t, []int64{1, 3}, repo.slotCalls[0].groups)
	g := result.Groups[0]
	require.EqualValues(t, 10, g.Requests)
	require.EqualValues(t, 1, g.Errors)
	require.InDelta(t, .1, *g.ErrorRate, .00001)
	require.EqualValues(t, 1000, *g.TTFTP50Ms)
	require.Equal(t, ChannelMonitorV3StatusDegraded, g.Status)
	require.Nil(t, result.Groups[1].ErrorRate)
	now = now.Add(10 * time.Minute)
	stale, err := svc.KeyGroupHealth(context.Background(), groups)
	require.NoError(t, err)
	require.False(t, stale.Fresh)
	require.Nil(t, stale.Groups[0].ErrorRate)
	require.Len(t, repo.slotCalls, 1)
}

func TestAiwanwuThinkingRejectionKeepsNextAccountEligible(t *testing.T) {
	a := &Account{Type: AccountTypeAPIKey, Platform: PlatformAnthropic, Credentials: map[string]any{"base_url": "https://www.aiwanwu.cc/v1"}}
	require.True(t, aiwanwuThinkingRoute(a, "claude-opus-5-5"))
	require.False(t, aiwanwuThinkingRoute(a, "claude-sonnet-5-5"))
	a.Credentials["base_url"] = "https://www.aiwanwu.cc.example.org"
	require.False(t, aiwanwuThinkingRoute(a, "claude-opus-5-5"))
	resp := &http.Response{StatusCode: http.StatusBadRequest}
	failure := aiwanwuThinkingFailover(nil, a, resp, []byte(`{"error":{"type":"invalid_request_error","message":"Unsupported field in thinking"}}`))
	require.NotNil(t, failure)
	require.True(t, failure.ShouldRetryNextAccount())
	require.False(t, failure.RetryableOnSameAccount)
	require.False(t, failure.SameAccountRetryOnly())
	require.Nil(t, aiwanwuThinkingFailover(nil, a, resp, []byte(`{"error":{"message":"invalid signature"}}`)))
}
