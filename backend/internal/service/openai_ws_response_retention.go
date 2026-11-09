package service

import (
	"context"
	"errors"
	"time"
)

// OpenAIWSResponseAccountRetentionStore can retain an existing response owner
// while its account cools down. Retention cannot recreate an expired binding,
// change its owner, or keep the provider's response/socket alive.
type OpenAIWSResponseAccountRetentionStore interface {
	RefreshResponseAccount(ctx context.Context, groupID int64, responseID string, accountID int64, ttl time.Duration) (bool, error)
}

// OpenAIWSResponseAccountRetentionCache extends only an unchanged, existing
// session-account record. It is optional so older cache adapters fail closed
// rather than falling back to an unconditional sticky write.
type OpenAIWSResponseAccountRetentionCache interface {
	RefreshSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) (bool, error)
}

func (s *defaultOpenAIWSStateStore) RefreshResponseAccount(ctx context.Context, groupID int64, responseID string, accountID int64, ttl time.Duration) (bool, error) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || accountID <= 0 || ttl.Milliseconds() <= 0 {
		return false, errors.New("invalid response ownership retention")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	mapKey := openAIWSResponseAccountMapKey(groupID, id)
	if s.cache != nil {
		shared, ok := s.cache.(OpenAIWSResponseAccountRetentionCache)
		if !ok {
			return false, nil
		}
		cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
		defer cancel()
		refreshed, err := shared.RefreshSessionAccountID(cacheCtx, groupID, openAIWSResponseAccountCacheKey(id), accountID, ttl)
		if err != nil {
			return false, err
		}
		if !refreshed {
			// A surviving process-local entry must not resurrect a missing or
			// replaced authoritative record on the next local lookup.
			s.responseToAccountMu.Lock()
			if binding, found := s.responseToAccount[mapKey]; found && binding.accountID == accountID {
				delete(s.responseToAccount, mapKey)
			}
			s.responseToAccountMu.Unlock()
			return false, nil
		}
		// The shared CAS is authoritative. Do not add a local cache entry or
		// overwrite a concurrently rebound local response.
		s.responseToAccountMu.Lock()
		if binding, found := s.responseToAccount[mapKey]; found && binding.accountID == accountID && time.Now().Before(binding.expiresAt) {
			if expiresAt := time.Now().Add(ttl); expiresAt.After(binding.expiresAt) {
				binding.expiresAt = expiresAt
				s.responseToAccount[mapKey] = binding
			}
		}
		s.responseToAccountMu.Unlock()
		return true, nil
	}

	s.responseToAccountMu.Lock()
	defer s.responseToAccountMu.Unlock()
	binding, found := s.responseToAccount[mapKey]
	now := time.Now()
	if !found || binding.accountID != accountID || !now.Before(binding.expiresAt) {
		return false, nil
	}
	if expiresAt := now.Add(ttl); expiresAt.After(binding.expiresAt) {
		binding.expiresAt = expiresAt
		s.responseToAccount[mapKey] = binding
	}
	return true, nil
}

var _ OpenAIWSResponseAccountRetentionStore = (*defaultOpenAIWSStateStore)(nil)
