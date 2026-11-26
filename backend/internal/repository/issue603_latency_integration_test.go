//go:build integration

package repository

import (
	"context"

	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestIssue603NetworkMinuteStorageAndBodySQL(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	now := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	instance := uuid.NewString()
	zero := 0.0
	drops := int64(3)
	for i := range 2 {
		p := service.OpsNetworkPoint{At: now.Add(time.Duration(i) * time.Minute), Instance: instance, EgressStatus: "unavailable", TCPStatus: "no_segments", QdiscStatus: "unsupported_unprivileged_collector", WindowSeconds: 60}
		if i == 1 {
			p.EgressMbps = &zero
			p.EgressStatus = "measured"
			p.QdiscDrops = &drops
			p.QdiscStatus = "measured_root_egress_qdisc"
		}
		require.NoError(t, insertNetworkSample(ctx, tx, p))
	}
	series, err := readNetworkSeries(ctx, tx, now, now.Add(2*time.Minute))
	require.NoError(t, err)
	var selected []service.OpsNetworkPoint
	for _, p := range series.Points {
		if p.Instance == instance {
			selected = append(selected, p)
		}
	}
	require.Len(t, selected, 2)
	require.Nil(t, selected[0].EgressMbps)
	require.NotNil(t, selected[1].EgressMbps)
	require.Equal(t, 0.0, *selected[1].EgressMbps)
	require.Nil(t, selected[0].QdiscDrops)
	require.Contains(t, selected[0].QdiscStatus, "unsupported")
	require.NotNil(t, selected[1].QdiscDrops)
	require.EqualValues(t, 3, *selected[1].QdiscDrops, "measured root qdisc drop delta must survive minute storage and aggregation")
	require.Equal(t, "measured_root_egress_qdisc", selected[1].QdiscStatus)
	require.Empty(t, series.BodyPoints)
}
