package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const CodexUsageObservationVersionKey = "codex_usage_observed_at_ns"
const CodexUsageFieldVersionsKey = "codex_usage_field_versions"

func IsCodexUsageFieldKey(key string) bool {
	return strings.HasPrefix(key, "codex_5h_") || strings.HasPrefix(key, "codex_7d_") ||
		strings.HasPrefix(key, "codex_primary_") || strings.HasPrefix(key, "codex_secondary_")
}

// CodexUsageFieldVersions retains each present field's original observation.
// Coalesced packets may contain several generations; their overall timestamp
// must never be used to restamp an already versioned field.
func CodexUsageFieldVersions(updates map[string]any, observed time.Time) map[string]any {
	original, _ := updates[CodexUsageFieldVersionsKey].(map[string]any)
	versions := make(map[string]any)
	for key := range updates {
		if !IsCodexUsageFieldKey(key) {
			continue
		}
		version, err := strconv.ParseInt(fmt.Sprint(original[key]), 10, 64)
		if err != nil || version <= 0 {
			version = observed.UnixNano()
		}
		versions[key] = strconv.FormatInt(version, 10)
	}
	return versions
}

func IsCodexUsageExtraKey(key string) bool {
	return key == "codex_usage_updated_at" || key == CodexUsageObservationVersionKey ||
		key == CodexUsageFieldVersionsKey || IsCodexUsageFieldKey(key)
}

// The callback runs while the shared database publication locks are held.
// Rebuilders and feedback publishers must not restore stale usage metadata.
type LatestSchedulerAccountsRepository interface {
	WithLatestSchedulerAccounts(context.Context, []int64, func([]Account) error) error
}

type codexOrderedSchedulerCache struct {
	SchedulerCache
	source LatestSchedulerAccountsRepository
}

// Keep the cache's existing bulk-read capability through the publication
// wrapper; candidate hydration must not turn one MGET into N round trips.
func (c *codexOrderedSchedulerCache) GetAccounts(ctx context.Context, ids []int64) (map[int64]*Account, error) {
	if reader, ok := c.SchedulerCache.(schedulerAccountBatchReader); ok {
		return reader.GetAccounts(ctx, ids)
	}
	accounts := make(map[int64]*Account, len(ids))
	for _, id := range ids {
		account, err := c.GetAccount(ctx, id)
		if err != nil {
			return nil, err
		}
		if account != nil {
			accounts[id] = account
		}
	}
	return accounts, nil
}

func (c *codexOrderedSchedulerCache) withFreshUsage(ctx context.Context, accounts []Account, publish func([]Account) error) error {
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	return c.source.WithLatestSchedulerAccounts(ctx, ids, func(fresh []Account) error {
		byID := make(map[int64]Account, len(fresh))
		for _, a := range fresh {
			byID[a.ID] = a
		}
		out := make([]Account, 0, len(accounts))
		for _, a := range accounts {
			current, exists := byID[a.ID]
			if !exists {
				continue
			}
			if isOutboxRetryPublication(ctx) {
				// A delayed retry may publish after a newer main-stream event. Use
				// every field read under the shared publication lock, including hard
				// eligibility and credentials, instead of restoring the old object.
				// Ordinary request feedback retains its existing merge semantics.
				out = append(out, current)
				continue
			}
			// Preserve other request-triggered runtime feedback; replace only usage
			// fields with committed values read inside the publication lock.
			extra := make(map[string]any, len(a.Extra)+len(current.Extra))
			for k, v := range a.Extra {
				if !IsCodexUsageExtraKey(k) {
					extra[k] = v
				}
			}
			for k, v := range current.Extra {
				if IsCodexUsageExtraKey(k) {
					extra[k] = v
				}
			}
			a.Extra = extra
			out = append(out, a)
		}
		return publish(out)
	})
}

func (c *codexOrderedSchedulerCache) SetAccount(ctx context.Context, a *Account) error {
	if a == nil {
		return nil
	}
	return c.withFreshUsage(ctx, []Account{*a}, func(fresh []Account) error {
		if len(fresh) == 0 {
			return c.DeleteAccount(ctx, a.ID)
		}
		return c.SchedulerCache.SetAccount(ctx, &fresh[0])
	})
}
func (c *codexOrderedSchedulerCache) SetSnapshot(ctx context.Context, b SchedulerBucket, t SchedulerBucketWriteToken, accounts []Account) error {
	return c.withLatestSnapshotAccounts(ctx, b, accounts, func(fresh []Account) error {
		return c.SchedulerCache.SetSnapshot(ctx, b, t, fresh)
	})
}
func (c *codexOrderedSchedulerCache) SetSnapshotAndReturnAccountIDs(ctx context.Context, b SchedulerBucket, t SchedulerBucketWriteToken, accounts []Account) ([]int64, error) {
	var ids []int64
	err := c.withLatestSnapshotAccounts(ctx, b, accounts, func(fresh []Account) error {
		if writer, ok := c.SchedulerCache.(schedulerSnapshotAccountIDWriter); ok {
			var err error
			ids, err = writer.SetSnapshotAndReturnAccountIDs(ctx, b, t, fresh)
			return err
		}
		if err := c.SchedulerCache.SetSnapshot(ctx, b, t, fresh); err != nil {
			return err
		}
		for _, a := range fresh {
			ids = append(ids, a.ID)
		}
		return nil
	})
	return ids, err
}

// A rebuild's initial query can precede a committed disable, credential edit or
// BPS/quality change. Unlike request feedback, it has no runtime fields to merge:
// publish the complete row read under the shared account fence, in input order.
// Keep this separate from SetAccount's request-feedback merge contract.
func (c *codexOrderedSchedulerCache) withLatestSnapshotAccounts(ctx context.Context, b SchedulerBucket, accounts []Account, publish func([]Account) error) error {
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return c.source.WithLatestSchedulerAccounts(ctx, ids, func(fresh []Account) error {
		byID := make(map[int64]Account, len(fresh))
		for _, account := range fresh {
			byID[account.ID] = account
		}
		ordered := make([]Account, 0, len(accounts))
		for _, account := range accounts {
			if current, exists := byID[account.ID]; exists {
				ordered = append(ordered, current)
			}
		}
		if isOutboxRetryPublication(ctx) {
			ordered = filterOutboxRetryBucketAccounts(ctx, b, ordered)
		}
		return publish(ordered)
	})
}

func (c *codexOrderedSchedulerCache) SetSnapshotByAccountIDs(ctx context.Context, b SchedulerBucket, t SchedulerBucketWriteToken, ids []int64) error {
	if writer, ok := c.SchedulerCache.(schedulerSnapshotAccountIDWriter); ok {
		return writer.SetSnapshotByAccountIDs(ctx, b, t, ids)
	}
	return c.source.WithLatestSchedulerAccounts(ctx, ids, func(fresh []Account) error { return c.SchedulerCache.SetSnapshot(ctx, b, t, fresh) })
}
