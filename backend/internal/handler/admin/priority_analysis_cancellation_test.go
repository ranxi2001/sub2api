package admin

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type analysisHTTPSettings struct{ service.SettingRepository }

func (*analysisHTTPSettings) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}

type analysisHTTPAccounts struct{ service.AccountRepository }

func (*analysisHTTPAccounts) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]service.Account, error) {
	return []service.Account{{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}}, nil
}

type analysisHTTPHistory struct {
	service.UsageLogRepository
	entered chan context.Context
	exited  chan struct{}
}

func (r *analysisHTTPHistory) ReadPrioritySchedulingSignals(ctx context.Context, _ service.PrioritySchedulingQuery) (map[int64]service.PrioritySchedulingSignal, error) {
	r.entered <- ctx
	<-ctx.Done()
	close(r.exited)
	return nil, ctx.Err()
}
func (*analysisHTTPHistory) ReadPriorityLatencySeries(ctx context.Context, _ service.PriorityLatencySeriesQuery) ([]service.PriorityLatencyBucket, error) {
	return nil, ctx.Err()
}
func TestPriorityAnalysisHTTPCancellationReachesRepository(t *testing.T) {
	history := &analysisHTTPHistory{entered: make(chan context.Context, 1), exited: make(chan struct{})}
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	settings := service.NewSettingService(&analysisHTTPSettings{}, nil)
	gateway := service.NewOpenAIGatewayService(&analysisHTTPAccounts{}, nil, history, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil)
	defer gateway.CloseOpenAIWSPool()
	h := &AccountHandler{openAIGatewayService: gateway}
	router := gin.New()
	router.GET("/analysis", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
		c.Set("auth_method", "jwt")
		h.PrioritySchedulingAnalysis(c)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("GET", "/analysis?model=fixture&minutes=15", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { router.ServeHTTP(httptest.NewRecorder(), request); close(done) }()
	var work context.Context
	select {
	case work = <-history.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("real handler did not reach repository")
	}
	require.NoError(t, work.Err())
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler ignored request cancellation")
	}
	select {
	case <-history.exited:
	case <-time.After(time.Second):
		t.Fatal("HTTP context cancellation did not reach service repository")
	}
	require.ErrorIs(t, work.Err(), context.Canceled)
}
