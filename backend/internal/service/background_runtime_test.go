package service

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestGatewayRoleDoesNotStartGlobalMonitorOrSchedule(t *testing.T) {
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	// Empty dependencies would fail if either provider performed its startup DB scan.
	monitor := ProvideChannelMonitorRunner(&ChannelMonitorService{}, &SettingService{}, nil, cfg)
	require.False(t, monitor.started)
	monitor.Stop()
	runner := ProvideScheduledTestRunnerService(nil, nil, nil, nil, cfg, &QualityJudgeService{}, nil, &PelicanGroupTestService{}, &ChannelMonitorV2Service{}, nil)
	require.Nil(t, runner.cron)
	runner.Stop()
	expiry := ProvideAccountExpiryService(nil, cfg)
	expiry.Stop()
}

type gatewayAccountOpsRepo struct {
	AccountOpsRepository
	recorded chan AccountOpsEvent
}

func (r *gatewayAccountOpsRepo) SuppressDisabled(context.Context, AccountOpsConfig) error { return nil }
func (r *gatewayAccountOpsRepo) Record(_ context.Context, event AccountOpsEvent) error {
	r.recorded <- event
	return nil
}

func TestGatewayRoleStillPersistsRequestObservations(t *testing.T) {
	c := defaultAccountOpsConfig()
	c.Enabled = true
	c.Recipient = "ops@example.com"
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	repo := &gatewayAccountOpsRepo{recorded: make(chan AccountOpsEvent, 1)}
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	svc := ProvideAccountOpsService(&accountOpsSettingsStub{raw: string(raw)}, repo, nil, nil, nil, cfg, nil, nil, nil, nil, nil)
	defer svc.Stop()
	_, err = svc.GetConfig(context.Background())
	require.NoError(t, err)
	headers := http.Header{}
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-window-minutes", "10080")
	svc.Observe(&Account{ID: 7, Platform: PlatformOpenAI}, 429, headers, nil)
	select {
	case event := <-repo.recorded:
		require.Equal(t, int64(7), event.AccountID)
		require.Equal(t, "weekly_quota", event.Kind)
	case <-time.After(time.Second):
		t.Fatal("gateway must drain its local account event queue")
	}
}

func TestGatewayRoleStillDrainsOllamaRequestProbeWakeups(t *testing.T) {
	cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	svc := ProvideOllamaCloudUsageService(nil, nil, nil, nil, cfg, nil, nil)
	defer svc.Stop()
	svc.probeWake <- struct{}{}
	require.Eventually(t, func() bool { return len(svc.probeWake) == 0 }, time.Second, time.Millisecond)
}
