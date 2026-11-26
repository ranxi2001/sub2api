package admin

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type issue603LatencyRepo struct {
	service.OpsRepository
}

func (r *issue603LatencyRepo) InsertNetworkSample(context.Context, service.OpsNetworkPoint) error {
	return nil
}
func (r *issue603LatencyRepo) ReadNetworkSeries(context.Context, time.Time, time.Time) (*service.OpsNetworkSeries, error) {
	return &service.OpsNetworkSeries{Points: []service.OpsNetworkPoint{{EgressStatus: "unavailable", TCPStatus: "no_segments", QdiscStatus: "unsupported_unprivileged_collector"}}}, nil
}

func TestIssue603RealUsageAndOpsHandlers(t *testing.T) {
	stub := &issue603LatencyRepo{}
	ops := NewOpsHandler(service.NewOpsService(stub, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/ops/network-series", ops.NetworkSeries)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/ops/network-series", nil))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"egress_mbps":null`)
	require.Contains(t, w.Body.String(), "unsupported_unprivileged_collector")
}
