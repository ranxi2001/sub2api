package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const excelBPSWarmSyncInterval = 5 * time.Second
const excelBPSWarmMaxTarget = 4096

// BPSWarmAccountRepository avoids hydrating credentials, proxies and groups for
// the periodic pool-sizing pass. Implementations must retain eligibility flags.
type BPSWarmAccountRepository interface {
	ListBPSWarmAccounts(context.Context) ([]Account, error)
}

// Recovery probes remain eligible for warming without mutating the repository
// account or re-enabling it for normal scheduling.
func effectiveExcelBPSWarmAccount(account *Account) (*Account, bool) {
	if !account.IsExcelBPS403RecoveryPending() {
		return account, false
	}
	probe := *account
	probe.Extra = cloneExcelBPSRecoveryExtra(account.Extra)
	probe.Extra["openai_excel_bps"] = true
	return &probe, true
}

func excelBPSWarmTargets(accounts []Account) (managed, static int) {
	for i := range accounts {
		a, recovery := effectiveExcelBPSWarmAccount(&accounts[i])
		// Keep one healthy exit available for opted-in disabled accounts, even
		// when no production account still uses their proxy pool.
		if recovery {
			a.Concurrency = 1
		}
		if !a.IsActive() || !a.Schedulable || (!a.IsExcelBPSMihomoEnabled() && !a.OpenAIRequestProxyEnabled()) {
			continue
		}
		target := max(1, min(a.Concurrency, excelBPSWarmMaxTarget))
		if a.OpenAIRequestProxyEnabled() || a.ExcelBPSProxySource() == ExcelBPSProxySourceIPPool {
			static = min(excelBPSWarmMaxTarget, static+target)
		} else {
			managed = min(excelBPSWarmMaxTarget, managed+target)
		}
	}
	return
}

// Started explicitly by application wiring, so warming begins before requests
// and unit constructors do not unexpectedly start network/database workers.
func (s *OpenAIGatewayService) StartBPSWarmPool() {
	if s == nil || s.accountRepo == nil {
		return
	}
	s.excelBPSWarmMu.Lock()
	defer s.excelBPSWarmMu.Unlock()
	if s.excelBPSWarmStopped || s.excelBPSWarmCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.excelBPSWarmCancel = cancel
	s.excelBPSWarmDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(excelBPSWarmSyncInterval)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			s.safeSyncBPSWarmPool(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (s *OpenAIGatewayService) StopBPSWarmPool() {
	if s == nil {
		return
	}
	s.excelBPSWarmMu.Lock()
	s.excelBPSWarmStopped = true
	cancel, done := s.excelBPSWarmCancel, s.excelBPSWarmDone
	s.excelBPSWarmMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
func (s *OpenAIGatewayService) syncBPSWarmPool(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	var accounts []Account
	var err error
	if repo, ok := s.accountRepo.(BPSWarmAccountRepository); ok {
		accounts, err = repo.ListBPSWarmAccounts(queryCtx)
	} else {
		// Compatibility for alternate repositories; the production repository
		// implements the narrow projection. Never retry a projection error here.
		accounts, err = s.accountRepo.ListByPlatform(queryCtx, PlatformOpenAI)
	}
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			logger.FromContext(ctx).Warn("excel_bps.warm_account_refresh_failed")
		}
		return
	}
	managed, static := excelBPSWarmTargets(accounts)
	groups := make(map[int64][]int64)
	for i := range accounts {
		a, _ := effectiveExcelBPSWarmAccount(&accounts[i])
		if a.IsActive() && a.Schedulable && a.OpenAIRequestProxyEnabled() {
			groups[a.ID] = a.OpenAIRequestProxyIDs()
		}
	}
	mihomo.SetBPSStaticRequestGroups(groups)
	if static > 0 {
		proxyCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		s.refreshExcelBPSIPPool(proxyCtx)
		stop()
	}
	logger.FromContext(ctx).Debug("excel_bps.warm_pool_target", zap.Int("mihomo", managed), zap.Int("ip_pool", static))
	mihomo.WarmBPSPools(ctx, managed, static)
}

// A maintenance failure must not crash the request-serving process.
func (s *OpenAIGatewayService) safeSyncBPSWarmPool(ctx context.Context) {
	defer func() {
		if recover() != nil {
			logger.FromContext(ctx).Error("excel_bps.warm_pool_refresh_panicked")
		}
	}()
	s.syncBPSWarmPool(ctx)
}
