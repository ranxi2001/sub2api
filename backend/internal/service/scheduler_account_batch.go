package service

import (
	"context"
	"fmt"
)

const schedulerAccountReadBatchSize = 128

// Optional capability: production Redis implements a chunked MGET, while
// existing cache implementations may continue to provide GetAccount only.
type schedulerAccountBatchReader interface {
	GetAccounts(context.Context, []int64) (map[int64]*Account, error)
}

// GetAccounts reads complete accounts, never the candidate-list projection.
// Misses fall back in bounded batches under the existing fallback policy.
// Results belong to this call; they are not a cross-request authority cache.
func (s *SchedulerSnapshotService) GetAccounts(ctx context.Context, accountIDs []int64) (result map[int64]*Account, resultErr error) {
	defer func() {
		if resultErr == nil {
			result, resultErr = s.markPerformanceAccounts(ctx, result)
		}
	}()
	if s.localSnapshotReader != nil {
		reader, ok := s.localSnapshotReader.(LocalSchedulerDetailReader)
		if !ok {
			return nil, ErrLocalSchedulerSnapshotUnavailable
		}
		return reader.GetLocalAccounts(ctx, accountIDs)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	out := make(map[int64]*Account, len(ids))
	for start := 0; start < len(ids); start += schedulerAccountReadBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+schedulerAccountReadBatchSize, len(ids))
		batch := ids[start:end]
		cached := make(map[int64]*Account, len(batch))
		if s.cache != nil {
			if reader, ok := s.cache.(schedulerAccountBatchReader); ok {
				values, err := reader.GetAccounts(ctx, batch)
				if err == nil {
					cached = values
				}
			} else {
				for _, id := range batch {
					account, err := s.cache.GetAccount(ctx, id)
					if err == nil && account != nil {
						cached[id] = account
					}
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		missing := make([]int64, 0, len(batch))
		for _, id := range batch {
			if account := cached[id]; account != nil && account.ID == id {
				out[id] = account
			} else {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			continue
		}
		if err := s.guardFallback(ctx); err != nil {
			return nil, err
		}
		if s.accountRepo == nil {
			return nil, ErrSchedulerCacheNotReady
		}
		fallbackCtx, cancel := s.withFallbackTimeout(ctx)
		accounts, err := s.accountRepo.GetByIDs(fallbackCtx, missing)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("read complete scheduler accounts: %w", err)
		}
		wanted := make(map[int64]struct{}, len(missing))
		for _, id := range missing {
			wanted[id] = struct{}{}
		}
		for _, account := range accounts {
			if account != nil {
				if _, ok := wanted[account.ID]; ok {
					out[account.ID] = account
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
