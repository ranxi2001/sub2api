package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/shopspring/decimal"
	"strconv"
	"time"
)

// SetAutoCredit is construction-time wiring; the cache worker only retries
// invalidation, never the monetary operation. Stop joins it with other workers.
func (s *APIKeyService) SetAutoCredit(repo AutoCreditRepository, billing *BillingCacheService) {
	s.autoCredit = repo
	s.autoCreditBilling = billing
	if billing != nil && repo != nil {
		billing.autoCredit = repo
		billing.autoCreditStop = make(chan struct{})
		billing.cacheWriteWg.Add(1)
		go func() {
			defer billing.cacheWriteWg.Done()
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-billing.autoCreditStop:
					return
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					_ = billing.RecoverAutoCreditInvalidations(ctx)
					cancel()
				}
			}
		}()
	}
}

func (s *BillingCacheService) RecoverAutoCreditInvalidations(ctx context.Context) error {
	if s.autoCredit == nil {
		return nil
	}
	events, err := s.autoCredit.PendingInvalidations(ctx)
	if err != nil {
		return err
	}
	for _, e := range events {
		if err = s.InvalidateUserBalance(ctx, e.UserID); err != nil {
			return err
		}
		if err = s.autoCredit.AcknowledgeInvalidation(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

func (s *APIKeyService) RecoverAutoCreditAdmission(ctx context.Context, key *APIKey) (bool, error) {
	if s == nil || s.autoCredit == nil || key == nil || key.User == nil || !key.User.IsActive() || !key.IsActive() || key.IsExpired() || key.IsQuotaExhausted() {
		return false, nil
	}
	// Regional replicas are permission snapshots, never a monetary authority.
	// Do not use the primary pool or mutate replica balances behind that boundary.
	if s.localAuth != nil || s.cfg != nil && s.cfg.Runtime.Role == config.RuntimeRoleGateway && s.cfg.Runtime.RegionalAuthFile != "" {
		return false, nil
	}
	result, err := s.autoCredit.EvaluateAdmission(ctx, key.UserID, key.ID)
	if err != nil {
		return false, err
	}
	if result.Issued {
		if s.autoCreditBilling != nil {
			if err = s.autoCreditBilling.InvalidateUserBalance(ctx, key.UserID); err != nil {
				return false, err
			}
		}
		if err = s.InvalidateAuthCacheByUserIDChecked(ctx, key.UserID); err != nil {
			return false, err
		}
	}
	balance, err := decimal.NewFromString(result.Balance)
	if err != nil {
		return false, err
	}
	return result.Issued || balance.IsPositive(), nil
}

// Read-only selection predicate: preserve subscription fallback for non-VIPs,
// spent epochs and cap-blocked users. Mutation only follows full group selection.
func (s *APIKeyService) CanRecoverAutoCreditAdmission(ctx context.Context, key *APIKey) (bool, error) {
	if s == nil || s.autoCredit == nil || key == nil || key.User == nil || !key.User.IsActive() || !key.IsActive() || key.IsExpired() || key.IsQuotaExhausted() {
		return false, nil
	}
	if s.localAuth != nil || s.cfg != nil && s.cfg.Runtime.Role == config.RuntimeRoleGateway {
		return false, nil
	}
	minimum := 0.0
	if s.cfg != nil && s.cfg.Billing.MinimumBalanceReserve > 0 {
		minimum = s.cfg.Billing.MinimumBalanceReserve
	}
	return s.autoCredit.CanRecover(ctx, key.UserID, strconv.FormatFloat(minimum, 'f', -1, 64))
}
