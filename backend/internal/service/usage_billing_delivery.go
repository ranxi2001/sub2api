package service

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

var ErrUsageBillingOutcomeUnknown = errors.New("usage billing outcome is unknown; durable state must be checked")
var ErrUsageBillingJournalRetired = errors.New("legacy billing journal retired")
var ErrUsageBillingEventVersion = errors.New("unsupported usage billing event version")

type UsageBillingEventKey struct {
	RequestID string
	APIKeyID  int64
}

// The delivery snapshot contains only billing metadata, never relation objects
// (which may contain API keys, account credentials or customer profile data).
type UsageBillingEvent struct {
	Version int                 `json:"version"`
	Command UsageBillingCommand `json:"command"`
	Usage   UsageLog            `json:"usage"`
}

type UsageBillingRecovery struct {
	Event      UsageBillingEvent
	Result     UsageBillingApplyResult
	NeedsCache bool
	// Settled proves that this journal event committed its own canonical usage
	// and effects. Applied describes whether this call performed those effects.
	Settled bool
}

// Stage is a separate durable boundary before monetary execution. A crash after
// it commits can be recovered without the handler closure or another model call.
type DurableUsageBillingRepository interface {
	StageUsageBilling(context.Context, *UsageBillingCommand, *UsageLog) error
	ApplyStagedUsageBilling(context.Context, UsageBillingEventKey) (*UsageBillingRecovery, error)
	PendingUsageBilling(context.Context, int) ([]UsageBillingEventKey, error)
	AcknowledgeUsageBillingCache(context.Context, UsageBillingEventKey) error
}

// UsageSettlementRepository uses the upstream claim and the usage ledger in a single transaction.
type UsageSettlementRepository interface {
	ApplyUsage(context.Context, *UsageBillingCommand, *UsageLog) (*UsageBillingRecovery, error)
}

type imageBillingStageRepository interface {
	// True only if this call created the journal event and confirmed its commit.
	StageUsageBillingForReceipt(context.Context, *UsageBillingCommand, *UsageLog) (bool, error)
}

func (s *GatewayService) DurableUsageBillingEnabled() bool {
	if s == nil {
		return false
	}
	_, ok := s.usageBillingRepo.(DurableUsageBillingRepository)
	return ok
}

func (s *OpenAIGatewayService) DurableUsageBillingEnabled() bool {
	if s == nil || s.regionalMetering != nil {
		return false
	}
	_, ok := s.usageBillingRepo.(DurableUsageBillingRepository)
	return ok
}

// Invalidations can safely be repeated after a crash. Never replay a Redis
// monetary delta when the caller cannot know whether it was applied previously.
func invalidateDurableUsageBilling(ctx context.Context, recovered *UsageBillingRecovery, cache *BillingCacheService, auth APIKeyAuthCacheInvalidator) error {
	if recovered == nil || !recovered.NeedsCache {
		return nil
	}
	e := &recovered.Event
	var err error
	if cache != nil {
		if e.Command.BalanceCost > 0 {
			err = errors.Join(err, cache.InvalidateUserBalance(ctx, e.Command.UserID))
		}
		if e.Command.SubscriptionCost > 0 && e.Usage.GroupID != nil {
			err = errors.Join(err, cache.InvalidateSubscription(ctx, e.Command.UserID, *e.Usage.GroupID))
		}
		if e.Command.APIKeyRateLimitCost > 0 {
			err = errors.Join(err, cache.InvalidateAPIKeyRateLimit(ctx, e.Command.APIKeyID))
		}
		if e.Command.PlatformQuotaCost > 0 {
			err = errors.Join(err, cache.InvalidateDurablePlatformQuota(ctx, e.Command.UserID, e.Command.PlatformQuotaPlatform))
		}
	}
	if recovered.Result.APIKeyQuotaExhausted {
		checked, ok := auth.(interface {
			InvalidateAuthCacheByUserIDChecked(context.Context, int64) error
		})
		if !ok {
			err = errors.Join(err, errors.New("checked auth cache invalidation unavailable; receipt remains pending"))
		} else {
			err = errors.Join(err, checked.InvalidateAuthCacheByUserIDChecked(ctx, e.Command.UserID))
		}
	}
	return err
}

func applyDurableUsageBilling(ctx context.Context, repo DurableUsageBillingRepository, cmd *UsageBillingCommand, usage *UsageLog, p *postUsageBillingParams, deps *billingDeps) (bool, error) {
	receipt, _ := ctx.Value(imageBillingReceiptKey{}).(*ImageBillingReceipt)
	var recovered *UsageBillingRecovery
	var err error
	var created bool
	key := UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
	direct, upstream := repo.(UsageSettlementRepository)
	if upstream {
		recovered, err = direct.ApplyUsage(ctx, cmd, usage)
		created = recovered != nil && recovered.Result.Applied
	} else {
		if stager, ok := repo.(imageBillingStageRepository); receipt != nil && ok {
			created, err = stager.StageUsageBillingForReceipt(ctx, cmd, usage)
		} else {
			err = repo.StageUsageBilling(ctx, cmd, usage)
		}
		if err == nil {
			recovered, err = repo.ApplyStagedUsageBilling(ctx, key)
		}
	}
	if err != nil {
		return false, err
	}
	if target, ok := ctx.Value(probePenaltyResultKey{}).(*ForwardResult); ok && target != nil {
		if recovered == nil || !recovered.Settled {
			return false, ErrUsageBillingOutcomeUnknown
		}
		canonical := recovered.Event.Usage
		target.ProbePenalty = nil
		if canonical.CustomerCostDetails != nil {
			target.ProbePenalty = canonical.CustomerCostDetails.ProbePenalty
		}
		target.Usage.InputTokens = canonical.InputTokens
		target.Usage.OutputTokens = canonical.OutputTokens
		target.Usage.CacheReadInputTokens = canonical.CacheReadTokens
		target.Usage.CacheCreationInputTokens = canonical.CacheCreationTokens
	}
	if upstream {
		if recovered != nil && recovered.Result.Applied {
			finalizeCtx, cancel := detachedBillingContext(ctx)
			defer cancel()
			if recovered.Result.APIKeyQuotaExhausted && p.APIKey != nil {
				if auth, ok := p.APIKeyService.(apiKeyAuthCacheInvalidator); ok {
					auth.InvalidateAuthCacheByKey(finalizeCtx, p.APIKey.Key)
				}
			}
			finalizePostUsageBilling(finalizeCtx, p, deps, &recovered.Result)
		}
	} else {
		auth, _ := p.APIKeyService.(APIKeyAuthCacheInvalidator)
		if err := invalidateDurableUsageBilling(ctx, recovered, deps.billingCacheService, auth); err != nil {
			// The committed canonical usage is already authoritative. Cache work
			// stays pending for the recovery worker; never call legacy billing.
			deferUsageBillingCache(repo, ctx, key)
			return false, err
		}
		if recovered != nil && recovered.NeedsCache {
			if err := repo.AcknowledgeUsageBillingCache(ctx, key); err != nil {
				return false, err
			}
		}
		if deps.deferredService != nil && p.Account != nil {
			deps.deferredService.ScheduleLastUsedUpdate(p.Account.ID)
		}
		if recovered != nil && recovered.Result.Applied && !p.SimpleModeKeyRateLimitOnly {
			go notifyBalanceLow(p, deps, &recovered.Result)
			go notifyAccountQuota(p, deps, &recovered.Result)
		}
	}
	if recovered != nil && recovered.Settled && created && !p.SimpleModeKeyRateLimitOnly {
		if receipt != nil {
			canonical := recovered.Event.Usage
			mode := ""
			if canonical.BillingMode != nil {
				mode = *canonical.BillingMode
			}
			*receipt = ImageBillingReceipt{Settled: true, ActualCost: canonical.ActualCost, BillingMode: mode, ImageCount: canonical.ImageCount}
			if d := canonical.CustomerCostDetails; d != nil {
				receipt.MinimumCharge = d.ImageMinimumCharge
				receipt.MinimumChargeAdjustment = d.ImageMinimumChargeAdjustment
			}
		}
	}
	return recovered != nil && recovered.Result.Applied, nil
}

// This provider deliberately shares the pool's shutdown/join boundary. It does
// not start financial work merely by constructing a repository in a test/tool.
func ProvideUsageRecordWorkerPool(cfg *config.Config, billing UsageBillingRepository, cache *BillingCacheService, auth APIKeyAuthCacheInvalidator) *UsageRecordWorkerPool {
	p := NewUsageRecordWorkerPool(cfg)
	repo, ok := billing.(DurableUsageBillingRepository)
	if !ok {
		return p
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.billingRecoveryCancel = cancel
	p.lifecycleWg.Add(1)
	go func() {
		defer p.lifecycleWg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			cycle, stop := context.WithTimeout(ctx, 5*time.Second)
			err := recoverUsageBillingBatch(cycle, repo, cache, auth)
			if errors.Is(err, ErrUsageBillingJournalRetired) {
				stop()
				return
			}
			if err != nil && ctx.Err() == nil {
				logger.LegacyPrintf("usage_billing.recovery", "durable billing cycle incomplete: %v", err)
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return p
}

func recoverUsageBillingBatch(ctx context.Context, repo DurableUsageBillingRepository, cache *BillingCacheService, auth APIKeyAuthCacheInvalidator) error {
	keys, err := repo.PendingUsageBilling(ctx, 64)
	if err != nil {
		return err
	}
	apply := repo.ApplyStagedUsageBilling
	if worker, ok := repo.(interface {
		RecoverStagedUsageBilling(context.Context, UsageBillingEventKey) (*UsageBillingRecovery, error)
	}); ok {
		apply = worker.RecoverStagedUsageBilling
	}
	var failures error
	for _, key := range keys {
		if ctx.Err() != nil {
			return errors.Join(failures, ctx.Err())
		}
		r, err := apply(ctx, key)
		if err == nil {
			err = invalidateDurableUsageBilling(ctx, r, cache, auth)
		}
		if err == nil && r != nil && r.NeedsCache {
			err = repo.AcknowledgeUsageBillingCache(ctx, key)
		}
		if err != nil && r != nil && r.NeedsCache {
			deferUsageBillingCache(repo, ctx, key)
		}
		failures = errors.Join(failures, err)
	}
	return failures
}

func deferUsageBillingCache(repo DurableUsageBillingRepository, parent context.Context, key UsageBillingEventKey) {
	if retry, ok := repo.(interface {
		DeferUsageBillingCache(context.Context, UsageBillingEventKey) error
	}); ok {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		defer cancel()
		_ = retry.DeferUsageBillingCache(ctx, key)
	}
}
