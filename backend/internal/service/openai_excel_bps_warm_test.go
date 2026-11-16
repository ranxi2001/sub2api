package service

import (
	"context"
	"errors"
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

func TestExcelBPSWarmRequestRecoveryDoesNotMutateAccount(t *testing.T) {
	account := recoveryTestAccount(time.Now())
	account.Concurrency = 6
	account.Extra[ExcelBPSProxyModeKey] = ExcelBPSProxyModeRequest
	account.Extra[ExcelBPSProxyIDsKey] = []int64{11, 22}
	projection, recovery := effectiveExcelBPSWarmAccount(account)
	require.True(t, recovery)
	require.True(t, projection.ExcelBPSRequestProxyEnabled())
	require.Equal(t, []int64{11, 22}, projection.ExcelBPSRequestProxyIDs())
	require.Equal(t, 6, projection.Concurrency, "group projection must retain account settings")
	managed, static := excelBPSWarmTargets([]Account{*account})
	require.Zero(t, managed)
	require.Equal(t, 1, static, "recovery target only needs one healthy exit")
	require.False(t, account.IsExcelBPSEnabled(), "warming cannot re-enable the stored account")
	require.Equal(t, 6, account.Concurrency)
}

type warmProjectionRepo struct {
	AccountRepository
	projectionCalls int
	legacyCalls     int
}

func (r *warmProjectionRepo) ListBPSWarmAccounts(context.Context) ([]Account, error) {
	r.projectionCalls++
	return nil, errors.New("projection unavailable")
}

func (r *warmProjectionRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	r.legacyCalls++
	return nil, errors.New("full hydration must not be retried")
}

func TestBPSWarmUsesProjectionWithoutFullHydrationFallback(t *testing.T) {
	repo := &warmProjectionRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	svc.syncBPSWarmPool(context.Background())
	require.Equal(t, 1, repo.projectionCalls)
	require.Zero(t, repo.legacyCalls)
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
