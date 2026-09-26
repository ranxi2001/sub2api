package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestExcelBPSWarmTargetsUseConfiguredConcurrency(t *testing.T) {
	makeAccount := func(n int, source string) Account {
		a := *excelAccount()
		a.Concurrency = n
		a.Status = StatusActive
		a.Schedulable = true
		a.Extra["openai_excel_bps_mihomo"] = true
		a.Extra[ExcelBPSProxySourceKey] = source
		return a
	}
	a := makeAccount(4, "mihomo")
	b := makeAccount(3, "mihomo")
	c := makeAccount(2, ExcelBPSProxySourceIPPool)
	off := makeAccount(90, "mihomo")
	off.Extra["openai_excel_bps_mihomo"] = false
	inactive := makeAccount(90, "mihomo")
	inactive.Status = "disabled"
	paused := makeAccount(90, "mihomo")
	paused.Schedulable = false
	m, s := excelBPSWarmTargets([]Account{a, b, c, off, inactive, paused})
	require.Equal(t, 7, m)
	require.Equal(t, 2, s)
	a.Concurrency = 9
	m, s = excelBPSWarmTargets([]Account{a, c})
	require.Equal(t, 9, m)
	require.Equal(t, 2, s)
	large := makeAccount(100000, "mihomo")
	m, _ = excelBPSWarmTargets([]Account{large, large})
	require.Equal(t, excelBPSWarmMaxTarget, m)
}

type warmLifecycleRepo struct {
	AccountRepository
	started chan struct{}
}

func (r *warmLifecycleRepo) ListByPlatform(ctx context.Context, _ string) ([]Account, error) {
	close(r.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestExcelBPSWarmWorkerStartsBeforeTrafficAndStops(t *testing.T) {
	repo := &warmLifecycleRepo{started: make(chan struct{})}
	svc := &OpenAIGatewayService{accountRepo: repo}
	svc.StartBPSWarmPool()
	svc.StartBPSWarmPool()
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start without a request")
	}
	done := make(chan struct{})
	go func() { svc.StopBPSWarmPool(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	svc.StartBPSWarmPool()
	svc.StopBPSWarmPool()
}
